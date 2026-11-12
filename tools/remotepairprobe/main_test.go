package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/ALFO111111/agent-sync/internal/relay"
)

func TestDictionary(t *testing.T) {
	for key, pair := range texts {
		if pair[0] == "" || pair[1] == "" {
			t.Errorf("翻译缺失 %s", key)
		}
		for _, r := range pair[1] {
			if unicode.Is(unicode.Han, r) {
				t.Errorf("英文里有中文 %s", key)
			}
		}
	}
}

func TestBackoffCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if backoff(ctx, 8) {
		t.Fatal("退避没有响应取消")
	}
}

// TestSelftestThroughRelay 用本机中转跑完整闭环；公网验收在 Mac 上执行同一条命令。
func TestSelftestThroughRelay(t *testing.T) {
	r := relay.New()
	s := httptest.NewServer(r)
	defer func() { r.Close(); s.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(s.URL, "http") + "/ws"
	if err := selftest(ctx, url, true); err != nil {
		t.Fatal(err)
	}
}
