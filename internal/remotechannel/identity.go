package remotechannel

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"math/big"
	"time"

	"github.com/ALFO111111/agent-sync/internal/i18n"
)

// ServerName 是内层 TLS 使用的固定名字；真实信任来自证书指纹 pin。
const ServerName = "agent-sync-remote.invalid"

// ErrIdentity 对端身份不符合预期。
var ErrIdentity = i18n.Err("远程身份验证失败")

// Identity 只存在于内存；私钥绝不序列化、不写文件。
type Identity struct {
	Certificate tls.Certificate
	DER         []byte
	Fingerprint string
}

// NewIdentity 生成本次运行的临时 Ed25519 自签名证书。
func NewIdentity() (*Identity, error) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: ServerName},
		DNSNames:              []string{ServerName},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, key)
	if err != nil {
		return nil, err
	}
	return &Identity{
		Certificate: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key},
		DER:         der,
		Fingerprint: Fingerprint(der),
	}, nil
}

// Fingerprint 是证书 DER 的 SHA-256 十六进制串。
func Fingerprint(der []byte) string {
	h := sha256.Sum256(der)
	return hex.EncodeToString(h[:])
}

// ClientConfig 用正常证书验证 + 固定内部服务名 + 精确 fingerprint pin。
// 不用无替代校验的 InsecureSkipVerify。
func (i *Identity) ClientConfig(hostDER []byte) (*tls.Config, error) {
	cert, err := x509.ParseCertificate(hostDER)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	pin := Fingerprint(hostDER)
	return &tls.Config{
		MinVersion:             tls.VersionTLS13,
		MaxVersion:             tls.VersionTLS13,
		ServerName:             ServerName,
		RootCAs:                roots,
		Certificates:           []tls.Certificate{i.Certificate},
		SessionTicketsDisabled: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) != 1 || Fingerprint(cs.PeerCertificates[0].Raw) != pin {
				return ErrIdentity
			}
			return nil
		},
	}, nil
}

// ServerConfig 要求客户端出示证书（证明持有私钥），再由 allow 决定
// 这个指纹是不是已批准或仍可进入配对。VerifyConnection 只读状态，
// 真正的授权状态变更发生在握手成功之后。
func (i *Identity) ServerConfig(allow func(string) bool) *tls.Config {
	return &tls.Config{
		MinVersion:             tls.VersionTLS13,
		MaxVersion:             tls.VersionTLS13,
		Certificates:           []tls.Certificate{i.Certificate},
		ClientAuth:             tls.RequireAnyClientCert,
		SessionTicketsDisabled: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) != 1 {
				return ErrIdentity
			}
			cert := cs.PeerCertificates[0]
			if time.Now().Before(cert.NotBefore) || time.Now().After(cert.NotAfter) ||
				cert.PublicKeyAlgorithm != x509.Ed25519 || !allow(Fingerprint(cert.Raw)) {
				return ErrIdentity
			}
			return nil
		},
	}
}
