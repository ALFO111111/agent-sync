import { test } from 'node:test'
import assert from 'node:assert/strict'
import { shouldStickToBottom, shouldSendOnEnter, nextStick } from '../src/lib/scroll.js'

test('stick to bottom only when near the bottom', () => {
  assert.equal(shouldStickToBottom({ scrollTop: 900, scrollHeight: 1000, clientHeight: 200 }), true)
  assert.equal(shouldStickToBottom({ scrollTop: 600, scrollHeight: 1000, clientHeight: 200 }), false)
})

test('content growth must not stop following without a user gesture', () => {
  // 内容突然变长，滚动位置暂时不在底部，但不是用户在滑。
  assert.equal(nextStick({ near: false, programmatic: false, userScrolling: false, stick: true }), true)
  // 程序化滚动自身触发的 scroll 事件同样不能关闭跟随。
  assert.equal(nextStick({ near: false, programmatic: true, userScrolling: true, stick: true }), true)
  // 只有真实手势上滑才关闭。
  assert.equal(nextStick({ near: false, programmatic: false, userScrolling: true, stick: true }), false)
  // 滑回底部重新开启。
  assert.equal(nextStick({ near: true, programmatic: false, userScrolling: true, stick: false }), true)
})

test('IME composition enter must not send', () => {
  assert.equal(shouldSendOnEnter({ isComposing: true }), false)
  assert.equal(shouldSendOnEnter({ keyCode: 229 }), false)
  assert.equal(shouldSendOnEnter({ isComposing: false, keyCode: 13 }), true)
})
