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
