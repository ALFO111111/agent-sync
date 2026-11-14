# 手机遥控 MQTT 部署指南

本文对应 0.7.0 起的 MQTT 遥控。旧 WebSocket relay 在验收后删除。

## 端口

| 位置 | 端口 | 作用 |
|---|---|---|
| ECS nginx | 443 | TLS 终止，`/mqtt` 反代 |
| ECS frps | 7000 | frp 控制/隧道 |
| ECS frps | 18083（仅 127.0.0.1） | 转发到 Mac 的 Mosquitto |
| Mac | 8083（仅 127.0.0.1） | Mosquitto WebSocket |
| Mac | 8787（仅 127.0.0.1） | token 服务（官方模式） |

手机和电脑统一连接：

```text
wss://agent-sync.yirenzaowusuo.cn/mqtt
```

注意域名是 `agent-sync`（带连字符），不是 `agent`；后者没有 DNS 记录。

## ECS

### nginx

在 `agent-sync.yirenzaowusuo.cn` 的 server 块中，把原来的：

```nginx
location / {
    proxy_pass http://127.0.0.1:18765;
    ...
}
```

替换为 `deploy/nginx/mqtt.conf` 里的 `/mqtt` location。证书继续复用
`/etc/letsencrypt/live/xlkd.yirenzaowusuo.cn/`（SAN 已包含 agent-sync 域名）。

```bash
nginx -t && systemctl reload nginx
```

### frps

`/etc/frp/frps.toml` 的 `allowPorts` 增加 18083：

```toml
{ start = 18083, end = 18083 }
```

然后：

```bash
systemctl restart frps
```

**注意：** 重启 frps 会让所有 frpc 隧道短暂断开，frpc 会自动重连；xlkd 和 seaweedfs 会抖动几秒。

## Mac mini（Apple Silicon）

### 安装 Mosquitto

```bash
brew install mosquitto
```

### 编译 go-auth 插件

Mac 无法直连 GitHub 时，用 Go 模块代理取源码：

```bash
export GOPROXY=https://goproxy.cn,direct
cd /tmp
~/sdk/go/bin/go mod download -json github.com/iegomez/mosquitto-go-auth@latest
# 把输出里的 Dir 复制到 /tmp/mosquitto-go-auth-build
cp -R <Dir> /tmp/mosquitto-go-auth-build
cd /tmp/mosquitto-go-auth-build
export PATH=$HOME/sdk/go/bin:$PATH
CGO_CFLAGS="-I/opt/homebrew/include -fPIC" \
CGO_LDFLAGS="-shared" \
make
sudo mkdir -p /opt/homebrew/lib/mosquitto
sudo cp go-auth.so /opt/homebrew/lib/mosquitto/go-auth.so
```

实际使用版本：

```text
module: github.com/iegomez/mosquitto-go-auth
version: v0.0.0-20250608182151-20ee9310fe3a
Mosquitto: 2.1.2 (Homebrew)
Go: 1.27.2 darwin/arm64
```

上游已归档；升级 Mosquitto 后必须重新编译并跑一次鉴权测试。

### 配置

把 `deploy/mosquitto/mosquitto.conf` 合并进
`/opt/homebrew/etc/mosquitto/mosquitto.conf`（先备份原文件），然后：

```bash
brew services restart mosquitto
```

此时 token 服务还没启动，用 test/test 连接应该得到
`Connection Refused: not authorized`，说明 go-auth 已生效。

### frpc

`~/.frpc.toml` 增加：

```toml
transport.tls.enable = true

[[proxies]]
name = "mqtt-ws"
type = "tcp"
localIP = "127.0.0.1"
localPort = 8083
remotePort = 18083
```

保留原有 `xlkd-web` 和 `object-store`，暂时保留 `agent-sync-relay` 以便回滚。
重启 frpc：

```bash
launchctl kickstart -k gui/$(id -u)/com.agentsync.frpc
```

### 禁止休眠

```bash
sudo pmset -a sleep 0 disksleep 0
```

## 验证

1. 本机 WebSocket 鉴权生效：

```bash
mosquitto_pub -L 'ws://127.0.0.1:8083/mqtt' -u test -P test -t test -m hello
# 预期：not authorized
```

2. 公网握手：

```bash
curl --http1.1 -i -N \
  -H "Connection: Upgrade" \
  -H "Upgrade: websocket" \
  -H "Sec-WebSocket-Version: 13" \
  -H "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==" \
  -H "Sec-WebSocket-Protocol: mqtt" \
  https://agent-sync.yirenzaowusuo.cn/mqtt
# 预期：101 Switching Protocols
```

必须带 `Sec-WebSocket-Protocol: mqtt`，并用 HTTP/1.1；Mosquitto 不接受 HTTP/2 直连的 WebSocket 升级。真实 MQTT 客户端会自动这样做。

3. 自建模式联调使用独立实例，不要用官方 go-auth 实例：

```bash
docker run -d --name mqtt-selfhost -p 1883:1883 \
  -v "$PWD/deploy/mosquitto/selfhost.conf:/mosquitto/config/mosquitto.conf" \
  eclipse-mosquitto:2
```

## 日志

| 进程 | 位置 |
|---|---|
| Mosquitto | `/opt/homebrew/var/log/mosquitto/mosquitto.log` |
| frpc | `~/Library/Logs/agentsync-frpc.log`（按 LaunchAgent 实际配置） |
| token 服务 | `~/Library/Logs/agent-sync-auth.log` |
| host | `~/Library/Logs/agentsync-host.log` |

## 回滚

- nginx：恢复备份的 server 块，`systemctl reload nginx`；
- frps：恢复 `/etc/frp/frps.toml.bak-*`，重启 frps；
- Mac：恢复 `~/.frpc.toml` 和 Mosquitto 配置，重启对应 LaunchAgent；
- 旧 relay 在 M3 验收前不要删除。
