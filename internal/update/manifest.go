package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"

	"github.com/ALFO111111/agent-sync/internal/i18n"
)

// 官方更新清单地址：随 Release 一起发布的签名 JSON（GitHub 只负责托管，签名保证完整性）。
// 之前指向官网 /api/update/latest，但该路由并不存在，实际一直回退到 GitHub API。
var manifestBase = "https://github.com/ALFO111111/agent-sync/releases/latest/download/"

// manifestPubKey 是更新清单的 Ed25519 公钥（base64）。
// 轮换公钥必须随客户端发版，旧客户端不能验新清单。
var manifestPubKey = "xGCQ1dQYzkulJ5OfZOJZmbt5cY3ZIdZnC+mwLQrA9yI="

// Manifest 是签名后的更新清单。
type Manifest struct {
	Version     string          `json:"version"`
	PublishedAt string          `json:"publishedAt"`
	Assets      []ManifestAsset `json:"assets"`
	Signature   string          `json:"signature"`
}

type ManifestAsset struct {
	Platform string `json:"platform"`
	Name     string `json:"name,omitempty"`
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size,omitempty"`
	Notes    string `json:"notes,omitempty"`
}

func (m *Manifest) payload() ([]byte, error) {
	cp := *m
	cp.Signature = ""
	return json.Marshal(cp)
}

// Verify 验证清单签名；伪造或损坏的清单一律拒绝。
func (m *Manifest) Verify(pubB64 string) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(pubB64))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return errors.New("update: public key is invalid")
	}
	sig, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New(i18n.T("更新清单签名无效"))
	}
	body, err := m.payload()
	if err != nil {
		return err
	}
	if !ed25519.Verify(ed25519.PublicKey(raw), body, sig) {
		return errors.New(i18n.T("更新清单签名不正确，已拒绝"))
	}
	return nil
}

// checkManifest 拉取并验证自有更新清单；没有签名配置时返回错误，由调用方回退 GitHub。
func checkManifest(ctx context.Context, current string) (*Info, error) {
	if manifestPubKey == "" {
		return nil, errors.New("update: manifest public key not configured")
	}
	assetPlatform := runtime.GOOS + "/" + runtime.GOARCH
	u := strings.TrimRight(manifestBase, "/") + "/latest-" + runtime.GOOS + "-" + runtime.GOARCH + ".json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "agent-sync/"+current)
	resp, err := httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update manifest: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if err := m.Verify(manifestPubKey); err != nil {
		return nil, err
	}
	info := &Info{
		Current: current, Latest: strings.TrimPrefix(m.Version, "v"), Published: m.PublishedAt,
		URL: "https://agent-sync.yirenzaowusuo.cn/download",
		Dev: parseVersion(current) == nil,
	}
	for _, a := range m.Assets {
		if a.Platform != assetPlatform {
			continue
		}
		if a.SHA256 != "" && len(a.SHA256) != 64 {
			return nil, errors.New("update manifest: bad sha256")
		}
		if _, err := hex.DecodeString(a.SHA256); err != nil {
			return nil, errors.New("update manifest: bad sha256")
		}
		u, err := url.Parse(a.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, errors.New("update manifest: bad asset url")
		}
		info.Asset, info.assetURL, info.size, info.sha256 = a.Name, a.URL, a.Size, strings.ToLower(a.SHA256)
		info.Notes = a.Notes
		if info.Asset == "" {
			info.Asset = assetName(runtime.GOOS, runtime.GOARCH, info.Latest)
		}
		break
	}
	info.Available = !info.Dev && Newer(info.Latest, current)
	return info, nil
}

// signManifest 生成可发布的清单（生成器与测试共用）。
func signManifest(m *Manifest, seed []byte) error {
	if len(seed) != ed25519.SeedSize {
		return errors.New("update: private key must be a 32-byte seed")
	}
	body, err := m.payload()
	if err != nil {
		return err
	}
	m.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.NewKeyFromSeed(seed), body))
	return nil
}

// manifestSHA256 便于生成器打印校验值。
func manifestSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
