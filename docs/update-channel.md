# 更新通道

## 客户端顺序

1. **自有签名清单**：`GET https://agent-sync.yirenzaowusuo.cn/api/update/latest?platform=windows/amd64`
2. 清单用内置 Ed25519 公钥验签；版本、URL、SHA256 任一被改都会失败。
3. 验签通过后按清单里的 `url` 下载，先核对清单里的 SHA256，再替换程序。
4. 清单不可用（未部署、404、断网、验签失败）才回退 GitHub Releases API。回退路径仍校验 `SHA256SUMS.txt`。

当前内置公钥在 `internal/update/manifest.go` 的 `manifestPubKey`。私钥在开发机仓库外：

```
D:\developProject\.agent-sync-signing\update-ed25519.key
```

**私钥必须离线备份，不要提交、不要上传网盘。** 丢失私钥后，已发布的客户端无法信任新清单，只能随下一次客户端发版更换公钥。

## 生成清单

```bash
go run ./scripts/update-manifest -genkey D:\secure\update.key   # 只需一次
go run ./scripts/update-manifest \
  -version 0.7.1 \
  -platform windows/amd64 \
  -file dist/agent-sync.exe \
  -url https://dl.example.com/agent-sync.exe \
  -key D:\secure\update.key \
  -out latest-windows-amd64.json
```

生成器会输出文件 SHA256。清单在发布前放在 CDN/OSS，服务端按 `platform` 返回对应文件：

- `latest-windows-amd64.json`
- `latest-darwin-arm64.json`

## GitHub Actions

- `UPDATE_SIGNING_KEY`：私钥文件内容的 base64（32 字节 seed）。
- `UPDATE_ASSET_BASE`：安装包的公开下载前缀；不填则用 GitHub Release 的下载地址。
- 没配置 `UPDATE_SIGNING_KEY` 时，发布流程跳过清单生成并打印警告，不会发布未签名清单。

发布后，把生成的 `latest-*.json` 同步到 OSS/CDN，并让 `https://agent-sync.yirenzaowusuo.cn/api/update/latest?platform=...` 返回对应 JSON（`Content-Type: application/json`）。

## 回滚

- 客户端只接受比当前版本更新的清单，不能直接“降级”。
- 回滚做法：发布一个版本号更高的修复版，或让清单指向旧版本的**新构建**并把版本号抬高。
- 清单签名错误时客户端会回退 GitHub；如果两边都不可用，只提示检查失败，不会自动下载任何东西。

## 本地验证

`go test ./internal/update` 覆盖：正确签名通过、篡改版本失败、篡改 SHA256 失败、清单失败时回退 GitHub。
