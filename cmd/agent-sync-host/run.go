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
	"github.com/ALFO111111/agent-sync/internal/mqttlink"
	"github.com/ALFO111111/agent-sync/internal/remotev1"
)

type daemon struct {
	auth         *remotev1.Authority
	host         *remotev1.Host
	hostName     string
	broker       string
	mode         string
	controlToken string
	sessions     *sessionService
	cancel       context.CancelFunc
}

func (d *daemon) stop() {
	if d.cancel != nil {
		d.cancel()
	}
}

// runDaemon 常驻：加载身份 → 连接 MQTT → 处理消息；断开自动退避重连。
func runDaemon(ctx context.Context, dir, broker, mode, username, password, hostName, claudeBin string) (result error) {
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
	auth, err := remotev1.NewAuthority(store, broker)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	logf := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "%s %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
	}

	host := &remotev1.Host{Authority: auth, Name: hostName, Logf: logf, AttachDir: dir}
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

	d := &daemon{auth: auth, host: host, hostName: hostName, broker: broker, mode: mode, controlToken: remotev1.RandomToken(16), sessions: svc, cancel: cancel}

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
		state := auth.State()
		uid := "local"
		if mode == "official" {
			uid = state.AccountID
		}
		auth.SetUID(uid)
		topics := remotev1.TopicSet{UID: uid, HID: state.HostID, DID: state.HostID}

		var token mqttlink.TokenProvider
		if mode == "official" {
			if state.AccountID == "" || state.RefreshToken == "" {
				fmt.Fprintln(os.Stderr, i18n.T("等待账号登录后连接 MQTT"))
				select {
				case <-ctx.Done():
				case <-time.After(time.Second):
				}
				continue
			}
			token = &mqttlink.HTTPTokenProvider{
				BaseURL:      "https://agent-sync.yirenzaowusuo.cn/api",
				RefreshToken: state.RefreshToken,
				DeviceID:     state.HostID,
				HostID:       state.HostID,
			}
		} else {
			token = mqttlink.StaticToken{Username: username, Password: password}
		}

		if mode == "official" {
			identity, err := remotev1.IdentityFromBase64(state.PrivateKey)
			if err != nil {
				return err
			}
			ac := remotev1.NewAuthClient("https://agent-sync.yirenzaowusuo.cn/api", state.RefreshToken)
			if err := ac.RegisterHost(ctx, state.HostID, hostName, identity.PublicBase64()); err != nil {
				logf("register host: %v", err)
				select {
				case <-ctx.Done():
				case <-time.After(backoff):
				}
				continue
			}
			host.Auth = ac
		} else {
			host.Auth = nil
		}

		tr, err := mqttlink.New(mqttlink.Config{
			URL:         broker,
			ClientID:    state.HostID,
			Token:       token,
			WillTopic:   topics.Status(),
			WillPayload: []byte(`{"online":false}`),
		})
		if err == nil {
			err = host.Run(ctx, tr, topics)
			tr.Close()
		}
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "mqtt error: %v\n", err)
			fmt.Fprintln(os.Stderr, i18n.T("正在重连 MQTT（%s）", backoff.String()))
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
