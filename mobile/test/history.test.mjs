import test from 'node:test'
import assert from 'node:assert/strict'
import { Session } from '../src/lib/protocol.js'

function client(replies, features = ['history.paged.v1']) {
  const sent = []
  const link = {
    send(message) { sent.push(message) },
    async until(predicate) {
      const value = replies.shift()
      if (value instanceof Error) throw value
      const message = { ...value, id: sent.at(-1).id }
      assert.ok(predicate(message))
      return message
    },
    close() {},
  }
  return { session: new Session(link, { features }), sent }
}

test('history reassembles large Unicode and escaped message across frames', async () => {
  const messages = [{ role: 'user', text: '前文' }, { role: 'assistant', text: '中文\\\"\n'.repeat(9000) }]
  const raw = JSON.stringify(messages)
  const replies = []
  for (let offset = 0, index = 0; offset < raw.length; offset += 2000, index++) {
    replies.push({ type: 'session.history.chunk', chunkIndex: index, data: raw.slice(offset, offset + 2000), done: offset + 2000 >= raw.length, cursor: 'older', hasMore: true, snapshot: 's1' })
  }
  const { session, sent } = client(replies)
  try {
    assert.deepEqual(await session.historyPage('codex', '/project', 'id'), { messages, cursor: 'older', hasMore: true, snapshot: 's1' })
    assert.equal(sent[0].limit, 50)
  } finally { session.close() }
})

test('history rejects out of order or incomplete frames', async () => {
  for (const replies of [
    [{ type: 'session.history.chunk', chunkIndex: 1, data: '[]', done: true }],
    [{ type: 'session.history.chunk', chunkIndex: 0, data: '[', done: false }, new Error('disconnected')],
  ]) {
    const { session } = client(replies)
    try { await assert.rejects(session.historyPage('claude', '/p', 'id')) }
    finally { session.close() }
  }
})

test('old hosts are not asked for unsafe unbounded history', async () => {
  const { session, sent } = client([], [])
  try {
    await assert.rejects(session.historyPage('claude', '/p', 'id'), /更新/)
    assert.equal(sent.length, 0)
  } finally { session.close() }
})

test('paging passes older cursor and exposes server errors without closing link', async () => {
  const { session, sent } = client([{ type: 'session.error', error: '历史快照已过期，请重新加载' }])
  try {
    await assert.rejects(session.historyPage('codex', '/p', 'id', 'old-cursor'), /快照已过期/)
    assert.equal(sent[0].cursor, 'old-cursor')
  } finally { session.close() }
})
