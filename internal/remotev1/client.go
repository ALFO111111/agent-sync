package remotev1

import (
	"context"
	"errors"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ALFO111111/agent-sync/internal/relay"
)

// Client 是 Go 版参考客户端：桌面测试、互操作测试使用。
// 手机端按同一协议在 TypeScript 中实现。
type Client struct {
	Identity   *Identity
	Name       string
	Invitation Invitation

	ws       *websocket.Conn
	s        *session
	DeviceID string
	HostName string
}

func NewClient(identity *Identity, name string, inv Invitation) *Client {
	return &Client{Identity: identity, Name: name, Invitation: inv}
}

// Pair 使用邀请完成首次配对（需要主机操作员批准）。
func (c *Client) Pair(ctx context.Context) error { return c.dial(ctx, true) }

// Connect 用已授权身份直接连接。
func (c *Client) Connect(ctx context.Context) error { return c.dial(ctx, false) }

func (c *Client) dial(ctx context.Context, pair bool) error {
	c.Close()
	inv := c.Invitation
	ws, err := relay.DialContext(ctx, inv.URL, relay.Control{
		Role: "connect", Route: inv.Route, Credential: inv.Credential,
	})
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { ws.Close() })
	if err = relay.Await(ws, "paired"); err != nil {
		stop()
		ws.Close()
		return err
	}
	key, err := ParseContentKey(inv.ContentKey)
	if err != nil {
		stop()
		ws.Close()
		return err
	}
	s, err := newSession(ws, key, HostToClient, ClientToHost)
	if err != nil {
		stop()
		ws.Close()
		return err
	}
	hello := Envelope{
		Type:       "hello",
		DevicePub:  c.Identity.PublicBase64(),
		DeviceName: c.Name,
	}
	if pair {
		hello.InviteID = inv.ID
		hello.InviteSecret = inv.Secret
	}
	if err = s.write(hello); err != nil {
		stop()
		ws.Close()
		return err
	}
	challenge, err := s.readWith(30 * time.Second)
	if err != nil || challenge.Type != "challenge" {
		stop()
		ws.Close()
		if err == nil {
			err = ErrProtocol
		}
		return err
	}
	if err = s.write(Envelope{
		Type:      "proof",
		DevicePub: c.Identity.PublicBase64(),
		InviteID:  hello.InviteID,
		Signature: c.Identity.SignProof(challenge.Nonce, hello.InviteID),
	}); err != nil {
		stop()
		ws.Close()
		return err
	}
	// 首次配对要等电脑操作员点批准，最长等邀请有效期再多 30 秒。
	wait := 30 * time.Second
	if pair {
		wait = InvitationTTL + 30*time.Second
	}
	for {
		env, err := s.readWith(wait)
		if err != nil {
			stop()
			ws.Close()
			return err
		}
		switch env.Type {
		case "pending":
			continue
		case "ready":
			c.ws = ws
			c.s = s
			c.DeviceID = env.DeviceID
			c.HostName = env.HostName
			return nil
		case "denied":
			stop()
			ws.Close()
			return ErrDenied
		default:
			stop()
			ws.Close()
			return ErrProtocol
		}
	}
}

// Echo 是一次同步测试往返；用于验收链路。
func (c *Client) Echo(ctx context.Context, data string) (string, error) {
	if c.s == nil {
		return "", ErrDenied
	}
	id := RandomToken(8)
	if err := c.s.write(Envelope{Type: "echo", ID: id, Data: data}); err != nil {
		return "", err
	}
	for {
		env, err := c.s.readWith(10 * time.Second)
		if err != nil {
			return "", err
		}
		switch env.Type {
		case "ping":
			if c.s.write(Envelope{Type: "pong"}) != nil {
				return "", ErrProtocol
			}
		case "echo":
			if env.ID != id {
				return "", ErrProtocol
			}
			return env.Data, nil
		case "devices", "status":
			continue
		case "error":
			return "", errors.New("remotev1: host error")
		default:
			return "", ErrProtocol
		}
	}
}

// Devices 拉取设备列表。
func (c *Client) Devices(ctx context.Context) ([]DeviceInfo, error) {
	if c.s == nil {
		return nil, ErrDenied
	}
	id := RandomToken(8)
	if err := c.s.write(Envelope{Type: "device.list", ID: id}); err != nil {
		return nil, err
	}
	for {
		env, err := c.s.readWith(10 * time.Second)
		if err != nil {
			return nil, err
		}
		switch env.Type {
		case "ping":
			_ = c.s.write(Envelope{Type: "pong"})
		case "devices":
			if env.ID != id {
				return nil, ErrProtocol
			}
			return env.Devices, nil
		case "error":
			return nil, ErrDenied
		}
	}
}

func (c *Client) Close() {
	if c.ws != nil {
		c.ws.Close()
	}
	c.ws = nil
	c.s = nil
}
