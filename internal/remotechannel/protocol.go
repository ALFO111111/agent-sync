package remotechannel

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"sync"
	"time"

	"github.com/ALFO111111/agent-sync/internal/relay"
)

// Message 是内层 TLS 里的定长前缀 JSON。只接受 pair、resume、status、
// increment，没有任何任意文本、路径、文件或子进程。
type Message struct {
	Type       string    `json:"type"`
	ID         string    `json:"id,omitempty"`
	Secret     string    `json:"secret,omitempty"`
	Generation string    `json:"generation,omitempty"`
	Payload    string    `json:"payload,omitempty"`
	Value      uint64    `json:"value,omitempty"`
	Expires    time.Time `json:"expires,omitempty"`
}

const (
	// 内层 TLS 走公网 WSS 转发的字节流，慢线路下每次往返可能到数秒；
	// 这里给足预算，超时只影响初次握手，不影响已建立的会话。
	HandshakeTimeout = 120 * time.Second
	// 等待批准要覆盖邀请有效期，再加一次网络抖动的余量。
	ApprovalWaitTimeout = ApprovalTTL + 30*time.Second
)

// Send 写一条长度前缀消息（最多 4096 字节）。
func Send(c net.Conn, m Message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(b) > 4096 {
		return ErrDenied
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(b)))
	if _, err = c.Write(size[:]); err != nil {
		return err
	}
	_, err = c.Write(b)
	return err
}

// Receive 读一条长度前缀消息。
func Receive(c net.Conn) (Message, error) {
	var m Message
	var size [4]byte
	if _, err := io.ReadFull(c, size[:]); err != nil {
		return m, err
	}
	n := binary.BigEndian.Uint32(size[:])
	if n == 0 || n > 4096 {
		return m, ErrDenied
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(c, b); err != nil {
		return m, err
	}
	err := json.Unmarshal(b, &m)
	return m, err
}

// Host 是执行端：持有一次性身份和内存授权表。
type Host struct {
	Identity   *Identity
	Authority  *Authority
	URL        string
	Route      string
	Credential string

	mu     sync.Mutex
	active map[net.Conn]bool
}

func NewHost(url string) (*Host, error) {
	i, err := NewIdentity()
	if err != nil {
		return nil, err
	}
	return &Host{
		Identity:   i,
		Authority:  NewAuthority(),
		URL:        url,
		Route:      relay.Token(),
		Credential: relay.Token(),
		active:     map[net.Conn]bool{},
	}, nil
}

// Invite 生成一份新的配对邀请。
func (h *Host) Invite() (Invitation, error) {
	return h.Authority.Invite(Invitation{
		URL:         h.URL,
		Route:       h.Route,
		Credential:  h.Credential,
		Certificate: h.Identity.DER,
	})
}

// Serve 登记一条控制通道并处理中转送来的接入请求。
// 返回后本次 Serve 的所有数据连接都会关闭；调用方可以退避重连。
func (h *Host) Serve(ctx context.Context, ready func()) error {
	ctx, cancel := context.WithCancel(ctx)
	control, err := relay.DialContext(ctx, h.URL, relay.Control{
		Role: "register", Route: h.Route, Credential: h.Credential,
	})
	if err != nil {
		cancel()
		return err
	}
	defer control.Close()
	done := make(chan struct{})
	var wg sync.WaitGroup
	// 结束顺序：取消上下文 → 关闭本次的数据连接 → 等所有接入协程退出。
	defer wg.Wait()
	defer func() {
		h.mu.Lock()
		for c := range h.active {
			c.Close()
		}
		h.mu.Unlock()
	}()
	defer cancel()
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			control.Close()
		case <-done:
		}
	}()
	if err = relay.Await(control, "ready"); err != nil {
		return err
	}
	if ready != nil {
		ready()
	}
	slots := make(chan struct{}, 8)
	for {
		var m relay.Control
		control.SetReadDeadline(time.Now().Add(120 * time.Second))
		if err = control.ReadJSON(&m); err != nil {
			return err
		}
		if m.Version == 1 && m.Role == "heartbeat" {
			if relay.Heartbeat(control) != nil {
				return relay.ErrProtocol
			}
			continue
		}
		if m.Version != 1 || m.Role != "incoming" || m.Route != h.Route || len(m.Ticket) != 64 {
			return relay.ErrProtocol
		}
		select {
		case slots <- struct{}{}:
		default:
			continue
		}
		wg.Add(1)
		go func(m relay.Control) {
			defer wg.Done()
			defer func() { <-slots }()
			h.attach(ctx, m)
		}(m)
	}
}

