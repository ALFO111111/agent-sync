# 自建 Mosquitto（只部署一个服务）

自建模式不需要账号系统，不需要 `agent-sync-auth`，也不需要数据库。你只需要一个 **Mosquitto 2.x**，用 Mosquitto 自带的用户名密码文件认证。

客户端的使用方式和官网登录一样：填 Broker 地址、用户名、密码，点连接。

---

## 1. 原理

Mosquitto 原生支持 `password_file`：

```conf
allow_anonymous false
password_file /etc/mosquitto/passwd
```

用户用 `mosquitto_passwd` 命令维护。加一个用户：

```bash
mosquitto_passwd -c /etc/mosquitto/passwd alice
# 第一次用 -c 创建文件；以后再加用户不要加 -c，否则会清空
mosquitto_passwd /etc/mosquitto/passwd bob
```

改完不需要重启，Mosquitto 会自动重新加载密码文件。

> Mosquitto 原生没有注册网页、设备限制、套餐和 JWT。这些是官方托管才有的能力。自建就是“一个 Broker + 一组固定用户名密码”。

---

## 2. 安装 Mosquitto

### macOS

```bash
brew install mosquitto
```

配置文件：`/opt/homebrew/etc/mosquitto/mosquitto.conf`

### Debian / Ubuntu

```bash
sudo apt update && sudo apt install -y mosquitto mosquitto-clients
sudo systemctl enable --now mosquitto
```

配置文件：`/etc/mosquitto/mosquitto.conf`

### Docker

```bash
mkdir -p /opt/mosquitto/data /opt/mosquitto/log
# 先放好 /opt/mosquitto/mosquitto.conf 和 passwd，见下面
docker run -d --name mosquitto \
  -p 1883:1883 -p 8083:8083 \
  -v /opt/mosquitto/mosquitto.conf:/mosquitto/config/mosquitto.conf \
  -v /opt/mosquitto/passwd:/mosquitto/config/passwd \
  -v /opt/mosquitto/data:/mosquitto/data \
  -v /opt/mosquitto/log:/mosquitto/log \
  eclipse-mosquitto:2
```

密码文件用 Docker 里的命令生成：

```bash
docker run --rm -it -v /opt/mosquitto:/work eclipse-mosquitto:2 \
  mosquitto_passwd -c /work/passwd alice
```

### Windows

从 <https://mosquitto.org/download/> 安装。配置在
`C:\Program Files\mosquitto\mosquitto.conf`，密码文件用安装目录里的 `mosquitto_passwd.exe` 生成。

---

## 3. 配置

把下面写进 `mosquitto.conf`：

```conf
# 普通 TCP，可选
listener 1883 0.0.0.0
allow_anonymous false

# agent-sync 用的 WebSocket
listener 8083 0.0.0.0
protocol websockets
allow_anonymous false

password_file /etc/mosquitto/passwd

max_packet_size 262144
message_size_limit 262144
max_inflight_messages 20
max_queued_messages 100
max_queued_bytes 1048576

persistence true
persistence_location /var/lib/mosquitto/
```

macOS 把 `password_file` 改成 `/opt/homebrew/etc/mosquitto/passwd`，
`persistence_location` 改成 `/opt/homebrew/var/mosquitto/`。

生成用户：

```bash
# macOS
mosquitto_passwd -c /opt/homebrew/etc/mosquitto/passwd alice

# Linux
sudo mosquitto_passwd -c /etc/mosquitto/passwd alice
```

重启：

```bash
brew services restart mosquitto      # macOS
sudo systemctl restart mosquitto     # Linux
docker restart mosquitto             # Docker
```

---

## 4. 放行端口

- 局域网：放行 TCP 8083
- 云服务器：安全组放行 8083（建议配合白名单/TLS）
- 手机用 4G：`192.168.x.x` 不通，需要公网地址或 Tailscale 等虚拟内网

---

## 5. 验证

```bash
# 正确密码：能收到消息
mosquitto_sub -L 'ws://127.0.0.1:8083/mqtt' -u alice -P '你的密码' -t 'test/#' -v

# 另一个终端
mosquitto_pub -L 'ws://127.0.0.1:8083/mqtt' -u alice -P '你的密码' -t 'test/hello' -m hi

# 错误密码：应返回 not authorized
mosquitto_pub -L 'ws://127.0.0.1:8083/mqtt' -u alice -P wrong -t test -m hi
```

---

## 6. 在客户端填写

电脑端 → 手机遥控 → 编辑：

- 连接方式：**自建 Broker**
- Broker 地址：`ws://<这台机器的内网 IP>:8083/mqtt`
- 用户名：`alice`
- 密码：刚设置的密码

启动后点“邀请”生成二维码。**手机扫码或粘贴邀请内容即可连接，不需要在手机上填 Broker 地址和用户名密码**。

