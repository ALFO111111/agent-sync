package remotev1

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
)

func TestAttachmentReassembly(t *testing.T) {
	store := newAttachmentStore(t.TempDir())
	data := bytes.Repeat([]byte("a"), 200000)
	sum := sha256.Sum256(data)
	if err := store.Begin("a1", int64(len(data)), hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	for off, seq := 0, 0; off < len(data); off, seq = off+MQTTAttachChunk, seq+1 {
		end := off + MQTTAttachChunk
		if end > len(data) {
			end = len(data)
		}
		if err := store.Write("a1", seq, data[off:end]); err != nil {
			t.Fatal(err)
		}
	}
	path, err := store.Commit("a1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("reassembled data mismatch")
	}
}
