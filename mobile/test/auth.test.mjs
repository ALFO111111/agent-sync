import test from 'node:test'
import assert from 'node:assert/strict'
import { usernameValid, passwordValid } from '../src/lib/auth.js'

test('username rules', () => {
  assert.equal(usernameValid('alice_1'), true)
  assert.equal(usernameValid('admin'), false)
  assert.equal(usernameValid('a b'), false)
})

test('password rules', () => {
  assert.equal(passwordValid('1234567890'), true)
  assert.equal(passwordValid('short'), false)
})
