package agentsessions

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ALFO111111/agent-sync/internal/i18n"
	"github.com/ALFO111111/agent-sync/internal/remotev1"
)

const (
	ringSize    = 2000
	subBuffer   = 64
	stopGrace   = 5 * time.Second
	maxStderr   = 8 * 1024
	maxToolText = 200
	maxResult   = 400
)

// Config 控制 Claude CLI 的启动方式。
type Config struct {
	Bin         string   // claude 可执行文件；空表示从 PATH 找
	ExtraPATH   []string // 追加到子进程 PATH 前面（例如 ~/sdk/node/bin）
	MaxSessions int      // 同时运行的会话上限
	Mode        string   // readonly | edit | full（空=readonly）
}

// Manager 管理多个 Claude 会话进程。
type Manager struct {
	cfg Config

	mu      sync.Mutex
	runners map[string]*Runner
}

func NewManager(cfg Config) *Manager {
	if cfg.MaxSessions <= 0 {
		cfg.MaxSessions = 4
	}
	m := &Manager{cfg: cfg, runners: map[string]*Runner{}}
	go m.reapLoop()
	return m
}

// Start 在 cwd 启动一个新会话，或续聊 resumeID。
func (m *Manager) Start(cwd, resumeID, mode string) (*Runner, error) {
	if !filepath.IsAbs(cwd) {
		return nil, errors.New("cwd must be absolute")
	}
	fi, err := os.Stat(cwd)
	if err != nil || !fi.IsDir() {
		return nil, errors.New("cwd does not exist")
	}
	m.mu.Lock()
	if len(m.runners) >= m.cfg.MaxSessions && !m.evictIdleLocked() {
		m.mu.Unlock()
		return nil, errors.New("too many active sessions")
	}
	m.mu.Unlock()

	bin := m.cfg.Bin
	if bin == "" {
		// Windows 上通常是 claude.cmd，LookPath 会按 PATHEXT 找到它。
		if p, err := exec.LookPath("claude"); err == nil {
			bin = p
		}
	}
	if bin == "" {
		home, _ := os.UserHomeDir()
		candidate := filepath.Join(home, "sdk", "node", "bin", "claude")
		if _, err := os.Stat(candidate); err == nil {
			bin = candidate
		}
	}
	if bin == "" {
		return nil, errors.New("claude cli not found")
	}
	args := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
	}
	args = append(args, modeArgs(mode)...)
	args = append(args, "--permission-prompt-tool", "stdio")
	if resumeID != "" {
		args = append(args, "--resume", resumeID)
	}
	var cmd *exec.Cmd
	lower := strings.ToLower(bin)
	if runtime.GOOS == "windows" && (strings.HasSuffix(lower, ".cmd") || strings.HasSuffix(lower, ".bat")) {
		// Go 不能直接执行 .cmd/.bat，必须交给 cmd.exe。
		cmd = exec.Command("cmd.exe", append([]string{"/c", bin}, args...)...)
	} else {
		cmd = exec.Command(bin, args...)
	}
	cmd.Dir = cwd
	env := os.Environ()
	if len(m.cfg.ExtraPATH) > 0 {
		env = append(env, "PATH="+strings.Join(m.cfg.ExtraPATH, string(os.PathListSeparator))+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	r := &Runner{
		manager:    m,
		lastActive: time.Now(),
		mode:       normalizeMode(mode),
		done:       make(chan struct{}),
		Key:        remotev1.RandomToken(8),
		cmd:        cmd,
		stdin:      stdin,
		subs:       map[chan Event]bool{},
		approvals:  map[string]chan bool{},
		controls:   map[string]chan string{},
		cwd:        cwd,
		config:     m.cfg,
	}
	m.mu.Lock()
	m.runners[r.Key] = r
	m.mu.Unlock()

	// SDK 握手：告诉 CLI 有一个可回答审批的主机，之后才会发 can_use_tool。
	_ = r.writeControl(map[string]any{
		"type":       "control_request",
		"request_id": randomID(),
		"request":    map[string]any{"subtype": "initialize"},
	})

	go r.readStdout(stdout)
	go r.readStderr(stderr)
	go func() {
		err := cmd.Wait()
		r.finish(err)
	}()
	return r, nil
}

// Get 按 key 找运行中的会话。
func (m *Manager) Get(key string) *Runner {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runners[key]
}

// StopAll 退出时关闭所有子进程。
func (m *Manager) StopAll() {
	m.mu.Lock()
	list := make([]*Runner, 0, len(m.runners))
	for _, r := range m.runners {
		list = append(list, r)
	}
	m.mu.Unlock()
	for _, r := range list {
		r.Stop()
	}
}

// Event 是归一化后推给手机的事件。
type Event struct {
	Seq        int64  `json:"seq"`
	Kind       string `json:"kind"`
	Text       string `json:"text,omitempty"`
	Tool       string `json:"tool,omitempty"`
	ToolID     string `json:"toolId,omitempty"`
	SessionID  string `json:"sessionId,omitempty"`
	ApprovalID string `json:"approvalId,omitempty"`
}

// Runner 是一个 Claude 会话进程。
type Runner struct {
	Key        string
	cwd        string
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	config     Config
	manager    *Manager
	mode       string
	done       chan struct{}
	lastActive time.Time

	wmu       sync.Mutex
	approvals map[string]chan bool
	controls  map[string]chan string

	mu       sync.Mutex
	seq      int64
	events   []Event
	subs     map[chan Event]bool
	closed   bool
	claudeID string
	stderr   string
}

// ImageAttachment 是一张随消息发送的图片（对话级，不落盘）。
type ImageAttachment struct {
	MediaType string
	Data      []byte
}

// UserContent 生成 stream-json 的用户消息内容：
// 没有图片时保持原来的字符串形式；有图片时用内容块数组（和 Claude Code 粘贴图片一致）。
func UserContent(text string, images []ImageAttachment) any {
	if len(images) == 0 {
		return text
	}
	blocks := make([]map[string]any, 0, len(images)+1)
	if strings.TrimSpace(text) != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": text})
	}
	for _, img := range images {
		blocks = append(blocks, map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": img.MediaType,
				"data":       base64.StdEncoding.EncodeToString(img.Data),
			},
		})
	}
	if len(blocks) == 0 {
		blocks = append(blocks, map[string]any{"type": "text", "text": i18n.T("请看附件。")})
	}
	return blocks
}

