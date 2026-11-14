package mqttlink

import "context"

// TokenProvider 每次连接前提供 MQTT 用户名和密码。
type TokenProvider interface {
	Credentials(ctx context.Context) (username, password string, err error)
}

// StaticToken 用于自建模式：用户名密码直接来自配置。
type StaticToken struct {
	Username string
	Password string
}

func (s StaticToken) Credentials(context.Context) (string, string, error) {
	return s.Username, s.Password, nil
}
