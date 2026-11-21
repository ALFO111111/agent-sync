import { test } from 'node:test'
import assert from 'node:assert/strict'
import { isStatusTopic, parseHostStatus } from '../src/lib/status.js'

test('status topic is recognised separately from encrypted frames', () => {
  assert.equal(isStatusTopic('u/a/h/h1/status'), true)
  assert.equal(isStatusTopic('u/a/h/h1/evt'), false)
})

test('host online status is plaintext JSON', () => {
  assert.equal(parseHostStatus('{"online":true}'), true)
  assert.equal(parseHostStatus('{"online":false}'), false)
  assert.equal(parseHostStatus('not-json'), null)
  assert.equal(parseHostStatus('{"online":"yes"}'), null)
})