// Send 写一条纯文本用户消息。
func (r *Runner) Send(text string) error { return r.SendWith(text, nil) }

// SendWith 写一条带可选图片的用户消息。
func (r *Runner) SendWith(text string, images []ImageAttachment) error {
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return errors.New("session is closed")
	}
	line, _ := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": UserContent(text, images),
		},
	})
	r.lastActive = time.Now()
	r.wmu.Lock()
	defer r.wmu.Unlock()
	_, err := io.WriteString(r.stdin, string(line)+"\n")
	return err
}

// Stop 先温柔中断，超时再强杀。
func (r *Runner) Stop() {
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed || r.cmd == nil || r.cmd.Process == nil {
		return
	}
	if runtime.GOOS == "windows" {
		// Windows 不支持向子进程发 SIGINT；直接结束（会话可 resume）。
		_ = r.cmd.Process.Kill()
		return
	}
	_ = r.cmd.Process.Signal(os.Interrupt)
	time.AfterFunc(stopGrace, func() {
		r.mu.Lock()
		closed := r.closed
		r.mu.Unlock()
		if !closed {
			_ = r.cmd.Process.Kill()
		}
	})
}

// Snapshot 返回 since 之后的事件和当前 seq。
func (r *Runner) Snapshot(since int64) ([]Event, int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.copySinceLocked(since), r.seq
}

// Subscribe 先返回历史，再返回实时事件通道。
func (r *Runner) Subscribe(since int64) ([]Event, <-chan Event, func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	history := r.copySinceLocked(since)
	ch := make(chan Event, subBuffer)
	if r.closed {
		close(ch)
		return history, ch, func() {}
	}
	r.subs[ch] = true
	cancel := func() {
		r.mu.Lock()
		if r.subs[ch] {
			delete(r.subs, ch)
			close(ch)
		}
		r.mu.Unlock()
	}
	return history, ch, cancel
}

// ClaudeID 返回 CLI 自己分配（resume 用）的会话 ID。
func (r *Runner) ClaudeID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.claudeID
}

func (r *Runner) copySinceLocked(since int64) []Event {
	var out []Event
	for _, e := range r.events {
		if e.Seq > since {
			out = append(out, e)
		}
	}
	return out
}

