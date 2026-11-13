package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ALFO111111/agent-sync/internal/fsutil"
	"github.com/ALFO111111/agent-sync/internal/remotev1"
)

func TestVersionJSON(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	code := run([]string{"version"})
	os.Stdout = old
	w.Close()
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if code != 0 || len(got) != 4 || got["version"] != version || got["buildID"] != buildID || got["goos"] != runtime.GOOS || got["goarch"] != runtime.GOARCH {
		t.Fatalf("version: %s, code %d", b, code)
	}
}

func TestLifecycleLockBeforeIdentity(t *testing.T) {
	dir := t.TempDir()
	f, err := os.OpenFile(filepath.Join(dir, "host.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ok, err := fsutil.TryLockFile(f)
	if err != nil || !ok {
		t.Fatalf("lock: %v %v", ok, err)
	}
	defer fsutil.UnlockFile(f)
	if err := runDaemon(context.Background(), dir, "ws://127.0.0.1:1/ws", "ws://127.0.0.1:1/ws", "test", "missing"); err == nil {
		t.Fatal("duplicate daemon accepted")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "host.lock" {
		t.Fatalf("identity written before locking: %v", entries)
	}
}

func TestStatusIdentity(t *testing.T) {
	store, err := newFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	auth, err := remotev1.NewAuthority(store, "ws://127.0.0.1:1/ws")
	if err != nil {
		t.Fatal(err)
	}
	d := daemon{auth: auth, hostName: "test", relayURL: "ws://127.0.0.1:1/ws"}
	got := d.handle(ctlRequest{Cmd: "status"})
	if !got.OK || got.Version != version || got.BuildID != buildID || got.PID != os.Getpid() {
		t.Fatalf("status: %+v", got)
	}
}
