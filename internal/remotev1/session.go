package remotev1

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// MaxFrame 是单条应用消息上限。
	MaxFrame = 16 * 1024
	// ProtocolVersion 是协议版本。
	ProtocolVersion = 1
	// ReadTimeout 覆盖客户端 20 秒一次的心跳。
	ReadTimeout = 75 * time.Second
	// WriteTimeout 单次写超时。
	WriteTimeout = 10 * time.Second
	// MaxMsgPerSecond 单连接消息频率上限（含控制类）。
	MaxMsgPerSecond = 20
)

// ErrProtocol 帧或字段不合法。
var ErrProtocol = errors.New("remotev1: protocol rejected")

// Envelope 是 v1 的唯一帧格式（二进制帧承载 UTF-8 JSON）。
type Envelope struct {
	V int `json:"v"`

	Type string `json:"type"`
	ID   string `json:"id,omitempty"`

	DevicePub    string `json:"devicePub,omitempty"`
	DeviceName   string `json:"deviceName,omitempty"`
	InviteID     string `json:"inviteId,omitempty"`
	InviteSecret string `json:"inviteSecret,omitempty"`
	Nonce        string `json:"nonce,omitempty"`
	Signature    string `json:"signature,omitempty"`
	DeviceID     string `json:"deviceId,omitempty"`

	HostID   string `json:"hostId,omitempty"`
	HostName string `json:"hostName,omitempty"`

	Data     string       `json:"data,omitempty"`
	Error    string       `json:"error,omitempty"`
	Devices  []DeviceInfo `json:"devices,omitempty"`
	Features []string     `json:"features,omitempty"`

	// 会话相关（v2）
	Cwd             string         `json:"cwd,omitempty"`
	ResumeID        string         `json:"resumeId,omitempty"`
	SessionKey      string         `json:"sessionKey,omitempty"`
	SinceSeq        int64          `json:"sinceSeq,omitempty"`
	Seq             int64          `json:"seq,omitempty"`
	Kind            string         `json:"kind,omitempty"`
	Mode            string         `json:"mode,omitempty"`
	ApprovalID      string         `json:"approvalId,omitempty"`
	Allow           bool           `json:"allow,omitempty"`
	Text            string         `json:"text,omitempty"`
	Tool            string         `json:"tool,omitempty"`
	ToolID          string         `json:"toolId,omitempty"`
	ClaudeSessionID string         `json:"sessionId,omitempty"`
	Projects        []ProjectInfo  `json:"projects,omitempty"`
	Sessions        []SessionInfo  `json:"sessions,omitempty"`
	Events          []SessionEvent `json:"events,omitempty"`
	Active          []ActiveInfo   `json:"active,omitempty"`

	// 对话级附件（临时，不落项目目录）
	Name        string   `json:"name,omitempty"`
	MediaType   string   `json:"mediaType,omitempty"`
	AttachID    string   `json:"attachId,omitempty"`
	ChunkSize   int      `json:"chunkSize,omitempty"`
	Size        int64    `json:"size,omitempty"`
	Attachments []string `json:"attachments,omitempty"`

	// 电脑概况（首页/设置，只读）
	Overview *HostOverview `json:"overview,omitempty"`

	// 只读文件浏览 / git diff
	Path      string      `json:"path,omitempty"`
	Entries   []FileEntry `json:"entries,omitempty"`
	Changes   []GitChange `json:"changes,omitempty"`
	Truncated bool        `json:"truncated,omitempty"`
	Binary    bool        `json:"binary,omitempty"`
	Offset    int64       `json:"offset,omitempty"`
	Next      int64       `json:"next,omitempty"`
}

// DeviceInfo 是给手机看的最小设备信息，不含公钥。
type DeviceInfo struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Fingerprint string    `json:"fingerprint"`
	LastSeen    time.Time `json:"lastSeen,omitempty"`
	Online      bool      `json:"online"`
	Revoked     bool      `json:"revoked,omitempty"`
}

