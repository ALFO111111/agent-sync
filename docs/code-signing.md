# 代码签名与公证

当前仓库**只准备了流程，没有签名证书**。没有证书时构建仍可发布，但 Windows SmartScreen、macOS Gatekeeper 会提示未知开发者。不要提交证书、私钥或“假签名”。

## Windows

需要：

- 受信任的代码签名证书（PFX），建议 EV 或 OV；
- 证书密码；
- 可选：时间戳服务（默认 DigiCert）。

配置 GitHub Actions secret：

| Secret | 内容 |
| --- | --- |
| `WINDOWS_CERT_BASE64` | PFX 文件的 base64 |
| `WINDOWS_CERT_PASSWORD` | PFX 密码 |

`package-desktop` 会调用 Windows SDK 的 `signtool.exe` 给 `agent-sync.exe` 签名。secret 为空时跳过并打印警告。

验证：

```powershell
Get-AuthenticodeSignature .\agent-sync.exe
# Status 应为 Valid
```

## macOS

需要：

- Apple Developer Program 账号；
- Developer ID Application 证书（P12）；
- App 专用密码或 App Store Connect API Key；
- Team ID。

配置 secret：

| Secret | 内容 |
| --- | --- |
| `MACOS_CERT_BASE64` | P12 的 base64 |
| `MACOS_CERT_PASSWORD` | P12 密码 |
| `MACOS_NOTARY_APPLE_ID` | Apple ID |
| `MACOS_NOTARY_PASSWORD` | App 专用密码 |
| `MACOS_NOTARY_TEAM_ID` | Team ID |

流程：导入临时钥匙串 → `codesign --options runtime` → `notarytool submit --wait` → `stapler staple`。缺少公证 secret 时会签名但不公证，并打印警告。

验证：

```bash
codesign --verify --deep --strict --verbose=2 agent-sync.app
spctl --assess --type execute --verbose agent-sync.app
xcrun stapler validate agent-sync.app
```

## 更新清单签名

更新清单使用独立的 Ed25519 密钥，和 Apple/Windows 证书无关。见 [update-channel.md](update-channel.md)。轮换清单公钥必须发新版本客户端。
