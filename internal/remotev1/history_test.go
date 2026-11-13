package remotev1

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func decodePage(t *testing.T, chunks []Envelope) ([]HistoryMessage, Envelope) {
	t.Helper()
	var data strings.Builder
	for i, c := range chunks {
		b, e := json.Marshal(c)
		if e != nil || len(b) > MaxPlain {
			t.Fatalf("oversized frame %d: %d %v", i, len(b), e)
		}
		if c.ChunkIndex != i || c.Done != (i == len(chunks)-1) {
			t.Fatalf("bad chunk %+v", c)
		}
		data.WriteString(c.Data)
	}
	var msgs []HistoryMessage
	if err := json.Unmarshal([]byte(data.String()), &msgs); err != nil {
		t.Fatal(err)
	}
	return msgs, chunks[len(chunks)-1]
}
func TestHistoryPagedSnapshotAndFragments(t *testing.T) {
	var pager historyPager
	source := make([]HistoryMessage, 450)
	for i := range source {
		source[i] = HistoryMessage{Role: "user", Text: fmt.Sprint(i)}
	}
	source[449].Text = strings.Repeat("中文\"\\\n<>&", 10000)
	env := Envelope{ID: "r", Tool: "codex", Cwd: "project", ClaudeSessionID: "session", Limit: 200}
	calls := 0
	load := func() ([]HistoryMessage, error) { calls++; return append([]HistoryMessage(nil), source...), nil }
	chunks, err := pager.page(env, load)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 2 {
		t.Fatal("single message must fragment")
	}
	msgs, last := decodePage(t, chunks)
	if len(msgs) != 200 || msgs[0].Text != "250" || msgs[199].Text != source[449].Text {
		t.Fatal("bad latest")
	}
	source = append(source, HistoryMessage{Role: "user", Text: "new append"})
	env.Cursor = last.Cursor
	chunks, err = pager.page(env, load)
	if err != nil {
		t.Fatal(err)
	}
	msgs, last = decodePage(t, chunks)
	if len(msgs) != 200 || msgs[0].Text != "50" || msgs[199].Text != "249" {
		t.Fatal("unstable old page")
	}
	env.Cursor = last.Cursor
	chunks, err = pager.page(env, load)
	if err != nil {
		t.Fatal(err)
	}
	msgs, last = decodePage(t, chunks)
	if len(msgs) != 50 || last.HasMore || last.Cursor != "" || calls != 1 {
		t.Fatal("bad final page")
	}
	env.Cursor = pager.snapshots[0].token + ":400"
	env.Cwd = "other"
	if _, err = pager.page(env, load); err == nil {
		t.Fatal("cross session cursor")
	}
	env.Cwd = "project"
	pager.snapshots[0].expires = time.Now().Add(-time.Second)
	if _, err = pager.page(env, load); err == nil {
		t.Fatal("expired snapshot")
	}
}
func TestHistoryMoreThan200Pages(t *testing.T) {
	var p historyPager
	source := make([]HistoryMessage, 250)
	for i := range source {
		source[i] = HistoryMessage{Role: "user", Text: fmt.Sprint(i)}
	}
	env := Envelope{Limit: 1}
	load := func() ([]HistoryMessage, error) { return source, nil }
	for i := 249; i >= 0; i-- {
		chunks, err := p.page(env, load)
		if err != nil {
			t.Fatal(err)
		}
		msgs, last := decodePage(t, chunks)
		if len(msgs) != 1 || msgs[0].Text != fmt.Sprint(i) {
			t.Fatal("missing or duplicate message", i)
		}
		env.Cursor = last.Cursor
	}
	if env.Cursor != "" {
		t.Fatal("unexpected remaining page")
	}
}

func TestHistoryLegacyBounded(t *testing.T) {
	resp := legacyHistory(Envelope{ID: "request"}, []HistoryMessage{{Role: "user", Text: strings.Repeat("x", MaxPlain)}}, nil)
	if resp.Type != "session.error" {
		t.Fatalf("%+v", resp)
	}
	b, _ := json.Marshal(resp)
	if len(b) > MaxPlain {
		t.Fatal("oversized error")
	}
}
func TestHistorySnapshotBound(t *testing.T) {
	var p historyPager
	for i := 0; i < 6; i++ {
		if _, err := p.page(Envelope{}, func() ([]HistoryMessage, error) { return nil, nil }); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.snapshots) != 4 {
		t.Fatal(len(p.snapshots))
	}
}
