# 远程遥控 v1（正式中转 + 安卓端）

日期：2026-11-11。状态：可用原型；已通过公网端到端互操作验证；安卓 APK 已构建，待真机验收。

> v1 只使用**外层 TLS（wss）**，没有内层端到端加密。配对与设备身份由 Ed25519 签名校验；中转服务器（你自己的 ECS）理论上能看到消息内容。后续版本再补 E2EE（可复用阶段 A 的 TLS 实现，通过 gomobile 打包）。

## 结构

```text
手机 App（Capacitor + Vue）
   │ wss://agent-sync.yirenzaowusuo.cn/ws（443，Let's Encrypt）
   ▼
阿里云 ECS 47.80.63.39
   ├─ nginx：80（ACME/跳转）/ 443（TLS、WS 转发）
   ├─ frps：7000（控制线），远端端口只绑 127.0.0.1
   │   ├─ 18765 → Mac 的 agent-sync 中转
   │   └─ 18000 → Mac 的 xlkd 应用
   └─ certbot.timer：自动续期
   │ frp 隧道（Mac 主动连出）
   ▼
Mac mini
   ├─ relay：127.0.0.1:18765（纯转发，不解析内容）
   └─ host：agent-sync-host（设备白名单、配对批准、受控操作）
```

## 安全模型（v1）

- **传输**：手机到 nginx 使用公网证书（Let's Encrypt），TLS 1.2/1.3。
- **身份**：设备生成 Ed25519 密钥；配对时用邀请 secret 换一次人工批准；之后每次连接由服务器下发随机 nonce，设备签名 `agent-sync-v1|nonce|devicePub|inviteId`。
- **授权**：设备白名单在电脑端持久化；撤销立即断开连接且白名单标记持久化。
- **可见性**：中转只转发，但 nginx/中转/主机运营者能看到明文协议内容。**v1 不承诺对服务器保密。**
- **不在范围**：账号体系、多用户、中继端审计、抗 DoS、内层 E2EE。

## 协议（v1）

WS 二进制帧承载 UTF-8 JSON，单帧 ≤16 KiB；每连接 ≤20 条/秒；心跳 20 秒；75 秒无消息断开。

| 方向 | type | 字段 | 说明 |
|---|---|---|---|
| C→H | `hello` | devicePub, deviceName, inviteId?, inviteSecret? | 首次配对带邀请 |
| H→C | `challenge` | nonce, hostId, hostName | 32 字节随机 |
| C→H | `proof` | devicePub, inviteId, signature | Ed25519 签名 |
| H→C | `pending` | id | 等待电脑操作员批准 |
| H→C | `ready` | deviceId, hostId, hostName | 认证完成 |
| H→C | `denied` | error | 拒绝/过期/撤销 |
| 双向 | `ping` / `pong` | — | 20 秒心跳 |
| C→H | `echo` | id, data | 链路测试 |
| C→H | `device.list` | id | 设备列表 |
| C→H | `status` | id | 主机状态 |

中转控制协议（首包，text JSON）：`register`（执行端登记）、`connect`（控制端申请）、`attach`（执行端接入），回复 `ready` / `incoming` / `paired` / `heartbeat`。

## 主机守护

```sh
# 常驻（LaunchAgent 自动运行）
agent-sync-host run -relay ws://127.0.0.1:18765/ws \
  -public wss://agent-sync.yirenzaowusuo.cn/ws \
  -name "Mac mini" -state "$HOME/Library/Application Support/AgentSync/remote"

# 生成配对邀请（终端显示二维码 + JSON）
agent-sync-host invite

# 查看/批准/拒绝（批准必须由电脑操作员执行）
agent-sync-host pending
agent-sync-host approve <requestID>
agent-sync-host deny <requestID>

# 设备管理
agent-sync-host devices
agent-sync-host revoke <deviceID|指纹>
agent-sync-host rename <deviceID|指纹> <名称>
agent-sync-host status
```

本地控制通道只监听 `127.0.0.1`，端口与随机令牌写在状态目录的 `control` 文件（0600）。

持久化：

- 状态文件 `host.json`：AES-256-GCM 加密；密钥优先存 macOS 钥匙串（`security` CLI），不可用时退回 0600 的 `state.key` 并在日志警告。
- 设备白名单、主机 ID、中转 route/credential 均在其中。

## 安卓端

