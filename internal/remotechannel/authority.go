package remotechannel

import (
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/ALFO111111/agent-sync/internal/i18n"
	"github.com/ALFO111111/agent-sync/internal/relay"
)

// ErrDenied 授权、邀请或请求被拒绝。
var ErrDenied = i18n.Err("远程授权被拒绝")

const (
	ApprovalTTL   = 2 * time.Minute
	RequestWindow = 5 * time.Minute
	MaxRequests   = 256
	MaxIdentities = 64
)

// Invitation 是带外分享的配对邀请。只应在可信的本机终端里手工传递，
// 绝不放 URL、日志或普通存储。
type Invitation struct {
	Version     int       `json:"version"`
	URL         string    `json:"url"`
	Route       string    `json:"route"`
	Credential  string    `json:"credential"`
	Certificate []byte    `json:"certificate"`
	ID          string    `json:"id"`
	Secret      string    `json:"secret"`
	Expires     time.Time `json:"expires"`
}

// Validate 检查邀请结构。local 只允许回环测试使用 ws。
func (i Invitation) Validate(local bool) error {
	u, err := url.Parse(i.URL)
	if err != nil {
		return ErrDenied
	}
	loop := net.ParseIP(u.Hostname())
	allowedLocal := local && loop != nil && loop.IsLoopback() && u.Scheme == "ws"
	if (!allowedLocal && u.Scheme != "wss") || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		u.Path != "/ws" || u.Host == "" || i.Version != 1 || len(i.Route) != 64 ||
		len(i.Credential) != 64 || len(i.ID) != 64 || len(i.Secret) != 64 ||
		len(i.Certificate) == 0 || len(i.Certificate) > 4096 {
		return ErrDenied
	}
	return nil
}

func EncodeInvitation(i Invitation) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func DecodeInvitation(b []byte, local bool) (Invitation, error) {
	var i Invitation
	if len(b) > 8192 || json.Unmarshal(b, &i) != nil {
		return i, ErrDenied
	}
	return i, i.Validate(local)
}

type approval struct {
	ID          string
	Fingerprint string
	Invitation  string
	Expires     time.Time
	Connection  net.Conn
	Result      chan bool
}

type record struct {
	payload string
	value   uint64
}

type grant struct {
	generation string
	expires    time.Time
	records    map[string]record
}

// Authority 是执行端的内存授权表：邀请、待批准、授权、撤销和去重。
type Authority struct {
	mu          sync.Mutex
	invitations map[string]Invitation
	pending     map[string]*approval
	grants      map[string]*grant
	revoked     map[string]bool
	connections map[string]map[net.Conn]bool
	failed      int
	failWindow  time.Time
	count       uint64
}

func NewAuthority() *Authority {
	return &Authority{
		invitations: map[string]Invitation{},
		pending:     map[string]*approval{},
		grants:      map[string]*grant{},
		revoked:     map[string]bool{},
		connections: map[string]map[net.Conn]bool{},
	}
}

// Invite 生成一份短期邀请；同一时刻最多 8 份有效邀请。
func (a *Authority) Invite(template Invitation) (Invitation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for id, v := range a.invitations {
		if now.After(v.Expires) {
			delete(a.invitations, id)
		}
	}
	if len(a.invitations) >= 8 {
		return Invitation{}, ErrDenied
	}
	template.ID = relay.Token()
	template.Secret = relay.Token()
	template.Version = 1
	template.Expires = now.Add(ApprovalTTL)
	a.invitations[template.ID] = template
	return template, nil
}

// AllowHandshake 决定是否接受这个客户端证书继续握手。
// TLS 只证明对方持有私钥，握手成功不等于获得业务权限。
func (a *Authority) AllowHandshake(fp string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.revoked[fp] {
		return false
	}
	if g := a.grants[fp]; g != nil && time.Now().Before(g.expires) {
		return true
	}
	now := time.Now()
	for _, i := range a.invitations {
		if now.Before(i.Expires) {
			return true
		}
	}
	return false
}

func (a *Authority) Track(fp string, c net.Conn) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.revoked[fp] || len(a.connections[fp]) >= 8 {
		return false
	}
	if a.connections[fp] == nil {
		if len(a.connections) >= MaxIdentities {
			return false
		}
		a.connections[fp] = map[net.Conn]bool{}
	}
	a.connections[fp][c] = true
	return true
}

func (a *Authority) Untrack(fp string, c net.Conn) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.connections[fp], c)
	if len(a.connections[fp]) == 0 {
		delete(a.connections, fp)
	}
}

