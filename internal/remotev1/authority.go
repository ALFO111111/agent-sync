package remotev1

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/url"
	"sync"
	"time"

	"github.com/ALFO111111/agent-sync/internal/i18n"
)

const (
	// InvitationTTL 配对邀请有效期。扫码要快，降低泄露窗口。
	InvitationTTL = 10 * time.Minute
	// PendingTTL 待批准请求的有效期：用户 5 分钟不操作就自动过期。
	PendingTTL     = 5 * time.Minute
	maxInvitations = 8
	maxPending     = 8
	maxDevices     = 128
)

// ErrDenied 配对/授权被拒绝。
var ErrDenied = errors.New("remotev1: authorization denied")

// Device 是一台已批准的设备。
type Device struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	PublicKey   string    `json:"publicKey"`
	Fingerprint string    `json:"fingerprint"`
	ContentKey  string    `json:"contentKey,omitempty"` // 该设备专属的会话加密密钥
	CreatedAt   time.Time `json:"createdAt"`
	LastSeen    time.Time `json:"lastSeen,omitempty"`
	Revoked     bool      `json:"revoked,omitempty"`
}

// State 是主机需要跨重启保存的全部内容。
type State struct {
	Version    int      `json:"version"`
	HostID     string   `json:"hostId"`
	PrivateKey string   `json:"privateKey"`
	ContentKey string   `json:"contentKey,omitempty"` // 手机与电脑共用的内容加密密钥（base64）
	Devices    []Device `json:"devices"`

	// 旧 WebSocket 中转字段，仅用于识别旧状态并强制重新配对。
	LegacyRoute      string `json:"route,omitempty"`
	LegacyCredential string `json:"credential,omitempty"`

	// 官方模式登录后写入；自建模式保持为空。
	AccountID    string `json:"accountId,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`
}

// Store 负责状态的持久化（cmd 层实现加密文件）。
type Store interface {
	Load() (*State, error)
	Save(*State) error
}

// Invitation 是给手机扫码/粘贴的配对邀请。
type Invitation struct {
	Version    int       `json:"v"`
	URL        string    `json:"url"`
	UID        string    `json:"uid,omitempty"`
	ID         string    `json:"id"`
	Secret     string    `json:"secret"`
	ContentKey string    `json:"contentKey"`
	HostID     string    `json:"hostId"`
	HostName   string    `json:"hostName"`
	Expires    time.Time `json:"expires"`
	// 官方模式的短期配对凭据；手机扫码后用它连 Broker 的 pair 主题，不需要登录账号。
	MQTTUser     string `json:"mqttUser,omitempty"`
	MQTTPassword string `json:"mqttPassword,omitempty"`
	Mode         string `json:"mode,omitempty"`
}

// Validate 只做结构校验；URL 必须是 wss（自建也建议 wss）。
func (i Invitation) Validate() error {
	u, err := url.Parse(i.URL)
	schemeOK := err == nil && (u.Scheme == "wss" || (i.Mode == "selfhost" && u.Scheme == "ws"))
	if !schemeOK || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || u.Path != "/mqtt" {
		return ErrDenied
	}
	if i.Version != 3 || i.HostID == "" || len(i.ID) != 64 || len(i.Secret) != 64 {
		return ErrDenied
	}
	if _, err := ParseContentKey(i.ContentKey); err != nil {
		return ErrDenied
	}
	return nil
}

func EncodeInvitation(i Invitation) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func DecodeInvitation(s string) (Invitation, error) {
	var i Invitation
	if len(s) > 8192 || json.Unmarshal([]byte(s), &i) != nil {
		return i, ErrDenied
	}
	return i, i.Validate()
}

// Pending 是一次等待电脑操作员批准的配对请求。
type Pending struct {
	ID          string
	DeviceName  string
	DevicePub   string
	Fingerprint string
	InviteID    string
	ContentKey  string `json:"-"` // 只在内存里用，不暴露给桌面 UI
	CreatedAt   time.Time
	Expires     time.Time
	Result      chan bool
}

// purgeExpiredLocked 清掉超过 5 分钟没操作的待批准请求；调用方持有 a.mu。
// 不在这里回复：等待中的 goroutine 有自己的超时分支，负责给手机发 denied。
func (a *Authority) purgeExpiredLocked(now time.Time) {
	for id, p := range a.pending {
		if now.After(p.Expires) {
			delete(a.pending, id)
		}
	}
}

