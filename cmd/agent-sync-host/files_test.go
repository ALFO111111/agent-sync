package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testFiles(t *testing.T, content string) (*sessionService, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &sessionService{allowed: []string{dir}, allowedTime: time.Now()}
	return s, dir
}

func TestReadChunks(t *testing.T) {
	content := strings.Repeat("a", 10*1024)
	s, dir := testFiles(t, content)
	var got strings.Builder
	var offset int64
	for i := 0; i < 3; i++ {
		text, truncated, binary, next, err := s.Read(dir, "big.txt", offset)
		if err != nil {
			t.Fatal(err)
		}
		if binary {
			t.Fatal("text file marked binary")
		}
		got.WriteString(text)
		if next <= offset {
			t.Fatalf("offset did not advance: %d", next)
		}
		offset = next
		if !truncated {
			break
		}
	}
	if got.String() != content {
		t.Fatalf("got %d bytes, want %d", got.Len(), len(content))
	}
}

func TestReadOffsetValidation(t *testing.T) {
	s, dir := testFiles(t, "hello")
	if _, _, _, _, err := s.Read(dir, "big.txt", -1); err == nil {
		t.Fatal("negative offset accepted")
	}
	if _, _, _, _, err := s.Read(dir, "big.txt", 99); err == nil {
		t.Fatal("offset past EOF accepted")
	}
}
