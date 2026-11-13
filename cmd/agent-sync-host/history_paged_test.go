package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ALFO111111/agent-sync/internal/agentsessions"
)

func TestReadHistoryCompleteLargeAndMalformed(t *testing.T) {
	text := strings.Repeat("中文", (9<<20)/6)
	line, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]string{"content": text}})
	var data strings.Builder
	data.Write(line)
	data.WriteByte('\n')
	for i := 0; i < 450; i++ {
		data.WriteString("{\"type\":\"user\",\"message\":{\"content\":\"next\"}}\n")
	}
	msgs, err := readHistory(strings.NewReader(data.String()), "claude")
	if err != nil || len(msgs) != 451 || msgs[0].Text != text {
		t.Fatalf("messages=%d err=%v", len(msgs), err)
	}
	if _, err = readHistory(strings.NewReader("{broken"), "claude"); err == nil {
		t.Fatal("must report malformed JSON")
	}
}
func TestCodexHistoryTrustedIndex(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "rollout-test.jsonl")
	data := `{"type":"session_meta","payload":{"id":"session-one","cwd":"project"}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"AGENTS.md instructions"}]}}
{"type":"event_msg","payload":{"type":"user_message","message":"# Context\n## My request for Codex:\nhello"}}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"world"}]}}
{"type":"event_msg","payload":{"type":"agent_message","message":"world"}}
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	s := &sessionService{codexIdx: &agentsessions.CodexIndex{Root: root}}
	msgs, err := s.History("codex", "project", "session-one")
	if err != nil || len(msgs) != 2 || msgs[0].Text != "hello" || msgs[1].Text != "world" {
		t.Fatalf("%+v %v", msgs, err)
	}
	for _, id := range []string{"../rollout-test", "..\\rollout-test", "unknown"} {
		if _, e := s.History("codex", "project", id); e == nil {
			t.Fatal("accepted unindexed id", id)
		}
	}
	if _, e := s.History("codex", "other", "session-one"); e == nil {
		t.Fatal("wrong cwd accepted")
	}
}
