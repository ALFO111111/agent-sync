// Package relay 转发不透明的二进制流。
//
// 路由凭据只决定"这条连接能不能被配对到某个执行端"，
// 永远不能代替执行端自己的授权判断：中转被攻破也不能让电脑放行设备。
package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ALFO111111/agent-sync/internal/i18n"
)

const (
	Version        = 1
	FrameLimit     = 16 * 1024
	MaxConnections = 128
	MaxRoutes      = 16
	MaxPending     = 32

	// 公网线路可能要经过环境代理，TLS 往返也可能不止一次。
	DialTimeout   = 60 * time.Second
	AttachTimeout = 90 * time.Second
	PairTimeout   = 120 * time.Second
	// 数据空闲期限必须覆盖两端两分钟的审批等待，否则还在等人批准就被中转断开。
	DataIdleTimeout = 240 * time.Second
)

// Control 是版本化的首包路由协议。
//
//	register: Route+Credential（执行端登记控制通道）
//	connect:  Route+Credential（控制端申请一条数据通道）
//	attach:   Route+Ticket（执行端接入这条数据通道）
//
// 回复：ready、incoming(Ticket)、paired、heartbeat、error。
type Control struct {
	Version    int    `json:"version"`
	Role       string `json:"role"`
	Route      string `json:"route,omitempty"`
	Credential string `json:"credential,omitempty"`
	Ticket     string `json:"ticket,omitempty"`
}

