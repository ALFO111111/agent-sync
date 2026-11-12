// 与 Go 的 internal/remotev1/crypto.go 完全一致的帧加密：
// AES-256-GCM，nonce = 8 字节随机前缀 + 4 字节递增计数器，AAD 区分方向。
import { gcm } from '@noble/ciphers/aes.js'

export const MAX_PLAIN = 11 * 1024
const TAG = 16

const aadFor = dir => new TextEncoder().encode(`agent-sync-e2ee-v1|${dir}`)

export function parseKey(b64) {
  const raw = Uint8Array.from(atob(b64), c => c.charCodeAt(0))
  if (raw.length !== 32) throw new Error('invalid key')
  return raw
}

export class Sealer {
  constructor(key, dir) {
    if (!key || key.length !== 32) throw new Error('invalid key')
    this.key = key
    this.aad = aadFor(dir)
    this.prefix = crypto.getRandomValues(new Uint8Array(8))
    this.counter = 0
  }

  // 返回 nonce || 密文 || tag。
  seal(plain) {
    if (!plain.length || plain.length > MAX_PLAIN || this.counter >= 0xffffffff) {
      throw new Error('frame rejected')
    }
    const nonce = new Uint8Array(12)
    nonce.set(this.prefix, 0)
    new DataView(nonce.buffer).setUint32(8, this.counter)
    this.counter++
    const ct = gcm(this.key, nonce, this.aad).encrypt(plain)
    const out = new Uint8Array(12 + ct.length)
    out.set(nonce, 0)
    out.set(ct, 12)
    return out
  }
}

export class Opener {
  constructor(key, dir) {
    if (!key || key.length !== 32) throw new Error('invalid key')
    this.key = key
    this.aad = aadFor(dir)
    this.prefix = null
    this.counter = 0
  }

  open(frame) {
    if (frame.length < 12 + TAG || frame.length > 12 + MAX_PLAIN + TAG || this.counter >= 0xffffffff) {
      throw new Error('frame rejected')
    }
    const prefix = frame.subarray(0, 8)
    if (!this.prefix) {
      this.prefix = prefix.slice()
    } else if (!this.prefix.every((b, i) => b === prefix[i])) {
      throw new Error('frame rejected')
    }
    const ctr = new DataView(frame.buffer, frame.byteOffset + 8, 4).getUint32(0)
    if (ctr !== this.counter) throw new Error('frame rejected')
    const plain = gcm(this.key, frame.subarray(0, 12), this.aad).decrypt(frame.subarray(12))
    if (!plain.length || plain.length > MAX_PLAIN) throw new Error('frame rejected')
    this.counter++
    return plain
  }
}
