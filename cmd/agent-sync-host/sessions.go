package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ALFO111111/agent-sync/internal/agentsessions"
	"github.com/ALFO111111/agent-sync/internal/remotev1"
)

const allowedCacheTTL = 30 * time.Second

// sessionService 把 Claude/Codex 两种后端适配成 remotev1.SessionSink。
type sessionService struct {
	index    *agentsessions.Index
	mgr      *agentsessions.Manager
	codex    *agentsessions.CodexManager
	codexIdx *agentsessions.CodexIndex

	mu          sync.Mutex
	allowed     []string
	allowedTime time.Time

	attachMu   sync.Mutex
	attachDirs map[string][]string // 会话 key → 本轮附件临时目录（会话结束清理）
}

func newSessionService(bin string, extraPath []string) (*sessionService, error) {
	idx, err := agentsessions.NewIndex()
	if err != nil {
		return nil, err
	}
	if bin == "" {
		home, _ := os.UserHomeDir()
		candidate := filepath.Join(home, "sdk", "node", "bin", "claude")
		if _, err := os.Stat(candidate); err == nil {
			bin = candidate
		}
	}
	mgr := agentsessions.NewManager(agentsessions.Config{
		Bin:         bin,
		ExtraPATH:   extraPath,
		MaxSessions: 4,
		Mode:        "readonly",
	})
	codex := agentsessions.NewCodexManager(agentsessions.Config{
		ExtraPATH:   extraPath,
		MaxSessions: 4,
	})
	s := &sessionService{index: idx, mgr: mgr, codex: codex}
	if ci, err := agentsessions.NewCodexIndex(); err == nil {
		s.codexIdx = ci
	}
	return s, nil
}

// allowedRoots：已有 Claude/Codex 项目 + 用户主目录。
func (s *sessionService) allowedRoots() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.allowedTime) < allowedCacheTTL && s.allowed != nil {
		return s.allowed
	}
	roots := []string{}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, home)
	}
	if projects, err := s.index.Projects(); err == nil {
		for _, p := range projects {
			if p.Cwd != "" {
				roots = append(roots, p.Cwd)
			}
		}
	}
	if s.codexIdx != nil {
		if projects, err := s.codexIdx.Projects(); err == nil {
			for _, p := range projects {
				if p.Cwd != "" {
					roots = append(roots, p.Cwd)
				}
			}
		}
	}
	s.allowed = roots
	s.allowedTime = time.Now()
	return roots
}

func (s *sessionService) cwdAllowed(cwd string) bool {
	if !filepath.IsAbs(cwd) {
		return false
	}
	clean := filepath.Clean(cwd)
	for _, root := range s.allowedRoots() {
		if pathWithin(clean, filepath.Clean(root)) {
			return true
		}
	}
	return false
}

// pathWithin 判断 p 是否等于 root 或在 root 下面。
// Windows 路径不区分大小写，而会话索引里的盘符可能是小写（例如 d:\...），
// 这里必须用 EqualFold，否则合法项目会被拒绝。
func pathWithin(p, root string) bool {
	if strings.EqualFold(p, root) {
		return true
	}
	prefix := root + string(filepath.Separator)
	return len(p) >= len(prefix) && strings.EqualFold(p[:len(prefix)], prefix)
}

func (s *sessionService) Projects(tool string) ([]remotev1.ProjectInfo, error) {
	if tool == "codex" {
		if s.codexIdx == nil {
			return nil, errors.New("codex unavailable")
		}
		list, err := s.codexIdx.Projects()
		if err != nil {
			return nil, err
		}
		out := make([]remotev1.ProjectInfo, 0, len(list))
		for _, p := range list {
			out = append(out, remotev1.ProjectInfo{Path: p.Path, Cwd: p.Cwd, Sessions: p.Sessions, UpdatedAt: p.UpdatedAt})
		}
		return out, nil
	}
	list, err := s.index.Projects()
	if err != nil {
		return nil, err
	}
	out := make([]remotev1.ProjectInfo, 0, len(list))
	for _, p := range list {
		out = append(out, remotev1.ProjectInfo{Path: p.Path, Cwd: p.Cwd, Sessions: p.Sessions, UpdatedAt: p.UpdatedAt})
	}
	return out, nil
}

func (s *sessionService) Sessions(tool, cwd string) ([]remotev1.SessionInfo, error) {
	if tool == "codex" {
		if s.codexIdx == nil {
			return nil, errors.New("codex unavailable")
		}
		list, err := s.codexIdx.Sessions(cwd)
		if err != nil {
			return nil, err
		}
		out := make([]remotev1.SessionInfo, 0, len(list))
		for _, x := range list {
			out = append(out, remotev1.SessionInfo{ID: x.ID, Title: x.Title, Cwd: x.Cwd, UpdatedAt: x.UpdatedAt})
		}
		return out, nil
	}
	list, err := s.index.Sessions(cwd)
	if err != nil {
		return nil, err
	}
	out := make([]remotev1.SessionInfo, 0, len(list))
	for _, x := range list {
		out = append(out, remotev1.SessionInfo{ID: x.ID, Title: x.Title, Cwd: x.Cwd, UpdatedAt: x.UpdatedAt})
	}
	return out, nil
}

func (s *sessionService) Start(tool, cwd, resumeID, mode string) (string, string, error) {
	if !validMode(mode) {
		return "", "", errors.New("invalid mode")
	}
	if !s.cwdAllowed(cwd) {
		return "", "", fmt.Errorf("cwd not allowed: %q", cwd)
	}
	if tool == "codex" {
		r, err := s.codex.Start(cwd, resumeID, mode)
		if err != nil {
			return "", "", err
		}
		return r.Key, r.ThreadID(), nil
	}
	r, err := s.mgr.Start(cwd, resumeID, mode)
	if err != nil {
		return "", "", err
	}
	return r.Key, r.ClaudeID(), nil
}

