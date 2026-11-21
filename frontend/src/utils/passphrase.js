// 同步口令工具：最短长度与随机生成（本地生成，不经过网络）。
export const PASSPHRASE_MIN = 12

const ALPHABET = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789-_'

// generatePassphrase 用浏览器加密随机数生成 20 位口令。
export function generatePassphrase(length = 20) {
  const out = []
  const limit = 256 - (256 % ALPHABET.length)
  const buf = new Uint8Array(32)
  while (out.length < length) {
    crypto.getRandomValues(buf)
    for (const b of buf) {
      if (b >= limit) continue
      out.push(ALPHABET[b % ALPHABET.length])
      if (out.length === length) break
    }
  }
  return out.join('')
}
