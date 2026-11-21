package remotev1

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestChunkSessionEventsFramesEveryEvent(t *testing.T) {
	events := make([]SessionEvent, 2000)
	for i := range events {
		events[i] = SessionEvent{Seq: int64(i + 1), Kind: "text", Text: strings.Repeat("x", 500)}
	}
	frames := chunkSessionEvents("req-1", "sess-1", events)
	if len(frames) < 2 {
		t.Fatalf("expected multiple frames, got %d", len(frames))
	}
	total := 0
	for i, f := range frames {
		if f.ChunkIndex != i {
			t.Fatalf("frame %d chunkIndex = %d", i, f.ChunkIndex)
		}
		if f.Done != (i == len(frames)-1) {
			t.Fatalf("frame %d done = %v", i, f.Done)
		}
		if f.ID != "req-1" || f.SessionKey != "sess-1" {
			t.Fatalf("frame %d lost request fields: %+v", i, f)
		}
		b, err := json.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		if len(b) > MaxPlain {
			t.Fatalf("frame %d is %d bytes, over MaxPlain", i, len(b))
		}
		total += len(f.Events)
	}
	if total != len(events) {
		t.Fatalf("total events = %d want %d", total, len(events))
	}
	if frames[len(frames)-1].Events[len(frames[len(frames)-1].Events)-1].Seq != 2000 {
		t.Fatal("last event missing")
	}
}

func TestChunkSessionEventsEmptyStillTerminates(t *testing.T) {
	frames := chunkSessionEvents("id", "key", nil)
	if len(frames) != 1 || !frames[0].Done || frames[0].ChunkIndex != 0 {
		t.Fatalf("empty chunk = %+v", frames)
	}
}
