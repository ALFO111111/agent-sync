package remotechannel

import (
	"context"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ALFO111111/agent-sync/internal/relay"
)

// newPipePair 提供一个可当 net.Conn 用的连接对，用于纯状态机测试。
func newPipePair() (net.Conn, net.Conn) { return net.Pipe() }

func setup(t *testing.T) (*Host, Invitation, context.Context) {
	t.Helper()
	r := relay.New()
	s := httptest.NewServer(r)
	ctx, cancel := context.WithCancel(context.Background())
	h, err := NewHost("ws" + strings.TrimPrefix(s.URL, "http") + "/ws")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := h.Invite()
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Serve(ctx, func() { close(ready) })
	}()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("主机没有就绪")
	}
	t.Cleanup(func() {
		cancel()
		r.Close()
		s.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("主机协程泄漏")
		}
	})
	return h, inv, ctx
}

func pair(t *testing.T, h *Host, inv Invitation, ctx context.Context) *Client {
	t.Helper()
	c, err := NewClient(inv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	done := make(chan error, 1)
	go func() { done <- c.Connect(ctx, true) }()
	deadline := time.Now().Add(5 * time.Second)
	for len(h.Authority.Pending()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("没有出现待批准请求")
		}
		time.Sleep(time.Millisecond)
	}
	p := h.Authority.Pending()[0]
	if err := h.Authority.Decide(p.ID, p.Fingerprint, true); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("配对卡住")
	}
	return c
}

func TestPairApproveIncrementReconnectRevoke(t *testing.T) {
	h, inv, ctx := setup(t)
	c := pair(t, h, inv, ctx)

	id := relay.Token()
	if n, err := c.Increment(id); err != nil || n != 1 {
		t.Fatalf("第一次计数 %d %v", n, err)
	}
	if n, err := c.Increment(id); err != nil || n != 1 {
		t.Fatalf("重复请求应返回原值 %d %v", n, err)
	}
	if err := c.Status(); err != nil {
		t.Fatal(err)
	}
	c.Close()
	if err := c.Connect(ctx, false); err != nil {
		t.Fatal(err)
	}
	if n, err := c.Increment(id); err != nil || n != 1 {
		t.Fatalf("重连后重复请求应返回原值 %d %v", n, err)
	}
	if n, err := c.Increment(relay.Token()); err != nil || n != 2 {
		t.Fatalf("重连后新计数 %d %v", n, err)
	}

	if err := h.Authority.Revoke(c.Identity.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if err := c.Status(); err == nil {
		t.Fatal("撤销后活动连接仍可用")
	}
	if err := c.Connect(ctx, false); err == nil {
		t.Fatal("撤销后仍能重连")
	}
	fresh, err := h.Invite()
	if err != nil {
		t.Fatal(err)
	}
	c.Invitation = fresh
	if err := c.Connect(ctx, true); err == nil {
		t.Fatal("撤销的旧身份又配上了")
	}
	// 新身份不受影响。
	pair(t, h, fresh, ctx)
}

func TestWrongCertificateAndUnapproved(t *testing.T) {
	h, inv, ctx := setup(t)
	wrong, _ := NewIdentity()
	bad := inv
	bad.Certificate = wrong.DER
	c, err := NewClient(bad)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = c.Connect(ctx, true); err == nil {
		t.Fatal("错误的执行端证书被信任")
	}

	c2, err := NewClient(inv)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	// 没有授权、没有待批准请求时直接 resume 必须失败。
	if err = c2.Connect(ctx, false); err == nil {
		t.Fatal("未批准的连接被接受")
	}
	if len(h.Authority.Pending()) != 0 {
		t.Fatal("未证明身份的连接污染了待批准状态")
	}
}

func TestInvitationAndDedupBounds(t *testing.T) {
	a := NewAuthority()
	inv, err := a.Invite(Invitation{})
	if err != nil {
		t.Fatal(err)
	}
	fp := "fingerprint"
	if _, err = a.Begin(fp, inv.ID, "wrong", nil); err == nil {
		t.Fatal("错误 secret 被接受")
	}
	left, _ := newPipePair()
	a.Track(fp, left)
	p, err := a.Begin(fp, inv.ID, inv.Secret, left)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil {
		t.Fatal("没有生成待批准")
	}
	// 错误绑定不能批准。
	if err = a.Decide(p.ID, "other", true); err == nil {
		t.Fatal("错误指纹批准成功")
	}
	if err = a.Decide(p.ID, fp, true); err != nil {
		t.Fatal(err)
	}
	if err = a.Decide(p.ID, fp, true); err == nil {
		t.Fatal("批准可以重放")
	}
	gen, _, err := a.Grant(fp)
	if err != nil {
		t.Fatal(err)
	}
	id := relay.Token()
	if _, err = a.Increment(fp, gen, id, "increment-one"); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Increment(fp, gen, id, "different"); err == nil {
		t.Fatal("同一 ID 改了载荷仍被接受")
	}
	for i := 1; i < MaxRequests; i++ {
		if _, err = a.Increment(fp, gen, relay.Token(), "increment-one"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = a.Increment(fp, gen, relay.Token(), "increment-one"); err == nil {
		t.Fatal("去重记录没有上限")
	}
	a.grants[fp].expires = time.Now().Add(-time.Second)
	if _, err = a.Increment(fp, gen, id, "increment-one"); err == nil {
		t.Fatal("过期窗口仍被执行")
	}
}

func TestDeniedOrCancelledDoesNotConsume(t *testing.T) {
	a := NewAuthority()
	left, _ := newPipePair()
	a.Track("x", left)
	for _, mode := range []string{"deny", "cancel", "timeout"} {
		inv, err := a.Invite(Invitation{})
		if err != nil {
			t.Fatal(err)
		}
		p, err := a.Begin("x", inv.ID, inv.Secret, left)
		if err != nil {
			t.Fatal(err)
		}
		switch mode {
		case "deny":
			_ = a.Decide(p.ID, "x", false)
		case "cancel":
			a.Cancel(p.ID)
		case "timeout":
			p.Expires = time.Now().Add(-time.Second)
			_ = a.Decide(p.ID, "x", true)
		}
		if <-p.Result {
			t.Fatal("不该批准")
		}
		if _, _, err = a.Grant("x"); err == nil {
			t.Fatal("不该产生授权")
		}
		// 拒绝/取消/超时不消费邀请，允许重试。
		p, err = a.Begin("x", inv.ID, inv.Secret, left)
		if err != nil {
			t.Fatalf("%s 后邀请被错误消费: %v", mode, err)
		}
		a.Cancel(p.ID)
		<-p.Result
	}
}

func TestConcurrentApprovalHasSingleWinner(t *testing.T) {
	a := NewAuthority()
	inv, err := a.Invite(Invitation{})
	if err != nil {
		t.Fatal(err)
	}
	first, _ := newPipePair()
	second, _ := newPipePair()
	a.Track("first", first)
	a.Track("second", second)
	p, err := a.Begin("first", inv.ID, inv.Secret, first)
	if err != nil {
		t.Fatal(err)
	}
	q, err := a.Begin("second", inv.ID, inv.Secret, second)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{}, 2)
	go func() { _ = a.Decide(p.ID, "first", true); done <- struct{}{} }()
	go func() { _ = a.Decide(q.ID, "second", true); done <- struct{}{} }()
	<-done
	<-done
	wins := 0
	for _, fp := range []string{"first", "second"} {
		if _, _, err := a.Grant(fp); err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("同一邀请批准了 %d 个身份", wins)
	}
}
