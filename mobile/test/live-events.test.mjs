import { test } from 'node:test'
import assert from 'node:assert/strict'
import { MQTTLink } from '../src/lib/mqttlink.js'

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
