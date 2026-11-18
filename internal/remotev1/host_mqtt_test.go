package remotev1

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

type fakeTransport struct {
	mu      sync.Mutex
	subs    []string
	handler func(string, []byte)
	sent    chan struct {
		topic   string
		payload []byte
	}
	ready chan struct{}
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{
		sent: make(chan struct {
			topic   string
			payload []byte
		}, 16),
		ready: make(chan struct{}),
	}
}

func (f *fakeTransport) Subscribe(_ context.Context, filters []string, h func(string, []byte)) error {
	f.mu.Lock()
	f.subs = append(f.subs, filters...)
	f.handler = h
	f.mu.Unlock()
	close(f.ready)
	return nil
}

func (f *fakeTransport) Publish(_ context.Context, topic string, p []byte, _ byte, _ bool) error {
	f.sent <- struct {
		topic   string
		payload []byte
	}{topic, p}
	return nil
}

func (f *fakeTransport) inject(topic string, p []byte) {
	f.mu.Lock()
	h := f.handler
	f.mu.Unlock()
	h(topic, p)
}

func TestHostPairingOverTransport(t *testing.T) {
	key, _ := NewContentKey()
	auth := NewMemoryAuthority(t, key)
	host := &Host{Authority: auth, Name: "pc", Logf: func(string, ...any) {}}
	tr := newFakeTransport()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go host.Run(ctx, tr, TopicSet{UID: "u1", HID: "h1", DID: "d1"})
	<-tr.ready

	f, _ := SealFrame(key, ClientToHost, "d1", 1, mustJSON(t, Envelope{
		Type: "hello", DevicePub: testDevicePub(t), DeviceName: "phone",
	}))
	tr.inject("u/u1/h/h1/pair/d1", mustJSON(t, f))
	nextOn(t, tr, "u/u1/h/h1/pair/d1")
}

func TestHostDispatchDeviceList(t *testing.T) {
	key, _ := NewContentKey()
	auth := NewMemoryAuthority(t, key)
	id, _ := NewIdentity()
	auth.state.Devices = append(auth.state.Devices, Device{
		ID: "d1", Name: "phone", PublicKey: id.PublicBase64(), Fingerprint: id.Fingerprint(),
	})
	host := &Host{Authority: auth, Name: "pc", Logf: func(string, ...any) {}}
	tr := newFakeTransport()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go host.Run(ctx, tr, TopicSet{UID: "u1", HID: "h1", DID: "d1"})
	<-tr.ready

	f, _ := SealFrame(key, ClientToHost, "d1", 1, mustJSON(t, Envelope{
		Type: "hello", DevicePub: id.PublicBase64(), DeviceName: "phone",
	}))
	tr.inject("u/u1/h/h1/pair/d1", mustJSON(t, f))

	challenge := readEnvelope(t, key, auth.state.HostID, 1, nextOn(t, tr, "u/u1/h/h1/pair/d1"))
	if challenge.Type != "challenge" {
		t.Fatalf("want challenge, got %s", challenge.Type)
	}

	proof := Envelope{
		Type: "proof", DevicePub: id.PublicBase64(),
		Signature: id.SignProof(challenge.Nonce, ""),
	}
	f, _ = SealFrame(key, ClientToHost, "d1", 2, mustJSON(t, proof))
	tr.inject("u/u1/h/h1/pair/d1", mustJSON(t, f))

	ready := readEnvelope(t, key, auth.state.HostID, 2, nextOn(t, tr, "u/u1/h/h1/pair/d1"))
	if ready.Type != "ready" {
		t.Fatalf("want ready, got %s", ready.Type)
	}

	f, _ = SealFrame(key, ClientToHost, "d1", 3, mustJSON(t, Envelope{Type: "device.list", ID: "q1"}))
	tr.inject("u/u1/h/h1/req/d1", mustJSON(t, f))

	reply := readEnvelope(t, key, auth.state.HostID, 3, nextOn(t, tr, "u/u1/h/h1/evt"))
	if reply.Type != "devices" {
		t.Fatalf("want devices, got %s", reply.Type)
	}
}

