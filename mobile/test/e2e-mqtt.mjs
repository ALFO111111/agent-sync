// 手动端到端验收：需要先启动 host 和 Broker。
// 用法：node test/e2e-mqtt.mjs <invitation.json> <host-bin> <state-dir> <broker-url>
import fs from 'node:fs'
import { execFileSync } from 'node:child_process'
import { newDevice, pair } from '../src/lib/protocol.js'

const [, , invPath, hostBin, stateDir, url] = process.argv
if (!invPath || !hostBin || !stateDir || !url) {
  console.error('usage: node test/e2e-mqtt.mjs <inv> <host> <state> <url>')
  process.exit(2)
}

const invitation = JSON.parse(fs.readFileSync(invPath, 'utf8').trim().split('\n').pop())
const device = newDevice()
const onPending = id => {
  console.log('PENDING', id)
  execFileSync(hostBin, ['approve', id, '-state', stateDir], { stdio: 'inherit' })
}

const session = await pair(invitation, device, 'e2e-phone', onPending, { url })
console.log('READY', session.ready.hostId)
const echoed = await session.echo('hello-mqtt')
if (echoed !== 'hello-mqtt') throw new Error(`echo mismatch: ${echoed}`)
console.log('ECHO', echoed)
const attachId = await session.uploadAttachment({
  name: 'e2e.txt',
  mediaType: 'text/plain',
  kind: 'text',
  data: new TextEncoder().encode('e2e attachment'),
})
if (!attachId) throw new Error('attachment failed')
console.log('ATTACH', attachId)
session.close()
