package mqttlink

import (
	"context"
	"errors"
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
}

type Link struct {
	cfg    Config
	mu     sync.RWMutex
	client mqtt.Client
	closed bool
}

func New(cfg Config) (*Link, error) {
	if cfg.URL == "" || cfg.ClientID == "" || cfg.Token == nil {
		return nil, errors.New("mqttlink: incomplete config")
	}
	l := &Link{cfg: cfg}
	if err := l.connect(context.Background()); err != nil {
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
	opts.SetAutoReconnect(false)
	if l.cfg.WillTopic != "" {
		opts.SetWill(l.cfg.WillTopic, string(l.cfg.WillPayload), 1, true)
	}
	c := mqtt.NewClient(opts)
	tok := c.Connect()
	if !tok.WaitTimeout(10*time.Second) || tok.Error() != nil {
		return errors.New("mqttlink: connect failed")
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
		tok := c.Subscribe(f, 1, func(_ mqtt.Client, m mqtt.Message) {
			handler(m.Topic(), m.Payload())
		})
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
