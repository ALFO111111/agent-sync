// agent-sync-relay 是阶段 A 的独立中转服务：只转发不透明密文，
// 不运行 AI、不读取项目文件、不决定任何设备授权。
// 默认只监听回环地址，由边缘隧道（例如 Cloudflare）提供公网入口。
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ALFO111111/agent-sync/internal/i18n"
	"github.com/ALFO111111/agent-sync/internal/relay"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	// 先根据参数切语言，再定义帮助文字。
	for n, arg := range args {
		if arg == "-lang=en" || (arg == "-lang" && n+1 < len(args) && args[n+1] == "en") {
			i18n.Set("en")
		}
	}
	flags := flag.NewFlagSet("agent-sync-relay", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	listen := flags.String("listen", "127.0.0.1:18765", i18n.T("中转监听地址（仅回环）"))
	duration := flags.Duration("duration", 10*time.Minute, i18n.T("中转自动退出时间（最多一小时，0 表示常驻）"))
	allowOrigin := flags.String("allow-origin", "", "comma-separated WebSocket origins")
	lang := flags.String("lang", "zh-CN", "zh-CN / en")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			fmt.Fprintln(os.Stderr, "usage: agent-sync-relay [-listen 127.0.0.1:18765] [-duration 10m|0] [-lang zh-CN]")
			return 0
		}
		fmt.Fprintln(os.Stderr, i18n.T("中转参数无效：仅允许回环地址和最多一小时"))
		return 2
	}
	i18n.Set(*lang)
	host, _, err := net.SplitHostPort(*listen)
	ip := net.ParseIP(host)
	// duration=0 表示常驻运行，直到收到 SIGINT/SIGTERM（用于开机自启）。
	if err != nil || ip == nil || !ip.IsLoopback() || *duration < 0 || *duration > time.Hour {
		fmt.Fprintln(os.Stderr, i18n.T("中转参数无效：仅允许回环地址和最多一小时"))
		return 2
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.T("中转监听失败"))
		return 1
	}
	r := relay.New()
	for _, o := range strings.Split(*allowOrigin, ",") {
		if o = strings.TrimSpace(o); o != "" {
			r.AllowOrigin(o)
		}
	}
	srv := &http.Server{
		Handler:           r,
		ReadHeaderTimeout: 60 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    8192,
		ErrorLog:          nil,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cancel := func() {}
	if *duration > 0 {
		ctx, cancel = context.WithTimeout(ctx, *duration)
	}
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(listener) }()
	fmt.Println(i18n.T("中转已启动（仅转发密文）：%s", listener.Addr().String()))
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-done:
	}
	r.Close()
	_ = srv.Close()
	_ = listener.Close()
	fmt.Println(i18n.T("中转已停止"))
	if serveErr != nil && serveErr != http.ErrServerClosed {
		return 1
	}
	return 0
}
