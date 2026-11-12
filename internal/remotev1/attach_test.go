package remotev1

import (
	"bytes"
	"strings"
	"testing"
)

func TestAttachmentUpload(t *testing.T) {
	s := &session{}
	id, chunk, err := s.beginAttachment("photo.png", "image/png", "image", 5)
	if err != nil || id == "" || chunk != AttachChunkSize {
		t.Fatalf("begin = %q %d %v", id, chunk, err)
	}
	if err := s.addAttachmentChunk(id, 0, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := s.addAttachmentChunk(id, 0, []byte("x")); err == nil {
		t.Fatal("duplicate seq accepted")
	}
	att, err := s.finishAttachment(id)
	if err != nil || att == nil {
		t.Fatal(err)
	}
	if string(att.Data) != "hello" || att.Kind != "image" || att.MediaType != "image/png" {
		t.Fatalf("att = %+v", att)
	}
	// finish 之后仍要能在 turn.send 里被取到，取一次后失效。
	atts, err := s.takeAttachments([]string{id})
	if err != nil || len(atts) != 1 {
		t.Fatalf("take after finish = %+v %v", atts, err)
	}
	if _, err := s.takeAttachments([]string{id}); err == nil {
		t.Fatal("attachment not consumed")
	}
}

func TestAttachmentTake(t *testing.T) {
	s := &session{}
	id, _, _ := s.beginAttachment("a.txt", "text/plain", "text", 3)
	if err := s.addAttachmentChunk(id, 0, []byte("abc")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.finishAttachment(id); err != nil {
		t.Fatal(err)
	}
	atts, err := s.takeAttachments([]string{id})
	if err != nil || len(atts) != 1 || string(atts[0].Data) != "abc" {
		t.Fatalf("take = %+v %v", atts, err)
	}
	if _, err := s.takeAttachments([]string{id}); err == nil {
		t.Fatal("attachment not consumed")
	}
}

func TestAttachmentRules(t *testing.T) {
	s := &session{}
	if _, _, err := s.beginAttachment("x", "image/png", "image", 0); err == nil {
		t.Fatal("empty accepted")
	}
	if _, _, err := s.beginAttachment("x", "image/png", "image", MaxAttachmentSize+1); err == nil {
		t.Fatal("oversize accepted")
	}
	if _, _, err := s.beginAttachment("x", "image/png", "exe", 10); err == nil {
		t.Fatal("bad kind accepted")
	}
	id, _, _ := s.beginAttachment("../../evil.txt", "text/plain", "text", 1)
	if s.uploads[id].name != "evil.txt" {
		t.Fatalf("name = %q", s.uploads[id].name)
	}
	// 长度和序号必须严格匹配。
	if err := s.addAttachmentChunk(id, 1, []byte("a")); err == nil {
		t.Fatal("gap accepted")
	}
	if err := s.addAttachmentChunk(id, 0, []byte("too long")); err == nil {
		t.Fatal("oversize chunk accepted")
	}
	if _, err := s.finishAttachment(id); err == nil {
		t.Fatal("incomplete accepted")
	}
}

func TestSanitizeAttachmentName(t *testing.T) {
	cases := map[string]string{
		"../../a.txt":            "a.txt",
		`C:\tmp\x.png`:           "x.png",
		"a\x00b\n.jpg":           "ab.jpg",
		"":                       "attachment",
		strings.Repeat("x", 200): strings.Repeat("x", 120),
	}
	for in, want := range cases {
		if got := sanitizeAttachmentName(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestAttachmentDataRoundTrip(t *testing.T) {
	// 分块边界：6 KiB 一块，最后一块可以更短。
	data := bytes.Repeat([]byte("z"), AttachChunkSize*2+17)
	s := &session{}
	id, chunk, _ := s.beginAttachment("big.bin", "application/octet-stream", "binary", int64(len(data)))
	for seq, off := int64(0), 0; off < len(data); seq, off = seq+1, off+chunk {
		end := off + chunk
		if end > len(data) {
			end = len(data)
		}
		if err := s.addAttachmentChunk(id, seq, data[off:end]); err != nil {
			t.Fatal(err)
		}
	}
	att, err := s.finishAttachment(id)
	if err != nil || !bytes.Equal(att.Data, data) {
		t.Fatalf("data mismatch: %v", err)
	}
}
