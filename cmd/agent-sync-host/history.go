package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ALFO111111/agent-sync/internal/remotev1"
)

const (
	maxHistoryBytes = 8 << 20
	maxHistoryMsgs  = 200
)

// History 读取 Claude 会话文件，返回进入老会话时要显示的历史消息。
func (s *sessionService) History(tool, cwd, sessionID string) ([]remotev1.HistoryMessage, error) {
	if tool == "codex" {
		return nil, errors.New("history unsupported for codex")
	}
	if sessionID == "" {
		return nil, errors.New("missing session id")
	}
	if s.index == nil {
		return nil, errors.New("session index unavailable")
	}
	projects, err := s.index.Projects()
	if err != nil {
		return nil, err
	}
	var path string
	for _, p := range projects {
		if samePath(p.Cwd, cwd) {
			path = filepath.Join(p.Path, sessionID+".jsonl")
			break
		}
	}
	if path == "" {
		return nil, errors.New("session not found")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxHistoryBytes))
	if err != nil {
		return nil, err
	}
	return parseHistory(data), nil
}

func samePath(a, b string) bool {
	clean := func(p string) string {
		return strings.TrimRight(filepath.Clean(p), string(filepath.Separator))
	}
	return strings.EqualFold(clean(a), clean(b))
}

func parseHistory(data []byte) []remotev1.HistoryMessage {
	out := make([]remotev1.HistoryMessage, 0, 64)
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if m, ok := parseHistoryLine(sc.Bytes()); ok {
			out = append(out, m)
		}
	}
	if len(out) > maxHistoryMsgs {
		out = out[len(out)-maxHistoryMsgs:]
	}
	return out
}

func parseHistoryLine(line []byte) (remotev1.HistoryMessage, bool) {
	var v struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		Message   struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &v) != nil || (v.Type != "user" && v.Type != "assistant") {
		return remotev1.HistoryMessage{}, false
	}
	var text string
	if json.Unmarshal(v.Message.Content, &text) != nil {
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(v.Message.Content, &parts) != nil {
			return remotev1.HistoryMessage{}, false
		}
		var texts []string
		for _, p := range parts {
			if p.Type == "text" && p.Text != "" {
				texts = append(texts, p.Text)
			}
		}
		text = strings.Join(texts, "\n")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return remotev1.HistoryMessage{}, false
	}
	ts := ""
	if t, err := time.Parse(time.RFC3339Nano, v.Timestamp); err == nil {
		ts = t.Format(time.RFC3339)
	}
	return remotev1.HistoryMessage{Role: v.Type, Text: text, Time: ts}, true
}
