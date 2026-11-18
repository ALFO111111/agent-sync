# 手机遥控 MQTT 部署指南

本文对应 0.7.0 起的 MQTT 遥控。旧 WebSocket relay 已删除。

**2026-11-16 迁移：官方 Broker 从家里的 Mac mini 迁到 ECS，Mac 上的 Mosquitto 已卸载。**
Broker 在 ECS 本地后，手机和电脑各只跨一次公网，消息往返从 116–209ms 降到 61–78ms。
Mac 只保留账号/token 服务和 frp 隧道。

## 端口

| 位置 | 端口 | 作用 |
|---|---|---|
| ECS nginx | 443 | TLS 终止，`/mqtt` 反代到本机 8083 |
| ECS Mosquitto | 8083（仅 127.0.0.1） | 官方 Broker（WebSocket） |
| ECS frps | 7000 | frp 控制/隧道 |
| ECS frps | 18084（仅 127.0.0.1） | 转发到 Mac 的 token 服务 8787，供 go-auth 回调 |
| Mac | 8787（仅 127.0.0.1） | token 服务（官方模式） |

手机和电脑统一连接：

```text
wss://agent-sync.yirenzaowusuo.cn/mqtt
```

注意域名是 `agent-sync`（带连字符），不是 `agent`；后者没有 DNS 记录。

## ECS（Ubuntu 24.04）

### 安装 Mosquitto 和编译依赖

```bash
apt-get update
apt-get install -y mosquitto mosquitto-clients libmosquitto-dev libcjson-dev \
  libssl-dev build-essential golang-go
```

Ubuntu 的 `libmosquitto-dev` 不带 Broker 插件头文件，需要从同版本源码补：

```bash
cd /tmp
curl -sL -o mosquitto-2.0.18.tar.gz \
  https://mosquitto.org/files/source/mosquitto-2.0.18.tar.gz
tar xzf mosquitto-2.0.18.tar.gz
cp mosquitto-2.0.18/include/*.h /usr/include/
```

### 编译 go-auth 插件

插件要求 Go >= 1.24；Ubuntu 的 Go 1.22 可以用 `GOTOOLCHAIN` 自动拉新工具链：

```bash
export GOPROXY=https://goproxy.cn,direct
export GOTOOLCHAIN=go1.26.11   # 或任意 >=1.24 的版本
export GOFLAGS=-mod=mod

cd /tmp
go mod download -json \
  github.com/iegomez/mosquitto-go-auth@v0.0.0-20250608182151-20ee9310fe3a \
  > /tmp/plugin.json
DIR=$(python3 -c "import json;print(json.load(open('/tmp/plugin.json'))['Dir'])")
cp -R "$DIR" /tmp/mosquitto-go-auth-build
cd /tmp/mosquitto-go-auth-build
make CFLAGS="-I/usr/include -fPIC"
strip go-auth.so
install -m 0755 go-auth.so /usr/lib/mosquitto/go-auth.so
```

实际使用版本：

```text
module: github.com/iegomez/mosquitto-go-auth
version: v0.0.0-20250608182151-20ee9310fe3a
Mosquitto: 2.0.18 (Ubuntu)
Go: 1.26.11 (toolchain)
```

上游已归档；升级 Mosquitto 后必须重新编译并跑一次鉴权测试。

### 配置

把 `deploy/mosquitto/mosquitto.conf` 覆盖到 `/etc/mosquitto/mosquitto.conf`（先备份），
然后：

```bash
systemctl enable --now mosquitto
systemctl restart mosquitto
```

鉴权回调指向 `127.0.0.1:18084`，也就是 frp 到 Mac token 服务的隧道。
用 test/test 连接应该得到 `Connection Refused: not authorised`。

### nginx

把 `deploy/nginx/mqtt.conf` 放进 `agent-sync.yirenzaowusuo.cn` 的 server 块，
`/mqtt` 反代到 `127.0.0.1:8083`，并给 `/mqtt/auth`、`/mqtt/acl` 加 404：

```bash
nginx -t && systemctl reload nginx
```

### frps

`/etc/frp/frps.toml` 的 `allowPorts` 需要包含 18084：

```toml
{ start = 18084, end = 18084 }
```

## Mac mini

官方 Broker 迁走后，Mac 只需要：

