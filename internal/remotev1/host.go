package remotev1

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ALFO111111/agent-sync/internal/i18n"
	"github.com/ALFO111111/agent-sync/internal/relay"
)

const (
	// 审批期间客户端不发言，读协程给足时间；连接存活由主循环的应用层心跳判断。
	approvalReadWindow = InvitationTTL + time.Minute
	// 认证后多久没有任何消息就断开。
	idleLimit        = 75 * time.Second
	maxSubscriptions = 4
)

// Host 是电脑端守护进程：登记到中转，处理手机连接、配对和受控操作。
type Host struct {
	Identity  *Identity
	Authority *Authority
	URL       string
	Name      string
	Logf      func(format string, args ...any)

	// Files 非空时提供只读文件浏览。
	Files FileSink

	// Sessions 非空时提供服务端会话能力（v2）。
	Sessions SessionSink

	// Data 非空时提供只读的电脑概况（首页/设置）。
	Data DataSink

	mu    sync.Mutex
	conns map[*websocket.Conn]string // 连接 → 设备公钥
}

// RotateContentKey 更换共享密钥并断开所有已连接的手机。
func (h *Host) RotateContentKey() error {
	if err := h.Authority.RotateContentKey(); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.conns {
		c.Close()
	}
	return nil
}

func NewHost(id *Identity, a *Authority, url, name string, logf func(string, ...any)) *Host {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Host{Identity: id, Authority: a, URL: url, Name: name, Logf: logf, conns: map[*websocket.Conn]string{}}
}

// Serve 登记控制通道并处理接入请求；返回后本次数据连接全部关闭。
func (h *Host) Serve(ctx context.Context, ready func()) error {
	ctx, cancel := context.WithCancel(ctx)
	state := h.Authority.State()
	control, err := relay.DialContext(ctx, h.URL, relay.Control{
		Role: "register", Route: state.Route, Credential: state.Credential,
	})
	if err != nil {
		cancel()
		return err
	}
	defer control.Close()
	done := make(chan struct{})
	var wg sync.WaitGroup
	defer wg.Wait()
	defer func() {
		h.mu.Lock()
		for c := range h.conns {
			c.Close()
		}
		h.mu.Unlock()
	}()
	defer cancel()
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			control.Close()
		case <-done:
		}
	}()
	if err = relay.Await(control, "ready"); err != nil {
		return err
	}
	if ready != nil {
		ready()
	}
	slots := make(chan struct{}, 8)
	for {
		var m relay.Control
		control.SetReadDeadline(time.Now().Add(45 * time.Second))
		if err = control.ReadJSON(&m); err != nil {
			return err
		}
		if m.Version == 1 && m.Role == "heartbeat" {
			if relay.Heartbeat(control) != nil {
				return relay.ErrProtocol
			}
			continue
		}
		if m.Version != 1 || m.Role != "incoming" || m.Route != state.Route || len(m.Ticket) != 64 {
			return relay.ErrProtocol
		}
		select {
		case slots <- struct{}{}:
		default:
			continue
		}
		wg.Add(1)
		go func(m relay.Control) {
			defer wg.Done()
			defer func() { <-slots }()
			h.attach(ctx, m)
		}(m)
	}
}

func (h *Host) attach(ctx context.Context, m relay.Control) {
	state := h.Authority.State()
	ws, err := relay.DialContext(ctx, h.URL, relay.Control{
		Role: "attach", Route: state.Route, Ticket: m.Ticket,
	})
	if err != nil {
		return
	}
	defer ws.Close()
	stop := context.AfterFunc(ctx, func() { ws.Close() })
	defer stop()
	if relay.Await(ws, "paired") != nil {
		return
	}
	h.handle(ctx, ws)
}

