// MQTT 传输层：主题、单条消息加密、连接与请求/响应队列。
import mqtt from 'mqtt'
import { seal, open, parseKey } from './e2ee.js'
import { isStatusTopic, parseHostStatus } from './status.js'

const enc = new TextEncoder()
const dec = new TextDecoder()

export function topicSet(uid, hid, did) {
  const p = `u/${uid}/h/${hid}/`
  return {
    req: `${p}req/${did}`,
    evt: `${p}evt`,
    up: `${p}up/${did}`,
    status: `${p}status`,
    pair: `${p}pair/${did}`,
  }
}

export function encodeFrame(key, dir, from, seq, plain) {
  return seal(key, dir, from, seq, plain)
}

export function decodeFrame(key, dir, from, seq, frame) {
  return open(key, dir, from, seq, frame)
}

export async function connectMQTT({ url, username, password, clientId, willTopic }) {
  const opts = {
    clientId,
    username,
    password,
    clean: true,
    // 15 秒心跳能更快发现手机息屏/切网后的假连接；断线由 App 自己重连。
    keepalive: 15,
    reconnectPeriod: 0,
    connectTimeout: 10000,
  }
  if (willTopic) {
    opts.will = {
      topic: willTopic,
      payload: new TextEncoder().encode('{"online":false}'),
      qos: 1,
      retain: true,
    }
  }
  return mqtt.connectAsync(url, opts)
}

export class MQTTLink {
  constructor({ client, topics, mode, contentKey, hostID, deviceID, onClose, onStatus, startSeq }) {
    this.client = client
    this.topics = topics
    this.mode = mode
    this.key = parseKey(contentKey)
    this.hostID = hostID
    this.deviceID = deviceID
    this.seq = startSeq || 0
    this.queue = []
    this.waiters = []
    this.closed = false
    this.onSessionEvent = null
    this.onStatus = onStatus || null
    this.onCloseCb = onClose
    // 每个发送方（主机）的连接内递增序号，重复/倒退的帧直接丢弃。
    this.lastSeqBySender = new Map()
    client.on('message', (topic, payload) => this.onMessage(topic, payload))
    client.on('error', () => {})
    client.on('close', () => this.failAll(new Error('disconnected')))
  }

  static async create(opts) {
    const client = await connectMQTT({
      url: opts.url,
      username: opts.username,
      password: opts.password,
      clientId: opts.deviceID,
    })
    const link = new MQTTLink({ ...opts, client })
    const filters = opts.mode === 'pair'
      ? [opts.topics.pair]
      : [opts.topics.evt, opts.topics.status, opts.topics.pair]
    await client.subscribeAsync(filters, { qos: 1 })
    return link
  }

  async switchToSession() {
    this.mode = 'session'
    await this.client.subscribeAsync([this.topics.evt, this.topics.status, this.topics.pair], { qos: 1 })
  }

  onMessage(topic, payload) {
    if (this.closed) return
    if (isStatusTopic(topic)) {
      const online = parseHostStatus(payload)
      if (online !== null) this.onStatus?.(online)
      return
    }
    let frame
    try {
      frame = JSON.parse(dec.decode(payload))
    } catch {
      return
    }
    let msg
    try {
      const last = this.lastSeqBySender.get(frame.from) || 0
      msg = JSON.parse(dec.decode(open(this.key, 'h2c', this.hostID, frame.seq, frame, last)))
      this.lastSeqBySender.set(frame.from, frame.seq)
    } catch {
      return
    }
    this.push(msg)
  }

  push(msg) {
    // subscribe 的首次回复也用 session.events，但它带 id，必须交给 until()。
    // 只有不带 id 的才是实时推送；否则 startChat 会一直等到超时。
    if (msg.type === 'session.events' && !msg.id && this.onSessionEvent) {
      for (const ev of msg.events || []) this.onSessionEvent({ ...ev, sessionKey: msg.sessionKey })
      return
    }
    if (msg.type === 'session.event' && !msg.id && this.onSessionEvent) {
      this.onSessionEvent(msg)
      return
    }
    // 没有 id 的错误不属于任何请求：放进独立错误通道，避免污染通用队列。
    if ((msg.type === 'error' || msg.type === 'session.error') && !msg.id) {
      this.lastError = msg
      if (this.onError) this.onError(msg)
      return
    }
    const index = msg.type === 'pong'
      ? this.waiters.findIndex(w => w.pred ? w.pred(msg) : false)
      : this.waiters.findIndex(w => !w.pred || w.pred(msg))
    if (msg.type === 'pong' && index < 0) return
    if (index >= 0) {
      this.waiters.splice(index, 1)[0].ok(msg)
      return
    }
    this.queue.push(msg)
    if (this.queue.length > 200) this.queue.splice(0, this.queue.length - 200)
  }

  next(timeoutMs = 15000) {
    if (this.queue.length) return Promise.resolve(this.queue.shift())
    if (this.closed) return Promise.reject(new Error('disconnected'))
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        const i = this.waiters.indexOf(w)
        if (i >= 0) this.waiters.splice(i, 1)
        reject(new Error('timeout'))
      }, timeoutMs)
      const w = {
        ok: msg => { clearTimeout(timer); resolve(msg) },
        fail: err => { clearTimeout(timer); reject(err) },
      }
      this.waiters.push(w)
    })
  }

  until(pred, timeoutMs = 30000) {
    const index = this.queue.findIndex(pred)
    if (index >= 0) return Promise.resolve(this.queue.splice(index, 1)[0])
    if (this.closed) return Promise.reject(new Error('disconnected'))
    return new Promise((resolve, reject) => {
      const waiter = {
        pred,
        ok: msg => { clearTimeout(timer); resolve(msg) },
        fail: err => { clearTimeout(timer); reject(err) },
      }
      const timer = setTimeout(() => {
        const i = this.waiters.indexOf(waiter)
        if (i >= 0) this.waiters.splice(i, 1)
        reject(new Error('timeout'))
      }, timeoutMs)
      this.waiters.push(waiter)
    })
  }

  setKey(b64) {
    this.key = parseKey(b64)
  }

  topicFor(msg) {
    // 握手消息永远走 pair 主题：电脑按主题选择全局配对密钥。
    if (msg.type === 'hello' || msg.type === 'proof') return this.topics.pair
    if (msg.type?.startsWith('attach.')) return this.topics.up
    if (this.mode === 'pair') return this.topics.pair
    return this.topics.req
  }

  async send(obj) {
    if (this.closed) throw new Error('disconnected')
    const frame = seal(this.key, 'c2h', this.deviceID, ++this.seq, enc.encode(JSON.stringify(obj)))
    const qos = obj.type === 'session.event' ? 0 : 1
    const pub = this.client.publishAsync(this.topicFor(obj), JSON.stringify(frame), { qos })
    let timer
    const timeout = new Promise((_, reject) => {
      timer = setTimeout(() => reject(new Error('publish timeout')), 10000)
    })
    try {
      await Promise.race([pub, timeout])
    } finally {
      clearTimeout(timer)
    }
  }

  failAll(err) {
    if (this.closed) return
    this.closed = true
    for (const w of this.waiters.splice(0)) w.fail(err)
    if (this.onCloseCb) this.onCloseCb()
  }

  close() {
    try {
      this.client.end(true)
    } catch {
      // 已关闭
    }
    this.failAll(new Error('closed'))
  }
}
