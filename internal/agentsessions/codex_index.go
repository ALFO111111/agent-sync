package agentsessions

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ALFO111111/agent-sync/internal/agentsessions/codexhistory"
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
	root := os.Getenv("CODEX_HOME")
	if root == "" {
		root = filepath.Join(home, ".codex")
	}
	return &CodexIndex{Root: filepath.Join(root, "sessions")}, nil
}

type codexSession struct {
	Path      string
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
	root, err := openTrustedRoot(i.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer root.Close()
	var files []string
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && p == i.Root {
				return filepath.SkipAll
			}
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if err := root.check(filepath.FromSlash(p)); err != nil {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		base := d.Name()
		if strings.HasPrefix(base, "rollout-") && strings.HasSuffix(base, ".jsonl") {
			files = append(files, p)
			if len(files) > 100000 {
				return errors.New("codex: index exceeds 100000 files")
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]codexSession, 0, len(files))
	for _, f := range files {
		file, err := root.open(filepath.FromSlash(f))
		if err != nil {
			continue
		}
		s, ok := readCodexMetaFile(file, filepath.Join(i.Root, filepath.FromSlash(f)))
		file.Close()
		if ok {
			out = append(out, s)
		}
	}
	return out, nil
}

// Resolve locates a session from trusted rollout metadata, never a client path.
func (i *CodexIndex) Resolve(cwd, id string) (string, error) {
	sessions, err := i.scan()
	if err != nil {
		return "", err
	}
	found := ""
	for _, s := range sessions {
		if s.ID == id && samePath(s.Cwd, cwd) {
			if found != "" {
				return "", errors.New("codex: ambiguous session id")
			}
			found = s.Path
		}
	}
	if found == "" {
		return "", errors.New("codex: session not found")
	}
	return found, nil
}

// readCodexMeta 读取 rollout 的首行元数据和第一条用户消息作为标题。
func readCodexMeta(path string) (codexSession, bool) {
	f, err := os.Open(path)
	if err != nil {
		return codexSession{}, false
	}
	defer f.Close()
	return readCodexMetaFile(f, path)
}

func readCodexMetaFile(f *os.File, path string) (codexSession, bool) {
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return codexSession{}, false
	}
	var s codexSession
	s.Path = path
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
		raw, err := codexhistory.Parse(line)
		if err != nil {
			continue
		}
		if raw.Type == "session_meta" && s.ID == "" {
			s.ID = raw.SessionID()
			s.Cwd = raw.Payload.Cwd
			continue
		}
		if role, text := raw.Conversation(); s.Title == "" && role == "user" && text != "" {
			s.Title = truncateTitle(text)
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
