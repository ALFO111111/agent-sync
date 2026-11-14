package remotev1

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/hosts/register" || r.Header.Get("Authorization") != "Bearer sess" {
			t.Fatalf("path=%s auth=%s", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.WriteHeader(201)
	}))
	defer srv.Close()
	c := &AuthClient{BaseURL: srv.URL, SessionToken: "sess", HTTP: srv.Client()}
	if err := c.RegisterHost(context.Background(), "h1", "pc", "pk"); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterDevice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/hosts/h1/devices" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	c := &AuthClient{BaseURL: srv.URL, SessionToken: "sess", HTTP: srv.Client()}
	if err := c.RegisterDevice(context.Background(), "h1", "d1", "phone", "pk"); err != nil {
		t.Fatal(err)
	}
}