func (r *Runner) append(e Event) {
	r.mu.Lock()
	r.seq++
	e.Seq = r.seq
	if e.SessionID == "" {
		e.SessionID = r.claudeID
	}
	r.events = append(r.events, e)
	if len(r.events) > ringSize {
		r.events = r.events[len(r.events)-ringSize:]
	}
	for ch := range r.subs {
		select {
		case ch <- e:
		default:
			// 慢订阅者丢弃并关闭，避免拖住进程。
			delete(r.subs, ch)
			close(ch)
		}
	}
	r.mu.Unlock()
}

func (r *Runner) readStdout(rd io.Reader) {
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if r.handleControl(sc.Bytes()) {
			continue
		}
		if ev, ok := parseEvent(sc.Bytes()); ok {
			if ev.Kind == "init" && ev.SessionID != "" {
				r.mu.Lock()
				r.claudeID = ev.SessionID
				r.mu.Unlock()
			}
			r.append(ev)
		}
	}
}

func (r *Runner) readStderr(rd io.Reader) {
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 16*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		r.mu.Lock()
		r.stderr = (r.stderr + "\n" + line)
		if len(r.stderr) > maxStderr {
			r.stderr = r.stderr[len(r.stderr)-maxStderr:]
		}
		r.mu.Unlock()
	}
}

func (r *Runner) finish(err error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	if r.done != nil {
		close(r.done)
	}
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	if r.stderr != "" {
		detail = strings.TrimSpace(detail + " | " + r.stderr)
		if len(detail) > maxResult {
			detail = detail[:maxResult]
		}
	}
	for ch := range r.subs {
		delete(r.subs, ch)
		close(ch)
	}
	r.mu.Unlock()
	r.append(Event{Kind: "exit", Text: detail})
	if r.manager != nil {
		r.manager.remove(r.Key)
	}
}

// parseEvent 把 Claude stream-json 的一行归一化成手机事件。
func parseEvent(line []byte) (Event, bool) {
	var raw map[string]any
	if json.Unmarshal(line, &raw) != nil {
		return Event{}, false
	}
	typ, _ := raw["type"].(string)
	switch typ {
	case "system":
		sub, _ := raw["subtype"].(string)
		switch sub {
		case "init":
			id, _ := raw["session_id"].(string)
			return Event{Kind: "init", SessionID: id}, true
		case "permission_denied":
			return Event{Kind: "permission_denied", Tool: str(raw["tool_name"]), Text: truncate(str(raw["reason"]), maxResult)}, true
		}
		return Event{}, false
	case "stream_event":
		ev, _ := raw["event"].(map[string]any)
		if ev == nil {
			return Event{}, false
		}
		delta, _ := ev["delta"].(map[string]any)
		if delta != nil {
			if dt, _ := delta["type"].(string); dt == "text_delta" {
				return Event{Kind: "text_delta", Text: str(delta["text"])}, true
			}
		}
		return Event{}, false
	case "assistant":
		msg, _ := raw["message"].(map[string]any)
		if msg == nil {
			return Event{}, false
		}
		blocks, _ := msg["content"].([]any)
		var texts []string
		var tool Event
		for _, b := range blocks {
			m, _ := b.(map[string]any)
			if m == nil {
				continue
			}
			switch str(m["type"]) {
			case "text":
				texts = append(texts, str(m["text"]))
			case "tool_use":
				tool = Event{Kind: "tool_use", Tool: str(m["name"]), ToolID: str(m["id"]), Text: briefInput(m["input"])}
			}
		}
		if tool.Kind != "" {
			return tool, true
		}
		if len(texts) > 0 {
			return Event{Kind: "text", Text: strings.Join(texts, "")}, true
		}
		return Event{}, false
	case "user":
		msg, _ := raw["message"].(map[string]any)
		if msg == nil {
			return Event{}, false
		}
		blocks, _ := msg["content"].([]any)
		for _, b := range blocks {
			m, _ := b.(map[string]any)
			if m != nil && str(m["type"]) == "tool_result" {
				return Event{Kind: "tool_result", ToolID: str(m["tool_use_id"]), Text: truncate(resultText(m["content"]), maxResult)}, true
			}
		}
		return Event{}, false
	case "result":
		sub := str(raw["subtype"])
		text, _ := raw["result"].(string)
		if text == "" {
			text = sub
		}
		id, _ := raw["session_id"].(string)
		return Event{Kind: "result", Text: truncate(text, maxResult), SessionID: id}, true
	}
	return Event{}, false
}

