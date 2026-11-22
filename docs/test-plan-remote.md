# 手机遥控 / 官方模式 · 真机验收清单

> 2026-11-21 审计加固后的手工验收。每次发版按本表执行；结果写在本文件末尾的“记录”里。

## 环境

- 桌面：Windows 11，构建 `build\bin\agent-sync.exe`（Wails dev build）
- 手机：Huawei ALN-AL00，`adb -s <device-serial>`，包名 `com.alfo.agentsync`
- 服务端：`agent-sync.yirenzaowusuo.cn`（ECS `<ECS_PUBLIC_IP>`）
- 账号：`<test-account>`（官方模式）

## 一、服务端

1. `curl -s -o /dev/null -w "%{http_code}" https://agent-sync.yirenzaowusuo.cn/api/v1/me` → `401`
2. `curl -s -o /dev/null -w "%{http_code}" -X POST https://agent-sync.yirenzaowusuo.cn/api/mqtt/superuser -d 'username=x'` → `404`
3. `curl -s -o /dev/null -w "%{http_code}" -X POST https://agent-sync.yirenzaowusuo.cn/api/internal/quota -d '{}'` → `404`
4. 用临时账号连 Broker，订阅 `u/#` → SUBACK `128`（拒绝）。
5. 删除临时账号/禁用。

## 二、桌面端

1. 启动 `build\bin\agent-sync.exe`，确认版本和“手机遥控”页状态。
2. 官方模式已登录（账号 `<test-account>`）；点“启动主机”，状态变为“中转已连接”。
3. 点“邀请”，出现二维码；记下 hostId 和邀请有效期。
4. 关闭桌面窗口（Windows 最小化到托盘），确认 host 仍在运行。

## 三、手机配对

1. `adb install -r <signed apk>`；打开 App。
2. 扫码 → 电脑出现待批准 → 电脑批准。
3. 手机显示“已连接”；`adb logcat` 中 MQTT username 应为 `<accountID>.<phoneDID>`。
4. 打开一个会话，发“你好”，收到回复。
5. 发一张图片（<5MB），确认发送成功；发 >5MB 图片，确认被拒绝并提示。

## 四、后台与长任务

1. 跑一个 >5 分钟的任务；期间不操作手机。
2. 按 Home 去微信 5 分钟，再回来：应自动重连，历史不重复，任务仍在跑。
3. 锁屏 2 分钟：通知栏应有“回合完成/待批准”通知（取决于系统省电策略）；回来能继续。
4. 长任务期间不碰屏幕：聊天应一直跟随底部；上滑后停止跟随；点“回到底部”恢复。
5. 返回键：应只离开聊天，电脑上的任务继续；再进入能接回。
6. “结束会话”：二次确认后电脑端进程停止。

## 五、撤销设备

1. 在用户中心“设备”页撤销这台手机。
2. 手机应在数秒内断开，显示“设备已撤销/凭据失效”，不再无限重连。
3. 手机重新连接应被拒绝（403/未授权）。
4. 另一台设备（如有）不受影响。
5. 重新扫码配对后恢复。

## 六、付费闭环（测试账号）

1. 免费账号：设备页显示 `/1`、`/1`；创建第 2 台电脑被拒。
2. 创建订单 → 二维码出现 → 15 分钟不支付 → 页面显示“已过期”，可“重新生成订单”。
3. 用签名回调模拟支付成功（不真实扣款）→ Pro 生效。
4. 到期/降级后超额设备暂停；续费后设备恢复或可“设为当前”。
5. 退款：订单变 `refunded`，账号回到免费，超额设备暂停。
6. 找回密码：用恢复码重置，旧 session 失效。

## 七、记录

| 日期 | 版本 | 执行人 | 结果 | 备注 |
|---|---|---|---|---|
| 2026-11-21 | 0.7.0-dev |  |  |  |
