// agent-sync-host 是电脑端守护进程：保存设备白名单，接受手机配对和受控操作。
//
// 子命令通过本机 Unix socket 控制常驻进程；配对批准必须由本机操作员执行。
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/ALFO111111/agent-sync/internal/i18n"
)

var version = "dev"
var buildID = "dev"

func main() { os.Exit(run(os.Args[1:])) }

func defaultStateDir() string {
	if runtime.GOOS == "darwin" {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support", "AgentSync", "remote")
		}
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "AgentSync", "remote")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".agent-sync", "remote")
}

func run(args []string) int {
	for n, arg := range args {
		if arg == "-lang=en" || (arg == "-lang" && n+1 < len(args) && args[n+1] == "en") {
			i18n.Set("en")
		}
	}
	if len(args) == 0 {
		usage()
		return 2
	}
	cmd := args[0]
	rest := args[1:]

	switch cmd {
	case "version":
		if err := json.NewEncoder(os.Stdout).Encode(struct {
			Version string `json:"version"`
			BuildID string `json:"buildID"`
			GOOS    string `json:"goos"`
			GOARCH  string `json:"goarch"`
		}{version, buildID, runtime.GOOS, runtime.GOARCH}); err != nil {
			return 1
		}
		return 0
	case "run":
		fs := flag.NewFlagSet("run", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		relayURL := fs.String("relay", "", "relay URL used by this host to dial")
		publicURL := fs.String("public", "", "wss relay URL written into invitations")
		name := fs.String("name", "", "host display name")
		claudeBin := fs.String("claude-bin", "", "path to the claude cli (auto-detected when empty)")
		dir := fs.String("state", defaultStateDir(), "state directory")
		lang := fs.String("lang", "zh-CN", "zh-CN / en")
		if err := fs.Parse(rest); err != nil || *relayURL == "" {
			usage()
			return 2
		}
		i18n.Set(*lang)
		hostName := *name
		if hostName == "" {
			hostName, _ = os.Hostname()
		}
		public := *publicURL
		if public == "" {
			public = *relayURL
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := runDaemon(ctx, *dir, *relayURL, public, hostName, *claudeBin); err != nil {
			fmt.Fprintln(os.Stderr, i18n.T("命令失败：%s", err))
			return 1
		}
		return 0

	case "invite", "pending", "devices", "status", "quit", "approve", "deny", "revoke", "rename", "sessions", "session-stop", "rotate-key", "remove-device":
		// 允许 -state/-lang 出现在任意位置（例如 approve ID -state DIR）。
		dir := defaultStateDir()
		lang := "zh-CN"
		var positional []string
		for i := 0; i < len(rest); i++ {
			switch rest[i] {
			case "-state", "--state":
				if i+1 < len(rest) {
					dir = rest[i+1]
					i++
				}
			case "-lang", "--lang":
				if i+1 < len(rest) {
					lang = rest[i+1]
					i++
				}
			default:
				positional = append(positional, rest[i])
			}
		}
		i18n.Set(lang)
		resp, err := sendControl(dir, buildRequest(cmd, positional))
		if err != nil {
			fmt.Fprintln(os.Stderr, i18n.T("命令失败：%s", err))
			return 1
		}
		if !resp.OK {
			fmt.Fprintln(os.Stderr, i18n.T("命令失败：%s", resp.Error))
			return 1
		}
		printResponse(cmd, resp)
		return 0
	default:
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  agent-sync-host version")
	fmt.Fprintln(os.Stderr, "  agent-sync-host run -relay ws://127.0.0.1:18765/ws -public wss://host/ws [-name NAME] [-state DIR] [-lang zh-CN|en]")
	fmt.Fprintln(os.Stderr, "  agent-sync-host invite|pending|devices|status|quit [-state DIR]")
	fmt.Fprintln(os.Stderr, "  agent-sync-host approve|deny <requestID> [-state DIR]")
	fmt.Fprintln(os.Stderr, "  agent-sync-host revoke <deviceID|fingerprint> [-state DIR]")
	fmt.Fprintln(os.Stderr, "  agent-sync-host sessions | session-stop <key|sessionId> [-state DIR]")
	fmt.Fprintln(os.Stderr, "  agent-sync-host rename <deviceID|fingerprint> <name> [-state DIR]")
}

func buildRequest(cmd string, args []string) ctlRequest {
	req := ctlRequest{Cmd: cmd}
	switch cmd {
	case "approve", "deny":
		if len(args) > 0 {
			req.ID = args[0]
		}
	case "revoke", "session-stop", "remove-device":
		if len(args) > 0 {
			req.Ref = args[0]
		}
	case "rename":
		if len(args) > 0 {
			req.Ref = args[0]
		}
		if len(args) > 1 {
			req.Name = args[1]
		}
	}
	return req
}

// pairDeepLink 把邀请编成手机可识别的深链；系统相机扫到会直接打开 App。
func pairDeepLink(invitation string) string {
	return "agent-sync://pair?d=" + base64.RawURLEncoding.EncodeToString([]byte(invitation))
}

func printResponse(cmd string, resp ctlResponse) {
	switch cmd {
	case "rotate-key":
		fmt.Println(i18n.T("已更换加密密钥，所有手机需要重新配对"))
	case "remove-device":
		fmt.Println(i18n.T("已删除设备记录"))
	case "invite":
		fmt.Println(i18n.T("配对邀请（10 分钟内有效）："))
		if err := renderQR(pairDeepLink(resp.Invitation), os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, i18n.T("命令失败：%s", err))
		}
		fmt.Println(i18n.T("用手机扫码，或复制这行："))
		fmt.Println(resp.Invitation)
	case "pending":
		if len(resp.Pending) == 0 {
			fmt.Println(i18n.T("没有待批准请求"))
			return
		}
		fmt.Println(i18n.T("待批准请求："))
		for _, p := range resp.Pending {
			fmt.Printf("%s  %s  %s  %s\n", p.ID, p.DeviceName, p.Fingerprint, p.Expires.Format("15:04:05"))
		}
	case "devices":
		if len(resp.Devices) == 0 {
			fmt.Println(i18n.T("没有已配对设备"))
			return
		}
		for _, d := range resp.Devices {
			state := i18n.T("离线")
			if d.Online {
				state = i18n.T("在线")
			}
			seen := "-"
			if !d.LastSeen.IsZero() {
				seen = d.LastSeen.Format("2006-01-02 15:04")
			}
			fmt.Printf("%s  %s  %s  %s  %s\n", d.ID, d.Name, d.Fingerprint, state, seen)
		}
	case "revoke":
		if resp.Device != nil {
			fmt.Println(i18n.T("已撤销设备 %s", resp.Device.Name))
		}
	case "rename":
		if resp.Device != nil {
			fmt.Println(i18n.T("已重命名设备 %s", resp.Device.Name))
		}
	case "sessions":
		if len(resp.Active) == 0 {
			fmt.Println(i18n.T("没有活动会话"))
			return
		}
		for _, a := range resp.Active {
			fmt.Printf("%s  %s  %s  %s  subs=%d\n", a.Key, a.ClaudeID, a.Mode, a.Cwd, a.Subscribers)
		}
	case "session-stop":
		fmt.Println(i18n.T("已结束会话"))
	case "status":
		fmt.Println(i18n.T("主机 %s，中转 %s", resp.HostName, resp.Relay))
	}
}
