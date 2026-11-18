// 与 Go 的 internal/remotev1/messagecrypto.go 一致：
// AES-256-GCM、每条消息随机 12 字节 nonce、AAD 绑定方向/发送方/序号。
import { gcm } from '@noble/ciphers/aes.js'

export const MAX_PLAIN = 64 * 1024
const TAG = 16

const aadFor = (dir, from, seq) =>
  new TextEncoder().encode(`agent-sync-e2ee-v3|${dir}|${from}|${seq}`)

export function parseKey(b64) {
  const raw = Uint8Array.from(atob(b64), c => c.charCodeAt(0))
  if (raw.length !== 32) throw new Error('invalid key')
  return raw
}

// newKey 生成 32 字节随机密钥（base64），用于每台手机自己的会话密钥。
export function newKey() {
  const raw = crypto.getRandomValues(new Uint8Array(32))
  let s = ''
  for (const b of raw) s += String.fromCharCode(b)
  return btoa(s)
}

const b64 = bytes => {
  let s = ''
  for (let i = 0; i < bytes.length; i += 0x8000) {
    s += String.fromCharCode.apply(null, bytes.subarray(i, i + 0x8000))
  }
  return btoa(s)
}

const unb64 = str => Uint8Array.from(atob(str), c => c.charCodeAt(0))

export function seal(key, dir, from, seq, plain) {
  if (!key || key.length !== 32 || !plain.length || plain.length > MAX_PLAIN || !from) {
    throw new Error('frame rejected')
  }
  const nonce = crypto.getRandomValues(new Uint8Array(12))
  const ct = gcm(key, nonce, aadFor(dir, from, seq)).encrypt(plain)
  return { v: 3, from, seq, nonce: b64(nonce), data: b64(ct) }
}

export function open(key, dir, from, seq, frame) {
  if (!key || key.length !== 32 || frame.v !== 3 || frame.from !== from || frame.seq !== seq) {
    throw new Error('frame rejected')
  }
  const nonce = unb64(frame.nonce)
  const ct = unb64(frame.data)
  if (nonce.length !== 12 || ct.length < TAG) throw new Error('frame rejected')
  const plain = gcm(key, nonce, aadFor(dir, from, seq)).decrypt(ct)
  if (!plain.length || plain.length > MAX_PLAIN) throw new Error('frame rejected')
  return plain
}