// Authority 管理邀请、待批准、设备白名单和撤销。
type Authority struct {
	mu          sync.Mutex
	state       *State
	store       Store
	url         string
	uid         string
	invitations map[string]Invitation
	pending     map[string]*Pending
	failed      int
	failWindow  time.Time
}

// NewAuthority 加载或初始化状态。store 为 nil 时仅内存（测试）。
// url 是邀请里写入的 wss 中转地址。
func NewAuthority(store Store, url string) (*Authority, error) {
	a := &Authority{
		store:       store,
		url:         url,
		uid:         "local",
		invitations: map[string]Invitation{},
		pending:     map[string]*Pending{},
	}
	if store != nil {
		s, err := store.Load()
		if err != nil {
			return nil, err
		}
		a.state = s
	}
	created := false
	if a.state == nil {
		a.state = &State{Version: 1, HostID: RandomToken(16)}
		created = true
	}
	// 旧 WebSocket 状态一律作废：清空设备并轮换内容密钥，强制重新配对。
	if a.state.LegacyRoute != "" || a.state.LegacyCredential != "" {
		a.state.LegacyRoute = ""
		a.state.LegacyCredential = ""
		a.state.Devices = nil
		key, err := NewContentKey()
		if err != nil {
			return nil, err
		}
		a.state.ContentKey = key.Base64()
		created = true
	}
	if a.state.PrivateKey == "" {
		id, err := NewIdentity()
		if err != nil {
			return nil, err
		}
		a.state.PrivateKey = id.PrivateBase64()
		created = true
	}
	if a.state.ContentKey == "" {
		key, err := NewContentKey()
		if err != nil {
			return nil, err
		}
		a.state.ContentKey = key.Base64()
		created = true
	}
	if _, err := ParseContentKey(a.state.ContentKey); err != nil {
		return nil, ErrDenied
	}
	if a.state.Version != 1 || a.state.HostID == "" {
		return nil, ErrDenied
	}
	if created && store != nil {
		if err := a.saveLocked(); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// State 返回内部状态副本（主机启动时读取身份）。
func (a *Authority) State() State {
	a.mu.Lock()
	defer a.mu.Unlock()
	return *a.state
}

// SetUID 设置邀请里的账号命名空间；自建模式固定为 local。
func (a *Authority) SetUID(uid string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if uid == "" {
		uid = "local"
	}
	a.uid = uid
}

// SetAccount 保存官方模式的账号和 refresh token（加密状态文件）。
func (a *Authority) SetAccount(accountID, refreshToken string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.AccountID = accountID
	a.state.RefreshToken = refreshToken
	return a.saveLocked()
}

// Logout 清除官方模式账号信息。
func (a *Authority) Logout() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.AccountID = ""
	a.state.RefreshToken = ""
	return a.saveLocked()
}

func (a *Authority) saveLocked() error {
	if a.store == nil {
		return nil
	}
	return a.store.Save(a.state)
}

// Invite 生成一份短期邀请。
func (a *Authority) Invite(hostName string) (Invitation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for id, v := range a.invitations {
		if now.After(v.Expires) {
			delete(a.invitations, id)
		}
	}
	if len(a.invitations) >= maxInvitations {
		return Invitation{}, ErrDenied
	}
	inv := Invitation{
		Version:    3,
		URL:        a.url,
		UID:        a.uid,
		ID:         RandomToken(32),
		Secret:     RandomToken(32),
		ContentKey: a.state.ContentKey,
		HostID:     a.state.HostID,
		HostName:   hostName,
		Expires:    now.Add(InvitationTTL),
	}
	a.invitations[inv.ID] = inv
	return inv, nil
}

// RotateContentKey 生成新密钥并作废所有未使用的邀请。
func (a *Authority) RotateContentKey() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.rotateContentKeyLocked()
}

// rotateContentKeyLocked 与 RotateContentKey 相同，但调用方必须已持有 a.mu。
func (a *Authority) rotateContentKeyLocked() error {
	key, err := NewContentKey()
	if err != nil {
		return err
	}
	old := a.state.ContentKey
	a.state.ContentKey = key.Base64()
	if err := a.saveLocked(); err != nil {
		a.state.ContentKey = old
		return err
	}
	a.invitations = map[string]Invitation{}
	return nil
}

// Device 按公钥查找设备（包括已撤销的）。
func (a *Authority) Device(pubB64 string) (Device, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, d := range a.state.Devices {
		if d.PublicKey == pubB64 {
			return d, true
		}
	}
	return Device{}, false
}

