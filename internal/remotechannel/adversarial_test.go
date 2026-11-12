package remotechannel

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// tapConn 代表不可信中继：它能看见、篡改或重放两层 TLS 之间的线缆字节，
// 但永远拿不到任何一端的私钥。
type tapConn struct {
	net.Conn
	mu      sync.Mutex
	capture bool
	corrupt bool
	wire    []byte
}

func (c *tapConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	data := append([]byte(nil), p...)
	if c.capture {
		c.wire = append(c.wire, data...)
	}
	if c.corrupt && len(data) > 5 {
		data[len(data)-1] ^= 1
	}
	c.mu.Unlock()
	return c.Conn.Write(data)
}

func secureTestPair(t *testing.T) (*tls.Conn, *tls.Conn, *tapConn) {
	t.Helper()
	host, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	tap := &tapConn{Conn: left}
	cfg, err := client.ClientConfig(host.DER)
	if err != nil {
		t.Fatal(err)
	}
	a := tls.Client(tap, cfg)
	b := tls.Server(right, host.ServerConfig(func(fp string) bool { return fp == client.Fingerprint }))
	t.Cleanup(func() { left.Close(); right.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.HandshakeContext(ctx) }()
	if err = a.HandshakeContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	cs := a.ConnectionState()
	if cs.Version != tls.VersionTLS13 || cs.DidResume {
		t.Fatal("必须是全新的 TLS 1.3 连接")
	}
	return a, b, tap
}

func TestTLSWireTamperRejected(t *testing.T) {
	a, b, tap := secureTestPair(t)
	tap.mu.Lock()
	tap.corrupt = true
	tap.mu.Unlock()
	a.SetDeadline(time.Now().Add(2 * time.Second))
	b.SetDeadline(time.Now().Add(2 * time.Second))
	done := make(chan error, 1)
	go func() {
		_, err := a.Write([]byte("synthetic-secret"))
		done <- err
	}()
	n, err := b.Read(make([]byte, 64))
	if err == nil || n != 0 {
		t.Fatal("被修改的密文被接受")
	}
	<-done
}

func TestTLSRecordReplayRejected(t *testing.T) {
	a, b, tap := secureTestPair(t)
	tap.mu.Lock()
	tap.capture = true
	tap.mu.Unlock()
	secret := []byte("fixed-private-test-payload")
	done := make(chan error, 1)
	go func() {
		_, err := a.Write(secret)
		done <- err
	}()
	got := make([]byte, len(secret))
	if _, err := io.ReadFull(b, got); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	tap.mu.Lock()
	record := append([]byte(nil), tap.wire...)
	tap.capture = false
	tap.mu.Unlock()
	if bytes.Contains(record, secret) {
		t.Fatal("线缆上出现了明文")
	}

	// 同一会话重放旧记录必须失败。
	b.SetDeadline(time.Now().Add(2 * time.Second))
	go func() {
		_, err := tap.Conn.Write(record)
		done <- err
	}()
	if n, err := b.Read(make([]byte, 64)); err == nil || n != 0 {
		t.Fatal("旧 TLS 记录被接受")
	}
	<-done

	// 新会话有全新密钥，旧记录同样必须失败。
	_, fresh, newTap := secureTestPair(t)
	fresh.SetDeadline(time.Now().Add(2 * time.Second))
	go func() {
		_, err := newTap.Conn.Write(record)
		done <- err
	}()
	if n, err := fresh.Read(make([]byte, 64)); err == nil || n != 0 {
		t.Fatal("旧记录在新会话被接受")
	}
	<-done
}

func TestDisconnectedCandidateCannotBeApproved(t *testing.T) {
	a := NewAuthority()
	inv, err := a.Invite(Invitation{})
	if err != nil {
		t.Fatal(err)
	}
	raw, peer := net.Pipe()
	defer peer.Close()
	a.Track("candidate", raw)
	p, err := a.Begin("candidate", inv.ID, inv.Secret, raw)
	if err != nil {
		t.Fatal(err)
	}
	a.Untrack("candidate", raw)
	if err = a.Decide(p.ID, "candidate", true); err == nil {
		t.Fatal("已断开的候选被批准")
	}
	if <-p.Result {
		t.Fatal("不该产生授权")
	}
}
