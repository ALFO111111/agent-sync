// 会话协议冒烟测试：配对 → 列项目 → 启动会话 → 订阅 → 发送一句话。
// 用法：INTEROP_HOST_CMD='ssh ... agent-sync-host {args} ...' \
//       NODE_USE_ENV_PROXY=1 node session-smoke.mjs <invitation.json> <key.json> <cwd>
import { readFileSync, writeFileSync, existsSync } from 'node:fs'
import { execSync } from 'node:child_process'
import { initCrypto, newDevice, pair } from '../src/lib/protocol.js'

const [invitePath, keyPath, cwd] = process.argv.slice(2)
if (!invitePath || !keyPath || !cwd) {
  console.error('usage: node session-smoke.mjs <invitation.json> <key.json> <cwd>')
  process.exit(2)
}
const invitation = JSON.parse(readFileSync(invitePath, 'utf8'))
const log = (...a) => console.log('[smoke]', ...a)

function hostCli(args) {
  execSync(process.env.INTEROP_HOST_CMD.replace('{args}', args.join(' ')), { stdio: 'inherit' })
}

await initCrypto()
let device
if (existsSync(keyPath)) {
  const saved = JSON.parse(readFileSync(keyPath, 'utf8'))
  device = { publicKey: saved.public, privateKey: saved.seed }
} else {
  device = newDevice()
  writeFileSync(keyPath, JSON.stringify({ public: device.publicKey, seed: device.privateKey }), { mode: 0o600 })
}

const session = await pair(invitation, device, 'Smoke Phone', id => {
  log('pending', id.slice(0, 8), 'approving...')
  hostCli(['approve', id])
})
log('paired with', session.ready.hostName)

const projects = await session.projects()
log('projects', projects.length, projects.slice(0, 3).map(p => `${p.cwd}(${p.sessions})`).join(' '))

const key = await session.startSession(cwd, '')
log('session key', key.slice(0, 8))
const events = []
await session.subscribe(key, 0, ev => {
  events.push(ev)
  if (ev.kind === 'result' || ev.kind === 'exit' || ev.kind === 'permission_denied') {
    log('event', ev.kind, (ev.text || '').slice(0, 120))
  }
})
session.sendTurn(key, '只回复 OK')
log('sent, waiting for result...')

const deadline = Date.now() + 30000
while (Date.now() < deadline) {
  if (events.some(e => e.kind === 'result' || e.kind === 'exit')) break
  await new Promise(r => setTimeout(r, 200))
}
log('events:', events.map(e => e.kind).join(',') || '(none)')
session.close()
if (!events.some(e => e.kind === 'result' || e.kind === 'exit')) {
  console.error('SESSION SMOKE FAIL: no result')
  process.exit(1)
}
console.log('SESSION SMOKE PASS')