1. token 服务（launchd `com.agentsync.auth`，127.0.0.1:8787）；
2. frpc 的 `auth-api` 隧道（8787 → ECS 18084）；
3. 其他原有隧道（xlkd-web、object-store）。

`~/.frpc.toml`：

```toml
transport.tls.enable = true

[[proxies]]
name = "auth-api"
type = "tcp"
localIP = "127.0.0.1"
localPort = 8787
remotePort = 18084
```

重启 frpc：

```bash
launchctl kickstart -k gui/$(id -u)/com.agentsync.frpc
```

**2026-11-16 已删除**：Mac 上的 Mosquitto、它的 LaunchAgent、`mqtt-ws` frp 隧道。
删除前备份在 `~/agent-sync-mosquitto-backup-20261116-225235.tar.gz`（含配置、日志、插件）。

## 验证

1. ECS 本机 WebSocket 握手：

```bash
curl -s -i -N --max-time 3 \
  -H "Connection: Upgrade" -H "Upgrade: websocket" \
  -H "Sec-WebSocket-Version: 13" \
  -H "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==" \
  -H "Sec-WebSocket-Protocol: mqtt" http://127.0.0.1:8083/mqtt
# 预期：101 Switching Protocols
```

2. 鉴权回调（ECS → frp → Mac）：

```bash
curl -s -o /dev/null -w '%{http_code}\n' -X POST \
  -d 'username=test.test&password=test&clientid=test' \
  http://127.0.0.1:18084/mqtt/auth
# 预期：403（假账号）
```

3. 公网握手：

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

必须带 `Sec-WebSocket-Protocol: mqtt`，并用 HTTP/1.1；Mosquitto 不接受 HTTP/2 直连的 WebSocket 升级。

4. 自建模式联调使用独立实例，不要用官方 go-auth 实例：

```bash
docker run -d --name mqtt-selfhost -p 1883:1883 \
  -v "$PWD/deploy/mosquitto/selfhost.conf:/mosquitto/config/mosquitto.conf" \
  eclipse-mosquitto:2
```

## 日志

| 进程 | 位置 |
|---|---|
| ECS Mosquitto | `/var/log/mosquitto/mosquitto.log` |
| ECS nginx | `/var/log/nginx/{access,error}.log` |
| Mac frpc | `~/Library/Logs/agentsync-frpc.log`（按 LaunchAgent 实际配置） |
| Mac token 服务 | `~/Library/Logs/agent-sync-auth.log` |
| 电脑端 host | `%APPDATA%\AgentSync\remote\host.log`（Windows） |

## 回滚到 Mac Broker

1. 从备份恢复：

```bash
sudo tar xzf ~/agent-sync-mosquitto-backup-20261116-225235.tar.gz -C /
brew install mosquitto
brew services start mosquitto
```

2. `~/.frpc.toml` 加回 `mqtt-ws`（8083 → 18083），重启 frpc。
3. ECS nginx 的 `/mqtt` 改回 `127.0.0.1:18083`，`nginx -t && systemctl reload nginx`。

## 验收记录

### 2026-11-14（Mac Broker）

- Mosquitto 2.1.2 + `mosquitto-go-auth` 编译并启动，未授权连接被拒绝。
- ECS nginx `/mqtt`、frp 18083、公网 `wss://agent-sync.yirenzaowusuo.cn/mqtt` 握手返回 101。
- 自建模式：Windows host 连接局域网 Mosquitto，`u/local/h/{hostId}/status` 收到 `{"online":true}`。
- 手机协议模拟（Node + mqtt.js）：配对 → 电脑批准 → ready → echo → 附件上传全部通过。

### 2026-11-16（迁移到 ECS）

- ECS 2.0.18 + go-auth 插件启动，WebSocket 8083 握手 101，假账号鉴权回调 403。
- nginx `/mqtt` 改为本机 8083，`/mqtt/auth`、`/mqtt/acl` 对外返回 404。
- 公网 CONNECT→CONNACK（含鉴权）实测 **61–78ms**（迁移前 116–209ms）。
- Windows host 与 Android 手机自动重连到新 Broker；项目/会话列表正常加载。
- Mac 上的 Mosquitto、LaunchAgent 和 `mqtt-ws` 隧道已删除，仅保留 token 服务与 `auth-api` 隧道。
- 已知问题：手机打开会话时历史记录为空；在旧 Mac Broker 上同样复现，**不是迁移导致的**，另行处理。
