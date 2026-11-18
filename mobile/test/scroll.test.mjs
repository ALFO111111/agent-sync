import { test } from 'node:test'
import assert from 'node:assert/strict'
import { shouldStickToBottom, shouldSendOnEnter } from '../src/lib/scroll.js'

test('stick to bottom only when near the bottom', () => {
  assert.equal(shouldStickToBottom({ scrollTop: 900, scrollHeight: 1000, clientHeight: 200 }), true)
  assert.equal(shouldStickToBottom({ scrollTop: 600, scrollHeight: 1000, clientHeight: 200 }), false)
})

test('IME composition enter must not send', () => {
  assert.equal(shouldSendOnEnter({ isComposing: true }), false)
  assert.equal(shouldSendOnEnter({ keyCode: 229 }), false)
  assert.equal(shouldSendOnEnter({ isComposing: false, keyCode: 13 }), true)
})
