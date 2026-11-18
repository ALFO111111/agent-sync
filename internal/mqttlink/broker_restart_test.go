package mqttlink

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// 真实的断线恢复测试：起一个临时 Mosquitto，订阅后把 Broker 杀掉再拉起，
// 验证客户端能自动重连、重新订阅，消息还能继续送达。
// 没装 mosquitto 的机器会自动跳过。
func TestLinkRecoversFromBrokerRestart(t *testing.T) {
	bin := os.Getenv("MOSQUITTO_BIN")
	if bin == "" {
		p, err := exec.LookPath("mosquitto")
		if err != nil {
			t.Skip("mosquitto 未安装；设置 MOSQUITTO_BIN 后再跑")
		}
		bin = p
	}

	port := freePort(t)
	dir := t.TempDir()
	conf := filepath.Join(dir, "mosquitto.conf")
	if err := os.WriteFile(conf, []byte(fmt.Sprintf("listener %d 127.0.0.1\nallow_anonymous true\n", port)), 0o600); err != nil {
		t.Fatal(err)
	}

	var broker *exec.Cmd
	start := func() {
		broker = exec.Command(bin, "-c", conf)
		if os.Getenv("MOSQUITTO_TEST_VERBOSE") == "" {
			broker.Stdout = io.Discard
			broker.Stderr = io.Discard
		}
		if err := broker.Start(); err != nil {
			t.Fatal(err)
		}
		waitPort(t, port)
	}
	start()
	t.Cleanup(func() {
		if broker != nil && broker.Process != nil {
			_ = broker.Process.Kill()
			_, _ = broker.Process.Wait()
		}
	})

	url := fmt.Sprintf("tcp://127.0.0.1:%d", port)
	sub, err := New(Config{URL: url, ClientID: "restart-sub", Token: StaticToken{}})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	pub, err := New(Config{URL: url, ClientID: "restart-pub", Token: StaticToken{}})
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()

	got := make(chan string, 16)
	if err := sub.Subscribe(context.Background(), []string{"t/restart"}, func(_ string, p []byte) {
		got <- string(p)
	}); err != nil {
		t.Fatal(err)
	}

	if err := pub.Publish(context.Background(), "t/restart", []byte("before"), 1, false); err != nil {
		t.Fatal(err)
	}
	waitForMessage(t, got, "before", 5*time.Second)

	// 模拟 Broker 重启（也等价于主机侧网络中断后服务恢复）。
	_ = broker.Process.Kill()
	_, _ = broker.Process.Wait()
	start()

	deadline := time.Now().Add(30 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("broker 重启后没有恢复：自动重连或重新订阅失败")
		}
		_ = pub.Publish(context.Background(), "t/restart", []byte("after"), 1, false)
		select {
		case m := <-got:
			if m == "after" {
				return
			}
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitPort(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("mosquitto 没有在端口 %d 上启动", port)
}

func waitForMessage(t *testing.T, ch <-chan string, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case m := <-ch:
			if m == want {
				return
			}
		case <-deadline:
			t.Fatalf("没有收到 %q", want)
		}
	}
}
