package mqttlink

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPTokenProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/mqtt/token" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in["sessionToken"] != "r" || in["role"] != "host" || in["scope"] != "session" {
			t.Fatalf("body = %+v", in)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"username":  "u/u1/d/d1",
			"password":  "jwt",
			"expiresAt": time.Now().Add(time.Hour).Unix(),
		})
	}))
	defer srv.Close()

	p := &HTTPTokenProvider{BaseURL: srv.URL, RefreshToken: "r", DeviceID: "d1", HostID: "h1"}
	u, pw, err := p.Credentials(context.Background())
	if err != nil || u != "u/u1/d/d1" || pw != "jwt" {
		t.Fatalf("got %q %q %v", u, pw, err)
	}
	// 第二次应命中缓存，不再请求服务。
	srv.Close()
	if _, _, err := p.Credentials(context.Background()); err != nil {
		t.Fatalf("cache miss: %v", err)
	}
}

func TestHTTPTokenProviderRejectsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	defer srv.Close()
	p := &HTTPTokenProvider{BaseURL: srv.URL, RefreshToken: "r", DeviceID: "d1", HostID: "h1"}
	if _, _, err := p.Credentials(context.Background()); err == nil {
		t.Fatal("401 accepted")
	}
}
