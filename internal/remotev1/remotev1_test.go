package remotev1

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ALFO111111/agent-sync/internal/relay"
)

type memStore struct {
	mu sync.Mutex
	s  *State
}

func (m *memStore) Load() (*State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.s == nil {
		return nil, nil
	}
	cp := *m.s
	cp.Devices = append([]Device(nil), m.s.Devices...)
	return &cp, nil
}

func (m *memStore) Save(s *State) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *s
	cp.Devices = append([]Device(nil), s.Devices...)
	m.s = &cp
	return nil
}

type fixture struct {
	host  *Host
	auth  *Authority
	store *memStore
	inv   Invitation
	url   string
	ctx   context.Context
}

func setup(t *testing.T) *fixture {
	t.Helper()
	r := relay.New()
	srv := httptest.NewServer(r)
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	store := &memStore{}
	auth, err := NewAuthority(store, url)
	if err != nil {
		t.Fatal(err)
	}
	hostID, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	host := NewHost(hostID, auth, url, "Test Mac", nil)
	inv, err := auth.Invite("Test Mac")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		host.Serve(ctx, func() { close(ready) })
	}()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("host not ready")
	}
	t.Cleanup(func() {
		cancel()
		r.Close()
		srv.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("host leaked")
		}
	})
	return &fixture{host: host, auth: auth, store: store, inv: inv, url: url, ctx: ctx}
}

// approveNext 等出现待批准请求后批准。
func approveNext(t *testing.T, f *fixture) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		list := f.auth.PendingList()
		if len(list) > 0 {
			if err := f.auth.Decide(list[0].ID, true); err != nil {
				t.Fatal(err)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no pending request")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPairEchoReconnectRevoke(t *testing.T) {
	f := setup(t)
	deviceID, _ := NewIdentity()
	c := NewClient(deviceID, "Pixel", f.inv)
	done := make(chan error, 1)
	go func() { done <- c.Pair(context.Background()) }()
	approveNext(t, f)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if out, err := c.Echo(context.Background(), "hello"); err != nil || out != "hello" {
		t.Fatalf("echo %q %v", out, err)
	}
	devs, err := c.Devices(context.Background())
	if err != nil || len(devs) != 1 || devs[0].Name != "Pixel" {
		t.Fatalf("devices %+v %v", devs, err)
	}
	c.Close()

	// 重启主机：设备白名单仍在，免配对直连。
	f2 := restart(t, f)
	c2 := NewClient(deviceID, "Pixel", f2.inv)
	if err := c2.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if out, err := c2.Echo(context.Background(), "again"); err != nil || out != "again" {
		t.Fatal("echo after restart", err)
	}

	// 撤销后立即断开且不能再连。
	if _, err := f2.host.Revoke(devs[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c2.Echo(context.Background(), "x"); err == nil {
		t.Fatal("revoked echo accepted")
	}
	c3 := NewClient(deviceID, "Pixel", f2.inv)
	if err := c3.Connect(context.Background()); err == nil {
		t.Fatal("revoked reconnect accepted")
	}
}

// restart 用同一个 store 重建 host 与 relay。
func restart(t *testing.T, old *fixture) *fixture {
	t.Helper()
	r := relay.New()
	srv := httptest.NewServer(r)
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	auth, err := NewAuthority(old.store, url)
	if err != nil {
		t.Fatal(err)
	}
	hostID, _ := NewIdentity()
	host := NewHost(hostID, auth, url, "Test Mac", nil)
	inv, err := auth.Invite("Test Mac")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() { defer close(done); host.Serve(ctx, func() { close(ready) }) }()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("host not ready")
	}
	t.Cleanup(func() {
		cancel()
		r.Close()
		srv.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("host leaked")
		}
	})
	return &fixture{host: host, auth: auth, store: old.store, inv: inv, url: url, ctx: ctx}
}

func TestWrongProofRejected(t *testing.T) {
	a, _ := NewIdentity()
	b, _ := NewIdentity()
	sig := b.SignProof("nonce", "")
	// 用自己的公钥声称，但签名来自另一把钥匙：必须失败。
	if VerifyProof(a.Public, "nonce", a.PublicBase64(), "", sig) {
		t.Fatal("signature for different key accepted")
	}
	if !VerifyProof(b.Public, "nonce", b.PublicBase64(), "", sig) {
		t.Fatal("valid proof rejected")
	}
	// 换了邀请 ID，签名作用域不同，必须失败。
	if VerifyProof(b.Public, "nonce", b.PublicBase64(), "other", sig) {
		t.Fatal("proof bound to wrong invitation accepted")
	}
	// 换了 nonce 也必须失败，防止重放。
	if VerifyProof(b.Public, "nonce2", b.PublicBase64(), "", sig) {
		t.Fatal("proof replayed with new nonce")
	}
}

func mustPub(t *testing.T) string {
	t.Helper()
	id, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return id.PublicBase64()
}

func TestAuthorityInvitationRules(t *testing.T) {
	a, err := NewAuthority(nil, "wss://relay.example.com/ws")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := a.Invite("Mac")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.BeginPair(inv.ID, "wrong", mustPub(t), "x"); err == nil {
		t.Fatal("wrong secret accepted")
	}
	pub := mustPub(t)
	p, err := a.BeginPair(inv.ID, inv.Secret, pub, "Phone")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.BeginPair(inv.ID, inv.Secret, pub, "Phone"); err == nil {
		t.Fatal("duplicate pending accepted")
	}
	if err := a.Decide(p.ID, false); err != nil {
		t.Fatal(err)
	}
	if <-p.Result {
		t.Fatal("denied but approved")
	}
	// 拒绝不消费邀请，可重试。
	p2, err := a.BeginPair(inv.ID, inv.Secret, pub, "Phone")
	if err != nil {
		t.Fatal("retry after deny failed")
	}
	if err := a.Decide(p2.ID, true); err != nil {
		t.Fatal(err)
	}
	if !<-p2.Result {
		t.Fatal("approve not signalled")
	}
	if _, err := a.BeginPair(inv.ID, inv.Secret, mustPub(t), "Other"); err == nil {
		t.Fatal("invitation reused")
	}
	if d, ok := a.Device(pub); !ok || d.Name != "Phone" {
		t.Fatal("device not stored")
	}
	if _, err := a.Revoke(dIDFor(t, a, pub)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.BeginPair(inv.ID, inv.Secret, pub, "Phone"); err == nil {
		t.Fatal("revoked device re-paired")
	}
}

func dIDFor(t *testing.T, a *Authority, pub string) string {
	t.Helper()
	d, ok := a.Device(pub)
	if !ok {
		t.Fatal("device missing")
	}
	return d.ID
}

func TestInvitationEncoding(t *testing.T) {
	inv := Invitation{
		Version: 1, URL: "wss://relay.example.com/ws",
		Route: RandomToken(32), Credential: RandomToken(32),
		ID: RandomToken(32), Secret: RandomToken(32),
		HostID: "h", HostName: "Mac", Expires: time.Now().Add(time.Minute),
	}
	enc := EncodeInvitation(inv)
	got, err := DecodeInvitation(enc)
	if err != nil || got.ID != inv.ID {
		t.Fatal(err)
	}
	bad := inv
	bad.URL = "http://relay.example.com/ws"
	if _, err := DecodeInvitation(EncodeInvitation(bad)); err == nil {
		t.Fatal("non-wss invitation accepted")
	}
}
