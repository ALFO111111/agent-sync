// 验证主机二维码深链格式与 App 解析一致（base64url、无填充）。
import { parseDeepLink } from '../src/lib/protocol.js'

const invitation = {
  v: 1,
  url: 'wss://relay.example.com/ws',
  route: 'a'.repeat(64),
  credential: 'b'.repeat(64),
  id: 'c'.repeat(64),
  secret: 'd'.repeat(64),
  hostId: 'h',
  hostName: 'Mac',
  expires: '2026-11-11T12:00:00Z',
}
const encoded = Buffer.from(JSON.stringify(invitation), 'utf8').toString('base64url')
const link = `agent-sync://pair?d=${encoded}`
const parsed = parseDeepLink(link)
if (!parsed || parsed.id !== invitation.id || parsed.url !== invitation.url) {
  console.error('deep link parse failed', parsed)
  process.exit(1)
}
if (parseDeepLink('agent-sync://other?d=x') !== null) {
  console.error('wrong host accepted')
  process.exit(1)
}
console.log('DEEPLINK PASS')
