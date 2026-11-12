// 权限模式冒烟：只读时写文件被拒；切到“可编辑”后同一对话能写文件。
// 用法：INTEROP_HOST_CMD='...agent-sync-host {args}...' NODE_USE_ENV_PROXY=1 \
//       node mode-smoke.mjs <invitation.json> <key.json> <cwd>
import { readFileSync, writeFileSync, existsSync, rmSync } from 'node:fs'
import { execSync } from 'node:child_process'
import { initCrypto, newDevice, pair } from '../src/lib/protocol.js'

const [invitePath, keyPath, cwd] = process.argv.slice(2)
const invitation = JSON.parse(readFileSync(invitePath, 'utf8'))
const log = (...a) => console.log('[mode]', ...a)
const target = `${cwd.replace(/[\\/]+$/, '')}/mode-test.txt`

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

const session = await pair(invitation, device, 'Mode Smoke', id => {
  log('pending', id.slice(0, 8), 'approving...')
  hostCli(['approve', id])
})
log('paired with', session.ready.hostName)

function waitResult(events, ms = 90000) {
  const deadline = Date.now() + ms
  return new Promise(resolve => {
    const tick = () => {
      const done = events.find(e => e.kind === 'result' || e.kind === 'exit')
      if (done || Date.now() > deadline) resolve(done)
      else setTimeout(tick, 200)
    }
    tick()
  })
}

async function runTurn(key, events, prompt) {
  session.sendTurn(key, prompt)
  return waitResult(events)
}

rmSync(target, { force: true })
let events = []
let key = await session.startSession(cwd, '', 'readonly')
await session.subscribe(key, 0, ev => events.push(ev))
log('readonly turn...')
await runTurn(key, events, '创建 mode-test.txt，内容写 hi，然后只回复 DONE')
const denied = events.some(e => e.kind === 'permission_denied')
if (existsSync(target)) throw new Error('readonly wrote a file')
if (!denied) throw new Error('readonly did not report permission_denied')
log('readonly denied write, ok')

events = []
key = await session.setMode(key, 'edit')
await session.subscribe(key, 0, ev => events.push(ev))
log('edit turn...')
await runTurn(key, events, '创建 mode-test.txt，内容写 hi，然后只回复 DONE')
if (!existsSync(target)) throw new Error('edit mode did not create file')
log('edit mode wrote file, ok')

await session.closeSession(key)
rmSync(target, { force: true })
session.close()
console.log('MODE SMOKE PASS')