func (s *sessionService) claudeRunner(key string) *agentsessions.Runner { return s.mgr.Get(key) }
func (s *sessionService) codexRunner(key string) *agentsessions.CodexRunner {
	if s.codex == nil {
		return nil
	}
	return s.codex.Get(key)
}

func (s *sessionService) Send(key, text string, atts []remotev1.Attachment) error {
	if (strings.TrimSpace(text) == "" && len(atts) == 0) || len(text) > 8000 {
		return errors.New("invalid message")
	}
	if r := s.claudeRunner(key); r != nil {
		extra, images, err := s.prepareAttachments(key, atts, true)
		if err != nil {
			return err
		}
		return r.SendWith(text+extra, images)
	}
	if r := s.codexRunner(key); r != nil {
		extra, _, err := s.prepareAttachments(key, atts, false)
		if err != nil {
			return err
		}
		return r.Send(text + extra)
	}
	return errors.New("session not found")
}

func (s *sessionService) Stop(key string) error {
	defer s.cleanupAttachments(key)
	if r := s.claudeRunner(key); r != nil {
		r.Stop()
		return nil
	}
	if r := s.codexRunner(key); r != nil {
		r.Stop()
		return nil
	}
	return errors.New("session not found")
}

func (s *sessionService) Close(key string) error {
	defer s.cleanupAttachments(key)
	if r := s.claudeRunner(key); r != nil {
		r.Stop()
		return nil
	}
	if r := s.codexRunner(key); r != nil {
		r.Stop()
		return nil
	}
	return nil
}

func (s *sessionService) Decide(key, approvalID string, allow bool) error {
	r := s.claudeRunner(key)
	if r == nil {
		return errors.New("approvals unsupported")
	}
	return r.Decide(approvalID, allow)
}

func (s *sessionService) SetMode(key, mode string) (string, string, error) {
	if !validMode(mode) {
		return "", "", errors.New("invalid mode")
	}
	if r := s.claudeRunner(key); r != nil {
		nr, err := s.mgr.ChangeMode(key, mode)
		if err != nil {
			return "", "", err
		}
		return nr.Key, nr.ClaudeID(), nil
	}
	if r := s.codexRunner(key); r != nil {
		r.ChangeMode(mode)
		return r.Key, r.ThreadID(), nil
	}
	return "", "", errors.New("session not found")
}

func (s *sessionService) Subscribe(key string, since int64) ([]remotev1.SessionEvent, <-chan remotev1.SessionEvent, func(), error) {
	if r := s.claudeRunner(key); r != nil {
		history, live, cancel := r.Subscribe(since)
		return pipeEvents(history, live, cancel)
	}
	if r := s.codexRunner(key); r != nil {
		history, live, cancel := r.Subscribe(since)
		return pipeEvents(history, live, cancel)
	}
	return nil, nil, nil, errors.New("session not found")
}

func pipeEvents(history []agentsessions.Event, live <-chan agentsessions.Event, cancel func()) ([]remotev1.SessionEvent, <-chan remotev1.SessionEvent, func(), error) {
	out := make(chan remotev1.SessionEvent, 64)
	go func() {
		defer close(out)
		for ev := range live {
			out <- convertEvent(ev)
		}
	}()
	return convertEvents(history), out, cancel, nil
}

func (s *sessionService) ActiveSessions() []remotev1.ActiveInfo {
	var out []remotev1.ActiveInfo
	for _, a := range s.mgr.Active() {
		out = append(out, remotev1.ActiveInfo{Key: a.Key, ClaudeSessionID: a.ClaudeID, Cwd: a.Cwd, Mode: a.Mode, Tool: "claude"})
	}
	if s.codex != nil {
		for _, a := range s.codex.Active() {
			out = append(out, remotev1.ActiveInfo{Key: a.Key, ClaudeSessionID: a.ClaudeID, Cwd: a.Cwd, Mode: a.Mode, Tool: "codex"})
		}
	}
	return out
}

// Active 供本机控制命令查看。
func (s *sessionService) Active() []agentsessions.Active {
	list := s.mgr.Active()
	if s.codex != nil {
		list = append(list, s.codex.Active()...)
	}
	return list
}

func (s *sessionService) StopRef(ref string) bool {
	if s.mgr.StopByRef(ref) {
		return true
	}
	if s.codex != nil {
		for _, a := range s.codex.Active() {
			if a.Key == ref || a.ClaudeID == ref {
				if r := s.codex.Get(a.Key); r != nil {
					r.Stop()
					return true
				}
			}
		}
	}
	return false
}

func validMode(mode string) bool {
	switch mode {
	case "", "readonly", "edit", "full", "default", "manual", "acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions":
		return true
	}
	return false
}

func convertEvents(in []agentsessions.Event) []remotev1.SessionEvent {
	out := make([]remotev1.SessionEvent, 0, len(in))
	for _, e := range in {
		out = append(out, convertEvent(e))
	}
	return out
}

func convertEvent(e agentsessions.Event) remotev1.SessionEvent {
	return remotev1.SessionEvent{Seq: e.Seq, Kind: e.Kind, Text: e.Text, Tool: e.Tool, ToolID: e.ToolID, SessionID: e.SessionID, ApprovalID: e.ApprovalID}
}

func (s *sessionService) Shutdown() {
	s.mgr.StopAll()
	if s.codex != nil {
		s.codex.StopAll()
	}
	s.cleanupAllAttachments()
}
