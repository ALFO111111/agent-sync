import test from 'node:test'
import assert from 'node:assert/strict'
import { Session } from '../src/lib/protocol.js'

function fakeLink(replies, onUntil) {
  const sent = []
  return {
    sent,
    send(message) { sent.push(message) },
    async until(predicate) {
      const message = { ...replies.shift(), id: sent.at(-1).id }
      assert.ok(predicate(message), `unexpected message ${JSON.stringify(message)}`)
      if (onUntil) onUntil(replies.length, this)
      return message
    },
    close() {},
  }
}

test('subscribe reassembles chunked history before delivering buffered live events', async () => {
  const replies = [
    { type: 'session.events', chunkIndex: 0, done: false, events: [{ seq: 1, kind: 'text', text: 'a' }, { seq: 2, kind: 'text', text: 'b' }] },
    { type: 'session.events', chunkIndex: 1, done: true, events: [{ seq: 3, kind: 'text', text: 'c' }] },
  ]
  let injected = false
  let link
  link = fakeLink(replies, remaining => {
    if (remaining === 1 && !injected) {
      injected = true
      // 历史还没加载完时来了一条实时事件，应排在历史之后。
      link.onSessionEvent({ sessionKey: 'k', seq: 4, kind: 'text', text: 'live' })
    }
  })
  const session = new Session(link, { features: ['session.events.chunked.v1'] })
  const got = []
  await session.subscribe('k', 0, ev => got.push(ev))
  session.close()
  assert.deepEqual(got.map(ev => ev.seq), [1, 2, 3, 4])
  assert.ok(got.every(ev => ev.sessionKey === 'k'))
})

test('subscribe falls back to a single frame from an old host', async () => {
  const replies = [{ type: 'session.events', events: [{ seq: 1, kind: 'text', text: 'a' }] }]
  const link = fakeLink(replies)
  const session = new Session(link, { features: ['history.paged.v1'] })
  const got = []
  await session.subscribe('k', 0, ev => got.push(ev))
  session.close()
  assert.deepEqual(got.map(ev => ev.seq), [1])
})