func TestForgedFrameDoesNotAdvanceReplayWindow(t *testing.T) {
	key, _ := NewContentKey()
	auth := NewMemoryAuthority(t, key)
	h := &Host{Authority: auth, Logf: func(string, ...any) {}}
	h.devices = map[string]*deviceState{}
	ts := TopicSet{UID: "u1", HID: "h1", DID: "d1"}
	// 用错误的密钥封一个极大序号；解密必须失败，且窗口不能前进。
	wrong, _ := NewContentKey()
	f, err := SealFrame(wrong, ClientToHost, "d1", 1<<40, []byte(`{"v":1,"type":"ping"}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(f)
	h.onMessage(context.Background(), newFakeTransport(), ts, ts.Req(), raw)
	if _, ok := h.devices["d1"]; ok {
		t.Fatal("forged frame created device state")
	}
}

func TestUnknownDeviceWithoutHelloIsIgnored(t *testing.T) {
	key, _ := NewContentKey()
	auth := NewMemoryAuthority(t, key)
	h := &Host{Authority: auth}
	h.devices = map[string]*deviceState{}
	ts := TopicSet{UID: "u1", HID: "h1", DID: "d1"}
	plain := []byte(`{"v":1,"type":"fs.list"}`)
	f, _ := SealFrame(key, ClientToHost, "d1", 1, plain)
	raw, _ := json.Marshal(f)
	h.onMessage(context.Background(), newFakeTransport(), ts, ts.Req(), raw)
	if _, ok := h.devices["d1"]; ok {
		t.Fatal("non-hello message created state for unknown device")
	}
}

func TestSubscribeSameSessionReplacesOldStream(t *testing.T) {
	st := &deviceState{subs: map[string]context.CancelFunc{}}
	cancelled := 0
	st.subs["sk"] = func() { cancelled++ }
	replaceSubscription(st, "sk", func() {})
	if cancelled != 1 {
		t.Fatalf("old cancel calls = %d, want 1", cancelled)
	}
	if len(st.subs) != 1 {
		t.Fatalf("subs = %d, want 1", len(st.subs))
	}
}

func TestHelloClearsPreviousSubscriptions(t *testing.T) {
	key, _ := NewContentKey()
	auth := NewMemoryAuthority(t, key)
	h := &Host{Authority: auth, Logf: func(string, ...any) {}}
	cancelled := make(chan struct{}, 2)
	st := &deviceState{window: NewReplayWindow(1024), subs: map[string]context.CancelFunc{
		"s1": func() { cancelled <- struct{}{} },
		"s2": func() { cancelled <- struct{}{} },
	}}
	h.devices = map[string]*deviceState{"d1": st}

	h.handleEnvelope(context.Background(), newFakeTransport(),
		TopicSet{UID: "u1", HID: "h1", DID: "d1"}, "d1", "pair",
		Envelope{Type: "hello", DevicePub: "bad"})

	for i := 0; i < 2; i++ {
		select {
		case <-cancelled:
		case <-time.After(time.Second):
			t.Fatal("subscription was not cancelled")
		}
	}
	st.mu.Lock()
	n := len(st.subs)
	st.mu.Unlock()
	if n != 0 {
		t.Fatalf("subs = %d, want 0", n)
	}
}

func TestEachDeviceUsesItsOwnKey(t *testing.T) {
	global, _ := NewContentKey()
	k1, _ := NewContentKey()
	k2, _ := NewContentKey()
	auth := NewMemoryAuthority(t, global)
	auth.state.Devices = append(auth.state.Devices,
		Device{ID: "d1", Fingerprint: "fp1", PublicKey: "p1", ContentKey: k1.Base64()},
		Device{ID: "d2", Fingerprint: "fp2", PublicKey: "p2", ContentKey: k2.Base64()},
	)
	h := &Host{Authority: auth}
	h.devices = map[string]*deviceState{
		"fp1": {device: Device{ID: "d1", Fingerprint: "fp1", ContentKey: k1.Base64()}},
		"fp2": {device: Device{ID: "d2", Fingerprint: "fp2", ContentKey: k2.Base64()}},
	}
	if h.keyFor("fp1").Base64() != k1.Base64() {
		t.Fatal("fp1 key mismatch")
	}
	if h.keyFor("fp2").Base64() != k2.Base64() {
		t.Fatal("fp2 key mismatch")
	}
	// 用 d1 的密钥加密的帧，d2 的密钥必须解不开。
	f, err := SealFrame(k1, HostToClient, "host", 1, []byte(`{"type":"pong"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFrame(k2, HostToClient, "host", 1, f); err == nil {
		t.Fatal("frame encrypted for d1 must not open with d2's key")
	}
}

func TestRemovingOneDeviceKeepsOtherDevice(t *testing.T) {
	global, _ := NewContentKey()
	k1, _ := NewContentKey()
	k2, _ := NewContentKey()
	auth := NewMemoryAuthority(t, global)
	auth.state.Devices = append(auth.state.Devices,
		Device{ID: "d1", Fingerprint: "fp1", PublicKey: "p1", ContentKey: k1.Base64()},
		Device{ID: "d2", Fingerprint: "fp2", PublicKey: "p2", ContentKey: k2.Base64()},
	)
	h := &Host{Authority: auth, Logf: func(string, ...any) {}}
	cancelled := 0
	h.devices = map[string]*deviceState{
		"fp1": {device: Device{ID: "d1", Fingerprint: "fp1", ContentKey: k1.Base64()},
			subs: map[string]context.CancelFunc{"s": func() { cancelled++ }}},
		"fp2": {device: Device{ID: "d2", Fingerprint: "fp2", ContentKey: k2.Base64()},
			subs: map[string]context.CancelFunc{}},
	}
	if err := h.RemoveDevice("d1"); err != nil {
		t.Fatal(err)
	}
	if cancelled != 1 {
		t.Fatalf("cancelled = %d, want 1", cancelled)
	}
	if _, ok := h.devices["fp1"]; ok {
		t.Fatal("removed device state remains")
	}
	if _, ok := h.devices["fp2"]; !ok {
		t.Fatal("other device state was dropped")
	}
	if auth.State().ContentKey != global.Base64() {
		t.Fatal("global pairing key must not rotate")
	}
	if !auth.IsActive("d2") {
		t.Fatal("other device should stay active")
	}
}

type blockingSessions struct {
	SessionSink
	started chan struct{}
	release chan struct{}
}

func (b *blockingSessions) History(tool, cwd, sessionID string) ([]HistoryMessage, error) {
	close(b.started)
	<-b.release
	return nil, nil
}

func TestSlowDeviceDoesNotBlockAnotherDevice(t *testing.T) {
	key, _ := NewContentKey()
	auth := NewMemoryAuthority(t, key)
	auth.state.Devices = append(auth.state.Devices,
		Device{ID: "slow", Fingerprint: "slow"},
		Device{ID: "fast", Fingerprint: "fast"},
	)
	slow := &blockingSessions{started: make(chan struct{}), release: make(chan struct{})}
	h := &Host{Authority: auth, Sessions: slow, Logf: func(string, ...any) {}}
	h.devices = map[string]*deviceState{
		"slow": {window: NewReplayWindow(1024), subs: map[string]context.CancelFunc{}, device: Device{ID: "slow"}},
		"fast": {window: NewReplayWindow(1024), subs: map[string]context.CancelFunc{}, device: Device{ID: "fast"}},
	}
	tr := newFakeTransport()
	ts := TopicSet{UID: "u1", HID: "h1", DID: "slow"}
	// 慢设备先发一个会阻塞的请求。
	h.enqueue(h.devices["slow"], inbound{ctx: context.Background(), tr: tr, ts: ts, did: "slow", kind: "pair",
		env: Envelope{Type: "session.history", ID: "1", Tool: "claude", Cwd: "/", ClaudeSessionID: "s1"}})
	<-slow.started
	// 快设备发 ping，必须在 100ms 内收到回复。
	start := time.Now()
	h.enqueue(h.devices["fast"], inbound{ctx: context.Background(), tr: tr, ts: ts, did: "fast", kind: "pair",
		env: Envelope{Type: "ping", ID: "2"}})
	select {
	case <-tr.sent:
		if time.Since(start) > 100*time.Millisecond {
			t.Fatalf("fast device waited %v", time.Since(start))
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("fast device was blocked by slow device")
	}
	close(slow.release)
}

func TestStreamEventsAreBatched(t *testing.T) {
	key, _ := NewContentKey()
	auth := NewMemoryAuthority(t, key)
	h := &Host{Authority: auth, Logf: func(string, ...any) {}}
	tr := newFakeTransport()
	ts := TopicSet{UID: "u1", HID: "h1", DID: "d1"}
	live := make(chan SessionEvent, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go h.streamEvents(ctx, tr, ts, "d1", "s1", live, func() {})
	for i := 1; i <= 10; i++ {
		live <- SessionEvent{Seq: int64(i), Kind: "text_delta", Text: "x"}
	}

	time.Sleep(250 * time.Millisecond)
	cancel()
	time.Sleep(50 * time.Millisecond)

	messages, events := 0, 0
	for {
		select {
		case msg := <-tr.sent:
			env := readEnvelope(t, key, auth.state.HostID, uint64(messages+1), msg)
			if env.Type != "session.events" {
				t.Fatalf("want session.events, got %s", env.Type)
			}
			messages++
			events += len(env.Events)
		default:
			if messages == 0 {
				t.Fatal("no batched message")
			}
			if messages >= 10 || events != 10 {
				t.Fatalf("messages=%d events=%d", messages, events)
			}
			return
		}
	}
}

func TestStreamEventsFlushPromptly(t *testing.T) {
	key, _ := NewContentKey()
	auth := NewMemoryAuthority(t, key)
	h := &Host{Authority: auth, Logf: func(string, ...any) {}}
	tr := newFakeTransport()
	ts := TopicSet{UID: "u1", HID: "h1", DID: "d1"}
	live := make(chan SessionEvent, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go h.streamEvents(ctx, tr, ts, "d1", "s1", live, func() {})
	// 让 ticker 先走一段，避免把「启动时刻」算成延迟。
	time.Sleep(20 * time.Millisecond)

	start := time.Now()
	live <- SessionEvent{Seq: 1, Kind: "text_delta", Text: "x"}
	select {
	case <-tr.sent:
		if elapsed := time.Since(start); elapsed > 75*time.Millisecond {
			t.Fatalf("first flush took %v, want <= 75ms", elapsed)
		}
	case <-time.After(75 * time.Millisecond):
		t.Fatal("first event was not flushed within 75ms")
	}
}

func readEnvelope(t *testing.T, key ContentKey, from string, seq uint64, msg struct {
	topic   string
	payload []byte
}) Envelope {
	t.Helper()
	var f Frame
	if err := json.Unmarshal(msg.payload, &f); err != nil {
		t.Fatal(err)
	}
	plain, err := OpenFrame(key, HostToClient, from, seq, f)
	if err != nil {
		t.Fatal(err)
	}
	var env Envelope
	if err := json.Unmarshal(plain, &env); err != nil {
		t.Fatal(err)
	}
	return env
}

func nextOn(t *testing.T, tr *fakeTransport, want string) struct {
	topic   string
	payload []byte
} {
	t.Helper()
	for {
		select {
		case msg := <-tr.sent:
			if msg.topic == want {
				return msg
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no message on %s", want)
		}
	}
}

func NewMemoryAuthority(t *testing.T, key ContentKey) *Authority {
	t.Helper()
	a, err := NewAuthority(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	a.state.ContentKey = key.Base64()
	return a
}

func testDevicePub(t *testing.T) string {
	t.Helper()
	id, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return id.PublicBase64()
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