func (h *Host) handle(parent context.Context, ws *websocket.Conn) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	key, err := ParseContentKey(h.Authority.State().ContentKey)
	if err != nil {
		return
	}
	s, err := newSession(ws, key, ClientToHost, HostToClient)
	if err != nil {
		return
	}
	hello, err := s.readWith(15 * time.Second)
	if err != nil || hello.Type != "hello" || len(hello.DevicePub) > 256 || len(hello.DeviceName) > 128 {
		return
	}
	pub, err := ParsePublic(hello.DevicePub)
	if err != nil {
		_ = s.write(Envelope{Type: "denied", Error: "identity"})
		return
	}
	nonce := RandomNonce()
	if s.write(Envelope{Type: "challenge", Nonce: nonce, HostID: h.Authority.State().HostID, HostName: h.Name}) != nil {
		return
	}
	proof, err := s.readWith(30 * time.Second)
	if err != nil || proof.Type != "proof" || proof.DevicePub != hello.DevicePub ||
		!VerifyProof(pub, nonce, hello.DevicePub, hello.InviteID, proof.Signature) {
		_ = s.write(Envelope{Type: "denied", Error: "proof"})
		return
	}

	// 认证之后只由一个读协程读；主循环通过通道消费，避免读超时弄坏 Gorilla 连接。
	frames := make(chan Envelope, 8)
	readErr := make(chan error, 1)
	readerCtx, stopReader := context.WithCancel(ctx)
	defer stopReader()
	go func() {
		for {
			env, err := s.readWith(approvalReadWindow)
			if err != nil {
				readErr <- err
				return
			}
			select {
			case frames <- env:
			case <-readerCtx.Done():
				return
			}
		}
	}()

	var device Device
	device, known := h.Authority.Device(hello.DevicePub)
	if known {
		if device.Revoked {
			_ = s.write(Envelope{Type: "denied", Error: "revoked"})
			return
		}
		if s.write(h.readyEnvelope(device.ID)) != nil {
			return
		}
	} else if hello.InviteID != "" && hello.InviteSecret != "" {
		p, err := h.Authority.BeginPair(hello.InviteID, hello.InviteSecret, hello.DevicePub, hello.DeviceName)
		if err != nil {
			_ = s.write(Envelope{Type: "denied", Error: "invitation"})
			return
		}
		defer h.Authority.Cancel(p.ID)
		if s.write(Envelope{Type: "pending", ID: p.ID}) != nil {
			return
		}
		timer := time.NewTimer(time.Until(p.Expires))
		defer timer.Stop()
		approved := false
	wait:
		for {
			select {
			case approved = <-p.Result:
				break wait
			case _, ok := <-frames:
				// 批准前发业务消息：视为非法，取消。
				if !ok {
					return
				}
				return
			case <-readErr:
				return
			case <-ctx.Done():
				return
			case <-timer.C:
				break wait
			}
		}
		if !approved {
			_ = s.write(Envelope{Type: "denied", Error: "approval"})
			return
		}
		device, _ = h.Authority.Device(hello.DevicePub)
		if s.write(h.readyEnvelope(device.ID)) != nil {
			return
		}
	} else {
		_ = s.write(Envelope{Type: "denied", Error: "unknown"})
		return
	}

	h.mu.Lock()
	h.conns[ws] = hello.DevicePub
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.conns, ws)
		h.mu.Unlock()
		h.Authority.Touch(hello.DevicePub)
	}()
	h.Logf(i18n.T("设备已连接：%s（%s）"), device.Name, shortFingerprint(device.Fingerprint))

	subs := 0
	idle := time.NewTimer(idleLimit)
	defer idle.Stop()
	for {
		select {
		case env, ok := <-frames:
			if !ok {
				return
			}
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(idleLimit)
			switch env.Type {
			case "ping":
				if s.write(Envelope{Type: "pong"}) != nil {
					return
				}
			case "echo":
				if len(env.Data) > 4096 {
					return
				}
				if s.write(Envelope{Type: "echo", ID: env.ID, Data: env.Data}) != nil {
					return
				}
			case "device.list":
				if s.write(Envelope{Type: "devices", ID: env.ID, Devices: h.DeviceList()}) != nil {
					return
				}
			case "status":
				if s.write(Envelope{Type: "status", ID: env.ID, DeviceID: device.ID, HostID: h.Authority.State().HostID, HostName: h.Name}) != nil {
					return
				}
			case "data.overview":
				if h.Data == nil {
					if s.write(Envelope{Type: "data.overview", ID: env.ID, Error: "overview unsupported"}) != nil {
						return
					}
					continue
				}
				ov, err := h.Data.Overview()
				if err != nil {
					if s.write(Envelope{Type: "data.overview", ID: env.ID, Error: err.Error()}) != nil {
						return
					}
					continue
				}
				ov.HostName = h.Name
				if s.write(Envelope{Type: "data.overview", ID: env.ID, Overview: &ov}) != nil {
					return
				}
			case "fs.list":
				if h.Files == nil {
					_ = s.write(Envelope{Type: "fs.error", ID: env.ID, Error: "files unsupported"})
					continue
				}
				entries, err := h.Files.List(env.Cwd, env.Path)
				if err != nil {
					if s.write(Envelope{Type: "fs.error", ID: env.ID, Error: err.Error()}) != nil {
						return
					}
					continue
				}
				if s.write(Envelope{Type: "fs.list", ID: env.ID, Path: env.Path, Entries: entries}) != nil {
					return
				}
			case "fs.read":
				if h.Files == nil {
					_ = s.write(Envelope{Type: "fs.error", ID: env.ID, Error: "files unsupported"})
					continue
				}
				text, truncated, binary, next, err := h.Files.Read(env.Cwd, env.Path, env.Offset)
				if err != nil {
					if s.write(Envelope{Type: "fs.error", ID: env.ID, Error: err.Error()}) != nil {
						return
					}
					continue
				}
				resp := Envelope{Type: "fs.read", ID: env.ID, Path: env.Path, Text: text,
					Truncated: truncated, Binary: binary, Next: next}
				// JSON 转义后可能超过加密帧上限：继续缩小本块，保证连接不断。
				for len(resp.Text) > 0 {
					b, _ := json.Marshal(resp)
					if len(b) <= MaxPlain {
						break
					}
					resp.Text = resp.Text[:len(resp.Text)/2]
					resp.Next = env.Offset + int64(len(resp.Text))
					resp.Truncated = true
				}
				if s.write(resp) != nil {
					return
				}
			case "git.status":
				if h.Files == nil {
					_ = s.write(Envelope{Type: "fs.error", ID: env.ID, Error: "files unsupported"})
					continue
				}
				changes, err := h.Files.GitStatus(env.Cwd)
				if err != nil {
					if s.write(Envelope{Type: "fs.error", ID: env.ID, Error: err.Error()}) != nil {
						return
					}
					continue
				}
				if s.write(Envelope{Type: "git.status", ID: env.ID, Changes: changes}) != nil {
					return
				}
			case "git.diff":
				if h.Files == nil {
					_ = s.write(Envelope{Type: "fs.error", ID: env.ID, Error: "files unsupported"})
					continue
				}
				text, truncated, err := h.Files.GitDiff(env.Cwd, env.Path)
				if err != nil {
					if s.write(Envelope{Type: "fs.error", ID: env.ID, Error: err.Error()}) != nil {
						return
					}
					continue
				}
				if s.write(Envelope{Type: "git.diff", ID: env.ID, Path: env.Path, Text: text, Truncated: truncated}) != nil {
					return
				}
			case "session.active":
				if h.Sessions == nil {
					if s.write(Envelope{Type: "session.active", ID: env.ID}) != nil {
						return
					}
					continue
				}
				if s.write(Envelope{Type: "session.active", ID: env.ID, Active: h.Sessions.ActiveSessions()}) != nil {
					return
				}
			case "session.projects":
				if h.Sessions == nil {
					if s.write(Envelope{Type: "session.error", ID: env.ID, Error: "sessions unsupported"}) != nil {
						return
					}
					continue
				}
				list, err := h.Sessions.Projects(env.Tool)
				if err != nil {
					if s.write(Envelope{Type: "session.error", ID: env.ID, Error: err.Error()}) != nil {
						return
					}
					continue
				}
				if s.write(Envelope{Type: "session.projects", ID: env.ID, Projects: list}) != nil {
					return
				}
			case "session.list":
				if h.Sessions == nil {
					if s.write(Envelope{Type: "session.error", ID: env.ID, Error: "sessions unsupported"}) != nil {
						return
					}
					continue
				}
				list, err := h.Sessions.Sessions(env.Tool, env.Cwd)
				if err != nil {
					if s.write(Envelope{Type: "session.error", ID: env.ID, Error: err.Error()}) != nil {
						return
					}
					continue
				}
				if s.write(Envelope{Type: "session.list", ID: env.ID, Sessions: list}) != nil {
					return
				}
			case "session.start":
				if h.Sessions == nil {
					if s.write(Envelope{Type: "session.error", ID: env.ID, Error: "sessions unsupported"}) != nil {
						return
					}
					continue
				}
				key, claudeID, err := h.Sessions.Start(env.Tool, env.Cwd, env.ResumeID, env.Mode)
				if err != nil {
					if s.write(Envelope{Type: "session.error", ID: env.ID, Error: err.Error()}) != nil {
						return
					}
					continue
				}
				if s.write(Envelope{Type: "session.ready", ID: env.ID, SessionKey: key, ClaudeSessionID: claudeID}) != nil {
					return
				}
			case "turn.send":
				if h.Sessions == nil {
					continue
				}
				if err := h.Sessions.Send(env.SessionKey, env.Text); err != nil {
					if s.write(Envelope{Type: "session.error", ID: env.ID, SessionKey: env.SessionKey, Error: err.Error()}) != nil {
						return
					}
				}
			case "turn.stop":
				if h.Sessions == nil {
					continue
				}
				if err := h.Sessions.Stop(env.SessionKey); err != nil {
					if s.write(Envelope{Type: "session.error", ID: env.ID, SessionKey: env.SessionKey, Error: err.Error()}) != nil {
						return
					}
				}
			case "approval.decide":
				if h.Sessions == nil {
					continue
				}
				if err := h.Sessions.Decide(env.SessionKey, env.ApprovalID, env.Allow); err != nil {
					if s.write(Envelope{Type: "session.error", ID: env.ID, SessionKey: env.SessionKey, Error: err.Error()}) != nil {
						return
					}
				}
			case "session.setmode":
				if h.Sessions == nil {
					continue
				}
				newKey, claudeID, err := h.Sessions.SetMode(env.SessionKey, env.Mode)
				if err != nil {
					if s.write(Envelope{Type: "session.error", ID: env.ID, SessionKey: env.SessionKey, Error: err.Error()}) != nil {
						return
					}
					continue
				}
				if s.write(Envelope{Type: "session.ready", ID: env.ID, SessionKey: newKey, ClaudeSessionID: claudeID, Mode: env.Mode}) != nil {
					return
				}
			case "session.close":
				if h.Sessions == nil {
					continue
				}
				if err := h.Sessions.Close(env.SessionKey); err != nil {
					if s.write(Envelope{Type: "session.error", ID: env.ID, SessionKey: env.SessionKey, Error: err.Error()}) != nil {
						return
					}
					continue
				}
				if s.write(Envelope{Type: "session.closed", ID: env.ID, SessionKey: env.SessionKey}) != nil {
					return
				}
			case "session.subscribe":
				if h.Sessions == nil {
					continue
				}
				if subs >= maxSubscriptions {
					if s.write(Envelope{Type: "session.error", ID: env.ID, SessionKey: env.SessionKey, Error: "too many subscriptions"}) != nil {
						return
					}
					continue
				}
				history, live, cancel, err := h.Sessions.Subscribe(env.SessionKey, env.SinceSeq)
				if err != nil {
					if s.write(Envelope{Type: "session.error", ID: env.ID, SessionKey: env.SessionKey, Error: err.Error()}) != nil {
						return
					}
					continue
				}
				subs++
				if s.write(Envelope{Type: "session.events", ID: env.ID, SessionKey: env.SessionKey, Events: history}) != nil {
					cancel()
					return
				}
				go func(key string, live <-chan SessionEvent) {
					defer cancel()
					for {
						select {
						case ev, ok := <-live:
							if !ok {
								return
							}
							if s.write(Envelope{
								Type: "session.event", SessionKey: key, Seq: ev.Seq,
								Kind: ev.Kind, Text: ev.Text, Tool: ev.Tool,
								ToolID: ev.ToolID, ClaudeSessionID: ev.SessionID, ApprovalID: ev.ApprovalID,
							}) != nil {
								return
							}
						case <-ctx.Done():
							return
						}
					}
				}(env.SessionKey, live)
			default:
				if s.write(Envelope{Type: "error", ID: env.ID, Error: "unsupported"}) != nil {
					return
				}
			}
		case <-readErr:
			return
		case <-ctx.Done():
			return
		case <-idle.C:
			return
		}
	}
}

