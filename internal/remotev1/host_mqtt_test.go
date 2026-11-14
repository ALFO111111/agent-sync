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
