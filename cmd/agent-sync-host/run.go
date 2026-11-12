package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ALFO111111/agent-sync/internal/i18n"
	"github.com/ALFO111111/agent-sync/internal/remotev1"
)

type daemon struct {
	auth         *remotev1.Authority
	host         *remotev1.Host
	hostName     string
	relayURL     string
	controlToken string
	sessions     *sessionService
	cancel       context.CancelFunc
}

func (d *daemon) stop() {
	if d.cancel != nil {
		d.cancel()
	}
}

// runDaemon 常驻：加载身份 → 登记中转 → 处理连接；断开自动退避重连。
func runDaemon(ctx context.Context, dir, dialURL, publicURL, hostName, claudeBin string) error {
	store, err := newFileStore(dir)
	if err != nil {
		return err
	}
	auth, err := remotev1.NewAuthority(store, publicURL)
	if err != nil {
		return err
	}
	state := auth.State()
	hostID, err := remotev1.IdentityFromBase64(state.PrivateKey)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	logf := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "%s %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
	}
	host := remotev1.NewHost(hostID, auth, dialURL, hostName, logf)
	extraPath := []string{}
	if home, err := os.UserHomeDir(); err == nil {
		extraPath = append(extraPath, filepath.Join(home, "sdk", "node", "bin"))
	}
	var svc *sessionService
	if sessions, err := newSessionService(claudeBin, extraPath); err == nil {
		svc = sessions
		host.Sessions = sessions
		host.Files = sessions
		host.Data = sessions
		defer sessions.Shutdown()
	} else {
		fmt.Fprintf(os.Stderr, "sessions disabled: %v\n", err)
	}
	d := &daemon{auth: auth, host: host, hostName: hostName, relayURL: publicURL, controlToken: remotev1.RandomToken(16), sessions: svc, cancel: cancel}

	controlErr := make(chan error, 1)
	go func() { controlErr <- serveControl(ctx, dir, d) }()

	backoff := time.Second
	for ctx.Err() == nil {
		err := host.Serve(ctx, func() {
			fmt.Fprintln(os.Stderr, i18n.T("主机守护已启动（%s）", publicURL))
			backoff = time.Second
		})
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			// 错误只用于本机日志：可能含中转地址，不含配对秘密。
			fmt.Fprintf(os.Stderr, "relay error: %v\n", err)
			fmt.Fprintln(os.Stderr, i18n.T("正在重连中转（%s）", backoff.String()))
		}
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
	fmt.Fprintln(os.Stderr, i18n.T("主机守护已停止"))
	select {
	case err := <-controlErr:
		if err != nil && ctx.Err() == nil {
			return err
		}
	default:
	}
	return nil
}
