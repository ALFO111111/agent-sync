package agentsessions

import (
	"bufio"
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

	"github.com/ALFO111111/agent-sync/internal/remotev1"
)

// CodexRunner 是一个 Codex 会话：每轮启动一个 codex exec 进程，结束后保留会话以便 resume。
type CodexRunner struct {
	Key      string
	cwd      string
	mode     string
	threadID string
	config   Config
	manager  *CodexManager

	mu         sync.Mutex
	seq        int64
	events     []Event
	subs       map[chan Event]bool
	closed     bool
	busy       bool
	cmd        *exec.Cmd
	lastActive time.Time
	done       chan struct{}
}

// CodexManager 管理 Codex 会话（进程按轮启动）。
type CodexManager struct {
	cfg Config

	mu      sync.Mutex
	runners map[string]*CodexRunner
}

func NewCodexManager(cfg Config) *CodexManager {
	m := &CodexManager{cfg: cfg, runners: map[string]*CodexRunner{}}
	go m.reapLoop()
	return m
}

func codexSandbox(mode string) string {
	switch normalizeMode(mode) {
	case "bypassPermissions":
		return "danger-full-access"
	case "dontAsk", "plan":
		return "read-only"
	default:
		return "workspace-write"
	}
}

// Start 创建一个 Codex 会话；resumeID 非空时后续轮次会 resume 该 thread。
func (m *CodexManager) Start(cwd, resumeID, mode string) (*CodexRunner, error) {
	if !filepath.IsAbs(cwd) {
		return nil, errors.New("cwd must be absolute")
	}
	if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
		return nil, errors.New("cwd does not exist")
	}
	m.mu.Lock()
	if len(m.runners) >= m.cfg.MaxSessions {
		m.mu.Unlock()
		return nil, errors.New("too many active sessions")
	}
	m.mu.Unlock()
	r := &CodexRunner{
		Key:        remotev1.RandomToken(8),
		cwd:        cwd,
		mode:       normalizeMode(mode),
		threadID:   resumeID,
		config:     m.cfg,
		manager:    m,
		subs:       map[chan Event]bool{},
		lastActive: time.Now(),
		done:       make(chan struct{}),
	}
	m.mu.Lock()
	m.runners[r.Key] = r
	m.mu.Unlock()
	return r, nil
}

func (m *CodexManager) Get(key string) *CodexRunner {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runners[key]
}

func (m *CodexManager) remove(key string) {
	m.mu.Lock()
	delete(m.runners, key)
	m.mu.Unlock()
}

// Active 返回活动会话。
func (m *CodexManager) Active() []Active {
	m.mu.Lock()
	list := make([]*CodexRunner, 0, len(m.runners))
	for _, r := range m.runners {
		list = append(list, r)
	}
	m.mu.Unlock()
	out := make([]Active, 0, len(list))
	for _, r := range list {
		r.mu.Lock()
		out = append(out, Active{Key: r.Key, ClaudeID: r.threadID, Cwd: r.cwd, Mode: r.mode, Subscribers: len(r.subs), Tool: "codex"})
		r.mu.Unlock()
	}
	return out
}

func (m *CodexManager) reapLoop() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for range t.C {
		m.mu.Lock()
		list := make([]*CodexRunner, 0, len(m.runners))
		for _, r := range m.runners {
			list = append(list, r)
		}
		m.mu.Unlock()
		for _, r := range list {
			r.mu.Lock()
			idle := !r.closed && !r.busy && len(r.subs) == 0 && time.Since(r.lastActive) > idleTTL
			r.mu.Unlock()
			if idle {
				r.Stop()
			}
		}
	}
}

// Send 启动一轮 codex exec（必要时 resume）。
func (r *CodexRunner) Send(text string) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return errors.New("session is closed")
	}
	if r.busy {
		r.mu.Unlock()
		return errors.New("turn already running")
	}
	r.busy = true
	r.lastActive = time.Now()
	threadID := r.threadID
	r.mu.Unlock()

	args := []string{"exec"}
	if threadID != "" {
		args = append(args, "resume", "--skip-git-repo-check", "--json", threadID, text)
	} else {
		args = append(args, "--json", "--skip-git-repo-check", "-C", r.cwd, "-s", codexSandbox(r.mode), text)
	}
	bin := r.config.Bin
	if bin == "" {
		if p, err := exec.LookPath("codex"); err == nil {
			bin = p
		}
	}
	if bin == "" {
		r.mu.Lock()
		r.busy = false
		r.mu.Unlock()
		return errors.New("codex cli not found")
	}
	var cmd *exec.Cmd
	lower := strings.ToLower(bin)
	if runtime.GOOS == "windows" && (strings.HasSuffix(lower, ".cmd") || strings.HasSuffix(lower, ".bat")) {
		cmd = exec.Command("cmd.exe", append([]string{"/c", bin}, args...)...)
	} else {
		cmd = exec.Command(bin, args...)
	}
	cmd.Dir = r.cwd
	env := os.Environ()
	if len(r.config.ExtraPATH) > 0 {
		env = append(env, "PATH="+strings.Join(r.config.ExtraPATH, string(os.PathListSeparator))+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	cmd.Env = env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		r.mu.Lock()
		r.busy = false
		r.mu.Unlock()
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		r.mu.Lock()
		r.busy = false
		r.mu.Unlock()
		return err
	}
	if err := cmd.Start(); err != nil {
		r.mu.Lock()
		r.busy = false
		r.mu.Unlock()
		return err
	}
	r.mu.Lock()
	r.cmd = cmd
	r.mu.Unlock()

	go r.readStdout(stdout)
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 16*1024), 1024*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line != "" && strings.Contains(strings.ToLower(line), "error") {
				r.append(Event{Kind: "notice", Text: truncate(line, maxResult)})
			}
		}
	}()
	go func() {
		_ = cmd.Wait()
		r.mu.Lock()
		r.cmd = nil
		r.busy = false
		r.lastActive = time.Now()
		r.mu.Unlock()
	}()
	return nil
}

