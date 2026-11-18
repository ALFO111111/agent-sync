package remotev1

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"sync"
	"time"
)

const (
	streamBatch      = 40 * time.Millisecond
	maxSubscriptions = 4
	maxDeviceStates  = 64
	DeviceKeyFeature = "device.key.v1"
)

type deviceState struct {
	mu           sync.Mutex
	window       *ReplayWindow
	device       Device
	pending      bool
	nonce        string
	pub          string
	name         string
	inviteID     string
	inviteSecret string
	features     []string
	historyPaged bool
	history      historyPager
	subs         map[string]context.CancelFunc
	uploads      map[string]Attachment
	lastSeen     time.Time
	created      time.Time

	queue      chan inbound
	workerOnce sync.Once
}

const deviceQueueSize = 64

// inbound 是 paho 回调解析完的一帧；真正的业务处理放到每设备自己的队列里。
type inbound struct {
	ctx  context.Context
	tr   Transport
	ts   TopicSet
	did  string
	kind string
	env  Envelope
}

func (h *Host) enqueue(st *deviceState, in inbound) {
	st.mu.Lock()
	if st.queue == nil {
		st.queue = make(chan inbound, deviceQueueSize)
	}
	st.mu.Unlock()
	select {
	case st.queue <- in:
	default:
		if h.Logf != nil {
			h.Logf("device %s queue full, dropping %s", in.did, in.env.Type)
		}
	}
	st.workerOnce.Do(func() { go h.deviceWorker(st) })
}

// deviceWorker 每台设备一个，串行处理自己的请求；慢的文件/git/HTTP 只影响这台设备。
func (h *Host) deviceWorker(st *deviceState) {
	for in := range st.queue {
		h.handleEnvelope(in.ctx, in.tr, in.ts, in.did, in.kind, in.env)
	}
}

// replaceSubscription 取消同一会话的旧流并登记新流；调用方需持有 st.mu。
func replaceSubscription(st *deviceState, sessionKey string, cancel context.CancelFunc) {
	if old := st.subs[sessionKey]; old != nil {
		old()
	}
	st.subs[sessionKey] = cancel
}

// clearSubscriptions 取消某设备的全部订阅；设备重连时调用。
func (h *Host) clearSubscriptions(st *deviceState) {
	st.mu.Lock()
	subs := st.subs
	st.subs = map[string]context.CancelFunc{}
	st.mu.Unlock()
	for _, cancel := range subs {
		cancel()
	}
}

// clearSubscriptionsLocked 同上，但调用方已经持有 st.mu；取消动作放到独立 goroutine。
func (h *Host) clearSubscriptionsLocked(st *deviceState) {
	subs := st.subs
	st.subs = map[string]context.CancelFunc{}
	for _, cancel := range subs {
		go cancel()
	}
}

func (h *Host) contentKey() ContentKey {
	k, _ := ParseContentKey(h.Authority.State().ContentKey)
	return k
}

func (h *Host) nextSeq(did string) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.seqs == nil {
		h.seqs = map[string]uint64{}
	}
	h.seqs[did]++
	return h.seqs[did]
}

