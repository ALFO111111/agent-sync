import test from 'node:test'
import assert from 'node:assert/strict'
import { Session } from '../src/lib/protocol.js'

function client(replyFor) {
  const sent = []
  let link
  link = {
    sent,
    send(message) {
      sent.push(message)
      link.pending = replyFor(message)
    },
    async until(predicate) {
      const message = link.pending
      assert.ok(predicate(message), `predicate rejected ${JSON.stringify(message)}`)
      return message
    },
    close() {},
  }
  return link
}

test('sendTurn waits for a turn.ack with the same id', async () => {
  const link = client(msg => ({ type: 'turn.ack', id: msg.id, sessionKey: msg.sessionKey }))
  const session = new Session(link, { features: [] })
  await session.sendTurn('sk', 'hello')
  session.close()
  assert.equal(link.sent[0].type, 'turn.send')
  assert.ok(link.sent[0].id)
})

test('sendTurn rejects on a matching session.error and keeps the error out of the queue', async () => {
  const link = client(msg => ({ type: 'session.error', id: msg.id, error: 'session is closed' }))
  const session = new Session(link, { features: [] })
  await assert.rejects(() => session.sendTurn('sk', 'hello'), /session is closed/)
  session.close()
})

test('approval decisions wait for approval.ack', async () => {
  const link = client(msg => ({ type: 'approval.ack', id: msg.id, sessionKey: msg.sessionKey }))
  const session = new Session(link, { features: [] })
  await session.decide('sk', 'a1', true)
  session.close()
  assert.deepEqual(
    { type: link.sent[0].type, approvalId: link.sent[0].approvalId, allow: link.sent[0].allow },
    { type: 'approval.decide', approvalId: 'a1', allow: true },
  )
})
