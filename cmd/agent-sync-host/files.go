package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ALFO111111/agent-sync/internal/remotev1"
)

const (
	maxFileRead = 256 * 1024
	maxList     = 1000
)

// resolveFile 把手机传来的相对路径限制在授权项目目录内（含符号链接检查）。
func (s *sessionService) resolveFile(cwd, rel string) (string, string, error) {
	if !s.cwdAllowed(cwd) {
		return "", "", errors.New("cwd not allowed")
	}
	if filepath.IsAbs(rel) {
		return "", "", errors.New("path must be relative")
	}
	root, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", "", err
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." {
		clean = ""
	}
	target := filepath.Join(root, clean)
	if !within(root, target) {
		return "", "", errors.New("path escapes project")
	}
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		if !within(root, resolved) {
			return "", "", errors.New("path escapes project")
		}
		target = resolved
	}
	return root, target, nil
}

func within(root, target string) bool {
	r := strings.ToLower(filepath.Clean(root))
	t := strings.ToLower(filepath.Clean(target))
	return t == r || strings.HasPrefix(t, r+string(filepath.Separator))
}

func (s *sessionService) List(cwd, rel string) ([]remotev1.FileEntry, error) {
	_, target, err := s.resolveFile(cwd, rel)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(target)
	if err != nil {
		return nil, err
	}
	out := make([]remotev1.FileEntry, 0, len(ents))
	for _, e := range ents {
		if len(out) >= maxList {
			break
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, remotev1.FileEntry{
			Name: e.Name(), Dir: e.IsDir(), Size: info.Size(), ModTime: info.ModTime(),
		})
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Dir != out[b].Dir {
			return out[a].Dir
		}
		return strings.ToLower(out[a].Name) < strings.ToLower(out[b].Name)
	})
	return out, nil
}

func (s *sessionService) Read(cwd, rel string) (string, bool, bool, error) {
	_, target, err := s.resolveFile(cwd, rel)
	if err != nil {
		return "", false, false, err
	}
	fi, err := os.Stat(target)
	if err != nil {
		return "", false, false, err
	}
	if fi.IsDir() {
		return "", false, false, errors.New("is a directory")
	}
	f, err := os.Open(target)
	if err != nil {
		return "", false, false, err
	}
	defer f.Close()
	buf := make([]byte, maxFileRead)
	n, _ := f.Read(buf)
	buf = buf[:n]
	truncated := fi.Size() > int64(n)
	binary := bytes.IndexByte(buf[:min(len(buf), 8192)], 0) >= 0
	return string(buf), truncated, binary, nil
}

func gitRun(root string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), errors.New(msg)
	}
	return out.String(), nil
}

func (s *sessionService) GitStatus(cwd string) ([]remotev1.GitChange, error) {
	root, _, err := s.resolveFile(cwd, "")
	if err != nil {
		return nil, err
	}
	out, err := gitRun(root, "status", "--porcelain=v1", "-z")
	if err != nil {
		return nil, err
	}
	fields := strings.Split(out, "\x00")
	var changes []remotev1.GitChange
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 {
			continue
		}
		status := strings.TrimSpace(f[:2])
		path := f[3:]
		if f[0] == 'R' || f[0] == 'C' {
			// 重命名条目后面跟着原路径
			if i+1 < len(fields) {
				i++
			}
		}
		changes = append(changes, remotev1.GitChange{Path: filepath.ToSlash(path), Status: status})
		if len(changes) >= maxList {
			break
		}
	}
	return changes, nil
}

func (s *sessionService) GitDiff(cwd, rel string) (string, bool, error) {
	root, target, err := s.resolveFile(cwd, "")
	if err != nil {
		return "", false, err
	}
	args := []string{"diff", "--no-color", "HEAD"}
	if rel != "" {
		args = append(args, "--", filepath.ToSlash(rel))
	}
	out, derr := gitRun(root, args...)
	if derr != nil {
		// 还没有任何提交时没有 HEAD
		args = []string{"diff", "--no-color"}
		if rel != "" {
			args = append(args, "--", filepath.ToSlash(rel))
		}
		out, _ = gitRun(root, args...)
	}
	if strings.TrimSpace(out) == "" && rel != "" {
		// 未跟踪文件：把内容作为新增展示
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
			if content, _, binary, err := s.Read(cwd, rel); err == nil && !binary {
				var b strings.Builder
				for _, line := range strings.Split(strings.TrimRight(content, "\n"), "\n") {
					b.WriteString("+" + line + "\n")
				}
				out = "--- /dev/null\n+++ b/" + filepath.ToSlash(rel) + "\n" + b.String()
			}
		}
	}
	_ = target
	truncated := false
	if len(out) > maxFileRead {
		out = out[:maxFileRead]
		truncated = true
	}
	return out, truncated, nil
}