// Token 返回 32 字节随机数的十六进制串（64 个字符）。
func Token() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func valid(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

type route struct {
	credential string
	conn       *websocket.Conn
	notices    chan Control
	done       chan struct{}
}

type pending struct {
	route      *route
	controller *websocket.Conn
	attached   chan *websocket.Conn
	done       chan struct{}
}

// Server 是 http.Handler。Close 会主动关闭所有已升级的连接，
// 不能只依赖 http.Server.Shutdown（它不处理 hijacked 连接）。
type Server struct {
	mu           sync.Mutex
	closed       bool
	conns        map[*websocket.Conn]bool
	routes       map[string]*route
	pending      map[string]*pending
	slots        chan struct{}
	rateStart    time.Time
	rateCount    int
	done         chan struct{}
	totalConns   atomic.Int64
	activeConns  atomic.Int64
	bytesRelayed atomic.Int64
	wg           sync.WaitGroup
	origins      map[string]bool
}

func New() *Server {
	return &Server{
		conns:   map[*websocket.Conn]bool{},
		routes:  map[string]*route{},
		pending: map[string]*pending{},
		slots:   make(chan struct{}, MaxConnections),
		done:    make(chan struct{}),
		origins: map[string]bool{},
	}
}

// AllowOrigin 显式放行一个浏览器/WebView 来源（例如 App 的 https://localhost）。
// 不配置时与以前一样拒绝所有带 Origin 的请求；原生客户端不带 Origin。
func (s *Server) AllowOrigin(o string) {
	s.mu.Lock()
	s.origins[o] = true
	s.mu.Unlock()
}

func (s *Server) originAllowed(o string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.origins[o]
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Path == "/metrics" {
		if r.Method != http.MethodGet {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		s.mu.Lock()
		routes, pending := len(s.routes), len(s.pending)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# TYPE agentsync_relay_connections_total counter\nagentsync_relay_connections_total %d\n", s.totalConns.Load())
		fmt.Fprintf(w, "# TYPE agentsync_relay_connections_active gauge\nagentsync_relay_connections_active %d\n", s.activeConns.Load())
		fmt.Fprintf(w, "# TYPE agentsync_relay_bytes_relayed counter\nagentsync_relay_bytes_relayed %d\n", s.bytesRelayed.Load())
		fmt.Fprintf(w, "# TYPE agentsync_relay_routes gauge\nagentsync_relay_routes %d\n", routes)
		fmt.Fprintf(w, "# TYPE agentsync_relay_pending gauge\nagentsync_relay_pending %d\n", pending)
		return
	}
	if r.URL.Path == "/healthz" {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
		return
	}
	// 原生客户端只走 /ws：不接受查询参数（避免把凭据放 URL）和浏览器 Origin。
	origin := r.Header.Values("Origin")
	originOK := len(origin) == 0 || (len(origin) == 1 && s.originAllowed(origin[0]))
	if r.URL.Path != "/ws" || r.Method != http.MethodGet || r.URL.RawQuery != "" || r.URL.ForceQuery || !originOK {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	s.mu.Lock()
	if time.Since(s.rateStart) >= time.Second {
		s.rateStart = time.Now()
		s.rateCount = 0
	}
	s.rateCount++
	if s.closed || s.rateCount > 64 {
		s.mu.Unlock()
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()

	up := websocket.Upgrader{
		HandshakeTimeout:  60 * time.Second,
		EnableCompression: false,
		CheckOrigin: func(r *http.Request) bool {
			o := r.Header.Values("Origin")
			return len(o) == 0 || (len(o) == 1 && s.originAllowed(o[0]))
		},
	}
	c, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		c.Close()
		return
	}
	s.conns[c] = true
	s.totalConns.Add(1)
	s.activeConns.Add(1)
	s.mu.Unlock()
	defer func() {
		c.Close()
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		s.activeConns.Add(-1)
	}()

	c.SetReadLimit(2048)
	installControlLimits(c)
	c.SetReadDeadline(time.Now().Add(60 * time.Second))
	kind, b, err := c.ReadMessage()
	if err != nil || kind != websocket.TextMessage {
		return
	}
	var msg Control
	if json.Unmarshal(b, &msg) != nil || msg.Version != Version || !valid(msg.Route) {
		return
	}
	c.SetReadDeadline(time.Time{})
	switch msg.Role {
	case "register":
		s.register(c, msg)
	case "connect":
		s.connect(c, msg)
	case "attach":
		s.attach(c, msg)
	}
}

// installControlLimits 只允许读循环处理控制帧；回 pong 用 Gorilla 明确声明
// 并发安全的 WriteControl，不引入第二个数据写者。
func installControlLimits(c *websocket.Conn) {
	window := time.Now()
	count := 0
	limit := func(string) error {
		if time.Since(window) >= time.Second {
			window = time.Now()
			count = 0
		}
		count++
		if count > 32 {
			return ErrProtocol
		}
		return nil
	}
	c.SetPingHandler(func(payload string) error {
		if err := limit(payload); err != nil {
			return err
		}
		return c.WriteControl(websocket.PongMessage, []byte(payload), time.Now().Add(time.Second))
	})
	c.SetPongHandler(limit)
}

func write(c *websocket.Conn, m Control) error {
	c.SetWriteDeadline(time.Now().Add(60 * time.Second))
	return c.WriteJSON(m)
}

func (s *Server) register(c *websocket.Conn, m Control) {
	if !valid(m.Credential) {
		return
	}
	rt := &route{credential: m.Credential, conn: c, notices: make(chan Control, MaxPending), done: make(chan struct{})}
	s.mu.Lock()
	if s.closed || len(s.routes) >= MaxRoutes || s.routes[m.Route] != nil {
		s.mu.Unlock()
		return
	}
	s.routes[m.Route] = rt
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.routes[m.Route] == rt {
			delete(s.routes, m.Route)
		}
		close(rt.done)
		for key, p := range s.pending {
			if p.route == rt {
				p.controller.Close()
				delete(s.pending, key)
			}
		}
		s.mu.Unlock()
	}()
	if write(c, Control{Version: Version, Role: "ready"}) != nil {
		return
	}
	// 唯一读者同时负责发现断开；登记后客户端只能发心跳。
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		defer c.Close()
		for {
			c.SetReadDeadline(time.Now().Add(120 * time.Second))
			var ack Control
			if c.ReadJSON(&ack) != nil || ack.Version != Version || ack.Role != "heartbeat" {
				return
			}
		}
	}()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if write(c, Control{Version: Version, Role: "heartbeat"}) != nil {
				return
			}
		case <-s.done:
			return
		case <-ended:
			return
		case n := <-rt.notices:
			if write(c, n) != nil {
				return
			}
		}
	}
}