func (r *CodexRunner) readStdout(rd io.Reader) {
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		for _, ev := range parseCodexLine(sc.Bytes()) {
			if ev.Kind == "init" && ev.SessionID != "" {
				r.mu.Lock()
				r.threadID = ev.SessionID
				r.mu.Unlock()
			}
			r.append(ev)
		}
	}
}

// parseCodexLine 把 codex exec --json 的一行映射成手机事件。
func parseCodexLine(line []byte) []Event {
	var raw struct {
		Type     string `json:"type"`
		ThreadID string `json:"thread_id"`
		Message  string `json:"message"`
		Item     struct {
			ID               string `json:"id"`
			Type             string `json:"type"`
			Text             string `json:"text"`
			Message          string `json:"message"`
			Command          string `json:"command"`
			AggregatedOutput string `json:"aggregated_output"`
			ExitCode         *int   `json:"exit_code"`
			Status           string `json:"status"`
		} `json:"item"`
	}
	if json.Unmarshal(line, &raw) != nil {
		return nil
	}
	switch raw.Type {
	case "thread.started":
		return []Event{{Kind: "init", SessionID: raw.ThreadID}}
	case "turn.completed":
		return []Event{{Kind: "result", Text: "done"}}
	case "turn.failed":
		return []Event{{Kind: "result", Text: "failed"}}
	case "error":
		return []Event{{Kind: "notice", Text: truncate(raw.Message, maxResult)}}
	case "item.started", "item.completed":
		it := raw.Item
		switch it.Type {
		case "agent_message":
			if raw.Type == "item.completed" && it.Text != "" {
				return []Event{{Kind: "text", Text: it.Text}}
			}
		case "command_execution":
			if raw.Type == "item.started" {
				return []Event{{Kind: "tool", Tool: "command", ToolID: it.ID, Text: truncate(it.Command, maxToolText)}}
			}
			return []Event{{Kind: "tool_result", ToolID: it.ID, Text: truncate(it.AggregatedOutput, maxResult)}}
		case "file_change":
			if raw.Type == "item.started" {
				return []Event{{Kind: "tool", Tool: "file_change", ToolID: it.ID}}
			}
			return []Event{{Kind: "tool_result", ToolID: it.ID, Text: truncate(it.Text, maxResult)}}
		case "error":
			msg := it.Message
			if msg == "" {
				msg = it.Text
			}
			if msg == "" || strings.Contains(msg, "unrecognized configuration setting") {
				return nil
			}
			return []Event{{Kind: "notice", Text: truncate(msg, maxResult)}}
		}
	}
	return nil
}

func (r *CodexRunner) append(e Event) {
	r.mu.Lock()
	r.seq++
	e.Seq = r.seq
	if e.SessionID == "" {
		e.SessionID = r.threadID
	}
	r.events = append(r.events, e)
	if len(r.events) > ringSize {
		r.events = r.events[len(r.events)-ringSize:]
	}
	for ch := range r.subs {
		select {
		case ch <- e:
		default:
			delete(r.subs, ch)
			close(ch)
		}
	}
	r.mu.Unlock()
}

// Stop 结束会话：杀掉当前轮次并释放。
func (r *CodexRunner) Stop() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	if r.cmd != nil && r.cmd.Process != nil {
		_ = r.cmd.Process.Kill()
	}
	if r.done != nil {
		close(r.done)
	}
	for ch := range r.subs {
		delete(r.subs, ch)
		close(ch)
	}
	r.mu.Unlock()
	if r.manager != nil {
		r.manager.remove(r.Key)
	}
}

// ChangeMode 更新沙箱模式（下一轮生效）。
func (r *CodexRunner) ChangeMode(mode string) {
	r.mu.Lock()
	r.mode = normalizeMode(mode)
	r.mu.Unlock()
}

func (r *CodexRunner) Mode() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mode
}

// Subscribe 返回历史与实时事件。
func (r *CodexRunner) Subscribe(since int64) ([]Event, <-chan Event, func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var history []Event
	for _, e := range r.events {
		if e.Seq > since {
			history = append(history, e)
		}
	}
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

// ThreadID 返回 Codex 会话 ID（用于 resume）。
func (r *CodexRunner) ThreadID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.threadID
}

// StopAll 结束时清理所有 Codex 会话。
func (m *CodexManager) StopAll() {
	m.mu.Lock()
	list := make([]*CodexRunner, 0, len(m.runners))
	for _, r := range m.runners {
		list = append(list, r)
	}
	m.mu.Unlock()
	for _, r := range list {
		r.Stop()
	}
}
