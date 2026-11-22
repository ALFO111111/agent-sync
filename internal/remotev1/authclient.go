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
	// PairToken 生成只允许读写 pair 主题的短期凭据，供二维码里的手机配对使用。
	PairToken(ctx context.Context, hostID string) (username, password string, err error)
	// MQTTCredentials 取本机的 MQTT 凭据（账号+host 绑定的 JWT）。
	MQTTCredentials(ctx context.Context, hostID string) (username, password string, err error)
	// DeviceCredentials 取某台已登记手机的 MQTT 凭据（账号+手机绑定的 JWT），
	// 配对成功后通过加密通道交给手机。
	DeviceCredentials(ctx context.Context, hostID, deviceID string) (username, password string, err error)
	// AccountUsage 取当前套餐和本月流量，用于桌面端展示。
	AccountUsage(ctx context.Context) (plan string, planExpiresAt, usedBytes, quotaBytes int64, err error)
}

// StatusError 保留鉴权服务的 HTTP 状态码，调用方据此区分“登录失效”和“网络错误”。
type StatusError struct {
	Code   int
	Status string
}

func (e *StatusError) Error() string { return "auth service: " + e.Status }

type AuthClient struct {
	BaseURL      string
	SessionToken string
	HTTP         *http.Client
}

func NewAuthClient(base, session string) *AuthClient {
	return &AuthClient{BaseURL: base, SessionToken: session, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

func (c *AuthClient) post(ctx context.Context, path string, body any) error {
	return c.postInto(ctx, path, body, nil)
}

func (c *AuthClient) postInto(ctx context.Context, path string, body, out any) error {
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
		return &StatusError{Code: resp.StatusCode, Status: resp.Status}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *AuthClient) getInto(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.SessionToken)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return &StatusError{Code: resp.StatusCode, Status: resp.Status}
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Me 读取当前登录账号的用户名；同时用来判断会话是否还有效。
func (c *AuthClient) Me(ctx context.Context) (string, error) {
	var me struct {
		Username string `json:"username"`
	}
	if err := c.getInto(ctx, "/v1/me", &me); err != nil {
		return "", err
	}
	return me.Username, nil
}

func (c *AuthClient) AccountUsage(ctx context.Context) (string, int64, int64, int64, error) {
	var me struct {
		Plan          string `json:"plan"`
		PlanExpiresAt int64  `json:"planExpiresAt"`
	}
	if err := c.getInto(ctx, "/v1/me", &me); err != nil {
		return "", 0, 0, 0, err
	}
	var usage struct {
		UsedBytes  int64 `json:"usedBytes"`
		QuotaBytes int64 `json:"quotaBytes"`
	}
	if err := c.getInto(ctx, "/v1/usage", &usage); err != nil {
		return "", 0, 0, 0, err
	}
	return me.Plan, me.PlanExpiresAt, usage.UsedBytes, usage.QuotaBytes, nil
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

func (c *AuthClient) PairToken(ctx context.Context, hostID string) (string, string, error) {
	var out struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.postInto(ctx, "/v1/mqtt/pair-token", map[string]string{"hostId": hostID}, &out); err != nil {
		return "", "", err
	}
	if out.Username == "" || out.Password == "" {
		return "", "", fmt.Errorf("auth service: empty pair credentials")
	}
	return out.Username, out.Password, nil
}

func (c *AuthClient) mqttToken(ctx context.Context, deviceID, hostID, role, scope string) (string, string, error) {
	var out struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	body := map[string]string{
		"sessionToken": c.SessionToken,
		"deviceId":     deviceID,
		"hostId":       hostID,
		"role":         role,
		"scope":        scope,
	}
	if err := c.postInto(ctx, "/v1/mqtt/token", body, &out); err != nil {
		return "", "", err
	}
	if out.Username == "" || out.Password == "" {
		return "", "", fmt.Errorf("auth service: empty mqtt credentials")
	}
	return out.Username, out.Password, nil
}

func (c *AuthClient) MQTTCredentials(ctx context.Context, hostID string) (string, string, error) {
	return c.mqttToken(ctx, hostID, hostID, "host", "session")
}

func (c *AuthClient) DeviceCredentials(ctx context.Context, hostID, deviceID string) (string, string, error) {
	return c.mqttToken(ctx, deviceID, hostID, "phone", "session")
}
