package mqttlink

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type fakeToken struct{}

func (fakeToken) Wait() bool                     { return true }
func (fakeToken) WaitTimeout(time.Duration) bool { return true }
func (fakeToken) Error() error                   { return nil }
func (fakeToken) Done() <-chan struct{}          { ch := make(chan struct{}); close(ch); return ch }

type fakeClient struct {
	mqtt.Client
	mu   sync.Mutex
	subs []string
}

func (f *fakeClient) Connect() mqtt.Token { return fakeToken{} }
func (f *fakeClient) Subscribe(topic string, _ byte, _ mqtt.MessageHandler) mqtt.Token {
	f.mu.Lock()
	f.subs = append(f.subs, topic)
	f.mu.Unlock()
	return fakeToken{}
}
func (f *fakeClient) Publish(string, byte, bool, interface{}) mqtt.Token { return fakeToken{} }
func (f *fakeClient) Disconnect(uint)                                    {}

type rotatingToken struct{ n int }

func (r *rotatingToken) Credentials(context.Context) (string, string, error) {
	r.n++
	return fmt.Sprintf("user-%d", r.n), fmt.Sprintf("pass-%d", r.n), nil
}

// 用假客户端拿住 paho 的 ClientOptions，模拟一次「断线后重连」的回调。
func newLinkWithFake(t *testing.T) (*Link, *mqtt.ClientOptions, *fakeClient, *rotatingToken) {
	t.Helper()
	old := newClient
	t.Cleanup(func() { newClient = old })

	fc := &fakeClient{}
	var opts *mqtt.ClientOptions
	newClient = func(o *mqtt.ClientOptions) mqtt.Client {
		opts = o
		return fc
	}
	tok := &rotatingToken{}
	l, err := New(Config{URL: "tcp://127.0.0.1:1", ClientID: "c1", Token: tok})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l, opts, fc, tok
}

func TestLinkResubscribesAfterReconnect(t *testing.T) {
	l, opts, fc, _ := newLinkWithFake(t)
	if err := l.Subscribe(context.Background(), []string{"u/1/h/2/req/+"}, func(string, []byte) {}); err != nil {
		t.Fatal(err)
	}
	if len(fc.subs) != 1 {
		t.Fatalf("subs = %v, want 1 initial subscribe", fc.subs)
	}

	if opts.OnConnect == nil {
		t.Fatal("OnConnect handler not configured")
	}
	opts.OnConnect(fc) // paho 重连成功后回调

	if len(fc.subs) != 2 || fc.subs[1] != "u/1/h/2/req/+" {
		t.Fatalf("subs = %v, want resubscribe after reconnect", fc.subs)
	}
}

func TestLinkRefreshesCredentialsOnReconnect(t *testing.T) {
	_, opts, _, tok := newLinkWithFake(t)

	if !opts.AutoReconnect {
		t.Fatal("AutoReconnect is off: a dropped host connection would never come back")
	}
	if opts.MaxReconnectInterval <= 0 || opts.MaxReconnectInterval > 15*time.Second {
		t.Fatalf("MaxReconnectInterval = %v, want a fast retry (<=15s)", opts.MaxReconnectInterval)
	}
	if opts.CredentialsProvider == nil {
		t.Fatal("CredentialsProvider not configured: reconnect would reuse an expired token")
	}
	user, pass := opts.CredentialsProvider()
	if user != "user-2" || pass != "pass-2" {
		t.Fatalf("credentials = %q/%q, want a freshly fetched pair", user, pass)
	}
	if tok.n != 2 {
		t.Fatalf("token provider calls = %d, want 2", tok.n)
	}
}

func TestLinkCallsOnConnectCallback(t *testing.T) {
	old := newClient
	t.Cleanup(func() { newClient = old })
	fc := &fakeClient{}
	var opts *mqtt.ClientOptions
	newClient = func(o *mqtt.ClientOptions) mqtt.Client { opts = o; return fc }

	called := 0
	l, err := New(Config{
		URL: "tcp://127.0.0.1:1", ClientID: "c1", Token: StaticToken{},
		OnConnect: func() { called++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	if opts.OnConnect == nil {
		t.Fatal("OnConnect handler not configured")
	}
	opts.OnConnect(fc)
	if called != 1 {
		t.Fatalf("OnConnect called %d times, want 1", called)
	}
}