// DeviceByRef 按 ID / 指纹 / 公钥查找设备（含已撤销）。
func (a *Authority) DeviceByRef(ref string) (Device, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, d := range a.state.Devices {
		if d.ID == ref || d.Fingerprint == ref || d.PublicKey == ref {
			return d, true
		}
	}
	return Device{}, false
}

// IsActive 设备存在且未被撤销。
func (a *Authority) IsActive(ref string) bool {
	d, ok := a.DeviceByRef(ref)
	return ok && !d.Revoked
}

// SetDeviceKey 已知设备重新连接时更新它的独立密钥。
func (a *Authority) SetDeviceKey(pubB64, key string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.state.Devices {
		d := &a.state.Devices[i]
		if d.PublicKey != pubB64 {
			continue
		}
		if d.Revoked {
			return ErrDenied
		}
		old := d.ContentKey
		d.ContentKey = key
		if err := a.saveLocked(); err != nil {
			d.ContentKey = old
			return err
		}
		return nil
	}
	return ErrDenied
}

// BeginPair 登记待批准请求；不消费邀请，拒绝/取消可重试。
// contentKey 是手机提交的独立会话密钥（可选，旧 App 不提交）。
func (a *Authority) BeginPair(inviteID, secret, pubB64, name string, contentKey ...string) (*Pending, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	a.purgeExpiredLocked(now)
	if now.Sub(a.failWindow) > time.Minute {
		a.failWindow = now
		a.failed = 0
	}
	// 连续失败过多时暂停配对一分钟，防止暴力猜 secret。
	if a.failed >= 16 && now.Sub(a.failWindow) < time.Minute {
		return nil, ErrDenied
	}
	inv, ok := a.invitations[inviteID]
	if !ok || now.After(inv.Expires) || subtle.ConstantTimeCompare([]byte(inv.Secret), []byte(secret)) != 1 {
		a.failed++
		return nil, ErrDenied
	}
	for _, d := range a.state.Devices {
		if d.PublicKey == pubB64 {
			if d.Revoked {
				return nil, ErrDenied
			}
			break // 未撤销：允许重新配对（换加密密钥后恢复），仍然要走人工批准
		}
	}
	if len(a.pending) >= maxPending || len(a.state.Devices) >= maxDevices {
		return nil, ErrDenied
	}
	for _, p := range a.pending {
		if p.DevicePub == pubB64 && p.InviteID == inviteID {
			return nil, ErrDenied
		}
	}
	expires := now.Add(PendingTTL)
	if inv.Expires.Before(expires) {
		expires = inv.Expires
	}
	p := &Pending{
		ID:          RandomToken(16),
		DeviceName:  sanitizeName(name),
		DevicePub:   pubB64,
		Fingerprint: FingerprintOfBase64(pubB64),
		InviteID:    inviteID,
		CreatedAt:   now,
		Expires:     expires,
		Result:      make(chan bool, 1),
	}
	if len(contentKey) > 0 {
		p.ContentKey = contentKey[0]
	}
	a.pending[p.ID] = p
	return p, nil
}

// PendingList 返回待批准快照。
func (a *Authority) PendingList() []Pending {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.purgeExpiredLocked(time.Now())
	out := make([]Pending, 0, len(a.pending))
	for _, p := range a.pending {
		out = append(out, *p)
	}
	return out
}

// Cancel 取消一个待批准请求。
func (a *Authority) Cancel(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.purgeExpiredLocked(time.Now())
	if p := a.pending[id]; p != nil {
		delete(a.pending, id)
		p.Result <- false
	}
}