func (h *Host) attach(ctx context.Context, m relay.Control) {
	ws, err := relay.DialContext(ctx, h.URL, relay.Control{
		Role: "attach", Route: h.Route, Ticket: m.Ticket,
	})
	if err != nil {
		return
	}
	defer ws.Close()
	stopWS := context.AfterFunc(ctx, func() { ws.Close() })
	defer stopWS()
	if relay.Await(ws, "paired") != nil {
		return
	}
	raw := NewConn(ws)
	defer raw.Close()
	h.mu.Lock()
	h.active[raw] = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.active, raw)
		h.mu.Unlock()
	}()
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()
	h.Handle(ctx, raw)
}

// Handle 处理一条未经信任的内层连接。关闭 raw 而不是 tls.Conn：
// TLS 的 close_notify 可能阻塞，直接切断传输更可控。
func (h *Host) Handle(ctx context.Context, raw net.Conn) {
	defer raw.Close()
	c := tls.Server(raw, h.Identity.ServerConfig(h.Authority.AllowHandshake))
	c.SetDeadline(time.Now().Add(HandshakeTimeout))
	if c.HandshakeContext(ctx) != nil {
		return
	}
	fp := Fingerprint(c.ConnectionState().PeerCertificates[0].Raw)
	if !h.Authority.Track(fp, raw) {
		return
	}
	defer h.Authority.Untrack(fp, raw)

	first, err := Receive(c)
	if err != nil {
		return
	}
	switch first.Type {
	case "pair":
		p, err := h.Authority.Begin(fp, first.ID, first.Secret, raw)
		if err != nil {
			return
		}
		defer h.Authority.Cancel(p.ID)
		if Send(c, Message{Type: "pending", ID: p.ID}) != nil {
			return
		}
		// 等待批准期间另起一个读者，任何非法业务字节或断线都会取消请求。
		premature := make(chan struct{})
		readerDone := make(chan struct{})
		c.SetReadDeadline(p.Expires)
		go func() {
			defer close(readerDone)
			var b [1]byte
			n, err := c.Read(b[:])
			ne, ok := err.(net.Error)
			if n > 0 || (err != nil && !(ok && ne.Timeout())) {
				close(premature)
				h.Authority.Cancel(p.ID)
			}
		}()
		timer := time.NewTimer(time.Until(p.Expires))
		approved := false
		select {
		case approved = <-p.Result:
		case <-ctx.Done():
		case <-timer.C:
		case <-premature:
		}
		timer.Stop()
		c.SetReadDeadline(time.Now())
		<-readerDone
		if !approved {
			return
		}
		// 批准与非法字节同时发生：失败关闭，吊销刚建立的授权。
		select {
		case <-premature:
			_ = h.Authority.Revoke(fp)
			return
		default:
		}
	case "resume":
	default:
		return
	}

	gen, expires, err := h.Authority.Grant(fp)
	if err != nil {
		return
	}
	c.SetDeadline(time.Now().Add(45 * time.Second))
	if Send(c, Message{Type: "approved", Generation: gen, Expires: expires}) != nil {
		return
	}
	for {
		c.SetDeadline(time.Now().Add(45 * time.Second))
		m, err := Receive(c)
		if err != nil {
			return
		}
		switch m.Type {
		case "status":
			gen, exp, err := h.Authority.Grant(fp)
			if err != nil {
				return
			}
			if Send(c, Message{Type: "status", Generation: gen, Expires: exp}) != nil {
				return
			}
		case "increment":
			value, err := h.Authority.Increment(fp, m.Generation, m.ID, m.Payload)
			if err != nil {
				return
			}
			if Send(c, Message{Type: "result", ID: m.ID, Value: value}) != nil {
				return
			}
		default:
			return
		}
	}
}