func briefInput(v any) string {
	m, _ := v.(map[string]any)
	if m == nil {
		return ""
	}
	for _, k := range []string{"file_path", "path", "pattern", "query", "command", "description", "url"} {
		if s, ok := m[k].(string); ok && s != "" {
			return truncate(s, maxToolText)
		}
	}
	return ""
}

func resultText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		var parts []string
		for _, item := range x {
			m, _ := item.(map[string]any)
			if m != nil && str(m["type"]) == "text" {
				parts = append(parts, str(m["text"]))
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}

// remove 在进程退出后释放名额。
func (m *Manager) remove(key string) {
	m.mu.Lock()
	delete(m.runners, key)
	m.mu.Unlock()
}

// evictIdleLocked 名额满时回收一个没有订阅者、最久没活动的会话。
func (m *Manager) evictIdleLocked() bool {
	var victim *Runner
	var victimActive time.Time
	for _, r := range m.runners {
		r.mu.Lock()
		closed, subs, active := r.closed, len(r.subs), r.lastActive
		r.mu.Unlock()
		if closed || subs > 0 {
			continue
		}
		if victim == nil || active.Before(victimActive) {
			victim, victimActive = r, active
		}
	}
	if victim == nil {
		return false
	}
	delete(m.runners, victim.Key)
	go victim.Stop()
	return true
}

const idleTTL = 3 * time.Minute

// reapLoop 定时回收没人观看且已空闲的会话，避免长期占用会话文件。
func (m *Manager) reapLoop() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for range t.C {
		m.mu.Lock()
		list := make([]*Runner, 0, len(m.runners))
		for _, r := range m.runners {
			list = append(list, r)
		}
		m.mu.Unlock()
		for _, r := range list {
			r.mu.Lock()
			idle := !r.closed && len(r.subs) == 0 && time.Since(r.lastActive) > idleTTL
			r.mu.Unlock()
			if idle {
				r.Stop()
			}
		}
	}
}

func normalizeMode(mode string) string {
	switch mode {
	case "edit", "acceptEdits":
		return "acceptEdits"
	case "full", "bypassPermissions":
		return "bypassPermissions"
	case "plan":
		return "plan"
	case "auto":
		return "auto"
	case "default", "manual":
		return "default"
	default: // dontAsk / readonly / 空
		return "dontAsk"
	}
}

// modeArgs 决定本次会话的权限范围和自动批准的工具。
func modeArgs(mode string) []string {
	switch normalizeMode(mode) {
	case "acceptEdits":
		return []string{"--permission-mode", "acceptEdits", "--allowedTools", "Read,Grep,Glob,WebSearch,WebFetch"}
	case "bypassPermissions":
		return []string{"--permission-mode", "bypassPermissions"}
	case "plan":
		return []string{"--permission-mode", "plan"}
	case "auto":
		return []string{"--permission-mode", "auto"}
	case "default":
		return []string{"--permission-mode", "default"}
	default:
		return []string{"--permission-mode", "dontAsk", "--allowedTools", "Read,Grep,Glob,WebSearch,WebFetch"}
	}
}

// ChangeMode 优先在运行中切换；不支持时回退为用同一会话 ID 重启。
func (m *Manager) ChangeMode(key, mode string) (*Runner, error) {
	r := m.Get(key)
	if r == nil {
		return nil, errors.New("session not found")
	}
	canonical := normalizeMode(mode)
	if err := r.setMode(canonical); err == nil {
		r.mu.Lock()
		r.mode = canonical
		r.mu.Unlock()
		return r, nil
	}
	cwd := r.cwd
	claudeID := r.ClaudeID()
	r.Stop()
	select {
	case <-r.done:
	case <-time.After(12 * time.Second):
	}
	return m.Start(cwd, claudeID, canonical)
}

