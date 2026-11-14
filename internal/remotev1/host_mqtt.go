package remotev1

import (
	"context"
	"encoding/json"
	"sync"
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
			_ = h.reply(tr, ts.Pair(), hostID, h.nextSeq(did), Envelope{Type: "denied", Error: "identity"}, 1)
			return
		}
		st.nonce = RandomNonce()
		st.pub = env.DevicePub
		st.name = env.DeviceName
		st.inviteID = env.InviteID
		st.inviteSecret = env.InviteSecret
		_ = h.reply(tr, ts.Pair(), hostID, h.nextSeq(did), Envelope{
			Type: "challenge", Nonce: st.nonce, HostID: hostID, HostName: h.Name,
		}, 1)

	case "proof":
		pub, err := ParsePublic(st.pub)
		if err != nil || !VerifyProof(pub, st.nonce, st.pub, env.InviteID, env.Signature) {
			_ = h.reply(tr, ts.Pair(), hostID, h.nextSeq(did), Envelope{Type: "denied", Error: "proof"}, 1)
			return
		}
		device, known := h.Authority.Device(st.pub)
		if known {
			if device.Revoked {
				_ = h.reply(tr, ts.Pair(), hostID, h.nextSeq(did), Envelope{Type: "denied", Error: "revoked"}, 1)
				return
			}
			st.device = device
			_ = h.reply(tr, ts.Pair(), hostID, h.nextSeq(did), h.readyEnvelope(device.ID), 1)
			return
		}
		p, err := h.Authority.BeginPair(st.inviteID, st.inviteSecret, st.pub, st.name)
		if err != nil {
			_ = h.reply(tr, ts.Pair(), hostID, h.nextSeq(did), Envelope{Type: "denied", Error: "invitation"}, 1)
			return
		}
		_ = h.reply(tr, ts.Pair(), hostID, h.nextSeq(did), Envelope{Type: "pending", ID: p.ID}, 1)
		go func() {
			approved := <-p.Result
			st.mu.Lock()
			defer st.mu.Unlock()
			if approved {
				if d, ok := h.Authority.Device(st.pub); ok {
					st.device = d
					_ = h.reply(tr, ts.Pair(), hostID, h.nextSeq(did), h.readyEnvelope(d.ID), 1)
					return
				}
			}
			_ = h.reply(tr, ts.Pair(), hostID, h.nextSeq(did), Envelope{Type: "denied", Error: "approval"}, 1)
		}()

	default:
		if st.device.ID == "" {
			return
		}
		h.dispatchEnvelope(ctx, tr, ts, did, env)
	}
}

// dispatchEnvelope 在 Task 6 替换为完整业务分发。
func (h *Host) dispatchEnvelope(ctx context.Context, tr Transport, ts TopicSet, did string, env Envelope) {
	_ = h.reply(tr, ts.Evt(), h.Authority.State().HostID, h.nextSeq(did), Envelope{
		Type: "error", Error: "unsupported",
	}, 1)
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
