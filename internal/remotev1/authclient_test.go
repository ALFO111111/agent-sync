package remotev1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthClientDeviceCredentials(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/mqtt/token" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]string{"username": "acc.d1", "password": "jwt"})
	}))
	defer srv.Close()

	c := NewAuthClient(srv.URL, "sess")
	user, pass, err := c.DeviceCredentials(context.Background(), "h1", "d1")
	if err != nil {
		t.Fatal(err)
	}
	if user != "acc.d1" || pass != "jwt" {
		t.Fatalf("credentials = %q/%q", user, pass)
	}
	if got["deviceId"] != "d1" || got["hostId"] != "h1" || got["role"] != "phone" || got["scope"] != "session" {
		t.Fatalf("request body = %+v", got)
	}
	if got["sessionToken"] != "sess" {
		t.Fatalf("session token missing: %+v", got)
	}
}

func TestAuthClientAccountUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/me":
			_, _ = w.Write([]byte(`{"plan":"pro","planExpiresAt":123}`))
		case "/v1/usage":
			_, _ = w.Write([]byte(`{"usedBytes":456,"quotaBytes":789}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := NewAuthClient(srv.URL, "sess")
	plan, exp, used, quota, err := c.AccountUsage(context.Background())
	if err != nil || plan != "pro" || exp != 123 || used != 456 || quota != 789 {
		t.Fatalf("got %q %d %d %d err=%v", plan, exp, used, quota, err)
	}
}
