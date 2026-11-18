package mqttlink

import (
	"context"
	"os"
	"testing"
	"time"
)

type blockingToken struct{}

func (blockingToken) Credentials(ctx context.Context) (string, string, error) {
	<-ctx.Done()
	return "", "", ctx.Err()
}

func TestNewWithContextHonorsDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := NewWithContext(ctx, Config{URL: "ws://127.0.0.1:1/mqtt", ClientID: "c", Token: blockingToken{}})
	if err == nil {
		t.Fatal("want error")
	}
	if time.Since(start) > time.Second {
		t.Fatalf("NewWithContext took %v, want <= 1s", time.Since(start))
	}
}

func TestLinkRoundTrip(t *testing.T) {
	url := os.Getenv("MOSQUITTO_TEST_URL")
	if url == "" {
		t.Skip("set MOSQUITTO_TEST_URL=tcp://127.0.0.1:1883")
	}
	cfg := Config{
		URL:      url,
		ClientID: "test-a",
		Token:    StaticToken{Username: "test", Password: "test"},
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	got := make(chan string, 1)
	if err := a.Subscribe(context.Background(), []string{"test/topic"}, func(_ string, p []byte) {
		got <- string(p)
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.Publish(context.Background(), "test/topic", []byte("hi"), 1, false); err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-got:
		if v != "hi" {
			t.Fatalf("got %q", v)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}
}

func TestStaticToken(t *testing.T) {
	u, p, err := StaticToken{Username: "u", Password: "p"}.Credentials(context.Background())
	if err != nil || u != "u" || p != "p" {
		t.Fatalf("got %q %q %v", u, p, err)
	}
}