- 代码：`mobile/`（Vue 3 + Capacitor 7），`android/` 为生成的原生工程。
- 密钥：Ed25519 私钥经 `SecureStore` 插件用 Android Keystore 的 AES-GCM 加密保存；网页调试模式退回 localStorage 并提示不安全。
- 配对：电脑终端显示二维码（深链 `agent-sync://pair?d=...`），手机系统相机扫码直接打开 App；也可以粘贴 JSON。
- 页面：连接状态、配对、设备列表、回显测试、日志。
- 构建：
  ```sh
  cd mobile
  npm install
  npm run build
  npx cap sync android
  cd android && ./gradlew assembleDebug
  # 产物：android/app/build/outputs/apk/debug/app-debug.apk
  ```
  Android SDK 装在 `D:\developTools\android-sdk`（platform-tools、android-35、build-tools 35.0.0）。Gradle 需要代理时设置 `GRADLE_OPTS` 的 `systemProp.https.proxyHost/Port`。

## 已验收

- Go 单元测试：签名/挑战、邀请过期与重放、拒绝后重试、并发批准、撤销后重连拒绝、设备持久化、协议限额、中转 `/metrics`。
- 公网互操作（Windows 上的 Node 客户端 ↔ Mac 主机，经 ECS/nginx/frp）：
  配对 → echo → device.list → 同身份重连 → 主机撤销 → 旧连接断开且重连被拒。
- 中转 `/healthz` 正常，`/metrics` 在本机可查、公网已返回 404。
- 证书自动续期 dry-run 成功。

## 待验收/未完成

- 安卓真机安装 APK：扫码配对、Keystore 存储、撤销、切网重连。
- Mac 重启后 LaunchAgent 自动恢复（host/relay/frpc 均为 KeepAlive）。
- 内层 E2EE（后续）、真实会话/文件/命令执行（下一阶段）。
- 服务器与客户端日志脱敏检查、异常断电/网络抖动的长稳测试。

## 2026-11-11 修复：App 的 Origin

Capacitor WebView 发起 WebSocket 时会带 `Origin: https://localhost`，而中转默认拒绝所有带 Origin 的请求（防浏览器跨站）。已增加 `-allow-origin` 配置，生产中转以 `-allow-origin https://localhost,capacitor://localhost` 启动；未配置时行为不变。公网验证：带该 Origin 的握手返回 101。

## 2026-11-11 真机验收

- Android 手机完成配对（扫码/粘贴均可），回显、设备列表、在线状态正常。
- 遇到的问题与修复：
  - 手机 DNS 缓存仍指向旧 Cloudflare 记录 → 切网络/私人 DNS 后恢复（一次性缓存问题）。
  - Capacitor WebView 带 `Origin: https://localhost` 被中转拒绝 → 新增 `-allow-origin` 白名单。
  - 邀请 5 分钟过短导致 `denied` → 调整为 10 分钟。
- 两台互操作测试设备已撤销；手机设备保留。

## 部署位置修正（2026-11-11）

- **Mac mini 只做中转**（relay + frpc）；主机组件（agent-sync-host）已从 Mac 停用并禁用。
- **主机组件运行在用户的 Windows 工作机**（本机）：那里才有 Claude Code、项目和登录状态。
- 本机访问公网必须走代理（`HTTPS_PROXY=http://127.0.0.1:7890`）；Windows 启动脚本 `build/bin/agent-sync-host-startup.vbs` 已复制到用户启动目录，登录后隐藏启动。
- Claude CLI 在 Windows 是 `D:\developTools\nodejs\claude.cmd`；会话管理器会自动用 `cmd.exe /c` 启动 .cmd。
- 本机会话冒烟通过：列出 11 个项目，新建会话，发送“只回复 OK”，收到流式 `text_delta` 与 `result: OK`。
- 手机需要重新配对到本机主机（旧配对属于 Mac 主机身份）。

## v2.1 权限模式（2026-11-11）

- 会话新增权限模式：`readonly`（dontAsk + Read/Grep/Glob/WebSearch/WebFetch）、`edit`（acceptEdits）、`full`（bypassPermissions）。
- 手机聊天页可切换；切换时电脑端用同一 Claude 会话 ID 重启进程（`--resume`），上下文保留，界面提示“会话已重启”。
- “完全”模式需二次确认，允许 Claude 执行任意命令和修改文件。
- 实测：只读模式写文件被拒（`permission_denied`）；切到可编辑后同一对话成功写文件。
- **逐操作手机审批暂未实现**：本机 Claude Code 2.1.237 在 `-p` 模式下 PreToolUse/PermissionRequest hook 不触发（项目/`--settings` 均验证失败），无法通过 CLI hook 接审批。后续需要用 Agent SDK（TypeScript）的 canUseTool 或升级 CLI 后重试。
- 会话冲突：同一会话不要同时在手机和电脑端打开；手机“结束会话”会立即释放（进程结束），否则无人观看 3 分钟后自动回收。

