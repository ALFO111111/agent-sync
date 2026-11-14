// MQTT 传输层：主题、单条消息加密、连接与请求/响应队列。
import mqtt from 'mqtt'
import { seal, open, parseKey } from './e2ee.js'

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
    keepalive: 30,
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
  constructor({ client, topics, mode, contentKey, hostID, deviceID, onClose, startSeq }) {
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
    this.onCloseCb = onClose
    client.on('message', (_topic, payload) => this.onMessage(payload))
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

  onMessage(payload) {
    if (this.closed) return
    let frame
    try {
      frame = JSON.parse(dec.decode(payload))
    } catch {
      return
    }
    let msg
    try {
      msg = JSON.parse(dec.decode(open(this.key, 'h2c', this.hostID, frame.seq, frame)))
    } catch {
      return
    }
    this.push(msg)
  }

  push(msg) {
    if (msg.type === 'session.event' && this.onSessionEvent) {
      this.onSessionEvent(msg)
      return
    }
    const index = this.waiters.findIndex(w => !w.pred || w.pred(msg))
    if (index >= 0) this.waiters.splice(index, 1)[0].ok(msg)
    else this.queue.push(msg)
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

  topicFor(msg) {
    if (msg.type?.startsWith('attach.')) return this.topics.up
    if (this.mode === 'pair') return this.topics.pair
    return this.topics.req
  }

  async send(obj) {
    if (this.closed) throw new Error('disconnected')
    const frame = seal(this.key, 'c2h', this.deviceID, ++this.seq, enc.encode(JSON.stringify(obj)))
    const qos = obj.type === 'session.event' ? 0 : 1
    await this.client.publishAsync(this.topicFor(obj), JSON.stringify(frame), { qos })
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
