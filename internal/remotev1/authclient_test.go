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
