package mqttlink

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// HTTPTokenProvider 从官方 token 服务换取短期 MQTT 凭证。
type HTTPTokenProvider struct {
	BaseURL      string
	RefreshToken string
	DeviceID     string
	HostID       string

	mu        sync.Mutex
	username  string
	password  string
	expiresAt time.Time
}

func (p *HTTPTokenProvider) Credentials(ctx context.Context) (string, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.password != "" && time.Until(p.expiresAt) > 2*time.Minute {
		return p.username, p.password, nil
	}
	body, _ := json.Marshal(map[string]string{
		"refreshToken": p.RefreshToken,
		"deviceId":     p.DeviceID,
		"hostId":       p.HostID,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/v1/mqtt/token", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", "", fmt.Errorf("token service: %s", resp.Status)
	}
	var out struct {
		Username  string    `json:"username"`
		Password  string    `json:"password"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", "", err
	}
	if out.Username == "" || out.Password == "" {
		return "", "", fmt.Errorf("token service: empty credentials")
	}
	p.username, p.password, p.expiresAt = out.Username, out.Password, out.ExpiresAt
	return p.username, p.password, nil
}
