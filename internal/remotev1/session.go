package remotev1

import (
	"errors"
	"time"
)

const (
	// ProtocolVersion 是协议版本。
	ProtocolVersion = 3
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
	SHA256      string   `json:"sha256,omitempty"`
	Attachments []string `json:"attachments,omitempty"`

	// 历史对话（打开老会话时）
	Messages   []HistoryMessage `json:"messages,omitempty"`
	Cursor     string           `json:"cursor,omitempty"`
	Snapshot   string           `json:"snapshot,omitempty"`
	Limit      int              `json:"limit,omitempty"`
	ChunkIndex int              `json:"chunkIndex"`
	Done       bool             `json:"done,omitempty"`
	HasMore    bool             `json:"hasMore,omitempty"`

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

// HistoryMessage 是历史对话里的一条（进入老会话时展示）。
type HistoryMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
	Time string `json:"time,omitempty"`
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
	History(tool, cwd, sessionID string) ([]HistoryMessage, error)
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
