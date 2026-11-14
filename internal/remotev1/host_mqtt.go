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
	streamBatch      = 100 * time.Millisecond
	maxSubscriptions = 4
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
	subs         int
	uploads      map[string]Attachment
	lastSeen     time.Time
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
	h.mu.Lock()
	st := h.devices[did]
	if st == nil {
		st = &deviceState{window: NewReplayWindow(1024)}
		h.devices[did] = st
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
	key, err := ParseContentKey(h.Authority.State().ContentKey)
	if err != nil {
		return
	}
	plain, err := OpenFrame(key, ClientToHost, did, f.Seq, f)
	if err != nil {
		return
	}
	var env Envelope
	if json.Unmarshal(plain, &env) != nil {
		return
	}
	h.handleEnvelope(ctx, tr, ts, did, kind, env)
}

func (h *Host) handleEnvelope(ctx context.Context, tr Transport, ts TopicSet, did, kind string, env Envelope) {
	hostID := h.Authority.State().HostID
	dts := ts
	dts.DID = did
	h.mu.Lock()
	st := h.devices[did]
	if st == nil {
		st = &deviceState{window: NewReplayWindow(1024)}
		h.devices[did] = st
	}
	h.mu.Unlock()
	st.mu.Lock()
	defer st.mu.Unlock()

	switch env.Type {
	case "hello":
		if _, err := ParsePublic(env.DevicePub); err != nil {
			_ = h.reply(tr, dts.Pair(), hostID, h.nextSeq(did), Envelope{Type: "denied", Error: "identity"}, 1)
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
		_ = h.reply(tr, dts.Pair(), hostID, h.nextSeq(did), Envelope{
			Type: "challenge", Nonce: st.nonce, HostID: hostID, HostName: h.Name,
		}, 1)

	case "proof":
		pub, err := ParsePublic(st.pub)
		if err != nil || !VerifyProof(pub, st.nonce, st.pub, env.InviteID, env.Signature) {
			_ = h.reply(tr, dts.Pair(), hostID, h.nextSeq(did), Envelope{Type: "denied", Error: "proof"}, 1)
			return
		}
		device, known := h.Authority.Device(st.pub)
		if known {
			if device.Revoked {
				_ = h.reply(tr, dts.Pair(), hostID, h.nextSeq(did), Envelope{Type: "denied", Error: "revoked"}, 1)
				return
			}
			st.device = device
			_ = h.reply(tr, dts.Pair(), hostID, h.nextSeq(did), h.readyEnvelope(device.ID), 1)
			return
		}
		p, err := h.Authority.BeginPair(st.inviteID, st.inviteSecret, st.pub, st.name)
		if err != nil {
			_ = h.reply(tr, dts.Pair(), hostID, h.nextSeq(did), Envelope{Type: "denied", Error: "invitation"}, 1)
			return
		}
		_ = h.reply(tr, dts.Pair(), hostID, h.nextSeq(did), Envelope{Type: "pending", ID: p.ID}, 1)
		go func() {
			approved := <-p.Result
			st.mu.Lock()
			defer st.mu.Unlock()
			if approved {
				if d, ok := h.Authority.Device(st.pub); ok {
					st.device = d
					_ = h.reply(tr, dts.Pair(), hostID, h.nextSeq(did), h.readyEnvelope(d.ID), 1)
					return
				}
			}
			_ = h.reply(tr, dts.Pair(), hostID, h.nextSeq(did), Envelope{Type: "denied", Error: "approval"}, 1)
		}()

	default:
		if st.device.ID == "" {
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
		return h.reply(tr, ts.Evt(), hostID, h.nextSeq(did), e, 1)
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
		if st.subs >= maxSubscriptions {
			_ = send(Envelope{Type: "session.error", ID: env.ID, SessionKey: env.SessionKey, Error: "too many subscriptions"})
			return
		}
		history, live, cancel, err := h.Sessions.Subscribe(env.SessionKey, env.SinceSeq)
		if err != nil {
			_ = send(Envelope{Type: "session.error", ID: env.ID, SessionKey: env.SessionKey, Error: err.Error()})
			return
		}
		st.subs++
		if send(Envelope{Type: "session.events", ID: env.ID, SessionKey: env.SessionKey, Events: history}) != nil {
			cancel()
			st.subs--
			return
		}
		go func() {
			defer h.releaseSub(did)
			h.streamEvents(ctx, tr, ts, did, env.SessionKey, live, cancel)
		}()

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

func (h *Host) releaseSub(did string) {
	h.mu.Lock()
	st := h.devices[did]
	h.mu.Unlock()
	if st == nil {
		return
	}
	st.mu.Lock()
	if st.subs > 0 {
		st.subs--
	}
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
		_ = h.reply(tr, ts.Evt(), h.Authority.State().HostID, h.nextSeq(did), env, 0)
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

func (h *Host) reply(tr Transport, topic string, from string, seq uint64, env Envelope, qos byte) error {
	plain, err := json.Marshal(env)
	if err != nil {
		return err
	}
	f, err := SealFrame(h.contentKey(), HostToClient, from, seq, plain)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return tr.Publish(context.Background(), topic, raw, qos, false)
}
