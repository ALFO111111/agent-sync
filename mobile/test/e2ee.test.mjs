import test from 'node:test'
import assert from 'node:assert/strict'
import { parseKey, seal, open, MAX_PLAIN } from '../src/lib/e2ee.js'

const ZERO_KEY = 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA='
const enc = new TextEncoder()
const dec = new TextDecoder()

test('seal/open roundtrip and sender binding', () => {
  const k = parseKey(ZERO_KEY)
  const f = seal(k, 'c2h', 'dev1', 1, enc.encode('hello'))
  assert.equal(dec.decode(open(k, 'c2h', 'dev1', 1, f)), 'hello')
  assert.throws(() => open(k, 'c2h', 'dev2', 1, f))
  assert.throws(() => open(k, 'h2c', 'dev1', 1, f))
})

test('rejects oversize and duplicate nonce reuse', () => {
  const k = parseKey(ZERO_KEY)
  assert.throws(() => seal(k, 'c2h', 'd', 1, new Uint8Array(MAX_PLAIN + 1)))
  const f = seal(k, 'h2c', 'h', 1, enc.encode('x'))
  assert.equal(dec.decode(open(k, 'h2c', 'h', 1, f)), 'x')
  assert.throws(() => open(k, 'h2c', 'h', 2, f))
})
