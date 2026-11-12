// 参考客户端：验证 Go 主机守护的 v1 协议，后续安卓端复用同一逻辑。
//
// 用法：
//   node interop.mjs <invitation.json> <host-exe> <state-dir> [key.json]
//
// 流程：配对（自动调用本机 host CLI 批准）→ echo → device.list →
//      用同一身份重连 → 主机撤销 → 再连必须失败。
import { readFileSync, writeFileSync, existsSync } from 'node:fs'
import { execFileSync, execSync } from 'node:child_process'
import { generateKeyPairSync, createPrivateKey, createPublicKey, sign } from 'node:crypto'

const [invitePath, hostExe, stateDir, keyPath = null] = process.argv.slice(2)
if (!invitePath || !hostExe || !stateDir) {
  console.error('usage: node interop.mjs <invitation.json> <host-exe> <state-dir> [key.json]')
  process.exit(2)
}

const invitation = JSON.parse(readFileSync(invitePath, 'utf8'))
const log = (...a) => console.log('[interop]', ...a)
const b64 = b => Buffer.from(b).toString('base64')

function loadOrCreateKey() {
  if (keyPath && existsSync(keyPath)) {
    const saved = JSON.parse(readFileSync(keyPath, 'utf8'))
    return {
      privateKey: createPrivateKey({ key: Buffer.from(saved.private, 'base64'), format: 'der', type: 'pkcs8' }),
      publicRaw: Buffer.from(saved.public, 'base64'),
    }
  }
  const { privateKey, publicKey } = generateKeyPairSync('ed25519')
  const pubDer = publicKey.export({ format: 'der', type: 'spki' })
  const publicRaw = pubDer.subarray(pubDer.length - 32)
  if (keyPath) {
    const privDer = privateKey.export({ format: 'der', type: 'pkcs8' })
    writeFileSync(keyPath, JSON.stringify({ private: b64(privDer), public: b64(publicRaw) }), { mode: 0o600 })
  }
  return { privateKey, publicRaw }
}

const key = loadOrCreateKey()
const devicePub = b64(key.publicRaw)
log('device fingerprint', Buffer.from(key.publicRaw).toString('hex').slice(0, 16))

function proofData(nonce, inviteId) {
  return Buffer.from(`agent-sync-v1|${nonce}|${devicePub}|${inviteId}`)
}

// 本地运行时直接调 host CLI；远程验收时用 INTEROP_HOST_CMD 把 {args} 换成
// 一条 SSH 命令（例如在另一台机器上批准/撤销）。
function hostCli(args) {
  const remote = process.env.INTEROP_HOST_CMD
  if (remote) {
    execSync(remote.replace('{args}', args.join(' ')), { stdio: 'inherit' })
    return ''
  }
  return execFileSync(hostExe, [...args, '-state', stateDir], { encoding: 'utf8' })
}

class Conn {
  constructor(ws) { this.ws = ws; this.queue = []; this.waiters = [] }
  static async open(url) {
    const ws = new WebSocket(url)
    ws.binaryType = 'arraybuffer'
    await new Promise((resolve, reject) => {
      ws.onopen = resolve
      ws.onerror = () => reject(new Error('websocket error'))
    })
    const c = new Conn(ws)
    ws.onmessage = ev => {
      const text = typeof ev.data === 'string' ? ev.data : Buffer.from(ev.data).toString('utf8')
      let msg
      try { msg = JSON.parse(text) } catch { return }
      c.push(msg)
    }
    return c
  }
  push(msg) {
    const w = this.waiters.shift()
    if (w) w(msg); else this.queue.push(msg)
  }
  next(timeoutMs = 10000) {
    if (this.queue.length) return Promise.resolve(this.queue.shift())
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error('timeout waiting for frame')), timeoutMs)
      this.waiters.push(msg => { clearTimeout(timer); resolve(msg) })
    })
  }
  async until(pred, timeoutMs = 60000) {
    const deadline = Date.now() + timeoutMs
    while (true) {
      const msg = await this.next(Math.max(1, deadline - Date.now()))
      if (pred(msg)) return msg
    }
  }
  send(obj) { this.ws.send(Buffer.from(JSON.stringify(obj))) }
  close() { this.ws.close() }
}

