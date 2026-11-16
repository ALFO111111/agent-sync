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
	if err := runDaemon(context.Background(), dir, "wss://example/mqtt", "selfhost", "", "", "test", "missing"); err == nil {
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

type fakeRegistrar struct{}

func (fakeRegistrar) RegisterDevice(context.Context, string, string, string, string) error {
	return nil
}
func (fakeRegistrar) PairToken(context.Context, string) (string, string, error) {
	return "pair-user", "pair-pass", nil
}
func (fakeRegistrar) MQTTCredentials(context.Context, string) (string, string, error) {
	return "mqtt-user", "mqtt-pass", nil
}

func TestInviteCarriesPairCredentials(t *testing.T) {
	store, err := newFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	auth, err := remotev1.NewAuthority(store, "wss://example/mqtt")
	if err != nil {
		t.Fatal(err)
	}
	host := &remotev1.Host{Authority: auth, Auth: fakeRegistrar{}}
	d := daemon{auth: auth, host: host, hostName: "test", broker: "wss://example/mqtt", mode: "official"}
	got := d.handle(ctlRequest{Cmd: "invite"})
	if !got.OK {
		t.Fatalf("invite: %+v", got)
	}
	inv, err := remotev1.DecodeInvitation(got.Invitation)
	if err != nil {
		t.Fatal(err)
	}
	if inv.MQTTUser != "pair-user" || inv.MQTTPassword != "pair-pass" {
		t.Fatalf("invite missing pair credentials: %+v", inv)
	}
}

func TestInviteCarriesSelfhostCredentials(t *testing.T) {
	store, err := newFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	auth, err := remotev1.NewAuthority(store, "ws://192.168.1.20:8083/mqtt")
	if err != nil {
		t.Fatal(err)
	}
	host := &remotev1.Host{Authority: auth}
	d := daemon{auth: auth, host: host, hostName: "test", broker: "ws://192.168.1.20:8083/mqtt", mode: "selfhost", mqttUser: "alice", mqttPass: "secret"}
	got := d.handle(ctlRequest{Cmd: "invite"})
	if !got.OK {
		t.Fatalf("invite: %+v", got)
	}
	inv, err := remotev1.DecodeInvitation(got.Invitation)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Mode != "selfhost" || inv.MQTTUser != "alice" || inv.MQTTPassword != "secret" {
		t.Fatalf("selfhost invite missing credentials: %+v", inv)
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
	d := daemon{auth: auth, hostName: "test", broker: "wss://example/mqtt", mode: "selfhost"}
	got := d.handle(ctlRequest{Cmd: "status"})
	if !got.OK || got.Version != version || got.BuildID != buildID || got.PID != os.Getpid() {
		t.Fatalf("status: %+v", got)
	}
}
