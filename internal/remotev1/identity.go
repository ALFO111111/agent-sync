// Package remotev1 实现手机与电脑之间的 v1 遥控协议。
//
// v1 的传输依赖外层 HTTPS/WSS（自己的中转服务器 + nginx），
// 应用层用配对密钥和设备 Ed25519 签名做身份校验；
// 内层端到端加密留到后续版本，文档必须明确中转理论上可见内容。
package remotev1

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
)

// Identity 是一台设备的 Ed25519 身份。私钥只用于本地持久化/Keystore。
type Identity struct {
	Public  ed25519.PublicKey
	Private ed25519.PrivateKey
}

// NewIdentity 生成新的设备身份。
func NewIdentity() (*Identity, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Identity{Public: pub, Private: priv}, nil
}

// IdentityFromBase64 从 base64 私钥恢复身份（存储层解密后调用）。
func IdentityFromBase64(privB64 string) (*Identity, error) {
	b, err := base64.StdEncoding.DecodeString(privB64)
	if err != nil || len(b) != ed25519.PrivateKeySize {
		return nil, ErrIdentity
	}
	priv := ed25519.PrivateKey(b)
	return &Identity{Public: priv.Public().(ed25519.PublicKey), Private: priv}, nil
}

// PrivateBase64 供加密存储使用，不要写日志。
func (i *Identity) PrivateBase64() string {
	return base64.StdEncoding.EncodeToString(i.Private)
}

// PublicBase64 是可以在邀请里公开的部分。
func (i *Identity) PublicBase64() string {
	return base64.StdEncoding.EncodeToString(i.Public)
}

// Fingerprint 是公钥的 SHA-256 十六进制串，用于人工核对。
func (i *Identity) Fingerprint() string { return Fingerprint(i.Public) }

// Fingerprint 计算任意公钥的指纹。
func Fingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}

// ParsePublic 解析 base64 的 Ed25519 公钥。
func ParsePublic(b64 string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, ErrIdentity
	}
	return ed25519.PublicKey(b), nil
}

// ProofData 是签名覆盖的字符串；版本和作用域都写死，防止跨协议重放。
func ProofData(nonce, devicePub, inviteID string) []byte {
	return []byte("agent-sync-v1|" + nonce + "|" + devicePub + "|" + inviteID)
}

// SignProof 用设备私钥签名挑战。
func (i *Identity) SignProof(nonce, inviteID string) string {
	sig := ed25519.Sign(i.Private, ProofData(nonce, i.PublicBase64(), inviteID))
	return base64.StdEncoding.EncodeToString(sig)
}

// VerifyProof 验证对端签名。
func VerifyProof(pub ed25519.PublicKey, nonce, devicePub, inviteID, sigB64 string) bool {
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pub, ProofData(nonce, devicePub, inviteID), sig)
}

// ErrIdentity 身份或签名无效。
var ErrIdentity = errors.New("remotev1: identity rejected")

// RandomToken 返回 n 字节随机数的十六进制串（默认 32 字节 = 64 字符）。
func RandomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// RandomNonce 返回 base64 的 32 字节挑战值。
func RandomNonce() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}