> [!WARNING]
> 自建二维码/邀请文本里包含这组 Broker 用户名密码，不要截图或转发。建议给 agent-sync 单独建一个 Mosquitto 用户，不要用管理员账号。

> 电脑和手机必须能访问同一个地址。手机在外面用 4G 时，需要公网地址（配 TLS）或虚拟内网。

---

## 7. 公网 + TLS（可选）

不要把匿名 Broker 暴露到公网。用 nginx 终止 TLS，转发到本机 8083：

```nginx
server {
    listen 443 ssl;
    server_name mqtt.example.com;

    ssl_certificate     /etc/letsencrypt/live/mqtt.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/mqtt.example.com/privkey.pem;

    location /mqtt {
        proxy_pass http://127.0.0.1:8083;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
        proxy_buffering off;
    }
}
```

客户端填 `wss://mqtt.example.com/mqtt`。

WebSocket 必须用 HTTP/1.1；Mosquitto 不接受 HTTP/2 直连的升级请求。真实 MQTT 客户端会自动带 `Sec-WebSocket-Protocol: mqtt`。

---

## 8. 安全清单

- [ ] `allow_anonymous false`
- [ ] 每个用户单独密码，不要共用
- [ ] 公网只开 443（WSS），不要直接暴露 1883
- [ ] 手机/电脑用 4G 时走 TLS
- [ ] 定期 `mosquitto_passwd` 改密码
- [ ] 不要把 Broker 放在没有任何访问控制的公网

---

## 交给 AI 的提示词

把下面整段复制给 Claude Code、Codex、ChatGPT 等助手，它会按你的机器情况帮你部署。

```text
你是一名熟悉 Mosquitto 和网络运维的工程师。请帮我在当前这台机器上部署一个
只使用 Mosquitto 自带 password_file 认证的 Broker，给 agent-sync 的「手机遥控」
自建模式使用。不要引入数据库、账号服务或 Docker Compose 之外的后台服务。

背景要求：
- agent-sync 客户端通过 WebSocket 连接，地址形如 ws://主机:8083/mqtt
- 只支持 Mosquitto 2.x
- 用户名密码用 mosquitto_passwd 维护，不使用匿名
- 手机和电脑可能在家里局域网，也可能通过 Tailscale/ZeroTier 访问
- 如果是公网访问，必须用 wss:// 和 TLS

请严格按下面流程做：

1. 先检查并告诉我：
   - 操作系统和 CPU 架构
   - 是否已经安装 mosquitto、版本是多少
   - 现有配置文件路径、是否正在运行
   - 8083 端口是否被占用
   信息不足时一次只问一个必要问题，不要猜。

2. 安装 Mosquitto（macOS 用 Homebrew；Debian/Ubuntu 用 apt；Windows 说明安装器；
   Docker 用 eclipse-mosquitto:2）。安装前备份原配置。

3. 生成密码文件（第一次用 -c，之后不要用 -c）：
   macOS: mosquitto_passwd -c /opt/homebrew/etc/mosquitto/passwd <用户名>
   Linux: sudo mosquitto_passwd -c /etc/mosquitto/passwd <用户名>
   不要让我把密码贴在对话里，用交互式输入。

4. 写配置，至少包含：
   listener 8083 0.0.0.0
   protocol websockets
   allow_anonymous false
   password_file <密码文件绝对路径>
   max_packet_size 262144
   message_size_limit 262144
   max_inflight_messages 20
   max_queued_messages 100
   max_queued_bytes 1048576
   persistence true
   保留 1883 普通 TCP 监听（可选，不需要可以去掉）。

5. 启动/重启服务，确认端口：
   macOS: brew services restart mosquitto
   Linux: systemctl restart mosquitto
   Docker: docker restart mosquitto
   然后用 ss -lntp 或 lsof -i :8083 确认监听。

6. 用 mosquitto_sub / mosquitto_pub 做 WebSocket 自测：
   mosquitto_sub -L 'ws://127.0.0.1:8083/mqtt' -u <用户名> -P <密码> -t 'test/#' -v
   mosquitto_pub -L 'ws://127.0.0.1:8083/mqtt' -u <用户名> -P <密码> -t 'test/hello' -m hi
   再用错误密码确认会被拒绝。把实际输出贴给我。

7. 告诉我：
   - 电脑应该填的 Broker 地址（用本机内网 IP，不要用 127.0.0.1）
   - 手机应该填的地址（局域网 IP 或 Tailscale IP）
   - 防火墙/安全组需要放行的端口和命令
   - 手机用 4G 访问的可选方案；公网必须加 TLS，给出 nginx 配置示例
   - 日志位置和回滚方法

安全约束：
- 不要把 8083 匿名暴露到公网
- 不要修改与本任务无关的服务
- 不要打印任何密码、token 或私钥
- 每一步执行前说明你要做什么；破坏性操作先征得我同意
```
