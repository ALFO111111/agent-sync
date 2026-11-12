package remotev1

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
)

const (
	// MaxPlain 是加密前单条消息的上限；base64 后必须仍能装进 MaxFrame。
	MaxPlain  = 11 * 1024
	NonceSize = 12
	KeySize   = 32
	tagSize   = 16
)

// ErrCipher 表示密钥、密文或序号不合法；调用方应直接断开。
var ErrCipher = errors.New("remotev1: cipher rejected")

// ContentKey 是电脑和手机共用的长期加密密钥。
type ContentKey [KeySize]byte

func NewContentKey() (ContentKey, error) {
	var k ContentKey
	if _, err := rand.Read(k[:]); err != nil {
		return k, err
	}
	return k, nil
}

func ParseContentKey(s string) (ContentKey, error) {
	var k ContentKey
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != KeySize {
		return k, ErrCipher
	}
	copy(k[:], b)
	return k, nil
}

func (k ContentKey) Base64() string { return base64.StdEncoding.EncodeToString(k[:]) }

// Direction 决定 AAD，防止把发出去的一帧原样反射回来。
type Direction string

const (
	ClientToHost Direction = "c2h"
	HostToClient Direction = "h2c"
)

func directionAAD(d Direction) []byte { return []byte("agent-sync-e2ee-v1|" + string(d)) }

// Sealer 负责一个方向的加密：每条连接随机前缀 + 递增计数器，避免 nonce 复用。
type Sealer struct {
	aead    cipher.AEAD
	aad     []byte
	prefix  [8]byte
	counter uint32
}

func NewSealer(key ContentKey, dir Direction) (*Sealer, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	s := &Sealer{aead: aead, aad: directionAAD(dir)}
	if _, err := rand.Read(s.prefix[:]); err != nil {
		return nil, err
	}
	return s, nil
}

// Seal 返回 nonce || 密文 || tag。
func (s *Sealer) Seal(plain []byte) ([]byte, error) {
	if len(plain) == 0 || len(plain) > MaxPlain || s.counter == ^uint32(0) {
		return nil, ErrCipher
	}
	nonce := make([]byte, NonceSize)
	copy(nonce[:8], s.prefix[:])
	binary.BigEndian.PutUint32(nonce[8:], s.counter)
	s.counter++
	ct := s.aead.Seal(nil, nonce, plain, s.aad)
	out := make([]byte, 0, NonceSize+len(ct))
	out = append(out, nonce...)
	return append(out, ct...), nil
}

// Opener 校验前缀和序号后解密，拒绝重放、乱序和跨连接混用。
type Opener struct {
	aead    cipher.AEAD
	aad     []byte
	prefix  [8]byte
	set     bool
	counter uint32
}

func NewOpener(key ContentKey, dir Direction) (*Opener, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	return &Opener{aead: aead, aad: directionAAD(dir)}, nil
}

func (o *Opener) Open(frame []byte) ([]byte, error) {
	if len(frame) < NonceSize+tagSize || len(frame) > NonceSize+MaxPlain+tagSize || o.counter == ^uint32(0) {
		return nil, ErrCipher
	}
	nonce := frame[:NonceSize]
	if !o.set {
		copy(o.prefix[:], nonce[:8])
		o.set = true
	} else if !equalPrefix(o.prefix[:], nonce[:8]) {
		return nil, ErrCipher
	}
	if binary.BigEndian.Uint32(nonce[8:]) != o.counter {
		return nil, ErrCipher
	}
	plain, err := o.aead.Open(nil, nonce, frame[NonceSize:], o.aad)
	if err != nil || len(plain) == 0 || len(plain) > MaxPlain {
		return nil, ErrCipher
	}
	o.counter++
	return plain, nil
}

func newAEAD(key ContentKey) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func equalPrefix(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}