## v2.1 修正：逐操作审批与在线切换（2026-11-11 晚）

- 通过官方 SDK 源码确认了 `--permission-prompt-tool stdio` + `initialize` 控制请求的用法：
  - 启动时发 `{"type":"control_request","request":{"subtype":"initialize"}}`；
  - CLI 遇到需要批准的操作会发 `can_use_tool`（tool_name/input/tool_use_id/description）；
  - 主机回 `control_response`（`response.response.behavior = allow|deny`）；
  - 手机断线或 120 秒未回应 → 按拒绝处理。
- 模式切换改为**运行中切换**：主机发 `set_permission_mode` 控制请求，成功时不重启；不支持时才回退到“结束进程 + 同会话 ID resume”。
- 模式列表与 Claude Code 对齐：`dontAsk`（只读）、`default`（手动）、`acceptEdits`（自动编辑）、`plan`（计划）、`auto`（自动）、`bypassPermissions`（完全）。
- 实测：
  - 手动模式下写文件 → 手机收到审批卡片 → 允许 → 文件创建；
  - 运行中切到只读（同一 key，无重启）→ 同一对话写文件被拒。
- 注意：手动/自动/计划模式下，手机没连、没人批准时，需要批准的操作会超时被拒（不会默认放行）。

## 会话生命周期（2026-11-12）

- 打开会话时先结束上一个会话（避免占满名额）；名额上限 4。
- 手机点“返回 / 断开 / 清除配对”时，先请求电脑端结束会话，再断开。
- 切到后台不算结束；App 被杀导致连接断开后，订阅自动取消，电脑端 60 秒后回收进程。
- 新增本机命令：`agent-sync-host sessions`（列出活动会话）、`agent-sync-host session-stop <key|会话ID>`。

## 2026-11-12 模拟器端到端联调（Android 35，x86_64）

在 Android 模拟器中完成真实 UI 流程，发现并修复：

1. **冷启动深链失效**：App 未打开时扫码，`appUrlOpen` 在 JS 监听注册前触发；同时 WebView 把 `agent-sync://pair?...` 的 hostname 解析为空。修复：启动时读取 `App.getLaunchUrl()`；解析只校验 scheme 和 `d` 参数。
2. **权限代码写到了 `</template>` 之后**（追加编辑失误），导致模式按钮不渲染、切换函数不存在。修复：移回 `<script setup>`。
3. **原生 `<select>` 在这台 WebView 上只改值、不派发 change 事件**。修复：权限改为可点按钮组（chips），不再依赖原生下拉。
4. **审批“允许”按钮守卫写错**（`null !== undefined` 恒真），点击后直接返回。修复为 `if (!sessionKey.value || item.decided) return`。
5. 会话生命周期：开新会话先结束上一个；返回/断开/清除配对先结束会话；App 被杀后连接断开，电脑端 60 秒回收；新增 `sessions` / `session-stop` 命令。

模拟器实测结果：

```text
新建会话 → 点“手动” → 日志“权限模式切换为 手动”（同一会话，无重启）
→ 发送“创建 emu-test.txt” → 弹出“需要批准 / Write” → 点允许
→ 电脑端文件创建成功 → 回复 DONE → 本轮结束
→ 点“结束会话” → 主机无活动会话
```

APK 与网页已同步修复；模拟器与测试会话已关闭，仅保留正式主机服务。

## 2026-11-12 自动重连与会话接回

- 手机 WebSocket 断开后自动退避重连（1s 起，最多 6 次）；重连成功后调用 `session.active`，按 Claude 会话 ID 找到仍在运行的会话，用上次的事件序号 `since` 重新订阅（不重复回放），界面提示“已接回原会话”。
- 电脑端在手机断开后保留会话 60 秒，超过才回收；若断开超过 60 秒，重连后提示“原会话已结束”。
- 模拟器实测：停中转 12 秒再恢复 → 日志“自动重连成功”“已接回原会话” → 同一会话继续对话成功（第二轮 OK2）。

## Codex 接入现状（2026-11-12）

- 本机 Codex CLI 0.163.0，`codex exec --json` 可用：事件为 JSONL（thread.started / turn.started / item.completed / error / turn.failed），支持 `-C` 工作目录、`-s read-only|workspace-write|danger-full-access`、`codex exec resume <id>`。
- **阻塞**：本机 Codex 配置指向自定义供应商 `https://code.ppchat.vip/v1/responses`，token 返回 401 Invalid token，无法完成真实对话。需要先修复 Codex 登录/供应商配置，再接入。
- 计划：Codex 先做“沙箱模式”（只读/可写/完全）而非逐操作审批；会话列表读取 ~/.codex/sessions，恢复用 `codex exec resume`。

