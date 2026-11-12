// 复现：先以“只读”启动，再切到“手动”，然后执行写操作，应当出现审批卡片。
import { readFileSync, writeFileSync, existsSync, rmSync } from 'node:fs'
import { execSync } from 'node:child_process'
import { initCrypto, newDevice, pair } from '../src/lib/protocol.js'

const [invitePath, keyPath, cwd] = process.argv.slice(2)
const invitation = JSON.parse(readFileSync(invitePath, 'utf8'))
const log = (...a) => console.log('[switch]', ...a)
const target = `${cwd.replace(/[\\/]+$/, '')}/switch-test.txt`

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

const session = await pair(invitation, device, 'Switch Smoke', id => {
  log('pending', id.slice(0, 8), 'approving...')
  hostCli(['approve', id])
})
log('paired with', session.ready.hostName)

let key = await session.startSession(cwd, '', 'dontAsk')
let events = []
let approvals = 0
const handler = ev => {
  events.push(ev)
  if (ev.kind === 'approval') {
    approvals++
    log('approval:', ev.tool, (ev.text || '').slice(0, 60))
    session.decide(session.currentKey, ev.approvalId, true)
  }
}
session.currentKey = key
await session.subscribe(key, 0, handler)

async function turn(prompt, ms = 60000) {
  events = []
  session.sendTurn(key, prompt)
  const deadline = Date.now() + ms
  while (Date.now() < deadline) {
    if (events.some(e => e.kind === 'result' || e.kind === 'exit')) break
    await new Promise(r => setTimeout(r, 200))
  }
  return events
}

rmSync(target, { force: true })
let evs = await turn('创建 switch-test.txt，内容 hi，然后只回复 DONE')
log('readonly kinds:', evs.map(e => e.kind).join(','))
if (existsSync(target)) throw new Error('readonly wrote file')

log('switching to default (manual)...')
const newKey = await session.setMode(key, 'default')
const moved = newKey !== key
log('key changed:', moved)
key = newKey
session.currentKey = key
if (moved) await session.subscribe(key, 0, handler)

evs = await turn('创建 switch-test.txt，内容 hi，然后只回复 DONE')
log('manual kinds:', evs.map(e => e.kind).join(','), 'approvals:', approvals)
if (!existsSync(target)) throw new Error('manual mode did not write after approval')
console.log('SWITCH SMOKE PASS')
session.close()