// ProjectInfo / SessionInfo 是会话索引的展示数据。
type ProjectInfo struct {
	Path      string    `json:"path"`
	Cwd       string    `json:"cwd"`
	Sessions  int       `json:"sessions"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type SessionInfo struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Cwd       string    `json:"cwd"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type SessionEvent struct {
	Seq        int64  `json:"seq"`
	Kind       string `json:"kind"`
	Text       string `json:"text,omitempty"`
	Tool       string `json:"tool,omitempty"`
	ToolID     string `json:"toolId,omitempty"`
	SessionID  string `json:"sessionId,omitempty"`
	ApprovalID string `json:"approvalId,omitempty"`
}

// SessionSink 由 cmd 层实现，把 Claude 会话能力接进协议；nil 表示不支持。
type SessionSink interface {
	Projects(tool string) ([]ProjectInfo, error)
	Sessions(tool, cwd string) ([]SessionInfo, error)
	Start(tool, cwd, resumeID, mode string) (key, claudeSessionID string, err error)
	SetMode(key, mode string) (newKey, claudeSessionID string, err error)
	Send(key, text string, atts []Attachment) error
	Stop(key string) error
	Close(key string) error
	Decide(key, approvalID string, allow bool) error
	Subscribe(key string, since int64) ([]SessionEvent, <-chan SessionEvent, func(), error)
	ActiveSessions() []ActiveInfo
}

type session struct {
	ws   *websocket.Conn
	wmu  sync.Mutex
	seal *Sealer
	open *Opener

	uploads map[string]*pendingAttachment // 本连接上的临时附件（随连接释放）

	window time.Time
	count  int
}

// newSession 建立一条加密会话：readDir 是对方→本端的方向，writeDir 是本端→对方。
func newSession(ws *websocket.Conn, key ContentKey, readDir, writeDir Direction) (*session, error) {
	seal, err := NewSealer(key, writeDir)
	if err != nil {
		return nil, err
	}
	open, err := NewOpener(key, readDir)
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(MaxFrame)
	ws.SetPongHandler(func(string) error { return nil })
	return &session{ws: ws, seal: seal, open: open, window: time.Now()}, nil
}

func (s *session) read() (Envelope, error) { return s.readWith(ReadTimeout) }

func (s *session) readWith(d time.Duration) (Envelope, error) {
	var env Envelope
	now := time.Now()
	if now.Sub(s.window) >= time.Second {
		s.window = now
		s.count = 0
	}
	s.count++
	if s.count > MaxMsgPerSecond {
		return env, ErrProtocol
	}
	s.ws.SetReadDeadline(now.Add(d))
	kind, b, err := s.ws.ReadMessage()
	if err != nil {
		return env, err
	}
	if kind != websocket.BinaryMessage && kind != websocket.TextMessage {
		return env, ErrProtocol
	}
	if len(b) == 0 || len(b) > MaxFrame {
		return env, ErrProtocol
	}
	// 外层只接受密文帧；明文（旧客户端或降级尝试）一律拒绝。
	var outer Envelope
	if json.Unmarshal(b, &outer) != nil || outer.V != ProtocolVersion || outer.Type != "enc" {
		return env, ErrProtocol
	}
	raw, err := base64.StdEncoding.DecodeString(outer.Data)
	if err != nil {
		return env, ErrProtocol
	}
	plain, err := s.open.Open(raw)
	if err != nil {
		return env, ErrProtocol
	}
	if json.Unmarshal(plain, &env) != nil || env.V != ProtocolVersion || env.Type == "" || env.Type == "enc" {
		return env, ErrProtocol
	}
	return env, nil
}

func (s *session) write(env Envelope) error {
	env.V = ProtocolVersion
	plain, err := json.Marshal(env)
	if err != nil {
		return err
	}
	if len(plain) > MaxPlain {
		return ErrProtocol
	}
	sealed, err := s.seal.Seal(plain)
	if err != nil {
		return err
	}
	outer := Envelope{V: ProtocolVersion, Type: "enc", Data: base64.StdEncoding.EncodeToString(sealed)}
	b, err := json.Marshal(outer)
	if err != nil {
		return err
	}
	if len(b) > MaxFrame {
		return ErrProtocol
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.ws.SetWriteDeadline(time.Now().Add(WriteTimeout))
	return s.ws.WriteMessage(websocket.BinaryMessage, b)
}

// ActiveInfo 是电脑端仍在运行的会话，供断线重连接回。
type ActiveInfo struct {
	Key             string `json:"key"`
	ClaudeSessionID string `json:"claudeSessionId,omitempty"`
	Cwd             string `json:"cwd"`
	Mode            string `json:"mode"`
	Tool            string `json:"tool,omitempty"`
}

// FileEntry 是目录里的一个条目。
type FileEntry struct {
	Name    string    `json:"name"`
	Dir     bool      `json:"dir"`
	Size    int64     `json:"size,omitempty"`
	ModTime time.Time `json:"modTime,omitempty"`
}

// GitChange 是 git status 的一条。
type GitChange struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

// FileSink 由 cmd 层实现：只读文件浏览和 git diff。
type FileSink interface {
	List(cwd, rel string) ([]FileEntry, error)
	Read(cwd, rel string, offset int64) (text string, truncated, binary bool, next int64, err error)
	GitStatus(cwd string) ([]GitChange, error)
	GitDiff(cwd, rel string) (text string, truncated bool, err error)
}
