package agentsessions

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseEvent(t *testing.T) {
	cases := []struct {
		line string
		kind string
		text string
		tool string
	}{
		{`{"type":"system","subtype":"init","session_id":"sess-1"}`, "init", "", ""},
		{`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"你"}}}`, "text_delta", "你", ""},
		{`{"type":"assistant","message":{"content":[{"type":"text","text":"你好"}]}}`, "text", "你好", ""},
		{`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/a/b"}}]}}`, "tool_use", "/a/b", "Read"},
		{`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"ok"}]}]}}`, "tool_result", "ok", ""},
		{`{"type":"system","subtype":"permission_denied","tool_name":"Bash","reason":"只读"}`, "permission_denied", "只读", "Bash"},
		{`{"type":"result","subtype":"success","result":"完成","session_id":"sess-1"}`, "result", "完成", ""},
	}
	for _, tc := range cases {
		ev, ok := parseEvent([]byte(tc.line))
		if !ok || ev.Kind != tc.kind {
			t.Fatalf("%s -> %+v ok=%v", tc.line, ev, ok)
		}
		if tc.text != "" && ev.Text != tc.text {
			t.Fatalf("text %q want %q", ev.Text, tc.text)
		}
		if tc.tool != "" && ev.Tool != tc.tool {
			t.Fatalf("tool %q want %q", ev.Tool, tc.tool)
		}
	}
	if _, ok := parseEvent([]byte(`{"type":"system","subtype":"hook_started"}`)); ok {
		t.Fatal("hook event should be ignored")
	}
}

func TestRunnerRingAndSubscribe(t *testing.T) {
	r := &Runner{subs: map[chan Event]bool{}}
	for i := 0; i < 5; i++ {
		r.append(Event{Kind: "text", Text: "x"})
	}
	history, seq := r.Snapshot(2)
	if seq != 5 || len(history) != 3 || history[0].Seq != 3 {
		t.Fatalf("snapshot seq=%d len=%d", seq, len(history))
	}
	hist2, live, cancel := r.Subscribe(4)
	if len(hist2) != 1 {
		t.Fatalf("subscribe history %d", len(hist2))
	}
	r.append(Event{Kind: "text", Text: "live"})
	select {
	case ev := <-live:
		if ev.Seq != 6 {
			t.Fatalf("live seq %d", ev.Seq)
		}
	case <-time.After(time.Second):
		t.Fatal("no live event")
	}
	cancel()
	if _, ok := <-live; ok {
		t.Fatal("channel not closed")
	}
}

func TestIndexSessions(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "-tmp-project")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := "12345678-1234-1234-1234-123456789abc"
	line := `{"type":"user","cwd":"/tmp/project","message":{"content":[{"type":"text","text":"第一句问题"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	// 子代理文件应被忽略。
	_ = os.WriteFile(filepath.Join(dir, "agent-abc.jsonl"), []byte(line), 0o600)

	idx := &Index{Root: root}
	projects, err := idx.Projects()
	if err != nil || len(projects) != 1 || projects[0].Cwd != "/tmp/project" || projects[0].Sessions != 1 {
		t.Fatalf("projects %+v err=%v", projects, err)
	}
	sessions, err := idx.Sessions("/tmp/project")
	if err != nil || len(sessions) != 1 || sessions[0].Title != "第一句问题" || sessions[0].ID != id {
		t.Fatalf("sessions %+v err=%v", sessions, err)
	}
}

// 用假的 claude 脚本验证进程管理（Windows 无 sh，跳过）。
func TestManagerFakeProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-claude")
	body := `#!/bin/sh
echo '{"type":"system","subtype":"init","session_id":"fake-session"}'
while IFS= read -r line; do
  echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}}'
  echo '{"type":"result","subtype":"success","result":"done","session_id":"fake-session"}'
  break
done
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	m := NewManager(Config{Bin: script, Mode: "readonly"})
	r, err := m.Start(dir, "", "readonly")
	if err != nil {
		t.Fatal(err)
	}
	history, live, cancel := r.Subscribe(0)
	defer cancel()
	if len(history) == 0 {
		select {
		case ev := <-live:
			if ev.Kind != "init" {
				t.Fatalf("first event %+v", ev)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("no init event")
		}
	}
	if err := r.Send("hello"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-live:
			if !ok {
				t.Fatal("channel closed early")
			}
			if ev.Kind == "result" {
				if !strings.Contains(ev.Text, "done") {
					t.Fatalf("result %q", ev.Text)
				}
				r.Stop()
				return
			}
		case <-deadline:
			t.Fatal("timeout waiting result")
		}
	}
}

func TestUserContent(t *testing.T) {
	if got := UserContent("hello", nil); got != "hello" {
		t.Fatalf("plain text = %#v", got)
	}
	img := ImageAttachment{MediaType: "image/png", Data: []byte("abc")}
	raw := UserContent("看图", []ImageAttachment{img})
	blocks, ok := raw.([]map[string]any)
	if !ok || len(blocks) != 2 || blocks[0]["type"] != "text" || blocks[1]["type"] != "image" {
		t.Fatalf("blocks = %#v", raw)
	}
	src := blocks[1]["source"].(map[string]any)
	if src["media_type"] != "image/png" || src["data"] != "YWJj" {
		t.Fatalf("image source = %#v", src)
	}
	// 只发图片、没有文字：不应出现空文本块。
	raw = UserContent("  ", []ImageAttachment{img})
	blocks = raw.([]map[string]any)
	if len(blocks) != 1 || blocks[0]["type"] != "image" {
		t.Fatalf("image-only blocks = %#v", blocks)
	}
}
