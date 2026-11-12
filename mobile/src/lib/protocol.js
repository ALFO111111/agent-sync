// v1 协议客户端：与 Go 的 internal/remotev1 对应。
// 传输是外层 WSS；本版本没有内层端到端加密（见文档）。
import * as ed from '@noble/ed25519'
import { sha256, sha512 } from '@noble/hashes/sha2.js'

ed.hashes.sha512 = sha512

const enc = new TextEncoder()
const dec = new TextDecoder()

export async function initCrypto() {
  // noble 是纯 JS 实现，同步可用；保留异步入口方便以后换实现。
}

export const b64 = bytes => {
  let s = ''
  for (let i = 0; i < bytes.length; i += 0x8000) {
    s += String.fromCharCode.apply(null, bytes.subarray(i, i + 0x8000))
  }
  return btoa(s)
}

export const unb64 = str => Uint8Array.from(atob(str), c => c.charCodeAt(0))

// 深链里的参数用 base64url（无填充），这里兼容标准与 URL 安全两种写法。
export const unb64url = str => {
  const std = str.replace(/-/g, '+').replace(/_/g, '/')
  const pad = std.length % 4 ? '='.repeat(4 - (std.length % 4)) : ''
  return unb64(std + pad)
}

export function newDevice() {
  const secret = ed.utils.randomSecretKey()
  return { publicKey: b64(ed.getPublicKey(secret)), privateKey: b64(secret) }
}

export function fingerprint(pubB64) {
  const hash = sha256(unb64(pubB64))
  return Array.from(hash, b => b.toString(16).padStart(2, '0')).join('')
}

export function parseDeepLink(url) {
  try {
    // 注意：WebView 对 agent-sync://pair?... 的 hostname 解析为空白，
    // 不能依赖 host，只校验 scheme 和 d 参数。
    if (typeof url !== 'string' || !url.startsWith('agent-sync:')) return null
    const data = new URL(url).searchParams.get('d')
    return data ? JSON.parse(new TextDecoder().decode(unb64url(data))) : null
  } catch {
    return null
  }
}

class Link {
  constructor(ws) {
    this.ws = ws
    this.queue = []
    this.waiters = []
    ws.binaryType = 'arraybuffer'
    ws.onmessage = ev => {
      const text = typeof ev.data === 'string' ? ev.data : dec.decode(new Uint8Array(ev.data))
      let msg
      try {
        msg = JSON.parse(text)
      } catch {
        return
      }
      this.push(msg)
    }
    this.onClose = null
    ws.onclose = () => {
      if (this.onClose) this.onClose()
    }
  }

  static async open(url) {
    const ws = new WebSocket(url)
    await new Promise((resolve, reject) => {
      ws.onopen = resolve
      ws.onerror = () => reject(new Error('relay dial failed'))
    })
    return new Link(ws)
  }

  push(msg) {
    // 会话事件是持续的推送，不进入请求/响应队列。
    if (msg.type === 'session.event' && this.onSessionEvent) {
      this.onSessionEvent(msg)
      return
    }
    const w = this.waiters.shift()
    if (w) w(msg)
    else this.queue.push(msg)
  }

  next(timeoutMs = 15000) {
    if (this.queue.length) return Promise.resolve(this.queue.shift())
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error('timeout')), timeoutMs)
      this.waiters.push(msg => {
        clearTimeout(timer)
        resolve(msg)
      })
    })
  }

  async until(pred, timeoutMs = 30000) {
    const deadline = Date.now() + timeoutMs
    for (;;) {
      const msg = await this.next(Math.max(1, deadline - Date.now()))
      if (pred(msg)) return msg
    }
  }

  // 应用帧必须是二进制：中转只转发二进制帧。
  send(obj) {
    this.ws.send(enc.encode(JSON.stringify(obj)))
  }

  close() {
    try {
      this.ws.close()
    } catch {
      // 已关闭
    }
  }
}

export class Session {
  constructor(link, ready, invitation, device) {
    this.link = link
    this.ready = ready
    this.invitation = invitation
    this.device = device
    this.timer = setInterval(() => {
      try {
        link.send({ v: 1, type: 'ping' })
      } catch {
        // 断线由 onclose 处理
      }
    }, 20000)
  }

  close() {
    clearInterval(this.timer)
    this.link.close()
  }

