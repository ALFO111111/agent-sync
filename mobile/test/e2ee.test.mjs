import test from 'node:test'
import assert from 'node:assert/strict'
import { gcm } from '@noble/ciphers/aes.js'
import { parseKey, Sealer, Opener, MAX_PLAIN } from '../src/lib/e2ee.js'

const hex = s => Uint8Array.from(s.match(/../g), x => parseInt(x, 16))
const ZERO_KEY = 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA='

test('fixed vector matches Go layout', () => {
  // key = 00..1f, prefix = 20..27, counter 0, AAD c2h；由 Go 侧同一组输入生成。
  const key = hex('000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f')
  const prefix = hex('2021222324252627')
  const nonce = new Uint8Array(12)
  nonce.set(prefix, 0)
  new DataView(nonce.buffer).setUint32(8, 0)
  const plain = new TextEncoder().encode('hello agent-sync')
  const aad = new TextEncoder().encode('agent-sync-e2ee-v1|c2h')
  const ct = gcm(key, nonce, aad).encrypt(plain)
  const out = new Uint8Array(12 + ct.length)
  out.set(nonce, 0)
  out.set(ct, 12)
  assert.equal(Buffer.from(out).toString('base64'), 'ICEiIyQlJicAAAAAToLfIXQVXmGiZnaDPmwi2L8E8BEMG35S63AA/3VV2pI=')
})

test('sealer/opener roundtrip and replay rejection', () => {
  const k = parseKey(ZERO_KEY)
  const s = new Sealer(k, 'c2h')
  const o = new Opener(k, 'c2h')
  const f0 = s.seal(new TextEncoder().encode('0'))
  assert.deepEqual(Buffer.from(o.open(f0)), Buffer.from('0'))
  assert.throws(() => o.open(f0))
  const f1 = s.seal(new TextEncoder().encode('1'))
  assert.deepEqual(Buffer.from(o.open(f1)), Buffer.from('1'))
})

test('rejects oversize and wrong direction', () => {
  const k = parseKey(ZERO_KEY)
  const s = new Sealer(k, 'c2h')
  assert.throws(() => s.seal(new Uint8Array(MAX_PLAIN + 1)))
  const o = new Opener(k, 'h2c')
  const f = s.seal(new TextEncoder().encode('x'))
  assert.throws(() => o.open(f))
})