func shortFingerprint(fp string) string {
	if len(fp) <= 12 {
		return fp
	}
	return fp[:12]
}

func (h *Host) DeviceList() []DeviceInfo {
	online := map[string]bool{}
	h.mu.Lock()
	for _, pub := range h.conns {
		online[pub] = true
	}
	h.mu.Unlock()
	var out []DeviceInfo
	for _, d := range h.Authority.Devices() {
		out = append(out, DeviceInfo{
			ID:          d.ID,
			Name:        d.Name,
			Fingerprint: d.Fingerprint,
			LastSeen:    d.LastSeen,
			Online:      online[d.PublicKey],
			Revoked:     d.Revoked,
		})
	}
	return out
}

// Revoke 撤销设备并立刻断开它的连接。
func (h *Host) Revoke(ref string) (Device, error) {
	d, err := h.Authority.Revoke(ref)
	if err != nil {
		return d, err
	}
	h.mu.Lock()
	for c, pub := range h.conns {
		if pub == d.PublicKey {
			c.Close()
		}
	}
	h.mu.Unlock()
	return d, nil
}

func (h *Host) readyEnvelope(deviceID string) Envelope {
	env := Envelope{Type: "ready", DeviceID: deviceID, HostID: h.Authority.State().HostID, HostName: h.Name}
	if h.Sessions != nil {
		env.Features = []string{"sessions"}
	}
	return env
}
