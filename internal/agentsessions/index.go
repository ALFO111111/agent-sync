// Package agentsessions 在电脑端管理 Claude Code 的会话：
// 列项目/会话、启动或续聊、把流式事件推给手机。
//
// v2.0 固定只读（dontAsk + 只读工具），审批和权限切换在 v2.1。
package agentsessions

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
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
	root, err := openTrustedRoot(i.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
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
		if root.check(e.Name()) != nil {
			continue
		}
		dir := filepath.Join(i.Root, e.Name())
		files, _ := fs.ReadDir(root.FS(), e.Name())
		count := 0
		var newest time.Time
		var cwdUpdated time.Time
		var cwd string
		for _, entry := range files {
			if !entry.Type().IsRegular() || !looksLikeSession(entry.Name()) {
				continue
			}
			f, err := root.open(filepath.Join(e.Name(), entry.Name()))
			if err != nil {
				continue
			}
			fi, err := f.Stat()
			if err == nil && fi.Mode().IsRegular() {
				count++
				if fi.ModTime().After(newest) {
					newest = fi.ModTime()
				}
				if fi.ModTime().After(cwdUpdated) {
					if value := sessionCwd(f); value != "" {
						cwd, cwdUpdated = value, fi.ModTime()
					}
				}
			}
			f.Close()
		}
		if count == 0 {
			continue
		}
		out = append(out, Project{
			Path:      dir,
			Cwd:       cwd,
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
	root, err := openTrustedRoot(i.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer root.Close()
	var out []Session
	for _, p := range projects {
		if cwd != "" && !samePath(p.Cwd, cwd) {
			continue
		}
		rel, err := filepath.Rel(i.Root, p.Path)
		if err != nil || root.check(rel) != nil {
			continue
		}
		files, _ := fs.ReadDir(root.FS(), filepath.ToSlash(rel))
		for _, entry := range files {
			base := entry.Name()
			f := filepath.Join(p.Path, base)
			if !looksLikeSession(base) {
				continue
			}
			file, err := root.openPath(f)
			if err != nil {
				continue
			}
			fi, err := file.Stat()
			if err != nil || !fi.Mode().IsRegular() {
				file.Close()
				continue
			}
			title := sessionTitleReader(file)
			file.Close()
			out = append(out, Session{
				ID:        strings.TrimSuffix(base, ".jsonl"),
				Title:     title,
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
	return sessionTitleReader(f)
}

func sessionTitleReader(f io.Reader) string {
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

func samePath(a, b string) bool { return SameLocalPath(a, b) }

func sessionCwd(f io.Reader) string {
	r := bufio.NewReaderSize(f, 64*1024)
	for n := 0; n < 200; n++ {
		line, err := r.ReadBytes('\n')
		var v struct {
			Cwd string `json:"cwd"`
		}
		if json.Unmarshal(line, &v) == nil && v.Cwd != "" {
			return v.Cwd
		}
		if err != nil {
			break
		}
	}
	return ""
}

// Resolve locates a Claude session by indexed cwd and ID, never by a client path.
func (i *Index) Resolve(cwd, id string) (string, error) {
	if !looksLikeSession(id+".jsonl") || strings.ContainsAny(id, `/\\:`) {
		return "", errors.New("invalid Claude session id")
	}
	projects, err := i.Projects()
	if err != nil {
		return "", err
	}
	root, err := openTrustedRoot(i.Root)
	if err != nil {
		return "", err
	}
	defer root.Close()
	found := ""
	for _, p := range projects {
		if !samePath(p.Cwd, cwd) {
			continue
		}
		path := filepath.Join(p.Path, id+".jsonl")
		f, e := root.openPath(path)
		if e != nil {
			continue
		}
		f.Close()
		if found != "" {
			return "", errors.New("ambiguous session id")
		}
		found = path
	}
	if found == "" {
		return "", errors.New("session not found")
	}
	return found, nil
}

// OpenSession revalidates the indexed path below its trusted root at open time.
func (i *Index) OpenSession(path string) (*os.File, error) { return openIndexedSession(i.Root, path) }
func (i *CodexIndex) OpenSession(path string) (*os.File, error) {
	return openIndexedSession(i.Root, path)
}
func openIndexedSession(rootPath, path string) (*os.File, error) {
	root, err := openTrustedRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.openPath(path)
}
