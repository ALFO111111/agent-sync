package service

import (
	"testing"

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
	// 没填 Bin：使用自动查找的结果，不应写进配置、换机器后不会指向旧路径。
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