// Begin 登记一次待批准请求。它不消费邀请：拒绝、取消或断线都允许用
// 同一份邀请重试；只有 Decide 批准时才原子消费。
func (a *Authority) Begin(fp, id, secret string, connection net.Conn) (*approval, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	if now.Sub(a.failWindow) > time.Minute {
		a.failWindow = now
		a.failed = 0
	}
	if a.failed >= 16 || a.revoked[fp] || a.grants[fp] != nil {
		return nil, ErrDenied
	}
	inv, ok := a.invitations[id]
	if !ok || now.After(inv.Expires) || subtle.ConstantTimeCompare([]byte(inv.Secret), []byte(secret)) != 1 {
		a.failed++
		return nil, ErrDenied
	}
	if len(a.pending) >= 8 || len(a.grants)+len(a.revoked) >= MaxIdentities {
		return nil, ErrDenied
	}
	if !a.connections[fp][connection] {
		return nil, ErrDenied
	}
	for _, existing := range a.pending {
		if existing.Invitation == id && existing.Fingerprint == fp {
			return nil, ErrDenied
		}
	}
	p := &approval{
		ID:          relay.Token(),
		Fingerprint: fp,
		Invitation:  id,
		Expires:     inv.Expires,
		Connection:  connection,
		Result:      make(chan bool, 1),
	}
	a.pending[p.ID] = p
	return p, nil
}

func (a *Authority) Cancel(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p := a.pending[id]; p != nil {
		delete(a.pending, id)
		p.Result <- false
	}
}

// Pending 返回当前待批准列表的快照（不是实时订阅）。
type Pending struct {
	ID          string
	Fingerprint string
	Expires     time.Time
}

func (a *Authority) Pending() []Pending {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Pending, 0, len(a.pending))
	for _, p := range a.pending {
		out = append(out, Pending{ID: p.ID, Fingerprint: p.Fingerprint, Expires: p.Expires})
	}
	return out
}

// Decide 批准或拒绝一个具体请求。批准时重新检查邀请、连接存活和撤销状态，
// 然后原子消费邀请并让同邀请的其他候选失败。
func (a *Authority) Decide(id, fp string, approve bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.pending[id]
	if p == nil || p.Fingerprint != fp {
		return ErrDenied
	}
	delete(a.pending, id)
	inv, exists := a.invitations[p.Invitation]
	now := time.Now()
	ok := approve && exists && now.Before(inv.Expires) && now.Before(p.Expires) &&
		a.connections[fp][p.Connection] && !a.revoked[fp] && a.grants[fp] == nil
	if ok {
		delete(a.invitations, p.Invitation)
		for otherID, other := range a.pending {
			if other.Invitation == p.Invitation {
				delete(a.pending, otherID)
				other.Result <- false
			}
		}
		a.grants[fp] = &grant{
			generation: relay.Token(),
			expires:    now.Add(RequestWindow),
			records:    map[string]record{},
		}
	}
	p.Result <- ok
	if approve && !ok {
		return ErrDenied
	}
	return nil
}

// Revoke 先原子失权，再关闭该身份的所有连接；旧身份在本进程内不能再次配对。
func (a *Authority) Revoke(fp string) error {
	a.mu.Lock()
	if len(a.revoked)+len(a.grants) >= MaxIdentities && a.grants[fp] == nil && !a.revoked[fp] {
		a.mu.Unlock()
		return ErrDenied
	}
	delete(a.grants, fp)
	a.revoked[fp] = true
	for id, p := range a.pending {
		if p.Fingerprint == fp {
			delete(a.pending, id)
			p.Result <- false
		}
	}
	conns := make([]net.Conn, 0, len(a.connections[fp]))
	for c := range a.connections[fp] {
		conns = append(conns, c)
	}
	a.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
	return nil
}

// Grant 返回当前有效的授权代次。
func (a *Authority) Grant(fp string) (string, time.Time, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	g := a.grants[fp]
	if g == nil || a.revoked[fp] || time.Now().After(g.expires) {
		return "", time.Time{}, ErrDenied
	}
	return g.generation, g.expires, nil
}

// Increment 是唯一的业务操作，和撤销共用一把锁，
// 已排队的工作不会在撤销之后生效。同一请求 ID 只执行一次。
func (a *Authority) Increment(fp, generation, id, payload string) (uint64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	g := a.grants[fp]
	if g == nil || a.revoked[fp] || generation != g.generation || time.Now().After(g.expires) || len(id) != 64 {
		return 0, ErrDenied
	}
	if r, ok := g.records[id]; ok {
		if r.payload != payload {
			return 0, ErrDenied
		}
		return r.value, nil
	}
	if payload != "increment-one" || len(g.records) >= MaxRequests {
		return 0, ErrDenied
	}
	a.count++
	g.records[id] = record{payload: payload, value: a.count}
	return a.count, nil
}
