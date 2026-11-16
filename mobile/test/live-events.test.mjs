import { test } from 'node:test'
import assert from 'node:assert/strict'
import { MQTTLink } from '../src/lib/mqttlink.js'

test('batched session.events reach the live handler', () => {
  const client = { on() {}, subscribeAsync: async () => {}, publishAsync: async () => {}, end() {} }
  const key = Buffer.alloc(32, 7).toString('base64')
  const link = new MQTTLink({ client, topics: {}, mode: 'session', contentKey: key, hostID: 'h', deviceID: 'd' })
  const got = []
  link.onSessionEvent = (ev) => got.push(ev)
  link.push({ type: 'session.events', sessionKey: 'sk', events: [{ kind: 'text', text: 'hi' }] })
  assert.deepEqual(got, [{ kind: 'text', text: 'hi', sessionKey: 'sk' }])
})
