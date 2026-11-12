// Package agentsessions 在电脑端管理 Claude Code 的会话：
// 列项目/会话、启动或续聊、把流式事件推给手机。
//
// v2.0 固定只读（dontAsk + 只读工具），审批和权限切换在 v2.1。
package agentsessions

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ALFO111111/agent-sync/internal/project"
)

const (
	maxSessions   = 2000
	maxTitleBytes = 256 * 1024
)

// Project 是 ~/.claude/projects 下的一个项目目录。
type Project struct {
	Path      string    `json:"path"`
	Cwd       string    `json:"cwd"`
	Sessions  int       `json:"sessions"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Session 是一条 Claude Code 会话。
type Session struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Cwd       string    `json:"cwd"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Index 扫描本地会话目录，只读。
type Index struct {
	Root string
}

// NewIndex 使用默认的 ~/.claude/projects。
func NewIndex() (*Index, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &Index{Root: filepath.Join(home, ".claude", "projects")}, nil
}

// Projects 列出有会话的项目，按最近更新排序。
func (i *Index) Projects() ([]Project, error) {
	entries, err := os.ReadDir(i.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Project
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(i.Root, e.Name())
		files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
		count := 0
		var newest time.Time
		for _, f := range files {
			base := filepath.Base(f)
			if !looksLikeSession(base) {
				continue
			}
			count++
			if fi, err := os.Stat(f); err == nil && fi.ModTime().After(newest) {
				newest = fi.ModTime()
			}
		}
		if count == 0 {
			continue
		}
		out = append(out, Project{
			Path:      dir,
			Cwd:       project.ReadCwd(dir),
			Sessions:  count,
			UpdatedAt: newest,
		})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].UpdatedAt.After(out[b].UpdatedAt) })
	return out, nil
}

// Sessions 列出会话；cwd 为空表示所有项目。
func (i *Index) Sessions(cwd string) ([]Session, error) {
	projects, err := i.Projects()
	if err != nil {
		return nil, err
	}
	var out []Session
	for _, p := range projects {
		if cwd != "" && !samePath(p.Cwd, cwd) {
			continue
		}
		files, _ := filepath.Glob(filepath.Join(p.Path, "*.jsonl"))
		for _, f := range files {
			base := filepath.Base(f)
			if !looksLikeSession(base) {
				continue
			}
			fi, err := os.Stat(f)
			if err != nil {
				continue
			}
			out = append(out, Session{
				ID:        strings.TrimSuffix(base, ".jsonl"),
				Title:     sessionTitle(f),
				Cwd:       p.Cwd,
				UpdatedAt: fi.ModTime(),
			})
			if len(out) >= maxSessions {
				break
			}
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].UpdatedAt.After(out[b].UpdatedAt) })
	return out, nil
}

func looksLikeSession(base string) bool {
	if !strings.HasSuffix(base, ".jsonl") || strings.HasPrefix(base, "agent-") {
		return false
	}
	id := strings.TrimSuffix(base, ".jsonl")
	return len(id) == 36 && strings.Count(id, "-") == 4
}

// sessionTitle 从会话文件里找第一条用户文本作为标题。
func sessionTitle(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64*1024)
	var read int
	for read < maxTitleBytes {
		line, err := r.ReadBytes('\n')
		read += len(line)
		if len(line) > 0 {
			var v struct {
				Type    string `json:"type"`
				Message struct {
					Content json.RawMessage `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &v) == nil && v.Type == "user" {
				if t := contentText(v.Message.Content); t != "" {
					return truncateTitle(t)
				}
			}
		}
		if err != nil {
			break
		}
	}
	return ""
}

func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				return b.Text
			}
		}
	}
	return ""
}

func truncateTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > 80 {
		r = append(r[:80], '…')
	}
	return string(r)
}

func samePath(a, b string) bool {
	clean := func(p string) string { return strings.TrimRight(filepath.Clean(p), string(filepath.Separator)) }
	return strings.EqualFold(clean(a), clean(b))
}
