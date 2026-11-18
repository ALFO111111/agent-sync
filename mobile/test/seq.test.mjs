import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createSeqGate } from '../src/lib/seq.js'

test('seq gate drops duplicates and accepts increasing sequence', () => {
  const g = createSeqGate()
  assert.equal(g.accept(1), true)
  assert.equal(g.accept(1), false)
  assert.equal(g.accept(2), true)
})

test('reset lets a restarted session start from 1 again', () => {
  const g = createSeqGate()
  g.accept(42)
  assert.equal(g.accept(1), false)
  g.reset()
  assert.equal(g.accept(1), true)
})
