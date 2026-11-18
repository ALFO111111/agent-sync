import { test } from 'node:test'
import assert from 'node:assert/strict'
import { MQTTLink } from '../src/lib/mqttlink.js'
import { newKey, parseKey } from '../src/lib/e2ee.js'

test('device keys are 32 random bytes and parse back', () => {
  const a = newKey()
  const b = newKey()
  assert.equal(parseKey(a).length, 32)
  assert.notEqual(a, b)
})

test('MQTTLink can switch to the session key after handshake', () => {
  const client = { on() {}, subscribeAsync: async () => {}, publishAsync: async () => {}, end() {} }
  const link = new MQTTLink({
    client, topics: {}, mode: 'session',
    contentKey: Buffer.alloc(32, 1).toString('base64'), hostID: 'h', deviceID: 'd',
  })
  const next = newKey()
  link.setKey(next)
  assert.deepEqual(Array.from(link.key), Array.from(parseKey(next)))
})

test('handshake messages always go to the pair topic', () => {
  const client = { on() {}, subscribeAsync: async () => {}, publishAsync: async () => {}, end() {} }
  const link = new MQTTLink({
    client, topics: { req: 'req', up: 'up', pair: 'pair' }, mode: 'session',
    contentKey: Buffer.alloc(32, 1).toString('base64'), hostID: 'h', deviceID: 'd',
  })
  assert.equal(link.topicFor({ type: 'hello' }), 'pair')
  assert.equal(link.topicFor({ type: 'proof' }), 'pair')
  assert.equal(link.topicFor({ type: 'turn.send' }), 'req')
})

test('client close invokes the onClose callback exactly once', () => {
  let closeHandler
  const client = {
    on(event, fn) { if (event === 'close') closeHandler = fn },
    subscribeAsync: async () => {}, publishAsync: async () => {}, end() {},
  }
  let closed = 0
  const link = new MQTTLink({
    client, topics: {}, mode: 'session',
    contentKey: Buffer.alloc(32, 7).toString('base64'),
    hostID: 'h', deviceID: 'd', onClose: () => { closed++ },
  })
  closeHandler()
  closeHandler()
  assert.equal(closed, 1)
  assert.equal(link.closed, true)
})

test('session.events reply with an id resolves the pending request', async () => {
  const client = { on() {}, subscribeAsync: async () => {}, publishAsync: async () => {}, end() {} }
  const key = Buffer.alloc(32, 7).toString('base64')
  const link = new MQTTLink({ client, topics: {}, mode: 'session', contentKey: key, hostID: 'h', deviceID: 'd' })
  link.onSessionEvent = () => {}

  const wait = link.until(m => m.type === 'session.events', 2000)
  link.push({ type: 'session.events', id: 'req-1', sessionKey: 'sk', events: [] })
  const reply = await Promise.race([
    wait,
    new Promise((_, reject) => setTimeout(() => reject(new Error('timeout waiting for subscribe reply')), 500)),
  ])
  assert.equal(reply.id, 'req-1')
})

test('batched session.events reach the live handler', () => {
  const client = { on() {}, subscribeAsync: async () => {}, publishAsync: async () => {}, end() {} }
  const key = Buffer.alloc(32, 7).toString('base64')
  const link = new MQTTLink({ client, topics: {}, mode: 'session', contentKey: key, hostID: 'h', deviceID: 'd' })
  const got = []
  link.onSessionEvent = (ev) => got.push(ev)
  link.push({ type: 'session.events', sessionKey: 'sk', events: [{ kind: 'text', text: 'hi' }] })
  assert.deepEqual(got, [{ kind: 'text', text: 'hi', sessionKey: 'sk' }])
})
