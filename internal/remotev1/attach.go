package remotev1

import (
	"errors"
	"path/filepath"
	"strings"
	"unicode"
)

const (
	// MaxAttachmentSize 单个附件上限（图片/文本/二进制都算）。
	MaxAttachmentSize = 10 << 20
	// MaxAttachments 每条消息最多带几个附件。
	MaxAttachments = 3
	// AttachChunkSize 每个分块解码后的字节数；base64 后仍能装进一帧。
	AttachChunkSize = 6 * 1024
	// maxPendingAttachments 一条连接上同时存在的未发送附件数。
	maxPendingAttachments = 8
)

// Attachment 是一份随消息发送的临时附件，不写入项目目录。
type Attachment struct {
	Name      string
	MediaType string
	Kind      string // image | text | binary
	Data      []byte
}

type pendingAttachment struct {
	name      string
	mediaType string
	kind      string
	size      int64
	data      []byte
	next      int64
	ready     bool
}

func (s *session) beginAttachment(name, mediaType, kind string, size int64) (string, int, error) {
	if size <= 0 || size > MaxAttachmentSize {
		return "", 0, errors.New("attachment too large")
	}
	switch kind {
	case "image", "text", "binary":
	default:
		return "", 0, errors.New("invalid attachment kind")
	}
	if s.uploads == nil {
		s.uploads = map[string]*pendingAttachment{}
	}
	if len(s.uploads) >= maxPendingAttachments {
		return "", 0, errors.New("too many pending attachments")
	}
	id := RandomToken(16)
	s.uploads[id] = &pendingAttachment{
		name:      sanitizeAttachmentName(name),
		mediaType: mediaType,
		kind:      kind,
		size:      size,
	}
	return id, AttachChunkSize, nil
}

func (s *session) addAttachmentChunk(id string, seq int64, data []byte) error {
	p := s.uploads[id]
	if p == nil {
		return errors.New("attachment not found")
	}
	if seq != p.next {
		return errors.New("chunk out of order")
	}
	if len(data) == 0 || int64(len(p.data))+int64(len(data)) > p.size {
		return errors.New("chunk too large")
	}
	p.data = append(p.data, data...)
	p.next++
	return nil
}

func (s *session) finishAttachment(id string) (*Attachment, error) {
	p := s.uploads[id]
	if p == nil {
		return nil, errors.New("attachment not found")
	}
	if int64(len(p.data)) != p.size {
		return nil, errors.New("attachment size mismatch")
	}
	// 只标记就绪；真正发送（turn.send）时才消费，避免 end 之后再也取不到。
	p.ready = true
	return &Attachment{Name: p.name, MediaType: p.mediaType, Kind: p.kind, Data: p.data}, nil
}

// takeAttachments 按 turn.send 里的 ID 取出附件并删除（只用于这一轮）。
func (s *session) takeAttachments(ids []string) ([]Attachment, error) {
	if len(ids) > MaxAttachments {
		return nil, errors.New("too many attachments")
	}
	out := make([]Attachment, 0, len(ids))
	for _, id := range ids {
		p := s.uploads[id]
		if p == nil {
			return nil, errors.New("attachment not found")
		}
		if !p.ready || int64(len(p.data)) != p.size {
			return nil, errors.New("attachment incomplete")
		}
		out = append(out, Attachment{Name: p.name, MediaType: p.mediaType, Kind: p.kind, Data: p.data})
		delete(s.uploads, id)
	}
	return out, nil
}

// sanitizeAttachmentName 只保留文件名本身，去掉路径和控制字符。
func sanitizeAttachmentName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	var b strings.Builder
	for _, r := range name {
		if r == unicode.ReplacementChar || r < 0x20 || r == 0x7f || r == '/' || r == '\\' {
			continue
		}
		b.WriteRune(r)
		if b.Len() >= 120 {
			break
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" || out == "." || out == ".." {
		return "attachment"
	}
	return out
}