  async echo(text) {
    const id = Math.random().toString(16).slice(2, 12)
    this.link.send({ v: 1, type: 'echo', id, data: text })
    const reply = await this.link.until(m => m.type === 'echo' || m.type === 'error', 10000)
    if (reply.type !== 'echo' || reply.id !== id) throw new Error('echo failed')
    return reply.data
  }

  async fsList(cwd, path = '') {
    const id = Math.random().toString(16).slice(2, 12)
    this.link.send({ v: 1, type: 'fs.list', id, cwd, path })
    const reply = await this.link.until(m => (m.type === 'fs.list' && m.id === id) || m.type === 'fs.error', 15000)
    if (reply.type !== 'fs.list') throw new Error(reply.error || 'fs.list failed')
    return reply.entries || []
  }

  async fsRead(cwd, path) {
    const id = Math.random().toString(16).slice(2, 12)
    this.link.send({ v: 1, type: 'fs.read', id, cwd, path })
    const reply = await this.link.until(m => (m.type === 'fs.read' && m.id === id) || m.type === 'fs.error', 20000)
    if (reply.type !== 'fs.read') throw new Error(reply.error || 'fs.read failed')
    return { text: reply.text || '', truncated: !!reply.truncated, binary: !!reply.binary }
  }

  async gitStatus(cwd) {
    const id = Math.random().toString(16).slice(2, 12)
    this.link.send({ v: 1, type: 'git.status', id, cwd })
    const reply = await this.link.until(m => (m.type === 'git.status' && m.id === id) || m.type === 'fs.error', 20000)
    if (reply.type !== 'git.status') throw new Error(reply.error || 'git.status failed')
    return reply.changes || []
  }

  async gitDiff(cwd, path = '') {
    const id = Math.random().toString(16).slice(2, 12)
    this.link.send({ v: 1, type: 'git.diff', id, cwd, path })
    const reply = await this.link.until(m => (m.type === 'git.diff' && m.id === id) || m.type === 'fs.error', 20000)
    if (reply.type !== 'git.diff') throw new Error(reply.error || 'git.diff failed')
    return { text: reply.text || '', truncated: !!reply.truncated }
  }

  async devices() {
    const id = Math.random().toString(16).slice(2, 12)
    this.link.send({ v: 1, type: 'device.list', id })
    const reply = await this.link.until(m => m.type === 'devices' || m.type === 'error', 10000)
    if (reply.type !== 'devices') throw new Error('device list failed')
    return reply.devices || []
  }

  // 电脑端仍在运行的会话，断线重连后用来接回。
  async activeSessions() {
    const id = Math.random().toString(16).slice(2, 12)
    this.link.send({ v: 1, type: 'session.active', id })
    const reply = await this.link.until(
      m => (m.type === 'session.active' && m.id === id) || m.type === 'session.error',
      10000,
    )
    if (reply.type !== 'session.active') throw new Error(reply.error || 'active sessions failed')
    return reply.active || []
  }

  async projects(tool = 'claude') {
    const id = Math.random().toString(16).slice(2, 12)
    this.link.send({ v: 1, type: 'session.projects', id, tool })
    const reply = await this.link.until(m => m.type === 'session.projects' || m.type === 'session.error', 15000)
    if (reply.type !== 'session.projects') throw new Error(reply.error || 'session.projects failed')
    return reply.projects || []
  }

  async sessions(tool = 'claude', cwd = '') {
    const id = Math.random().toString(16).slice(2, 12)
    this.link.send({ v: 1, type: 'session.list', id, tool, cwd })
    const reply = await this.link.until(m => m.type === 'session.list' || m.type === 'session.error', 15000)
    if (reply.type !== 'session.list') throw new Error(reply.error || 'session.list failed')
    return reply.sessions || []
  }

  async startSession(tool = 'claude', cwd, resumeId = '', mode = 'readonly') {
    const id = Math.random().toString(16).slice(2, 12)
    this.link.send({ v: 1, type: 'session.start', id, tool, cwd, resumeId, mode })
    const reply = await this.link.until(m => m.type === 'session.ready' || m.type === 'session.error', 20000)
    if (reply.type !== 'session.ready' || !reply.sessionKey) {
      throw new Error(reply.error || 'session.start failed')
    }
    return reply.sessionKey
  }

