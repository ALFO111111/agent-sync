package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ALFO111111/agent-sync/internal/agentsessions"
	"github.com/ALFO111111/agent-sync/internal/agentsessions/codexhistory"
	"github.com/ALFO111111/agent-sync/internal/remotev1"
)

const maxHistoryBytes = 64 << 20

// History returns a bounded, complete snapshot; oversized input is an explicit error.
func (s *sessionService) History(tool, cwd, sessionID string) ([]remotev1.HistoryMessage, error) {
	if sessionID == "" || len(sessionID) > 128 || strings.ContainsAny(sessionID, "/\\\\:") || sessionID == "." || sessionID == ".." {
		return nil, errors.New("invalid session id")
	}
	var path string
	var err error
	switch tool {
	case "codex":
		if s.codexIdx == nil {
			return nil, errors.New("codex index unavailable")
		}
		path, err = s.codexIdx.Resolve(cwd, sessionID)
	case "", "claude":
		if len(sessionID) != 36 || strings.Count(sessionID, "-") != 4 {
			return nil, errors.New("invalid Claude session id")
		}
		if s.index == nil {
			return nil, errors.New("session index unavailable")
		}
		path, err = s.index.Resolve(cwd, sessionID)
	default:
		return nil, errors.New("unsupported history tool")
	}
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, errors.New("session not found")
	}
	var f *os.File
	if tool == "codex" {
		f, err = s.codexIdx.OpenSession(path)
	} else {
		f, err = s.index.OpenSession(path)
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxHistoryBytes {
		return nil, errors.New("history: file exceeds 64 MiB or is not regular")
	}
	return readHistory(io.NewSectionReader(f, 0, fi.Size()), tool)
}

func readHistory(r io.Reader, tool string) ([]remotev1.HistoryMessage, error) {
	out := make([]remotev1.HistoryMessage, 0)
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var m remotev1.HistoryMessage
			var ok bool
			if tool == "codex" {
				v, e := codexhistory.Parse(line)
				if e != nil {
					return nil, errors.New("history: malformed Codex JSONL")
				}
				m.Role, m.Text = v.Conversation()
				m.Time = historyTime(v.Timestamp)
				ok = m.Role != "" && m.Text != ""
			} else {
				if !json.Valid(line) {
					return nil, errors.New("history: malformed Claude JSONL")
				}
				m, ok = parseHistoryLine(line)
			}
			if ok {
				out = append(out, m)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func historyTime(ts string) string {
	if t, e := time.Parse(time.RFC3339Nano, ts); e == nil {
		return t.Format(time.RFC3339)
	}
	return ""
}

func samePath(a, b string) bool { return agentsessions.SameLocalPath(a, b) }

func parseHistory(data []byte) []remotev1.HistoryMessage {
	out, _ := readHistory(bytes.NewReader(data), "claude")
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