## 2026-11-12 Codex 接入（沙箱版本）

- Codex 用 `codex exec --json`（每轮一个进程），支持 `resume <threadId>`；主机新增 CodexManager：会话文件扫描 `~/.codex/sessions`，事件映射为统一的 text/tool/tool_result/notice/result。
- 权限映射到 Codex 沙箱：只读/计划 → read-only；手动/自动/自动编辑 → workspace-write；完全 → danger-full-access。Codex 暂无逐操作审批（`codex exec` 非交互），界面已提示。
- 协议新增 `tool` 字段（claude/codex），手机会话页可切换工具；Codex 会话同样支持自动重连与接回。
- 验证：脚本测试新会话/recover（OK、OK2）；模拟器 UI 切换 Codex → 加载项目 → 新建会话 → 发送“只回复 OK” → 显示 Codex 的 OK。
- 本机 Codex 供应商为自定义服务（CC Switch/DeepSeek），事件与恢复均正常。

## 2026-11-12 手机界面完善

- 单页改为底部标签：连接（状态/设备/日志）、会话（工具/权限/项目/聊天）、设置（主题、配对信息、断开/清除配对、关于）。
- 新增手动主题（跟随系统/浅色/深色），保存在本机 WebView localStorage；CSS 增加 data-theme 覆盖。
- 聊天自动滚动到底部；助手气泡标签跟随当前工具（Claude Code / Codex）。
- 首次启动连接失败会进入自动重试（不再停在错误页）；连接成功后自动接回仍在运行的会话。
- 模拟器验证：三个标签切换、深色主题、会话新建与对话（Claude Code 回复 OK）均正常。

## 2026-11-12 手机端文件浏览 / diff

- 电脑端新增只读文件接口（`cmd/agent-sync-host/files.go`，实现 `remotev1.FileSink`）：
  - `fs.list`（目录列表，目录优先、最多 1000 条）
  - `fs.read`（单文件最多 256KB，二进制检测）
  - `git.status`（porcelain 解析）
  - `git.diff`（对比 HEAD；未跟踪文件按新增展示）
  - 所有路径限制在授权项目目录内，拒绝绝对路径、`..` 和符号链接逃逸。
- 手机会话页/聊天页新增“浏览文件”：目录导航（上一级/项目根）、文件查看、改动列表、单文件 diff。
- 修复长 diff 行把整页撑宽的问题（卡片 `min-width:0`、pre 内部横向滚动、头部按钮换行）。
- 验证：接口脚本（列表/读取/go.mod diff/路径逃逸拦截）；模拟器 UI 浏览项目根、打开 go.mod、查看 13 项改动与 diff。

## 2026-11-12 桌面端集成（“手机遥控”页）

- 桌面端新增“手机遥控”页（侧边栏“通用”分组）：主机状态、启动/停止、扫码配对（二维码 + 可复制的邀请原文）、待批准设备、已配对设备（重命名/撤销）、运行中会话（结束）、连接设置。
- 后端只做薄包装：`internal/service/remote.go` 通过本机控制通道操作 `agent-sync-host`；设置存在 `config.json`（中转地址、公网地址、电脑名、claude 路径、主机组件路径、代理）。
- 本轮修复：
  - 从桌面启动主机时会弹黑色控制台窗口 → Windows 用 `CREATE_NO_WINDOW` 隐藏。
  - 桌面进程通常没有终端里的代理环境 → 启动主机时依次使用：配置的代理、当前环境变量、本机 7890/7897 上正在监听的代理；否则主机连不上公网中转。
  - 从没连接过的设备不再显示成“739931 天前”（Go 零值时间按“从未”处理）。
  - 只有中转地址/公网地址/代理真的改了才重启主机，改名字不会打断手机连接。
- 验证（沙箱实例，`AGENT_SYNC_HOME` 指向临时目录）：页面读取真实主机状态与设备列表、生成配对二维码、停止/启动主机；重启后日志显示“主机守护已启动”，且没有控制台窗口；保存设置显示“已保存”且不重启。

## 2026-11-12 桌面页端到端验收（模拟手机 + 真实公网中转）

用临时协议客户端（走真实 `wss://agent-sync.yirenzaowusuo.cn/ws`）从桌面页完成第二轮验收：