// Decide 批准或拒绝。批准时原子消费邀请并持久化设备。
func (a *Authority) Decide(id string, approve bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.purgeExpiredLocked(time.Now())
	p := a.pending[id]
	if p == nil {
		return ErrDenied
	}
	delete(a.pending, id)
	now := time.Now()
	ok := approve && now.Before(p.Expires)
	if ok {
		inv, exists := a.invitations[p.InviteID]
		if !exists || now.After(inv.Expires) {
			ok = false
		}
	}
	if ok {
		delete(a.invitations, p.InviteID)
		// 同一邀请的其他候选拒绝。
		for otherID, other := range a.pending {
			if other.InviteID == p.InviteID {
				delete(a.pending, otherID)
				other.Result <- false
			}
		}
		existing := -1
		for i, d := range a.state.Devices {
			if d.PublicKey == p.DevicePub {
				existing = i
				break
			}
		}
		if existing >= 0 {
			// 重新配对：更新名字、最近连接时间和独立密钥，不重复添加。
			dev := &a.state.Devices[existing]
			if dev.Revoked {
				ok = false
			} else {
				oldName, oldKey := dev.Name, dev.ContentKey
				dev.Name = p.DeviceName
				dev.LastSeen = now
				if p.ContentKey != "" {
					dev.ContentKey = p.ContentKey
				}
				if err := a.saveLocked(); err != nil {
					dev.Name, dev.ContentKey = oldName, oldKey
					ok = false
				}
			}
		} else {
			a.state.Devices = append(a.state.Devices, Device{
				ID:          RandomToken(12),
				Name:        p.DeviceName,
				PublicKey:   p.DevicePub,
				Fingerprint: p.Fingerprint,
				ContentKey:  p.ContentKey,
				CreatedAt:   now,
			})
			if err := a.saveLocked(); err != nil {
				// 保存失败则回滚白名单，邀请保留可重试。
				a.state.Devices = a.state.Devices[:len(a.state.Devices)-1]
				ok = false
			}
		}
	}
	p.Result <- ok
	if approve && !ok {
		return ErrDenied
	}
	return nil
}

// Devices 返回设备列表快照。
func (a *Authority) Devices() []Device {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Device, len(a.state.Devices))
	copy(out, a.state.Devices)
	return out
}

// Revoke 撤销设备；返回被撤销的设备，便于调用方断开连接。
func (a *Authority) Revoke(ref string) (Device, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for idx := range a.state.Devices {
		d := &a.state.Devices[idx]
		if d.ID == ref || d.Fingerprint == ref || d.PublicKey == ref {
			if d.Revoked {
				return *d, nil
			}
			oldKey := d.ContentKey
			d.Revoked = true
			d.ContentKey = ""
			if err := a.saveLocked(); err != nil {
				d.Revoked = false
				d.ContentKey = oldKey
				return Device{}, err
			}
			// 撤销后换掉共用配对密钥，旧手机不能再拿它发 pair 握手。
			_ = a.rotateContentKeyLocked()
			return *d, nil
		}
	}
	return Device{}, ErrDenied
}

// RemoveDevice 从白名单里彻底删除一台设备（撤销 + 删除记录）。
func (a *Authority) RemoveDevice(ref string) (Device, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for idx := range a.state.Devices {
		d := a.state.Devices[idx]
		if d.ID == ref || d.Fingerprint == ref || d.PublicKey == ref {
			a.state.Devices = append(a.state.Devices[:idx], a.state.Devices[idx+1:]...)
			if err := a.saveLocked(); err != nil {
				a.state.Devices = append(a.state.Devices, Device{})
				copy(a.state.Devices[idx+1:], a.state.Devices[idx:])
				a.state.Devices[idx] = d
				return Device{}, err
			}
			// 删除设备后轮换共用配对密钥；其他设备用各自的 ContentKey，不受影响。
			_ = a.rotateContentKeyLocked()
			return d, nil
		}
	}
	return Device{}, ErrDenied
}

// Rename 修改设备备注名。
func (a *Authority) Rename(ref, name string) (Device, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for idx := range a.state.Devices {
		d := &a.state.Devices[idx]
		if d.ID == ref || d.Fingerprint == ref {
			old := d.Name
			d.Name = sanitizeName(name)
			if err := a.saveLocked(); err != nil {
				d.Name = old
				return Device{}, err
			}
			return *d, nil
		}
	}
	return Device{}, ErrDenied
}

// Touch 更新最近在线时间，不频繁落盘。
func (a *Authority) Touch(pubB64 string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for idx := range a.state.Devices {
		if a.state.Devices[idx].PublicKey == pubB64 {
			a.state.Devices[idx].LastSeen = time.Now()
			return
		}
	}
}

// FingerprintOfBase64 计算 base64 公钥的指纹；非法输入返回空串。
func FingerprintOfBase64(pubB64 string) string {
	pub, err := ParsePublic(pubB64)
	if err != nil {
		return ""
	}
	return Fingerprint(pub)
}

// sanitizeName 限制设备名长度并去掉控制字符。
func sanitizeName(name string) string {
	r := make([]rune, 0, len(name))
	for _, c := range name {
		if c < 0x20 || c == 0x7f {
			continue
		}
		r = append(r, c)
		if len(r) >= 48 {
			break
		}
	}
	if len(r) == 0 {
		return i18n.T("未命名设备")
	}
	return string(r)
}