// handleControl 处理 SDK 控制帧（can_use_tool 审批、set_permission_mode 回执）。
func (r *Runner) handleControl(line []byte) bool {
	var raw struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		Request   struct {
			Subtype        string         `json:"subtype"`
			ToolName       string         `json:"tool_name"`
			ToolUseID      string         `json:"tool_use_id"`
			Description    string         `json:"description"`
			Input          map[string]any `json:"input"`
			DecisionReason string         `json:"decision_reason"`
		} `json:"request"`
		Response struct {
			Subtype   string `json:"subtype"`
			RequestID string `json:"request_id"`
			Error     string `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal(line, &raw) != nil {
		return false
	}
	switch raw.Type {
	case "control_request":
		if raw.Request.Subtype != "can_use_tool" {
			return true
		}
		ch := make(chan bool, 1)
		r.mu.Lock()
		r.approvals[raw.RequestID] = ch
		r.mu.Unlock()
		text := raw.Request.Description
		if text == "" {
			text = briefInput(raw.Request.Input)
		}
		r.append(Event{
			Kind: "approval", Tool: raw.Request.ToolName, Text: text,
			ToolID: raw.Request.ToolUseID, ApprovalID: raw.RequestID,
		})
		go func(id string) {
			allow := false
			timer := time.NewTimer(120 * time.Second)
			defer timer.Stop()
			select {
			case allow = <-ch:
			case <-timer.C:
			case <-r.done:
			}
			r.mu.Lock()
			delete(r.approvals, id)
			r.mu.Unlock()
			result := map[string]any{"behavior": "allow"}
			if !allow {
				result = map[string]any{"behavior": "deny", "message": "User denied the request"}
			}
			r.writeControl(map[string]any{
				"type": "control_response",
				"response": map[string]any{
					"subtype": "success", "request_id": id, "response": result,
				},
			})
		}(raw.RequestID)
		return true
	case "control_response":
		r.mu.Lock()
		ch := r.controls[raw.Response.RequestID]
		delete(r.controls, raw.Response.RequestID)
		r.mu.Unlock()
		if ch != nil {
			if raw.Response.Subtype == "success" {
				ch <- ""
			} else {
				ch <- raw.Response.Error
			}
		}
		return true
	}
	return false
}

// Decide 回应一次审批请求（来自手机）。
func (r *Runner) Decide(approvalID string, allow bool) error {
	r.mu.Lock()
	ch := r.approvals[approvalID]
	r.mu.Unlock()
	if ch == nil {
		return errors.New("approval not found")
	}
	select {
	case ch <- allow:
	default:
	}
	return nil
}

func (r *Runner) writeControl(msg map[string]any) error {
	b, _ := json.Marshal(msg)
	r.wmu.Lock()
	defer r.wmu.Unlock()
	_, err := io.WriteString(r.stdin, string(b)+"\n")
	return err
}

// setMode 尝试在运行中切换权限模式（不重启会话）。
func (r *Runner) setMode(mode string) error {
	reqID := randomID()
	ch := make(chan string, 1)
	r.mu.Lock()
	r.controls[reqID] = ch
	r.mu.Unlock()
	if err := r.writeControl(map[string]any{
		"type":       "control_request",
		"request_id": reqID,
		"request":    map[string]any{"subtype": "set_permission_mode", "mode": mode},
	}); err != nil {
		return err
	}
	select {
	case msg := <-ch:
		if msg != "" {
			return errors.New(msg)
		}
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("set_permission_mode timeout")
	}
}

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Active 列出当前活动的会话，便于本地排查。
type Active struct {
	Key         string
	ClaudeID    string
	Cwd         string
	Mode        string
	Subscribers int
	Tool        string
}

func (m *Manager) Active() []Active {
	m.mu.Lock()
	list := make([]*Runner, 0, len(m.runners))
	for _, r := range m.runners {
		list = append(list, r)
	}
	m.mu.Unlock()
	out := make([]Active, 0, len(list))
	for _, r := range list {
		r.mu.Lock()
		out = append(out, Active{Key: r.Key, ClaudeID: r.claudeID, Cwd: r.cwd, Mode: r.mode, Subscribers: len(r.subs), Tool: "claude"})
		r.mu.Unlock()
	}
	return out
}

// StopByRef 按 key 或 Claude 会话 ID 结束一个会话。
func (m *Manager) StopByRef(ref string) bool {
	m.mu.Lock()
	list := make([]*Runner, 0, len(m.runners))
	for _, r := range m.runners {
		list = append(list, r)
	}
	m.mu.Unlock()
	for _, r := range list {
		r.mu.Lock()
		match := r.Key == ref || (r.claudeID != "" && r.claudeID == ref)
		r.mu.Unlock()
		if match {
			r.Stop()
			return true
		}
	}
	return false
}
