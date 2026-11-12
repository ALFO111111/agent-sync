// Codex 后端冒烟：新建 → 流式 → 结束 → 用 thread id resume 继续。
// 用法：INTEROP_HOST_CMD='...' NODE_USE_ENV_PROXY=1 node codex-smoke.mjs <invite> <keyfile> <cwd>
import { readFileSync, writeFileSync, existsSync } from 'node:fs'
import { execSync } from 'node:child_process'
import { initCrypto, newDevice, pair } from '../src/lib/protocol.js'

const [invitePath, keyPath, cwd] = process.argv.slice(2)
const invitation = JSON.parse(readFileSync(invitePath, 'utf8'))
const log = (...a) => console.log('[codex]', ...a)

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

const session = await pair(invitation, device, 'Codex Smoke', id => {
  log('pending', id.slice(0, 8), 'approving...')
  hostCli(['approve', id])
})
log('paired with', session.ready.hostName)

let threadID = ''
let events = []
const handler = ev => {
  events.push(ev)
  if (ev.kind === 'init' && ev.sessionId) threadID = ev.sessionId
  if (['text', 'tool', 'result', 'notice'].includes(ev.kind)) {
    log('event', ev.kind, (ev.text || ev.tool || '').slice(0, 100))
  }
}

async function runTurn(key, prompt) {
  events = []
  session.sendTurn(key, prompt)
  const deadline = Date.now() + 120000
  while (Date.now() < deadline) {
    if (events.some(e => e.kind === 'result')) break
    await new Promise(r => setTimeout(r, 200))
  }
  if (!events.some(e => e.kind === 'result')) throw new Error('no result for: ' + prompt)
}

log('start codex session')
let key = await session.startSession('codex', cwd, '', 'dontAsk')
await session.subscribe(key, 0, handler)
await runTurn(key, '只回复 OK')
log('first turn done, thread', threadID)
if (!threadID) throw new Error('no thread id')
await session.closeSession(key)

log('resume by thread id')
key = await session.startSession('codex', cwd, threadID, 'dontAsk')
await session.subscribe(key, 0, handler)
await runTurn(key, '第二句：只回复 OK2')
log('resume turn done')
await session.closeSession(key)
session.close()
console.log('CODEX SMOKE PASS')
