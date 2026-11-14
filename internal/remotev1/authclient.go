package remotev1

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// DeviceRegistrar 由官方 auth 客户端实现；自建模式为 nil。
type DeviceRegistrar interface {
	RegisterDevice(ctx context.Context, hostID, deviceID, name, publicKey string) error
}

type AuthClient struct {
	BaseURL      string
	SessionToken string
	HTTP         *http.Client
}

func NewAuthClient(base, session string) *AuthClient {
	return &AuthClient{BaseURL: base, SessionToken: session, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

func (c *AuthClient) post(ctx context.Context, path string, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.SessionToken)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("auth service: %s", resp.Status)
	}
	return nil
}

func (c *AuthClient) RegisterHost(ctx context.Context, hostID, name, publicKey string) error {
	return c.post(ctx, "/v1/hosts/register", map[string]string{
		"hostId": hostID, "name": name, "publicKey": publicKey,
	})
}

func (c *AuthClient) RegisterDevice(ctx context.Context, hostID, deviceID, name, publicKey string) error {
	return c.post(ctx, "/v1/hosts/"+hostID+"/devices", map[string]string{
		"deviceId": deviceID, "name": name, "publicKey": publicKey,
	})
}
