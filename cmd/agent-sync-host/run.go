package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ALFO111111/agent-sync/internal/fsutil"
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
func runDaemon(ctx context.Context, dir, dialURL, publicURL, hostName, claudeBin string) (result error) {
	// 身份文件读取之前锁住整个生命周期，清理完成后才释放。
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "host.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	locked, err := fsutil.TryLockFile(lock)
	if err != nil {
		return err
	}
	if !locked {
		return errors.New("remote host is already starting or running")
	}
	defer fsutil.UnlockFile(lock)
	// 旧版没有生命周期锁；控制通道仍可用时也不能启动第二份。
	if resp, err := sendControl(dir, ctlRequest{Cmd: "status"}); err == nil && resp.OK {
		return errors.New("remote host is already running")
	}
	pidPath := filepath.Join(dir, "host.pid")
	if err := writeFileAtomic(pidPath, []byte(fmt.Sprint(os.Getpid())), 0o600); err != nil {
		return err
	}
	defer os.Remove(pidPath)
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
	go func() {
		err := serveControl(ctx, dir, d)
		controlErr <- err
		cancel()
	}()
	defer func() {
		cancel()
		if err := <-controlErr; err != nil {
			result = err
		}
	}()

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
	return nil
}
