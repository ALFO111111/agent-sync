package remotev1

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
)

const (
	// MaxPlain 是加密前单条消息的上限；64 KiB 允许附件按 64 KiB 分块。
	MaxPlain  = 64 * 1024
	NonceSize = 12
	KeySize   = 32
	tagSize   = 16
)

// ErrCipher 表示密钥、密文或序号不合法；调用方应直接丢弃。
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