// Run 订阅本机主题并处理手机消息，直到 ctx 结束。
func (h *Host) Run(ctx context.Context, tr Transport, ts TopicSet) error {
	h.mu.Lock()
	if h.devices == nil {
		h.devices = map[string]*deviceState{}
	}
	if h.seqs == nil {
		h.seqs = map[string]uint64{}
	}
	if h.attachments == nil {
		h.attachments = newAttachmentStore(h.AttachDir)
	}
	h.mu.Unlock()

	err := tr.Subscribe(ctx, ts.HostSubscriptions(), func(topic string, payload []byte) {
		h.onMessage(ctx, tr, ts, topic, payload)
	})
	if err != nil {
		return err
	}
	if err := tr.Publish(ctx, ts.Status(), []byte(`{"online":true}`), 1, true); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

func (h *Host) onMessage(ctx context.Context, tr Transport, ts TopicSet, topic string, payload []byte) {
	kind, uid, hid, did, ok := ParseTopic(topic)
	if !ok || uid != ts.UID || hid != ts.HID || did == "" {
		return
	}
	if !IsDeviceTopic(kind, did, did) {
		return
	}
	var f Frame
	if json.Unmarshal(payload, &f) != nil || f.From != did {
		return
	}
	// 先解密验证，再推进重放窗口：否则任何人伪造一个大序号就能把该设备的正常指令顶死。
	key := h.keyFor(did)
	if kind == "pair" {
		key = h.contentKey()
	}
	usedGlobal := false
	plain, err := OpenFrame(key, ClientToHost, did, f.Seq, f)
	if err != nil && kind != "pair" {
		// 旧 App 的重连握手仍走 req 主题、用全局密钥；新 App 一律走 pair 主题。
		plain, err = OpenFrame(h.contentKey(), ClientToHost, did, f.Seq, f)
		usedGlobal = err == nil
	}
	if err != nil {
		return
	}
	var env Envelope
	if json.Unmarshal(plain, &env) != nil {
		return
	}
	// 已经有独立密钥的设备，业务消息必须用独立密钥；全局密钥只允许握手。
	if usedGlobal && env.Type != "hello" && env.Type != "proof" {
		if d, ok := h.Authority.DeviceByRef(did); ok && d.ContentKey != "" {
			return
		}
	}

	h.mu.Lock()
	st := h.devices[did]
	if st == nil {
		if env.Type != "hello" {
			h.mu.Unlock()
			return
		}
		st = &deviceState{
			window:  NewReplayWindow(1024),
			subs:    map[string]context.CancelFunc{},
			created: time.Now(),
		}
		h.devices[did] = st
		for len(h.devices) > maxDeviceStates {
			h.evictOldestDeviceLocked()
		}
	}
	h.mu.Unlock()

	st.mu.Lock()
	fresh := st.window.Accept(f.Seq)
	if fresh {
		st.lastSeen = time.Now()
	}
	st.mu.Unlock()
	if !fresh {
		return
	}
	h.enqueue(st, inbound{ctx: ctx, tr: tr, ts: ts, did: did, kind: kind, env: env})
}

// evictOldestDeviceLocked 在设备状态过多时丢掉最早创建的一台；调用方持有 h.mu。
func (h *Host) evictOldestDeviceLocked() {
	var oldest string
	var oldestAt time.Time
	for did, st := range h.devices {
		if oldest == "" || st.created.Before(oldestAt) {
			oldest, oldestAt = did, st.created
		}
	}
	if oldest != "" {
		delete(h.devices, oldest)
	}
}

func (h *Host) readyWithCredentials(ctx context.Context, deviceID string) Envelope {
	env := h.readyEnvelope(deviceID)
	if h.Auth != nil {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		user, pass, err := h.Auth.MQTTCredentials(cctx, env.HostID)
		cancel()
		if err == nil {
			env.MQTTUser, env.MQTTPassword = user, pass
		}
	}
	return env
}

func (h *Host) handleEnvelope(ctx context.Context, tr Transport, ts TopicSet, did, kind string, env Envelope) {
	hostID := h.Authority.State().HostID
	dts := ts
	dts.DID = did
	h.mu.Lock()
	st := h.devices[did]
	if st == nil {
		st = &deviceState{
			window:  NewReplayWindow(1024),
			subs:    map[string]context.CancelFunc{},
			created: time.Now(),
		}
		h.devices[did] = st
	}
	h.mu.Unlock()
	st.mu.Lock()
	defer st.mu.Unlock()

	switch env.Type {
	case "hello":
		// 新连接 = 旧连接已死，旧事件流必须停掉，否则重连会不断堆积订阅。
		h.clearSubscriptionsLocked(st)
		if _, err := ParsePublic(env.DevicePub); err != nil {
			_ = h.replyPair(tr, dts.Pair(), h.nextSeq(did), Envelope{Type: "denied", Error: "identity"}, 1)
			return
		}
		st.nonce = RandomNonce()
		st.pub = env.DevicePub
		st.name = env.DeviceName
		st.inviteID = env.InviteID
		st.inviteSecret = env.InviteSecret
		st.features = env.Features
		for _, f := range env.Features {
			if f == HistoryPagedFeature {
				st.historyPaged = true
			}
		}
		_ = h.replyPair(tr, dts.Pair(), h.nextSeq(did), Envelope{
			Type: "challenge", Nonce: st.nonce, HostID: hostID, HostName: h.Name,
			Features: []string{DeviceKeyFeature},
		}, 1)

	case "proof":
		pub, err := ParsePublic(st.pub)
		if err != nil || !VerifyProof(pub, st.nonce, st.pub, env.InviteID, env.Signature, env.DeviceKey) {
			_ = h.replyPair(tr, dts.Pair(), h.nextSeq(did), Envelope{Type: "denied", Error: "proof"}, 1)
			return
		}
		if env.DeviceKey != "" {
			if _, err := ParseContentKey(env.DeviceKey); err != nil {
				_ = h.replyPair(tr, dts.Pair(), h.nextSeq(did), Envelope{Type: "denied", Error: "device_key"}, 1)
				return
			}
		}
		device, known := h.Authority.Device(st.pub)
		if known {
			if device.Revoked {
				_ = h.replyPair(tr, dts.Pair(), h.nextSeq(did), Envelope{Type: "denied", Error: "revoked"}, 1)
				return
			}
			if env.DeviceKey != "" && device.ContentKey != env.DeviceKey {
				if err := h.Authority.SetDeviceKey(st.pub, env.DeviceKey); err != nil {
					_ = h.replyPair(tr, dts.Pair(), h.nextSeq(did), Envelope{Type: "denied", Error: "device_key"}, 1)
					return
				}
				device.ContentKey = env.DeviceKey
			}
			if err := h.registerDevice(ctx, hostID, did, device); err != nil {
				_ = h.replyPair(tr, dts.Pair(), h.nextSeq(did), Envelope{Type: "denied", Error: "device_limit"}, 1)
				return
			}
			st.device = device
			_ = h.replyPair(tr, dts.Pair(), h.nextSeq(did), h.readyWithCredentials(ctx, device.ID), 1)
			return
		}
		p, err := h.Authority.BeginPair(st.inviteID, st.inviteSecret, st.pub, st.name, env.DeviceKey)
		if err != nil {
			_ = h.replyPair(tr, dts.Pair(), h.nextSeq(did), Envelope{Type: "denied", Error: "invitation"}, 1)
			return
		}
		_ = h.replyPair(tr, dts.Pair(), h.nextSeq(did), Envelope{Type: "pending", ID: p.ID}, 1)
		go func() {
			select {
			case approved := <-p.Result:
				st.mu.Lock()
				defer st.mu.Unlock()
				if approved {
					if d, ok := h.Authority.Device(st.pub); ok {
						if err := h.registerDevice(ctx, hostID, did, d); err != nil {
							_ = h.replyPair(tr, dts.Pair(), h.nextSeq(did), Envelope{Type: "denied", Error: "device_limit"}, 1)
							return
						}
						st.device = d
						_ = h.replyPair(tr, dts.Pair(), h.nextSeq(did), h.readyWithCredentials(ctx, d.ID), 1)
						return
					}
				}
				_ = h.replyPair(tr, dts.Pair(), h.nextSeq(did), Envelope{Type: "denied", Error: "approval"}, 1)
			case <-time.After(time.Until(p.Expires)):
				_ = h.replyPair(tr, dts.Pair(), h.nextSeq(did), Envelope{Type: "denied", Error: "expired"}, 1)
			}
		}()

	default:
		dev := st.device
		if dev.ID == "" || !h.Authority.IsActive(dev.ID) {
			return
		}
		h.dispatchEnvelope(ctx, tr, ts, did, st, env)
	}
}

// dispatchEnvelope 把业务请求映射到现有 Files/Sessions/Data 能力。
// 调用方必须持有 st.mu。
func (h *Host) dispatchEnvelope(ctx context.Context, tr Transport, ts TopicSet, did string, st *deviceState, env Envelope) {
	hostID := h.Authority.State().HostID
	send := func(e Envelope) error {
		return h.reply(tr, ts.Evt(), did, h.nextSeq(did), e, 1)
	}

	switch env.Type {
	case "ping":
		_ = send(Envelope{Type: "pong"})

	case "echo":
		if len(env.Data) > 4096 {
			return
		}
		_ = send(Envelope{Type: "echo", ID: env.ID, Data: env.Data})

	case "device.list":
		_ = send(Envelope{Type: "devices", ID: env.ID, Devices: h.DeviceList()})

	case "status":
		_ = send(Envelope{Type: "status", ID: env.ID, DeviceID: st.device.ID, HostID: hostID, HostName: h.Name})

	case "data.overview":
		if h.Data == nil {
			_ = send(Envelope{Type: "data.overview", ID: env.ID, Error: "overview unsupported"})
			return
		}
		ov, err := h.Data.Overview()
		if err != nil {
			_ = send(Envelope{Type: "data.overview", ID: env.ID, Error: err.Error()})
			return
		}
		ov.HostName = h.Name
		_ = send(Envelope{Type: "data.overview", ID: env.ID, Overview: &ov})

	case "fs.list":
		if h.Files == nil {
			_ = send(Envelope{Type: "fs.error", ID: env.ID, Error: "files unsupported"})
			return
		}
		entries, err := h.Files.List(env.Cwd, env.Path)
		if err != nil {
			_ = send(Envelope{Type: "fs.error", ID: env.ID, Error: err.Error()})
			return
		}
		_ = send(Envelope{Type: "fs.list", ID: env.ID, Path: env.Path, Entries: entries})

	case "fs.read":
		if h.Files == nil {
			_ = send(Envelope{Type: "fs.error", ID: env.ID, Error: "files unsupported"})
			return
		}
		text, truncated, binary, next, err := h.Files.Read(env.Cwd, env.Path, env.Offset)
		if err != nil {
			_ = send(Envelope{Type: "fs.error", ID: env.ID, Error: err.Error()})
			return
		}
		resp := Envelope{Type: "fs.read", ID: env.ID, Path: env.Path, Text: text,
			Truncated: truncated, Binary: binary, Next: next}
		for len(resp.Text) > 0 {
			b, _ := json.Marshal(resp)
			if len(b) <= MaxPlain {
				break
			}
			resp.Text = resp.Text[:len(resp.Text)/2]
			resp.Next = env.Offset + int64(len(resp.Text))
			resp.Truncated = true
		}
		_ = send(resp)

	case "git.status":
		if h.Files == nil {
			_ = send(Envelope{Type: "fs.error", ID: env.ID, Error: "files unsupported"})
			return
		}
		changes, err := h.Files.GitStatus(env.Cwd)
		if err != nil {
			_ = send(Envelope{Type: "fs.error", ID: env.ID, Error: err.Error()})
			return
		}
		_ = send(Envelope{Type: "git.status", ID: env.ID, Changes: changes})

	case "git.diff":
		if h.Files == nil {
			_ = send(Envelope{Type: "fs.error", ID: env.ID, Error: "files unsupported"})
			return
		}
		text, truncated, err := h.Files.GitDiff(env.Cwd, env.Path)
		if err != nil {
			_ = send(Envelope{Type: "fs.error", ID: env.ID, Error: err.Error()})
			return
		}
		_ = send(Envelope{Type: "git.diff", ID: env.ID, Path: env.Path, Text: text, Truncated: truncated})

	case "session.history":
		if h.Sessions == nil {
			return
		}
		load := func() ([]HistoryMessage, error) { return h.Sessions.History(env.Tool, env.Cwd, env.ClaudeSessionID) }
		if st.historyPaged {
			chunks, err := st.history.page(env, load)
			if err != nil {
				_ = send(legacyHistory(env, nil, err))
				return
			}
			for _, chunk := range chunks {
				if send(chunk) != nil {
					return
				}
			}
			return
		}
		msgs, err := load()
		_ = send(legacyHistory(env, msgs, err))

	case "session.active":
		if h.Sessions == nil {
			_ = send(Envelope{Type: "session.active", ID: env.ID})
			return
		}
		_ = send(Envelope{Type: "session.active", ID: env.ID, Active: h.Sessions.ActiveSessions()})

	case "session.projects":
		if h.Sessions == nil {
			_ = send(Envelope{Type: "session.error", ID: env.ID, Error: "sessions unsupported"})
			return
		}
		list, err := h.Sessions.Projects(env.Tool)
		if err != nil {
			_ = send(Envelope{Type: "session.error", ID: env.ID, Error: err.Error()})
			return
		}
		_ = send(Envelope{Type: "session.projects", ID: env.ID, Projects: list})

	case "session.list":
		if h.Sessions == nil {
			_ = send(Envelope{Type: "session.error", ID: env.ID, Error: "sessions unsupported"})
			return
		}
		list, err := h.Sessions.Sessions(env.Tool, env.Cwd)
		if err != nil {
			_ = send(Envelope{Type: "session.error", ID: env.ID, Error: err.Error()})
			return
		}
		_ = send(Envelope{Type: "session.list", ID: env.ID, Sessions: list})

	case "session.start":
		if h.Sessions == nil {
			_ = send(Envelope{Type: "session.error", ID: env.ID, Error: "sessions unsupported"})
			return
		}
		key, claudeID, err := h.Sessions.Start(env.Tool, env.Cwd, env.ResumeID, env.Mode)
		if err != nil {
			_ = send(Envelope{Type: "session.error", ID: env.ID, Error: err.Error()})
			return
		}
		_ = send(Envelope{Type: "session.ready", ID: env.ID, SessionKey: key, ClaudeSessionID: claudeID})

	case "turn.send":
		if h.Sessions == nil {
			return
		}
		if len(env.Attachments) > MaxAttachments {
			_ = send(Envelope{Type: "session.error", ID: env.ID, SessionKey: env.SessionKey, Error: "too many attachments"})
			return
		}
		atts := make([]Attachment, 0, len(env.Attachments))
		for _, id := range env.Attachments {
			a, ok := st.uploads[id]
			if !ok {
				_ = send(Envelope{Type: "session.error", ID: env.ID, SessionKey: env.SessionKey, Error: "attachment missing"})
				return
			}
			atts = append(atts, a)
			delete(st.uploads, id)
		}
		if err := h.Sessions.Send(env.SessionKey, env.Text, atts); err != nil {
			_ = send(Envelope{Type: "session.error", ID: env.ID, SessionKey: env.SessionKey, Error: err.Error()})
		}

	case "turn.stop":
		if h.Sessions == nil {
			return
		}
		if err := h.Sessions.Stop(env.SessionKey); err != nil {
			_ = send(Envelope{Type: "session.error", ID: env.ID, SessionKey: env.SessionKey, Error: err.Error()})
		}

	case "approval.decide":
		if h.Sessions == nil {
			return
		}
		if err := h.Sessions.Decide(env.SessionKey, env.ApprovalID, env.Allow); err != nil {
			_ = send(Envelope{Type: "session.error", ID: env.ID, SessionKey: env.SessionKey, Error: err.Error()})
		}

	case "session.setmode":
		if h.Sessions == nil {
			return
		}
		newKey, claudeID, err := h.Sessions.SetMode(env.SessionKey, env.Mode)
		if err != nil {
			_ = send(Envelope{Type: "session.error", ID: env.ID, SessionKey: env.SessionKey, Error: err.Error()})
			return
		}
		_ = send(Envelope{Type: "session.ready", ID: env.ID, SessionKey: newKey, ClaudeSessionID: claudeID, Mode: env.Mode})

	case "session.close":
		if h.Sessions == nil {
			return
		}
		if err := h.Sessions.Close(env.SessionKey); err != nil {
			_ = send(Envelope{Type: "session.error", ID: env.ID, SessionKey: env.SessionKey, Error: err.Error()})
			return
		}
		_ = send(Envelope{Type: "session.closed", ID: env.ID, SessionKey: env.SessionKey})

	case "session.subscribe":
		if h.Sessions == nil {
			return
		}
		if len(st.subs) >= maxSubscriptions {
			if _, same := st.subs[env.SessionKey]; !same {
				_ = send(Envelope{Type: "session.error", ID: env.ID, SessionKey: env.SessionKey, Error: "too many subscriptions"})
				return
			}
		}
		// 同一会话先释放旧流，避免重连后重复推送。
		if old := st.subs[env.SessionKey]; old != nil {
			delete(st.subs, env.SessionKey)
			go old()
		}

		history, live, cancel, err := h.Sessions.Subscribe(env.SessionKey, env.SinceSeq)
		if err != nil {
			_ = send(Envelope{Type: "session.error", ID: env.ID, SessionKey: env.SessionKey, Error: err.Error()})
			return
		}
		replaceSubscription(st, env.SessionKey, cancel)

		if send(Envelope{Type: "session.events", ID: env.ID, SessionKey: env.SessionKey, Events: history}) != nil {
			delete(st.subs, env.SessionKey)
			cancel()
			return
		}
		go func() {
			defer h.releaseSub(did, env.SessionKey)
			h.streamEvents(ctx, tr, ts, did, env.SessionKey, live, cancel)
		}()

	case "session.unsubscribe":
		cancel := st.subs[env.SessionKey]
		delete(st.subs, env.SessionKey)
		if cancel != nil {
			go cancel()
		}
		_ = send(Envelope{Type: "session.unsubscribed", ID: env.ID, SessionKey: env.SessionKey})

	case "attach.begin":
		if st.uploads == nil {
			st.uploads = map[string]Attachment{}
		}
		if len(st.uploads) >= maxPendingAttachments {
			_ = send(Envelope{Type: "attach.error", ID: env.ID, Error: "too many pending attachments"})
			return
		}
		switch env.Kind {
		case "image", "text", "binary":
		default:
			_ = send(Envelope{Type: "attach.error", ID: env.ID, Error: "invalid attachment kind"})
			return
		}
		id := RandomToken(16)
		if err := h.attachments.Begin(id, env.Size, env.SHA256); err != nil {
			_ = send(Envelope{Type: "attach.error", ID: env.ID, Error: err.Error()})
			return
		}
		st.uploads[id] = Attachment{
			Name:      sanitizeAttachmentName(env.Name),
			MediaType: env.MediaType,
			Kind:      env.Kind,
		}
		_ = send(Envelope{Type: "attach.begin", ID: env.ID, AttachID: id, ChunkSize: MQTTAttachChunk})

	case "attach.chunk":
		raw, err := base64.StdEncoding.DecodeString(env.Data)
		if err == nil {
			err = h.attachments.Write(env.AttachID, int(env.Seq), raw)
		}
		if err != nil {
			_ = send(Envelope{Type: "attach.error", AttachID: env.AttachID, Error: err.Error()})
		}

	case "attach.end":
		path, err := h.attachments.Commit(env.AttachID)
		if err != nil {
			_ = send(Envelope{Type: "attach.error", ID: env.ID, AttachID: env.AttachID, Error: err.Error()})
			return
		}
		data, err := os.ReadFile(path)
		os.Remove(path)
		if err != nil {
			_ = send(Envelope{Type: "attach.error", ID: env.ID, AttachID: env.AttachID, Error: err.Error()})
			return
		}
		a, ok := st.uploads[env.AttachID]
		if !ok {
			_ = send(Envelope{Type: "attach.error", ID: env.ID, AttachID: env.AttachID, Error: "unknown attachment"})
			return
		}
		a.Data = data
		st.uploads[env.AttachID] = a
		_ = send(Envelope{Type: "attach.ready", ID: env.ID, AttachID: env.AttachID})

	default:
		_ = send(Envelope{Type: "error", ID: env.ID, Error: "unsupported"})
	}
}

func (h *Host) registerDevice(ctx context.Context, hostID, did string, d Device) error {
	if h.Auth == nil {
		return nil
	}
	return h.Auth.RegisterDevice(ctx, hostID, did, d.Name, d.PublicKey)
}

func (h *Host) releaseSub(did, sessionKey string) {
	h.mu.Lock()
	st := h.devices[did]
	h.mu.Unlock()
	if st == nil {
		return
	}
	st.mu.Lock()
	delete(st.subs, sessionKey)
	st.mu.Unlock()
}

// streamEvents 把高频事件按 100ms 合批后以 QoS 0 发布到 evt。
func (h *Host) streamEvents(ctx context.Context, tr Transport, ts TopicSet, did, sessionKey string, live <-chan SessionEvent, cancel func()) {
	defer cancel()
	t := time.NewTicker(streamBatch)
	defer t.Stop()
	var buf []SessionEvent
	flush := func() {
		if len(buf) == 0 {
			return
		}
		env := Envelope{Type: "session.events", SessionKey: sessionKey, Events: buf}
		_ = h.reply(tr, ts.Evt(), did, h.nextSeq(did), env, 0)
		buf = buf[:0]
	}
	for {
		select {
		case ev, ok := <-live:
			if !ok {
				flush()
				return
			}
			buf = append(buf, ev)
			if len(buf) >= 100 {
				flush()
			}
		case <-t.C:
			flush()
		case <-ctx.Done():
			flush()
			return
		}
	}
}

// reply 用收件设备自己的会话密钥加密业务回复。
func (h *Host) reply(tr Transport, topic, did string, seq uint64, env Envelope, qos byte) error {
	return h.replyWithKey(tr, topic, h.keyFor(did), seq, env, qos)
}

// replyPair 只用全局配对密钥，用于 hello/proof 阶段的 challenge/pending/ready/denied。
func (h *Host) replyPair(tr Transport, topic string, seq uint64, env Envelope, qos byte) error {
	return h.replyWithKey(tr, topic, h.contentKey(), seq, env, qos)
}

func (h *Host) replyWithKey(tr Transport, topic string, key ContentKey, seq uint64, env Envelope, qos byte) error {
	plain, err := json.Marshal(env)
	if err != nil {
		return err
	}
	f, err := SealFrame(key, HostToClient, h.Authority.State().HostID, seq, plain)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return tr.Publish(ctx, topic, raw, qos, false)
}
