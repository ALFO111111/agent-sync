// mqttprobe 是最小 MQTT 连通性探针：订阅一个主题、发布一条消息、打印收到的内容。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ALFO111111/agent-sync/internal/mqttlink"
)

func main() {
	url := flag.String("url", "", "broker URL, e.g. wss://agent-sync.yirenzaowusuo.cn/mqtt")
	user := flag.String("user", "", "username")
	pass := flag.String("pass", "", "password")
	topic := flag.String("topic", "probe", "topic")
	payload := flag.String("pub", "hello", "payload to publish")
	flag.Parse()

	if *url == "" {
		fmt.Fprintln(os.Stderr, "missing -url")
		os.Exit(2)
	}

	link, err := mqttlink.New(mqttlink.Config{
		URL:      *url,
		ClientID: fmt.Sprintf("probe-%d", time.Now().UnixNano()),
		Token:    mqttlink.StaticToken{Username: *user, Password: *pass},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer link.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	got := make(chan string, 1)
	if err := link.Subscribe(ctx, []string{*topic}, func(_ string, p []byte) {
		select {
		case got <- string(p):
		default:
		}
	}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := link.Publish(ctx, *topic, []byte(*payload), 1, false); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	select {
	case v := <-got:
		fmt.Println(v)
	case <-ctx.Done():
		fmt.Fprintln(os.Stderr, "timeout")
		os.Exit(1)
	}
}
