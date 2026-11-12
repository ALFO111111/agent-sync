package agentsessions

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ALFO111111/agent-sync/internal/i18n"
)

// CodexIndex 扫描 ~/.codex/sessions 下的 rollout 会话。
type CodexIndex struct {
	Root string
}

func NewCodexIndex() (*CodexIndex, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &CodexIndex{Root: filepath.Join(home, ".codex", "sessions")}, nil
}

type codexSession struct {
	ID        string
	Title     string
	Cwd       string
	UpdatedAt time.Time
}

// Projects 汇总 Codex 会话的项目。
func (i *CodexIndex) Projects() ([]Project, error) {
	sessions, err := i.scan()
	if err != nil {
		return nil, err
	}
	byCwd := map[string]*Project{}
	for _, s := range sessions {
		if s.Cwd == "" {
			continue
		}
		p := byCwd[s.Cwd]
		if p == nil {
			p = &Project{Cwd: s.Cwd, Path: i.Root}
			byCwd[s.Cwd] = p
		}
		p.Sessions++
		if s.UpdatedAt.After(p.UpdatedAt) {
			p.UpdatedAt = s.UpdatedAt
		}
	}
	out := make([]Project, 0, len(byCwd))
	for _, p := range byCwd {
		out = append(out, *p)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].UpdatedAt.After(out[b].UpdatedAt) })
	return out, nil
}

// Sessions 列出 Codex 会话；cwd 为空表示全部。
func (i *CodexIndex) Sessions(cwd string) ([]Session, error) {
	sessions, err := i.scan()
	if err != nil {
		return nil, err
	}
	var out []Session
	for _, s := range sessions {
		if cwd != "" && !samePath(s.Cwd, cwd) {
			continue
		}
		out = append(out, Session{ID: s.ID, Title: s.Title, Cwd: s.Cwd, UpdatedAt: s.UpdatedAt})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].UpdatedAt.After(out[b].UpdatedAt) })
	return out, nil
}

func (i *CodexIndex) scan() ([]codexSession, error) {
	var files []string
	err := filepath.WalkDir(i.Root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		base := d.Name()
		if strings.HasPrefix(base, "rollout-") && strings.HasSuffix(base, ".jsonl") {
			files = append(files, p)
			if len(files) >= 2000 {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]codexSession, 0, len(files))
	for _, f := range files {
		if s, ok := readCodexMeta(f); ok {
			out = append(out, s)
		}
	}
	return out, nil
}

// readCodexMeta 读取 rollout 的首行元数据和第一条用户消息作为标题。
func readCodexMeta(path string) (codexSession, bool) {
	f, err := os.Open(path)
	if err != nil {
		return codexSession{}, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return codexSession{}, false
	}
	var s codexSession
	s.UpdatedAt = fi.ModTime()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 256*1024), 1024*1024)
	var read int
	for sc.Scan() {
		line := sc.Bytes()
		read += len(line)
		if read > 512*1024 {
			break
		}
		var raw struct {
			Type    string `json:"type"`
			Payload struct {
				SessionID string `json:"session_id"`
				Cwd       string `json:"cwd"`
				Type      string `json:"type"`
				Role      string `json:"role"`
				Content   []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &raw) != nil {
			continue
		}
		if raw.Type == "session_meta" {
			s.ID = raw.Payload.SessionID
			s.Cwd = raw.Payload.Cwd
			continue
		}
		if s.Title == "" && raw.Type == "response_item" && raw.Payload.Type == "message" && raw.Payload.Role == "user" {
			for _, c := range raw.Payload.Content {
				if c.Type == "input_text" && c.Text != "" && !strings.HasPrefix(strings.TrimSpace(c.Text), "<environment_context>") {
					s.Title = truncateTitle(c.Text)
					break
				}
			}
		}
		if s.ID != "" && s.Title != "" {
			break
		}
	}
	if s.ID == "" {
		return codexSession{}, false
	}
	if s.Title == "" {
		s.Title = i18n.T("(无标题)")
	}
	return s, true
}
