package relay

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func fixture(t *testing.T) (*Server, string) {
	t.Helper()
	r := New()
	s := httptest.NewServer(r)
	address := s.Listener.Addr().String()
	t.Cleanup(func() {
		r.Close()
		s.Close()
		l, err := net.Listen("tcp", address)
		if err != nil {
			t.Errorf("端口没有释放: %v", err)
		} else {
			l.Close()
		}
	})
	return r, "ws" + strings.TrimPrefix(s.URL, "http") + "/ws"
}

func register(t *testing.T, url string) (*websocket.Conn, Control) {
	t.Helper()
	m := Control{Role: "register", Route: Token(), Credential: Token()}
	c, err := Dial(url, m)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err = Await(c, "ready"); err != nil {
		t.Fatal(err)
	}
	return c, m
}

func TestHealthAndRejectedRequests(t *testing.T) {
	_, url := fixture(t)
	for _, h := range []http.Header{
		{"Origin": []string{"https://evil.example"}},
		{"Origin": []string{"", "https://evil.example"}},
		{"Origin": []string{""}},
	} {
		c, _, err := websocket.DefaultDialer.Dial(url, h)
		if err == nil {
			c.Close()
			t.Fatal("浏览器 Origin 被接受")
		}
	}
	if c, _, err := websocket.DefaultDialer.Dial(url+"?credential=x", nil); err == nil {
		c.Close()
		t.Fatal("URL 里的凭据被接受")
	}
	c, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	defer c.Close()
	c.WriteJSON(Control{Version: 99, Role: "register", Route: Token(), Credential: Token()})
	c.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err = c.ReadMessage(); err == nil {
		t.Fatal("错误版本被接受")
	}
}

func TestTicketPairAndDisconnect(t *testing.T) {
	_, url := fixture(t)
	host, m := register(t, url)
	controller, err := Dial(url, Control{Role: "connect", Route: m.Route, Credential: m.Credential})
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	host.SetReadDeadline(time.Now().Add(2 * time.Second))
	var notice Control
	if err = host.ReadJSON(&notice); err != nil {
		t.Fatal(err)
	}
	peer, err := Dial(url, Control{Role: "attach", Route: m.Route, Ticket: notice.Ticket})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if err = Await(controller, "paired"); err != nil {
		t.Fatal(err)
	}
	if err = Await(peer, "paired"); err != nil {
		t.Fatal(err)
	}
	duplicate, err := Dial(url, Control{Role: "attach", Route: m.Route, Ticket: notice.Ticket})
	if err != nil {
		t.Fatal(err)
	}
	defer duplicate.Close()
	duplicate.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err = duplicate.ReadMessage(); err == nil {
		t.Fatal("一次性 ticket 被重复使用")
	}
	controller.WriteMessage(websocket.BinaryMessage, []byte("opaque"))
	peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, b, err := peer.ReadMessage()
	if err != nil || string(b) != "opaque" {
		t.Fatalf("转发失败 %q %v", b, err)
	}
	controller.Close()
	peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err = peer.ReadMessage(); err == nil {
		t.Fatal("一端的断开没有关闭另一端")
	}
}

func TestShutdownClosesWaitingController(t *testing.T) {
	r, url := fixture(t)
	host, m := register(t, url)
	_ = host
	controller, err := Dial(url, Control{Role: "connect", Route: m.Route, Credential: m.Credential})
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	// 执行端不 attach：等待期间关闭服务，控制器必须被唤醒而不是卡满 AttachTimeout。
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	r.Close()
	controller.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		if _, _, err = controller.ReadMessage(); err != nil {
			break
		}
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("关闭服务没有及时唤醒等待连接")
	}
}

func TestRoutesBound(t *testing.T) {
	_, url := fixture(t)
	held := make([]*websocket.Conn, 0, MaxRoutes)
	for i := 0; i < MaxRoutes; i++ {
		c, err := Dial(url, Control{Role: "register", Route: Token(), Credential: Token()})
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
		if err = Await(c, "ready"); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		for _, c := range held {
			c.Close()
		}
	}()
	extra, err := Dial(url, Control{Role: "register", Route: Token(), Credential: Token()})
	if err != nil {
		t.Fatal(err)
	}
	defer extra.Close()
	extra.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err = extra.ReadMessage(); err == nil {
		t.Fatal("路由数量没有上限")
	}
}

func TestConnectionBudgets(t *testing.T) {
	if AttachTimeout <= DialTimeout || PairTimeout <= AttachTimeout {
		t.Fatal("配对和接入预算必须覆盖出站建连时间")
	}
	if DataIdleTimeout < 3*time.Minute {
		t.Fatal("数据空闲期限必须覆盖两分钟审批等待")
	}
}

// ProxyFromEnvironment 会缓存环境，所以要在新进程里测。
func TestDialUsesEnvironmentProxy(t *testing.T) {
	if os.Getenv("RELAY_PROXY_TEST_CHILD") == "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		c, err := DialContext(ctx, "wss://relay-proxy-test.invalid/ws", Control{Role: "connect"})
		if c != nil {
			c.Close()
		}
		if err == nil {
			t.Fatal("测试代理本应拒绝 CONNECT")
		}
		return
	}
	var connects atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect && r.Host == "relay-proxy-test.invalid:443" {
			connects.Add(1)
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDialUsesEnvironmentProxy$", "-test.count=1")
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "NO_PROXY", "REQUEST_METHOD", "RELAY_PROXY_TEST_CHILD":
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "RELAY_PROXY_TEST_CHILD=1", "HTTPS_PROXY="+proxy.URL)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("子进程失败: %v\n%s", err, output)
	}
	if connects.Load() != 1 {
		t.Fatalf("期望经环境代理 CONNECT，实际 %d", connects.Load())
	}
}

func TestMetricsEndpoint(t *testing.T) {
	r := New()
	srv := httptest.NewServer(r)
	defer func() { r.Close(); srv.Close() }()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	c, m := register(t, url)
	_ = m
	defer c.Close()
	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{
		"agentsync_relay_connections_total 1",
		"agentsync_relay_routes 1",
	} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("metrics missing %q in:\n%s", want, body)
		}
	}
}

func TestAllowConfiguredOrigin(t *testing.T) {
	r := New()
	srv := httptest.NewServer(r)
	defer func() { r.Close(); srv.Close() }()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	h := http.Header{"Origin": []string{"https://localhost"}}
	if c, _, err := websocket.DefaultDialer.Dial(url, h); err == nil {
		c.Close()
		t.Fatal("origin accepted without configuration")
	}
	r.AllowOrigin("https://localhost")
	c, _, err := websocket.DefaultDialer.Dial(url, h)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}