func (s *Server) connect(c *websocket.Conn, m Control) {
	s.mu.Lock()
	rt := s.routes[m.Route]
	if s.closed || rt == nil || m.Credential != rt.credential || len(s.pending) >= MaxPending {
		s.mu.Unlock()
		return
	}
	ticket := Token()
	p := &pending{route: rt, controller: c, attached: make(chan *websocket.Conn, 1), done: make(chan struct{})}
	s.pending[ticket] = p
	select {
	case rt.notices <- Control{Version: Version, Role: "incoming", Route: m.Route, Ticket: ticket}:
	default:
		delete(s.pending, ticket)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		close(p.done)
		delete(s.pending, ticket)
		s.mu.Unlock()
	}()
	timer := time.NewTimer(AttachTimeout)
	defer timer.Stop()
	select {
	case peer := <-p.attached:
		defer peer.Close()
		if write(c, Control{Version: Version, Role: "paired"}) != nil || write(peer, Control{Version: Version, Role: "paired"}) != nil {
			return
		}
		c.SetReadLimit(FrameLimit)
		peer.SetReadLimit(FrameLimit)
		done := make(chan struct{})
		go func() {
			defer close(done)
			s.forward(peer, c)
			c.Close()
			peer.Close()
		}()
		s.forward(c, peer)
		c.Close()
		peer.Close()
		<-done
	case <-rt.done:
	case <-s.done:
	case <-timer.C:
	}
}

func (s *Server) attach(c *websocket.Conn, m Control) {
	if !valid(m.Ticket) {
		return
	}
	s.mu.Lock()
	p := s.pending[m.Ticket]
	if p == nil || s.routes[m.Route] != p.route {
		s.mu.Unlock()
		return
	}
	delete(s.pending, m.Ticket)
	s.mu.Unlock()
	// connect 拥有数据读写；这里只把 socket 交过去，等它整个生命周期结束。
	select {
	case p.attached <- c:
		<-p.done
		c.Close()
	case <-p.done:
		c.Close()
	}
}

func (s *Server) forward(dst, src *websocket.Conn) {
	window := time.Now()
	count := 0
	for {
		src.SetReadDeadline(time.Now().Add(DataIdleTimeout))
		kind, b, err := src.ReadMessage()
		if err != nil || kind != websocket.BinaryMessage {
			return
		}
		if time.Since(window) >= time.Second {
			window = time.Now()
			count = 0
		}
		count++
		if count > 256 {
			return
		}
		dst.SetWriteDeadline(time.Now().Add(60 * time.Second))
		if dst.WriteMessage(kind, b) != nil {
			return
		}
		s.bytesRelayed.Add(int64(len(b)))
	}
}

// Close 主动关闭所有已升级连接，再等待处理函数退出。
func (s *Server) Close() error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.done)
	}
	conns := make([]*websocket.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
	s.wg.Wait()
	return nil
}

// ErrProtocol 中转拒绝了对端发来的协议数据。
var ErrProtocol = i18n.Err("中转协议被拒绝")

// Dial 发送路由凭据时只放在 WebSocket 首包，不放 URL。
// 走标准环境代理（HTTPS_PROXY 等），因为公网线路可能需要它。
func Dial(url string, m Control) (*websocket.Conn, error) {
	return DialContext(context.Background(), url, m)
}

func DialContext(ctx context.Context, url string, m Control) (*websocket.Conn, error) {
	d := websocket.Dialer{
		Proxy:             http.ProxyFromEnvironment,
		HandshakeTimeout:  DialTimeout,
		EnableCompression: false,
	}
	c, _, err := d.DialContext(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	m.Version = Version
	if err = write(c, m); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// Heartbeat 让执行端控制通道向中转报活。
func Heartbeat(c *websocket.Conn) error {
	return write(c, Control{Version: Version, Role: "heartbeat"})
}

// Await 等待一条指定角色的控制回复。超时要覆盖公网建连带来的排队。
func Await(c *websocket.Conn, role string) error {
	c.SetReadLimit(2048)
	installControlLimits(c)
	c.SetReadDeadline(time.Now().Add(PairTimeout))
	var m Control
	err := c.ReadJSON(&m)
	c.SetReadDeadline(time.Time{})
	if err != nil {
		return err
	}
	if m.Version != Version || m.Role != role {
		return ErrProtocol
	}
	return nil
}
