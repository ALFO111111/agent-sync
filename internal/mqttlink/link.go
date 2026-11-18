package mqttlink

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type Config struct {
	URL         string
	ClientID    string
	WillTopic   string
	WillPayload []byte
	Token       TokenProvider
	// OnConnect 在每次（重新）连上 Broker 后调用；用来重发保留状态。
	OnConnect func()
}

// newClient 是 paho.NewClient 的间接层，测试里可以替换。
var newClient = mqtt.NewClient

type subscription struct {
	filter  string
	handler mqtt.MessageHandler
}

type Link struct {
	cfg    Config
	mu     sync.RWMutex
	client mqtt.Client
	subs   []subscription
	closed bool
}

// New 用 15 秒超时获取首次 token，避免调用方忘了传 ctx。
func New(cfg Config) (*Link, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return NewWithContext(ctx, cfg)
}

// NewWithContext 用调用方 ctx 获取首次 token；token 服务卡住时能按时放弃。
func NewWithContext(ctx context.Context, cfg Config) (*Link, error) {
	if cfg.URL == "" || cfg.ClientID == "" || cfg.Token == nil {
		return nil, errors.New("mqttlink: incomplete config")
	}
	l := &Link{cfg: cfg}
	if err := l.connect(ctx); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Link) connect(ctx context.Context) error {
	user, pass, err := l.cfg.Token.Credentials(ctx)
	if err != nil {
		return err
	}
	opts := mqtt.NewClientOptions()
	opts.AddBroker(l.cfg.URL)
	opts.SetClientID(l.cfg.ClientID)
	opts.SetUsername(user)
	opts.SetPassword(pass)
	opts.SetCleanSession(true)
	opts.SetKeepAlive(30 * time.Second)
	opts.SetConnectTimeout(10 * time.Second)
	// 断线后自动重连：否则主机掉线一次就永远不会再回来。
	opts.SetAutoReconnect(true)
	opts.SetMaxReconnectInterval(5 * time.Second)
	// token 只有 10 分钟有效期，每次重连都必须重新取，不能复用首次的凭证。
	opts.SetCredentialsProvider(func() (string, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		user, pass, err := l.cfg.Token.Credentials(ctx)
		if err != nil {
			return "", ""
		}
		return user, pass
	})
	// CleanSession 为 true 时 Broker 不保留订阅，重连后必须重新订阅。
	opts.SetOnConnectHandler(func(c mqtt.Client) {
		l.mu.RLock()
		subs := append([]subscription(nil), l.subs...)
		l.mu.RUnlock()
		for _, s := range subs {
			c.Subscribe(s.filter, 1, s.handler)
		}
		if l.cfg.OnConnect != nil {
			l.cfg.OnConnect()
		}
	})
	if l.cfg.WillTopic != "" {
		opts.SetWill(l.cfg.WillTopic, string(l.cfg.WillPayload), 1, true)
	}
	c := newClient(opts)
	tok := c.Connect()
	if !tok.WaitTimeout(10 * time.Second) {
		return errors.New("mqttlink: connect timeout")
	}
	if err := tok.Error(); err != nil {
		return fmt.Errorf("mqttlink: connect failed: %w", err)
	}
	l.mu.Lock()
	l.client = c
	l.mu.Unlock()
	return nil
}

func (l *Link) Subscribe(ctx context.Context, filters []string, handler func(string, []byte)) error {
	l.mu.RLock()
	c := l.client
	l.mu.RUnlock()
	if c == nil {
		return errors.New("mqttlink: not connected")
	}
	for _, f := range filters {
		h := func(_ mqtt.Client, m mqtt.Message) {
			handler(m.Topic(), m.Payload())
		}
		l.mu.Lock()
		l.subs = append(l.subs, subscription{filter: f, handler: h})
		l.mu.Unlock()

		tok := c.Subscribe(f, 1, h)
		if !tok.WaitTimeout(10*time.Second) || tok.Error() != nil {
			return errors.New("mqttlink: subscribe failed")
		}
	}
	return nil
}

func (l *Link) Publish(ctx context.Context, topic string, payload []byte, qos byte, retain bool) error {
	l.mu.RLock()
	c := l.client
	l.mu.RUnlock()
	if c == nil {
		return errors.New("mqttlink: not connected")
	}
	tok := c.Publish(topic, qos, retain, payload)
	if !tok.WaitTimeout(10*time.Second) || tok.Error() != nil {
		return errors.New("mqttlink: publish failed")
	}
	return nil
}

func (l *Link) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	if l.client != nil {
		l.client.Disconnect(250)
	}
	return nil
}
