package remotev1

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
)

const (
	// MaxAttachments 每条消息最多带几个附件。
	MaxAttachments = 3
	// MQTTAttachChunk 是 MQTT 路径的单块大小；加密和 base64 后仍小于 256 KiB。
	MQTTAttachChunk = 64 * 1024
	// maxPendingAttachments 一条连接上同时存在的未发送附件数。
	maxPendingAttachments = 8
	// maxMQTTAttachmentSize 是 MQTT 单附件上限。
	maxMQTTAttachmentSize = 100 << 20
)

// Attachment 是一份随消息发送的临时附件，不写入项目目录。
type Attachment struct {
	Name      string
	MediaType string
	Kind      string // image | text | binary
	Data      []byte
}

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

type attachmentItem struct {
	size   int64
	sha    string
	chunks map[int][]byte
}

type attachmentStore struct {
	dir   string
	mu    sync.Mutex
	items map[string]*attachmentItem
}

func newAttachmentStore(dir string) *attachmentStore {
	return &attachmentStore{dir: dir, items: map[string]*attachmentItem{}}
}

func (s *attachmentStore) Begin(id string, size int64, sha string) error {
	if id == "" || size <= 0 || size > maxMQTTAttachmentSize {
		return ErrProtocol
	}
	if sha != "" && len(sha) != 64 {
		return ErrProtocol
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[id] = &attachmentItem{size: size, sha: sha, chunks: map[int][]byte{}}
	return nil
}

func (s *attachmentStore) Write(id string, seq int, data []byte) error {
	if seq < 0 || len(data) == 0 || len(data) > MQTTAttachChunk {
		return ErrProtocol
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.items[id]
	if it == nil {
		return ErrProtocol
	}
	it.chunks[seq] = append([]byte(nil), data...)
	return nil
}

func (s *attachmentStore) Commit(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.items[id]
	if it == nil {
		return "", ErrProtocol
	}
	f, err := os.CreateTemp(s.dir, "attach-*")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	var total int64
	for seq := 0; ; seq++ {
		chunk, ok := it.chunks[seq]
		if !ok {
			break
		}
		if _, err := f.Write(chunk); err != nil {
			f.Close()
			os.Remove(f.Name())
			return "", err
		}
		h.Write(chunk)
		total += int64(len(chunk))
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	if total != it.size || (it.sha != "" && hex.EncodeToString(h.Sum(nil)) != it.sha) {
		os.Remove(f.Name())
		return "", ErrProtocol
	}
	delete(s.items, id)
	return f.Name(), nil
}
