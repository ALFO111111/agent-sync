import test from 'node:test'
import assert from 'node:assert/strict'
import { hostKey, upsertPairing, pickActive } from '../src/lib/pairings.js'

const p = (hostId, name) => ({ hostId, name, url: 'wss://x/ws', route: hostId + '-route' })

test('hostKey prefers hostId and falls back to route', () => {
  assert.equal(hostKey(p('h1')), 'h1')
  assert.equal(hostKey({ route: 'r1' }), 'r1')
  assert.equal(hostKey(null), '')
})

test('upsert updates the same computer instead of duplicating', () => {
  const a = p('h1', 'A')
  const b = p('h2', 'B')
  const list = upsertPairing(upsertPairing([], a), b)
  assert.equal(list.length, 2)
  const a2 = { ...a, name: 'A2', e2eeKey: 'new' }
  const again = upsertPairing(list, a2)
  assert.equal(again.length, 2)
  assert.equal(again.find(x => x.hostId === 'h1').name, 'A2')
})

test('pickActive falls back to the first computer', () => {
  const a = p('h1', 'A')
  const b = p('h2', 'B')
  assert.equal(pickActive([a, b], 'h2').name, 'B')
  assert.equal(pickActive([a, b], 'missing').name, 'A')
  assert.equal(pickActive([], 'x'), null)
})
