package remotechannel

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func wsPair(t *testing.T) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err == nil {
			accepted <- c
		}
	}))
	t.Cleanup(s.Close)
	a, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(s.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	b := <-accepted
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}

func TestStreamAcrossFrames(t *testing.T) {
	a, b := wsPair(t)
	x, y := NewConn(a), NewConn(b)
	defer x.Close()
	defer y.Close()
	data := bytes.Repeat([]byte("data"), FrameLimit)
	done := make(chan error, 1)
	go func() {
		_, err := x.Write(data)
		done <- err
	}()
	got := make([]byte, len(data))
	for i := range got {
		if _, err := io.ReadFull(y, got[i:i+1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, got) {
		t.Fatal("字节流不一致")
	}
}

func TestDeadlineAndCloseWakesBlockedRead(t *testing.T) {
	a, b := wsPair(t)
	x := NewConn(a)
	defer x.Close()
	x.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
	if _, err := x.Read(make([]byte, 1)); err == nil {
		t.Fatal("缺少读超时")
	} else if n, ok := err.(net.Error); !ok || !n.Timeout() {
		t.Fatal(err)
	}
	x.SetReadDeadline(time.Time{})
	go b.WriteMessage(websocket.BinaryMessage, []byte("ok"))
	if _, err := io.ReadFull(x, make([]byte, 2)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := x.Read(make([]byte, 1))
		done <- err
	}()
	x.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("关闭没有唤醒阻塞的读")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("关闭后读仍然卡住")
	}
}

func TestRejectInvalidFrames(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind int
		n    int
	}{
		{"text", websocket.TextMessage, 1},
		{"oversize", websocket.BinaryMessage, FrameLimit + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := wsPair(t)
			x := NewConn(a)
			defer x.Close()
			x.SetReadDeadline(time.Now().Add(2 * time.Second))
			go b.WriteMessage(tc.kind, make([]byte, tc.n))
			got, _ := io.ReadAll(x)
			if len(got) >= tc.n {
				t.Fatal("非法帧被接受")
			}
		})
	}
}

func TestWriteDeadlineAndClose(t *testing.T) {
	a, _ := wsPair(t)
	x := NewConn(a)
	defer x.Close()
	x.SetWriteDeadline(time.Now().Add(-time.Second))
	if _, err := x.Write([]byte("x")); err == nil {
		t.Fatal("缺少写超时")
	}
	x.SetWriteDeadline(time.Time{})
	done := make(chan error, 1)
	go func() {
		_, err := x.Write(make([]byte, 32*1024*1024))
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	x.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("写阻塞没有被关闭打断")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("关闭后写仍然卡住")
	}
}
