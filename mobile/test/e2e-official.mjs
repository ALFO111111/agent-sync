// 官方模式端到端：注册/登录 → 配对 token → 本地批准 → 设备登记 → 正式 token → echo/附件
import fs from 'node:fs'
import { execFileSync } from 'node:child_process'
import { newDevice, fingerprint, pair, reconnect } from '../src/lib/protocol.js'
import { login, mqttToken } from '../src/lib/auth.js'

const [, , invPath, hostBin, stateDir, broker, username, password] = process.argv
if (!invPath || !hostBin || !stateDir || !broker || !username || !password) {
  console.error('usage: node test/e2e-official.mjs <inv> <host> <state> <broker> <user> <pass>')
  process.exit(2)
}

const inv = JSON.parse(fs.readFileSync(invPath, 'utf8').trim().split('\n').pop())
const devPath = process.env.E2E_DEVICE || 'D:/tmp/e2e-official-device.json'
let dev
try {
  dev = JSON.parse(fs.readFileSync(devPath, 'utf8'))
} catch {
  dev = newDevice()
  fs.writeFileSync(devPath, JSON.stringify(dev))
}
const did = fingerprint(dev.publicKey)
const acct = await login(username, password)
console.log('LOGIN', acct.accountId)

const onPending = id => {
  console.log('PENDING', id)
  execFileSync(hostBin, ['approve', id, '-state', stateDir], { stdio: 'inherit' })
}

const pairCred = await mqttToken(acct.sessionToken, did, inv.hostId, 'phone', 'pair')
const s1 = await pair(inv, dev, 'official-phone', onPending, {
  url: broker, username: pairCred.username, password: pairCred.password,
})
console.log('READY', s1.ready.hostId)
s1.close()

const entry = {
  hostId: inv.hostId,
  uid: inv.uid || 'local',
  broker,
  name: s1.ready.hostName || '电脑',
  deviceId: did,
  e2eeKey: inv.contentKey,
  seq: s1.link.seq,
}
const sessCred = await mqttToken(acct.sessionToken, did, inv.hostId, 'phone', 'session')
const s2 = await reconnect(entry, dev, onPending, {
  url: broker, username: sessCred.username, password: sessCred.password,
})
const echoed = await s2.echo('official-hello')
if (echoed !== 'official-hello') throw new Error(`echo mismatch: ${echoed}`)
console.log('ECHO', echoed)
const attachId = await s2.uploadAttachment({
  name: 'official.txt', mediaType: 'text/plain', kind: 'text',
  data: new TextEncoder().encode('official attachment'),
})
console.log('ATTACH', attachId)
s2.close()
