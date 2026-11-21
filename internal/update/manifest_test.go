package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
)

func newTestManifest(t *testing.T, version string) (Manifest, string) {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	pub := base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
	m := Manifest{
		Version:     version,
		PublishedAt: "2026-11-21T00:00:00Z",
		Assets: []ManifestAsset{{
			Platform: runtime.GOOS + "/" + runtime.GOARCH,
			Name:     "agent-sync.exe",
			URL:      "https://dl.example.com/agent-sync.exe",
			SHA256:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Size:     123,
		}},
	}
	if err := signManifest(&m, seed); err != nil {
		t.Fatal(err)
	}
	return m, pub
}

func withManifest(t *testing.T, m Manifest, pub string) {
	t.Helper()
	oldBase, oldPub := manifestBase, manifestPubKey
	t.Cleanup(func() { manifestBase, manifestPubKey = oldBase, oldPub })
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	manifestBase = srv.URL
	manifestPubKey = pub
}

func TestCheckManifestVerified(t *testing.T) {
	m, pub := newTestManifest(t, "0.7.1")
	withManifest(t, m, pub)
	info, err := checkManifest(context.Background(), "0.7.0")
	if err != nil {
		t.Fatal(err)
	}
	if info.Latest != "0.7.1" || !info.Available || info.Asset != "agent-sync.exe" || info.sha256 != m.Assets[0].SHA256 {
		t.Fatalf("info = %+v", info)
	}
}

func TestCheckManifestRejectsTampering(t *testing.T) {
	m, pub := newTestManifest(t, "0.7.1")
	m.Version = "9.9.9" // 签名后改版本
	withManifest(t, m, pub)
	if _, err := checkManifest(context.Background(), "0.7.0"); err == nil {
		t.Fatal("tampered version must be rejected")
	}

	m, pub = newTestManifest(t, "0.7.1")
	m.Assets[0].SHA256 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	withManifest(t, m, pub)
	if _, err := checkManifest(context.Background(), "0.7.0"); err == nil {
		t.Fatal("tampered sha256 must be rejected")
	}
}

func TestCheckFallsBackToGitHub(t *testing.T) {
	oldAPI, oldBase, oldPub := apiBase, manifestBase, manifestPubKey
	t.Cleanup(func() { apiBase, manifestBase, manifestPubKey = oldAPI, oldBase, oldPub })
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"9.9.9","signature":"bad"}`))
	}))
	defer bad.Close()
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v0.7.0","html_url":"https://github.com/x","assets":[]}`))
	}))
	defer gh.Close()
	manifestBase, manifestPubKey, apiBase = bad.URL, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", gh.URL
	info, err := Check(context.Background(), "0.6.9")
	if err != nil {
		t.Fatal(err)
	}
	if info.Latest != "0.7.0" || !info.Available {
		t.Fatalf("expected github fallback, got %+v", info)
	}
}
