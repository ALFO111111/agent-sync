package main

import (
	"os"
	"strings"
	"testing"

	"github.com/ALFO111111/agent-sync/internal/remotev1"
)

func TestPrepareAttachmentsInlineAndTemp(t *testing.T) {
	s := &sessionService{}
	defer s.cleanupAttachments("sess")
	atts := []remotev1.Attachment{
		{Name: "a.txt", Kind: "text", MediaType: "text/plain", Data: []byte("hello-inline")},
		{Name: "b.bin", Kind: "binary", MediaType: "application/octet-stream", Data: []byte{1, 2, 3}},
		{Name: "c.png", Kind: "image", MediaType: "image/png", Data: []byte("png")},
	}
	extra, images, err := s.prepareAttachments("sess", atts, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(extra, "hello-inline") {
		t.Fatalf("text not inlined: %q", extra)
	}
	if len(images) != 1 || images[0].MediaType != "image/png" {
		t.Fatalf("images = %+v", images)
	}
	const marker = "已保存到："
	i := strings.Index(extra, marker)
	if i < 0 {
		t.Fatalf("binary path missing: %q", extra)
	}
	path := extra[i+len(marker):]
	path = strings.SplitN(path, "（", 2)[0]
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("staged file missing: %v", err)
	}
	s.cleanupAttachments("sess")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("temp file not cleaned: %v", err)
	}
}

func TestPrepareAttachmentsRejectsBadImage(t *testing.T) {
	s := &sessionService{}
	if _, _, err := s.prepareAttachments("k", []remotev1.Attachment{{Name: "x", Kind: "image", MediaType: "text/plain", Data: []byte("x")}}, true); err == nil {
		t.Fatal("bad media type accepted")
	}
	big := make([]byte, maxImageBytes+1)
	if _, _, err := s.prepareAttachments("k", []remotev1.Attachment{{Name: "x", Kind: "image", MediaType: "image/png", Data: big}}, true); err == nil {
		t.Fatal("oversize image accepted")
	}
}