  // 切换权限模式：电脑端用同一会话 ID 重启进程，上下文保留。
  async setMode(sessionKey, mode) {
    const id = Math.random().toString(16).slice(2, 12)
    this.link.send({ v: 1, type: 'session.setmode', id, sessionKey, mode })
    const reply = await this.link.until(
      m => (m.type === 'session.ready' && m.id === id) || m.type === 'session.error',
      15000,
    )
    if (reply.type !== 'session.ready' || !reply.sessionKey) {
      throw new Error(reply.error || 'set mode failed')
    }
    return reply.sessionKey
  }

  async subscribe(sessionKey, sinceSeq, handler) {
    const id = Math.random().toString(16).slice(2, 12)
    this.link.onSessionEvent = msg => {
      if (msg.sessionKey === sessionKey) handler(msg)
    }
    this.link.send({ v: 1, type: 'session.subscribe', id, sessionKey, sinceSeq })
    const reply = await this.link.until(m => m.type === 'session.events' || m.type === 'session.error', 15000)
    if (reply.type !== 'session.events') throw new Error(reply.error || 'subscribe failed')
    for (const ev of reply.events || []) handler({ ...ev, sessionKey })
  }

  sendTurn(sessionKey, text) {
    this.link.send({ v: 1, type: 'turn.send', sessionKey, text })
  }

  stopTurn(sessionKey) {
    this.link.send({ v: 1, type: 'turn.stop', sessionKey })
  }

  // 回应一次工具审批。
  decide(sessionKey, approvalId, allow) {
    this.link.send({ v: 1, type: 'approval.decide', sessionKey, approvalId, allow })
  }

  // 结束整个会话进程，立即释放电脑上的会话文件。
  async closeSession(sessionKey) {
    const id = Math.random().toString(16).slice(2, 12)
    this.link.send({ v: 1, type: 'session.close', id, sessionKey })
    const reply = await this.link.until(
      m => (m.type === 'session.closed' && m.id === id) || m.type === 'session.error',
      10000,
    )
    this.link.onSessionEvent = null
    if (reply.type !== 'session.closed') throw new Error(reply.error || 'close failed')
  }
}

async function handshake(link, device, name, invitation, onPending) {
  const hello = { v: 1, type: 'hello', devicePub: device.publicKey, deviceName: name }
  if (invitation) {
    hello.inviteId = invitation.id
    hello.inviteSecret = invitation.secret
  }
  link.send(hello)
  const challenge = await link.until(m => m.type === 'challenge' || m.type === 'denied', 20000)
  if (challenge.type === 'denied') throw new Error('denied')
  const data = `agent-sync-v1|${challenge.nonce}|${device.publicKey}|${hello.inviteId || ''}`
  const signature = b64(ed.sign(enc.encode(data), unb64(device.privateKey)))
  link.send({
    v: 1,
    type: 'proof',
    devicePub: device.publicKey,
    inviteId: hello.inviteId || '',
    signature,
  })
  const first = await link.next(20000)
  if (first.type === 'ready') return first
  if (first.type === 'denied') throw new Error('denied')
  if (first.type === 'pending') {
    onPending?.(first.id)
    const ready = await link.until(m => m.type === 'ready' || m.type === 'denied', 360000)
    if (ready.type === 'denied') throw new Error('denied')
    return ready
  }
  throw new Error('protocol')
}

async function connect(link, device, name, invitation, onPending) {
  const ready = await handshake(link, device, name, invitation, onPending)
  return new Session(link, ready, invitation, device)
}

// 首次配对：invitation 来自扫码/粘贴。
export async function pair(invitation, device, name, onPending) {
  const link = await Link.open(invitation.url)
  link.ws.send(
    JSON.stringify({
      version: 1,
      role: 'connect',
      route: invitation.route,
      credential: invitation.credential,
    }),
  )
  await link.until(m => m.role === 'paired', 60000)
  return connect(link, device, name, invitation, onPending)
}

// 已配对设备重连：只需要保存的 route/credential。
export async function reconnect(saved, device, onPending) {
  const link = await Link.open(saved.url)
  link.ws.send(
    JSON.stringify({ version: 1, role: 'connect', route: saved.route, credential: saved.credential }),
  )
  await link.until(m => m.role === 'paired', 60000)
  return connect(link, device, saved.name, null, onPending)
}
