package remotev1

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeFiles struct{}

func (fakeFiles) List(cwd, rel string) ([]FileEntry, error) { return nil, nil }

func (fakeFiles) Read(cwd, rel string, offset int64) (string, bool, bool, int64, error) {
	if offset == 0 {
		return strings.Repeat("a", 4096), true, false, 4096, nil
	}
	if offset == 4096 {
		return "tail", false, false, 4100, nil
	}
	return "", false, false, 0, errors.New("invalid offset")
}

func (fakeFiles) GitStatus(cwd string) ([]GitChange, error) { return nil, nil }

func (fakeFiles) GitDiff(cwd, rel string) (string, bool, error) { return "", false, nil }

// 加密连接下 fs.read 的分块字段必须能正确往返。
func TestEncryptedFileChunks(t *testing.T) {
	f := setup(t)
	f.host.Files = fakeFiles{}
	device, _ := NewIdentity()
	c := NewClient(device, "Pixel", f.inv)
	done := make(chan error, 1)
	go func() { done <- c.Pair(context.Background()) }()
	approveNext(t, f)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if err := c.s.write(Envelope{Type: "fs.read", ID: "1", Cwd: `C:\project`, Path: "big"}); err != nil {
		t.Fatal(err)
	}
	first, err := c.s.readWith(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if first.Type != "fs.read" || !first.Truncated || first.Next != 4096 || len(first.Text) != 4096 {
		t.Fatalf("first chunk = %+v", first)
	}

	if err := c.s.write(Envelope{Type: "fs.read", ID: "2", Cwd: `C:\project`, Path: "big", Offset: 4096}); err != nil {
		t.Fatal(err)
	}
	second, err := c.s.readWith(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if second.Type != "fs.read" || second.Truncated || second.Next != 4100 || second.Text != "tail" {
		t.Fatalf("second chunk = %+v", second)
	}
}