- 配对请求实时出现在“待批准的设备”（3 秒轮询），点“允许”后客户端拿到 `ready`；
- 新建只读会话成功，桌面页显示工具、目录、`权限：只读 · 1 个观看者`（CLI 内部模式名 `dontAsk` 已映射为“只读”）；
- 桌面点“结束”→ 客户端订阅断开、主机无活动会话；
- 设备重命名在桌面生效，主机设备列表同步更新；
- 点“撤销”→ 系统确认框 → 确认后手机连接立即断开，设备显示“已撤销”且不再显示重命名/撤销按钮；
- 停止主机后清空中转地址再点启动 → 提示“先填写下面的中转地址和公网地址，再启动主机”；页面重新进入后配置仍在，可正常再启动。
- 顺带修复：Windows 路径比较区分大小写，会话索引里的盘符是小写（`d:\...`），手机/客户端发大写盘符时合法项目会被拒绝；`cwdAllowed` 改为不区分大小写，并加了单测。

## 2026-11-12 内层端到端加密（长期共享密钥）

- 电脑端首次启用手机遥控时随机生成 32 字节共享密钥，保存在 `host.json`（本来就用系统钥匙串/`state.key` 加密）；配对二维码/邀请 JSON 里带上它，手机存进 SecureStore。
- 连接建立后所有应用帧都加密：外层 `{"v":1,"type":"enc","data":...}`，`data` 是 `nonce(12) || AES-256-GCM 密文 || tag`；`hello/proof/ready/会话/文件/审批` 全部在密文里。
- nonce = 8 字节随机连接前缀 + 4 字节递增计数器；接收方校验前缀和严格递增的序号，重放、乱序、跨连接混用都会被断开。AAD 区分手机→电脑和电脑→手机，防止反射。
- 邀请协议升到 v2，必须带 `contentKey`；旧版 App 会被拒绝，新版 App 也不接受旧邀请。邀请有效期统一为 10 分钟。
- 取舍（用户确认）：**没有前向保密**；密钥泄露后，服务器保存的历史流量可以被解开。撤销设备不会让它忘记密钥，所以桌面页提供“更换加密密钥”：重新生成密钥、清空旧邀请、断开全部手机，其他手机需要重新扫码。
- 大文件浏览改为分块：每次最多约 4 KiB，手机自动循环，总量上限 512 KiB；`git.diff` 单帧上限 8 KiB。
- 升级顺序：电脑端 exe → 手机 APK → 重新扫码配对。中转（Mac mini 的 relay）不需要改。
- 实测：公网中转下用手机端真实协议代码完成配对、项目列表、新建只读会话、流式回复 `OK`；17,853 字节的 `go.sum` 分块读回完全一致；换密钥后旧密钥 1.1 秒内被断开，重新扫码配对恢复；安卓模拟器安装新 APK 后加密配对成功并列出 13 个项目。

## 2026-11-12 手机端首页与电脑设置（只读）

- 协议新增只读接口 `data.overview`：电脑名、同步设备名、系统、语言、数据目录、项目/会话数、上次同步与统计、每个工具的安装/项目/会话数、同步内容开关明细。
- 电脑端由 `cmd/agent-sync-host/data.go` 直接读取 `config`、`state` 和会话索引；不返回密钥、口令、云端凭据，也不读取备份内容。
- 手机端底部增加“首页”标签：电脑概况、工具卡片、同步状态、同步内容开关；现有“设置”页增加只读的“电脑”卡片。没配对时默认停在“连接”页，配对成功后自动跳到“首页”。
- 实测：公网加密链路下 `data.overview` 返回真实数据；安卓模拟器首页显示电脑名“Windows 开发机”、19 个项目/158 个会话、Claude Code 13 项目/136 会话、Codex 6 项目/22 会话，设置页显示数据目录和语言。

## 2026-11-12 多设备选择

- 手机本地从“只存一台电脑”改为电脑列表（`ags:pairings`），每台独立保存中转地址、路由、凭据和加密密钥；旧的单条 `pairing` 会自动迁移，不需要用户操作。
- 连接页新增“电脑”列表：标出当前电脑，其他电脑可直接“连接”切换，也可以删除；同一时间只连一台。
- 切换电脑会先结束当前电脑上的会话再重连；新扫码配对按 `hostId` 更新同一台电脑或新增一台，不会覆盖其他电脑。
- 首页、会话、设置和自动重连都跟随当前选中的电脑；没有配对时默认停在“连接”页。
- 电脑端协议不需要改动。
- 实测：同一公网中转下启动两台主机（`Windows 开发机`、临时 `测试电脑 B`），模拟器先后配对两台并来回切换，首页分别显示两台电脑的数据；删除临时电脑后列表恢复为一台，当前连接不受影响。
