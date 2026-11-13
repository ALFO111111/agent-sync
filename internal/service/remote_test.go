package service

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ALFO111111/agent-sync/internal/config"
	"github.com/ALFO111111/agent-sync/internal/testutil"
)

// 手机遥控的设置要能原样存取；Bin 留空时回到自动查找，不把旧路径钉死。
func TestRemoteConfigRoundTrip(t *testing.T) {
	testutil.NewDevice(t, "remote-test")

	in := RemoteConfig{
		Relay:     " wss://relay.example.com/ws ",
		Public:    " wss://relay.example.com/ws ",
		Name:      " 我的电脑 ",
		ClaudeBin: ` D:\tools\claude.cmd `,
		Proxy:     " http://127.0.0.1:7890 ",
	}
	if err := SaveRemoteConfig(in); err != nil {
		t.Fatal(err)
	}

	got, err := GetRemoteConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.Relay != "https://relay.example.com" || got.Public != "https://relay.example.com" ||
		got.Name != "我的电脑" || got.ClaudeBin != `D:\tools\claude.cmd` || got.Proxy != "http://127.0.0.1:7890" {
		t.Fatalf("保存后读回不一致: %+v", got)
	}
	if got.Bin != "" {
		t.Fatalf("auto path must stay empty: %q", got.Bin)
	}
	// 没填 Bin：不应写进配置、换机器后不会指向旧路径。
	if cfg, _ := config.Load(); cfg.RemoteBin != "" {
		t.Fatalf("自动查找路径不应写进配置: %q", cfg.RemoteBin)
	}

	// 用户明确填一个和自动查找不同的路径，才记下来。
	if err := SaveRemoteConfig(RemoteConfig{Bin: `D:\custom\agent-sync-host.exe`}); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := config.Load(); cfg.RemoteBin != `D:\custom\agent-sync-host.exe` {
		t.Fatalf("自定义路径应保存: %q", cfg.RemoteBin)
	}

	// 再清空：回到自动查找。
	if err := SaveRemoteConfig(RemoteConfig{}); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := config.Load(); cfg.RemoteBin != "" {
		t.Fatalf("清空后应回到自动查找: %q", cfg.RemoteBin)
	}
}

func fakeRemoteControl(t *testing.T, dir string, status remoteResponse, quit func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			func() {
				defer c.Close()
				line, _ := bufio.NewReader(c).ReadBytes('\n')
				var req remoteRequest
				_ = json.Unmarshal(line, &req)
				resp := status
				if req.Cmd == "quit" {
					if quit != nil {
						quit()
					}
					resp = remoteResponse{OK: true}
				}
				b, _ := json.Marshal(resp)
				_, _ = c.Write(append(b, '\n'))
			}()
		}
	}()
	t.Cleanup(func() { ln.Close(); <-done })
	if err := os.WriteFile(filepath.Join(dir, "control"), []byte(ln.Addr().String()+" token\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteLegacyNeverQuit(t *testing.T) {
	dir := t.TempDir()
	quit := false
	fakeRemoteControl(t, dir, remoteResponse{OK: true}, func() { quit = true })
	if err := remoteStopAt(dir, 30*time.Millisecond); err == nil || !strings.Contains(err.Error(), "旧版") {
		t.Fatalf("legacy must be blocked: %v", err)
	}
	if quit {
		t.Fatal("sent quit to legacy host")
	}
	if err := remoteEnsureAt(&config.Config{RemoteBin: "missing"}, dir); err != nil {
		t.Fatalf("existing legacy must be kept: %v", err)
	}
}

func TestRemoteStopWaitsForProcess(t *testing.T) {
	dir := t.TempDir()
	fakeRemoteControl(t, dir, remoteResponse{OK: true, PID: os.Getpid()}, func() { _ = os.Remove(filepath.Join(dir, "control")) })
	if err := remoteStopAt(dir, 30*time.Millisecond); err == nil {
		t.Fatal("control removal must not confirm process exit")
	}
	delete(remoteStopping, dir)
}

func TestRemoteReadyVersionMismatch(t *testing.T) {
	dir := t.TempDir()
	fakeRemoteControl(t, dir, remoteResponse{OK: true, Version: "mismatch", BuildID: "mismatch"}, nil)
	if err := waitRemoteReady(dir, nil, time.Second, true); err == nil {
		t.Fatal("bundle mismatch accepted")
	}
	if err := waitRemoteReady(dir, nil, time.Second, false); err != nil {
		t.Fatalf("external host should not be compared with bundle: %v", err)
	}
}

func TestRemoteReadyExit(t *testing.T) {
	exited := make(chan error, 1)
	exited <- nil
	if err := waitRemoteReady(t.TempDir(), exited, time.Second, false); err == nil {
		t.Fatal("child exit accepted as ready")
	}
}

func TestRemoteLifecycleIntegration(t *testing.T) {
	// 真host只用临时目录和闭合的本机端口，不访问用户的真实遥控服务。
	bin := filepath.Join(t.TempDir(), "host-test.exe")
	build := exec.Command("go", "build", "-o", bin, "../../cmd/agent-sync-host")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	dir := t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	relay := "ws://" + ln.Addr().String() + "/ws"
	ln.Close()
	cfg := &config.Config{RemoteBin: bin, RemoteRelay: relay, RemotePublic: relay, RemoteClaudeBin: "missing-test-cli"}
	t.Cleanup(func() {
		if err := remoteStopAt(dir, 10*time.Second); err != nil {
			t.Errorf("cleanup: %v", err)
		}
		delete(remoteStopping, dir)
	})
	if err := remoteEnsureAt(cfg, dir); err != nil {
		t.Fatal(err)
	}
	first, err := remoteCall(dir, "status", time.Second)
	if err != nil || first.PID <= 0 {
		t.Fatalf("status: %+v %v", first, err)
	}
	if err := remoteEnsureAt(cfg, dir); err != nil {
		t.Fatal(err)
	}
	same, _ := remoteCall(dir, "status", time.Second)
	if same.PID != first.PID {
		t.Fatal("started duplicate")
	}
	if err := remoteStopAt(dir, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := remoteEnsureAt(cfg, dir); err != nil {
		t.Fatal(err)
	}
	second, err := remoteCall(dir, "status", time.Second)
	if err != nil || second.PID == first.PID {
		t.Fatalf("restart: %+v %v", second, err)
	}
	if first.HostID == "" || second.HostID != first.HostID {
		t.Fatalf("restart changed host identity: %q -> %q", first.HostID, second.HostID)
	}
}

func TestNormalizeRelayURL(t *testing.T) {
	cases := map[string]string{
		"https://relay.example.com":        "wss://relay.example.com/ws",
		"https://relay.example.com/":       "wss://relay.example.com/ws",
		"http://127.0.0.1:18765":           "ws://127.0.0.1:18765/ws",
		"wss://relay.example.com/ws":       "wss://relay.example.com/ws",
		"relay.example.com":                "wss://relay.example.com/ws",
		"https://relay.example.com/custom": "wss://relay.example.com/custom",
	}
	for in, want := range cases {
		got, err := NormalizeRelayURL(in)
		if err != nil || got != want {
			t.Errorf("%q -> %q (%v), want %q", in, got, err, want)
		}
	}
	if _, err := NormalizeRelayURL("ftp://x"); err == nil {
		t.Fatal("ftp accepted")
	}
	if DisplayRelayURL("wss://relay.example.com/ws") != "https://relay.example.com" {
		t.Fatal("display")
	}
}
