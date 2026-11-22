// 验证主机二维码深链格式与 App 解析一致（base64url、无填充）。
import { parseDeepLink, parseInvitation } from '../src/lib/protocol.js'

const invitation = {
  v: 3,
  url: 'wss://relay.example.com/mqtt',
  uid: 'a'.repeat(32),
  id: 'c'.repeat(64),
  secret: 'd'.repeat(64),
  contentKey: Buffer.alloc(32, 7).toString('base64'),
  hostId: 'a'.repeat(32),
  hostName: 'Mac',
  username: 'alice',
  mode: 'official',
  expires: '2026-11-11T12:00:00Z',
}
const encoded = Buffer.from(JSON.stringify(invitation), 'utf8').toString('base64url')
const link = `agent-sync://pair?d=${encoded}`
const parsed = parseDeepLink(link)
if (!parsed || parsed.id !== invitation.id || parsed.url !== invitation.url || parsed.username !== 'alice') {
  console.error('deep link parse failed', parsed)
  process.exit(1)
}
if (parseDeepLink('agent-sync://other?d=x') !== null) {
  console.error('wrong host accepted')
  process.exit(1)
}
// 旧 v1、缺字段、非法 URL、超长名称都必须拒绝。
const bad = [
  { ...invitation, v: 1 },
  { ...invitation, url: 'ws://relay.example.com/mqtt' },
  { ...invitation, contentKey: 'short' },
  { ...invitation, hostName: 'x'.repeat(200) },
]
for (const inv of bad) {
  const raw = Buffer.from(JSON.stringify(inv), 'utf8').toString('base64url')
  if (parseDeepLink(`agent-sync://pair?d=${raw}`) !== null) {
    console.error('invalid invitation accepted', inv)
    process.exit(1)
  }
}
if (parseInvitation(JSON.stringify(invitation))?.id !== invitation.id) {
  console.error('raw JSON invitation rejected')
  process.exit(1)
}
console.log('DEEPLINK PASS')
