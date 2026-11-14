package remotev1

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"strconv"
)

// Frame 是 MQTT 上的一条加密业务消息。
type Frame struct {
	V     int    `json:"v"`
	From  string `json:"from"`
	Seq   uint64 `json:"seq"`
	Nonce string `json:"nonce"`
	Data  string `json:"data"`
}

func frameAAD(dir Direction, from string, seq uint64) []byte {
	return []byte("agent-sync-e2ee-v3|" + string(dir) + "|" + from + "|" + strconv.FormatUint(seq, 10))
}

func newGCM(key ContentKey) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// SealFrame 用随机 nonce 加密一条消息；同一密钥下每条消息的 nonce 都不同。
func SealFrame(key ContentKey, dir Direction, from string, seq uint64, plain []byte) (Frame, error) {
	if len(plain) == 0 || len(plain) > MaxPlain || from == "" {
		return Frame{}, ErrCipher
	}
	aead, err := newGCM(key)
	if err != nil {
		return Frame{}, err
	}
	nonce := make([]byte, NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return Frame{}, err
	}
	ct := aead.Seal(nil, nonce, plain, frameAAD(dir, from, seq))
	return Frame{
		V:     3,
		From:  from,
		Seq:   seq,
		Nonce: base64.StdEncoding.EncodeToString(nonce),
		Data:  base64.StdEncoding.EncodeToString(ct),
	}, nil
}

// OpenFrame 校验发送方、方向和序号后解密。
func OpenFrame(key ContentKey, dir Direction, from string, seq uint64, f Frame) ([]byte, error) {
	if f.V != 3 || f.From != from || f.Seq != seq || from == "" {
		return nil, ErrCipher
	}
	nonce, err := base64.StdEncoding.DecodeString(f.Nonce)
	if err != nil || len(nonce) != NonceSize {
		return nil, ErrCipher
	}
	ct, err := base64.StdEncoding.DecodeString(f.Data)
	if err != nil || len(ct) < tagSize {
		return nil, ErrCipher
	}
	aead, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, nonce, ct, frameAAD(dir, from, seq))
	if err != nil || len(plain) == 0 || len(plain) > MaxPlain {
		return nil, ErrCipher
	}
	return plain, nil
}

// ReplayWindow 是按发送方维护的滑动窗口。
type ReplayWindow struct {
	size uint64
	max  uint64
	seen map[uint64]struct{}
}

func NewReplayWindow(size uint64) *ReplayWindow {
	if size == 0 {
		size = 1024
	}
	return &ReplayWindow{size: size, seen: map[uint64]struct{}{}}
}

// Accept 返回 true 表示这是新消息；重复或过旧返回 false。
func (w *ReplayWindow) Accept(seq uint64) bool {
	if seq == 0 {
		return false
	}
	if w.max == 0 {
		w.max = seq
		w.seen[seq] = struct{}{}
		return true
	}
	if seq > w.max {
		w.max = seq
		for s := range w.seen {
			if s+w.size <= w.max {
				delete(w.seen, s)
			}
		}
		w.seen[seq] = struct{}{}
		return true
	}
	if seq+w.size <= w.max {
		return false
	}
	if _, ok := w.seen[seq]; ok {
		return false
	}
	w.seen[seq] = struct{}{}
	return true
}
