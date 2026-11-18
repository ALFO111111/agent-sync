import { test } from 'node:test'
import assert from 'node:assert/strict'
import { notificationText } from '../src/lib/notify-text.js'

test('turn done notification extracts a short preview', () => {
  const { title, body } = notificationText('result', { text: '已修改 3 个文件' }, '书房电脑')
  assert.equal(title, '书房电脑：回复完成')
  assert.match(body, /已修改 3 个文件/)
})

test('approval notification names the tool', () => {
  const { title, body } = notificationText('approval', { tool: 'Bash', text: 'npm test' }, '书房电脑')
  assert.equal(title, '书房电脑：等待批准')
  assert.match(body, /Bash/)
})
