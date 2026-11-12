// 审批与在线模式切换冒烟：
// 1) “手动”模式下写文件会向手机发 approval，允许后文件创建；
// 2) 不重启切到“只读”，同一 key，写文件被拒。
// 用法：INTEROP_HOST_CMD='...' NODE_USE_ENV_PROXY=1 node approval-smoke.mjs <invite> <key> <cwd>
import { readFileSync, writeFileSync, existsSync, rmSync } from 'node:fs'
import { execSync } from 'node:child_process'
import { initCrypto, newDevice, pair } from '../src/lib/protocol.js'

const [invitePath, keyPath, cwd] = process.argv.slice(2)
const invitation = JSON.parse(readFileSync(invitePath, 'utf8'))
const log = (...a) => console.log('[approval]', ...a)
const target = `${cwd.replace(/[\\/]+$/, '')}/approval-test.txt`

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

const session = await pair(invitation, device, 'Approval Smoke', id => {
  log('pending', id.slice(0, 8), 'approving...')
  hostCli(['approve', id])
})
log('paired with', session.ready.hostName)

rmSync(target, { force: true })
let approvals = 0
let events = []
const handler = ev => {
  events.push(ev)
  if (ev.kind === 'approval') {
    approvals++
    log('approval request:', ev.tool, (ev.text || '').slice(0, 80))
    session.decide(session.currentKey, ev.approvalId, true)
  }
}

async function turn(key, prompt, ms = 90000) {
  events = []
  session.sendTurn(key, prompt)
  const deadline = Date.now() + ms
  while (Date.now() < deadline) {
    if (events.some(e => e.kind === 'result' || e.kind === 'exit')) break
    await new Promise(r => setTimeout(r, 200))
  }
}

let key = await session.startSession(cwd, '', 'default')
session.currentKey = key
await session.subscribe(key, 0, handler)
log('manual mode turn...')
await turn(key, '创建 approval-test.txt，内容写 ok，然后只回复 DONE')
if (!existsSync(target)) throw new Error('approved write did not happen')
if (approvals === 0) throw new Error('no approval request was delivered')
log('approval allowed and file created, ok')

log('live switch to dontAsk...')
const newKey = await session.setMode(key, 'dontAsk')
const moved = newKey !== key
if (moved) log('note: host restarted session (fallback)')
key = newKey
session.currentKey = key
if (moved) await session.subscribe(key, 0, handler)

rmSync(target, { force: true })
await turn(key, '创建 approval-test.txt，内容写 ok，然后只回复 DONE')
if (existsSync(target)) throw new Error('dontAsk mode still wrote a file')
if (!events.some(e => e.kind === 'permission_denied')) throw new Error('no permission_denied under dontAsk')
log('dontAsk denied write, ok')

await session.closeSession(key)
rmSync(target, { force: true })
session.close()
console.log('APPROVAL SMOKE PASS')