async function connectRelay(url, role, route, credential, ticket) {
  const ws = new WebSocket(url)
  ws.binaryType = 'arraybuffer'
  await new Promise((resolve, reject) => {
    ws.onopen = resolve
    ws.onerror = () => reject(new Error('relay dial failed'))
  })
  const c = new Conn(ws)
  ws.onmessage = ev => {
    const text = typeof ev.data === 'string' ? ev.data : Buffer.from(ev.data).toString('utf8')
    let msg; try { msg = JSON.parse(text) } catch { return }
    c.push(msg)
  }
  ws.send(JSON.stringify({ version: 1, role, route, credential, ticket }))
  await c.until(m => m.role === 'paired', 60000)
  return c
}

async function handshake(useInvite) {
  const c = await connectRelay(invitation.url, 'connect', invitation.route, invitation.credential)
  const hello = { v: 1, type: 'hello', devicePub, deviceName: 'Interop Phone' }
  if (useInvite) { hello.inviteId = invitation.id; hello.inviteSecret = invitation.secret }
  c.send(hello)
  const challenge = await c.until(m => m.type === 'challenge', 15000)
  const signature = b64(sign(null, proofData(challenge.nonce, hello.inviteId || ''), key.privateKey))
  c.send({ v: 1, type: 'proof', devicePub, inviteId: hello.inviteId || '', signature })
  const first = await c.next(15000)
  if (first.type === 'denied') return { c, error: 'denied' }
  if (first.type === 'ready') return { c, ready: first }
  if (first.type === 'pending') {
    log('pending approval', first.id.slice(0, 8), 'approving via host CLI...')
    hostCli(['approve', first.id])
    const ready = await c.until(m => m.type === 'ready' || m.type === 'denied', 60000)
    if (ready.type === 'denied') return { c, error: 'denied' }
    return { c, ready }
  }
  return { c, error: 'protocol' }
}

async function echo(c, text) {
  const id = Math.random().toString(16).slice(2, 12)
  c.send({ v: 1, type: 'echo', id, data: text })
  const reply = await c.until(m => m.type === 'echo' || m.type === 'error', 10000)
  if (reply.type !== 'echo' || reply.id !== id || reply.data !== text) throw new Error('echo mismatch')
}

function assert(cond, msg) { if (!cond) throw new Error(msg) }

const main = async () => {
  // 1. 首次配对
  let { c, ready, error } = await handshake(true)
  assert(ready, 'pairing failed: ' + error)
  log('paired, device', ready.deviceId.slice(0, 8), 'host', ready.hostName)
  await echo(c, 'hello-from-js')
  log('echo ok')

  c.send({ v: 1, type: 'device.list', id: 'list1' })
  const list = await c.until(m => m.type === 'devices', 10000)
  assert(list.devices.length >= 1 && list.devices[0].name === 'Interop Phone', 'device list mismatch')
  log('device.list ok')

  // 2. 同一身份重连
  c.close()
  ;({ c, ready, error } = await handshake(false))
  assert(ready, 'reconnect failed: ' + error)
  await echo(c, 'reconnect')
  log('reconnect ok')

  // 3. 主机撤销 → 旧连接断开，重连失败
  hostCli(['revoke', ready.deviceId])
  await new Promise(r => setTimeout(r, 300))
  let revoked = false
  try { await echo(c, 'after-revoke') } catch { revoked = true }
  c.close()
  ;({ c, error } = await handshake(false))
  assert(revoked && error === 'denied', 'revoked device still accepted')
  c.close()
  log('revoke ok')
  console.log('INTEROP PASS')
}

main().catch(err => {
  console.error('INTEROP FAIL:', err.message)
  process.exit(1)
})
