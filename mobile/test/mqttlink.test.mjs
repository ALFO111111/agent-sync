import test from 'node:test'
import assert from 'node:assert/strict'
import { topicSet, encodeFrame, decodeFrame } from '../src/lib/mqttlink.js'
import { parseKey } from '../src/lib/e2ee.js'

const ZERO_KEY = 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA='
const enc = new TextEncoder()
const dec = new TextDecoder()

test('topic set matches go format', () => {
  const t = topicSet('aa', 'bb', 'cc')
  assert.equal(t.req, 'u/aa/h/bb/req/cc')
  assert.equal(t.evt, 'u/aa/h/bb/evt')
  assert.equal(t.up, 'u/aa/h/bb/up/cc')
  assert.equal(t.status, 'u/aa/h/bb/status')
  assert.equal(t.pair, 'u/aa/h/bb/pair/cc')
})

test('frame round trip', () => {
  const key = parseKey(ZERO_KEY)
  const f = encodeFrame(key, 'c2h', 'cc', 1, enc.encode('hi'))
  assert.equal(dec.decode(decodeFrame(key, 'c2h', 'cc', 1, f)), 'hi')
  assert.throws(() => decodeFrame(key, 'c2h', 'dd', 1, f))
})
