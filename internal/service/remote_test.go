package service

import (
	"testing"

	"github.com/ALFO111111/agent-sync/internal/config"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := &config.Config{DeviceID: "dev-1", DeviceName: "test"}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// withRemoteSeams 把真正启动/查询/停止 host 的动作换成可观察的假实现。
func withRemoteSeams(t *testing.T, running bool, ensureErr error) *int {
	t.Helper()
	t.Setenv("AGENT_SYNC_HOME", t.TempDir())
	calls := 0
	oldEnsure, oldStatus, oldStop := remoteEnsureFn, remoteStatusFn, remoteStopFn
	remoteEnsureFn = func(cfg *config.Config) error { calls++; return ensureErr }
	remoteStatusFn = func(cfg *config.Config) RemoteStatus { return RemoteStatus{Running: running} }
	remoteStopFn = func() error { return nil }
	t.Cleanup(func() { remoteEnsureFn, remoteStatusFn, remoteStopFn = oldEnsure, oldStatus, oldStop })
	return &calls
}

func TestRemoteResumeOnStartupWhenEnabled(t *testing.T) {
	calls := withRemoteSeams(t, false, nil)
	cfg := testConfig(t)
	cfg.RemoteEnabled = true
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := RemoteResumeOnStartup(); err != nil {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Fatalf("ensure calls = %d, want 1", *calls)
	}
}

func TestRemoteResumeOnStartupSkipsWhenDisabledOrRunning(t *testing.T) {
	calls := withRemoteSeams(t, false, nil)
	cfg := testConfig(t)
	cfg.RemoteEnabled = false
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := RemoteResumeOnStartup(); err != nil {
		t.Fatal(err)
	}
	if *calls != 0 {
		t.Fatalf("disabled: ensure calls = %d, want 0", *calls)
	}

	cfg.RemoteEnabled = true
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	remoteStatusFn = func(cfg *config.Config) RemoteStatus { return RemoteStatus{Running: true} }
	if err := RemoteResumeOnStartup(); err != nil {
		t.Fatal(err)
	}
	if *calls != 0 {
		t.Fatalf("running: ensure calls = %d, want 0", *calls)
	}
}

func TestRemoteStartStopPersistsIntent(t *testing.T) {
	withRemoteSeams(t, false, nil)
	cfg := testConfig(t)
	if err := RemoteStart(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !got.RemoteEnabled {
		t.Fatal("RemoteStart should persist RemoteEnabled=true")
	}
	if err := RemoteStop(); err != nil {
		t.Fatal(err)
	}
	got, err = config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.RemoteEnabled {
		t.Fatal("RemoteStop should persist RemoteEnabled=false")
	}
}
