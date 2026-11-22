package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
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
	mqttUser     string
	mqttPass     string
	controlToken string
	sessions     *sessionService
	cancel       context.CancelFunc

	brokerMu    sync.RWMutex
	brokerState string // disconnected / connecting / connected / error
	lastError   string

	// host.Auth 会在重连循环里替换；控制通道并发读取，必须加锁。
	authMu sync.RWMutex
	// 当前一轮 MQTT 连接的取消函数；退出登录/换账号时用它立刻断开旧链路。
	loopMu     sync.Mutex
	loopCancel context.CancelFunc
	// 套餐/流量是公网请求，缓存 60 秒，避免桌面端每 3 秒轮询时反复打 auth。
	usageMu      sync.Mutex
	usageFetchMu sync.Mutex
	usageAt      time.Time
	usagePlan    string
	usageExp     int64
	usageUsed    int64
	usageQuota   int64
}

func (d *daemon) setAuth(ac remotev1.DeviceRegistrar) {
	if d.host == nil {
		return
	}
	d.authMu.Lock()
	d.host.Auth = ac
	d.authMu.Unlock()
}

func (d *daemon) getAuth() remotev1.DeviceRegistrar {
	if d.host == nil {
		return nil
	}
	d.authMu.RLock()
	defer d.authMu.RUnlock()
	return d.host.Auth
}

// setLoopCancel 登记新一轮连接的取消函数，并停掉上一轮（如果有）。
func (d *daemon) setLoopCancel(cancel context.CancelFunc) {
	d.loopMu.Lock()
	old := d.loopCancel
	d.loopCancel = cancel
	d.loopMu.Unlock()
	if old != nil {
		old()
	}
}

// reconnect 立刻断开当前 MQTT 链路，让主循环用最新账号状态重新连接。
func (d *daemon) reconnect() {
	d.loopMu.Lock()
	cancel := d.loopCancel
	d.loopMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (d *daemon) setBroker(state, errText string) {
	d.brokerMu.Lock()
	d.brokerState = state
	d.lastError = errText
	d.brokerMu.Unlock()
}

func (d *daemon) brokerInfo() (string, string) {
	d.brokerMu.RLock()
	defer d.brokerMu.RUnlock()
	return d.brokerState, d.lastError
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
		sessions.stateDir = dir
		svc = sessions
		host.Sessions = sessions
		host.Files = sessions
		host.Data = sessions
		defer sessions.Shutdown()
	} else {
		fmt.Fprintf(os.Stderr, "sessions disabled: %v\n", err)
	}

	d := &daemon{auth: auth, host: host, hostName: hostName, broker: broker, mode: mode, mqttUser: username, mqttPass: password, controlToken: remotev1.RandomToken(16), sessions: svc, cancel: cancel}

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

		if mode == "official" && (state.AccountID == "" || state.RefreshToken == "") {
			d.setAuth(nil)
			fmt.Fprintln(os.Stderr, i18n.T("等待账号登录后连接 MQTT"))
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
			continue
		}

		// 这一轮连接的独立 ctx：退出登录/换账号时 cancel 它，立刻断开旧链路。
		loopCtx, loopCancel := context.WithCancel(ctx)
		d.setLoopCancel(loopCancel)

		var token mqttlink.TokenProvider
		if mode == "official" {
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
			// 先校验会话并补用户名，再登记电脑：token 过期时 RegisterHost 会先 401，
			// 如果先调它，后面的 401 处理永远走不到。
			name, meErr := ac.Me(loopCtx)
			if meErr != nil {
				if authExpired(meErr) {
					logf("account session expired; waiting for a new login")
					_ = auth.Logout()
					d.setAuth(nil)
					d.setBroker("disconnected", "")
					select {
					case <-ctx.Done():
					case <-time.After(backoff):
					}
					continue
				}
				logf("check account: %v", meErr)
				select {
				case <-ctx.Done():
				case <-time.After(backoff):
				}
				continue
			}
			if name != "" && name != state.Username {
				_ = auth.SetAccount(state.AccountID, name, state.RefreshToken)
			}
			if err := ac.RegisterHost(loopCtx, state.HostID, hostName, identity.PublicBase64()); err != nil {
				if authExpired(err) {
					logf("account session expired; waiting for a new login")
					_ = auth.Logout()
					d.setAuth(nil)
					select {
					case <-ctx.Done():
					case <-time.After(backoff):
					}
					continue
				}
				logf("register host: %v", err)
				select {
				case <-ctx.Done():
				case <-time.After(backoff):
				}
				continue
			}
			d.setAuth(ac)
		} else {
			d.setAuth(nil)
		}

		// 断线时 Broker 会按遗嘱把状态置为 offline；重连后要重新写回 online，
		// 否则手机一直以为电脑不在线。
		var connected atomic.Bool
		var live atomic.Pointer[mqttlink.Link]
		d.setBroker("connecting", "")
		tr, err := mqttlink.NewWithContext(loopCtx, mqttlink.Config{
			URL:         broker,
			ClientID:    state.HostID,
			Token:       token,
			WillTopic:   topics.Status(),
			WillPayload: []byte(`{"online":false}`),
			OnConnect: func() {
				connected.Store(true)
				d.setBroker("connected", "")
				l := live.Load()
				if l == nil {
					return
				}
				go func() {
					_ = l.Publish(context.Background(), topics.Status(), []byte(`{"online":true}`), 1, true)
				}()
			},
		})
		if err == nil {
			live.Store(tr)
			err = host.Run(loopCtx, tr, topics)
			live.Store(nil)
			tr.Close()
		}
		if ctx.Err() != nil {
			d.setBroker("disconnected", "")
			break
		}
		// 主动断开（退出登录/换账号）不需要退避，下一轮立刻按新状态重连。
		if loopCtx.Err() != nil {
			d.setBroker("disconnected", "")
			backoff = time.Second
			continue
		}
		if connected.Load() {
			backoff = time.Second
		}
		if err != nil {
			d.setBroker("error", err.Error())
			fmt.Fprintf(os.Stderr, "mqtt error: %v\n", err)
			fmt.Fprintln(os.Stderr, i18n.T("正在重连 MQTT（%s）", backoff.String()))
		} else {
			d.setBroker("disconnected", "")
		}
		select {
		case <-ctx.Done():
			d.setBroker("disconnected", "")
			break
		case <-time.After(backoff):
		}
		if !connected.Load() && backoff < 30*time.Second {
			backoff *= 2
		}
	}
	fmt.Fprintln(os.Stderr, i18n.T("主机守护已停止"))
	return nil
}

// authExpired 判断鉴权服务是否明确拒绝了当前会话（401/403）；
// 网络错误不算，否则断网会把用户踢下线。
func authExpired(err error) bool {
	var se *remotev1.StatusError
	return errors.As(err, &se) && (se.Code == http.StatusUnauthorized || se.Code == http.StatusForbidden)
}