// Client 是控制端（未来是手机）。重连复用同一份内存身份，不重发操作。
type Client struct {
	Identity   *Identity
	Invitation Invitation
	Raw        net.Conn
	TLS        *tls.Conn
	Generation string
	Expires    time.Time
	PendingID  string

	// OnProgress 同步报告当前阶段，用于终端提示；不包含任何秘密。
	OnProgress func(stage string)
}

func NewClient(i Invitation) (*Client, error) {
	identity, err := NewIdentity()
	if err != nil {
		return nil, err
	}
	return &Client{Identity: identity, Invitation: i}, nil
}

// Connect 建立一条新的端到端连接。pair=false 复用本 Client 的身份重连。
func (c *Client) Connect(ctx context.Context, pair bool) error {
	c.Close()
	i := c.Invitation

	c.progress(StageRelayDial)
	ws, err := relay.DialContext(ctx, i.URL, relay.Control{
		Role: "connect", Route: i.Route, Credential: i.Credential,
	})
	if err != nil {
		return &StageError{Stage: StageRelayDial, Err: err}
	}
	stopWS := context.AfterFunc(ctx, func() { ws.Close() })
	defer stopWS()
	c.progress(StageRelayPair)
	if err = relay.Await(ws, "paired"); err != nil {
		ws.Close()
		return &StageError{Stage: StageRelayPair, Err: err}
	}
	raw := NewConn(ws)
	c.Raw = raw
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()

	c.progress(StageTLSConfig)
	cfg, err := c.Identity.ClientConfig(i.Certificate)
	if err != nil {
		c.Close()
		return &StageError{Stage: StageTLSConfig, Err: err}
	}
	c.progress(StageTLSHandshake)
	c.TLS = tls.Client(raw, cfg)
	c.TLS.SetDeadline(time.Now().Add(HandshakeTimeout))
	if err = c.TLS.HandshakeContext(ctx); err != nil {
		c.Close()
		return &StageError{Stage: StageTLSHandshake, Err: err}
	}

	c.progress(StagePairSend)
	m := Message{Type: "resume"}
	if pair {
		m = Message{Type: "pair", ID: i.ID, Secret: i.Secret}
	}
	if err = Send(c.TLS, m); err != nil {
		c.Close()
		return &StageError{Stage: StagePairSend, Err: err}
	}

	c.progress(StagePairWait)
	c.TLS.SetDeadline(time.Now().Add(ApprovalWaitTimeout))
	m, err = Receive(c.TLS)
	if err == nil && m.Type == "pending" {
		c.PendingID = m.ID
		c.progress(StageApprovalWait)
		m, err = Receive(c.TLS)
	}
	if err != nil {
		c.Close()
		return &StageError{Stage: StagePairWait, Err: err}
	}
	if m.Type != "approved" {
		c.Close()
		return ErrDenied
	}
	c.Generation = m.Generation
	c.Expires = m.Expires
	c.TLS.SetDeadline(time.Time{})
	return nil
}

// Increment 执行一次固定假计数；同一请求 ID 重试返回同一个值。
func (c *Client) Increment(id string) (uint64, error) {
	if c.TLS == nil {
		return 0, ErrDenied
	}
	c.TLS.SetDeadline(time.Now().Add(HandshakeTimeout))
	if err := Send(c.TLS, Message{Type: "increment", ID: id, Generation: c.Generation, Payload: "increment-one"}); err != nil {
		return 0, err
	}
	m, err := Receive(c.TLS)
	if err != nil {
		return 0, err
	}
	if m.Type != "result" || m.ID != id {
		return 0, ErrDenied
	}
	return m.Value, nil
}

// Status 是只读心跳，也用来发现授权已被撤销。
func (c *Client) Status() error {
	if c.TLS == nil {
		return ErrDenied
	}
	c.TLS.SetDeadline(time.Now().Add(HandshakeTimeout))
	if err := Send(c.TLS, Message{Type: "status"}); err != nil {
		return err
	}
	m, err := Receive(c.TLS)
	if err != nil {
		return err
	}
	if m.Type != "status" || m.Generation != c.Generation {
		return ErrDenied
	}
	return nil
}

func (c *Client) Close() {
	if c.Raw != nil {
		c.Raw.Close()
	}
	c.Raw = nil
	c.TLS = nil
}
