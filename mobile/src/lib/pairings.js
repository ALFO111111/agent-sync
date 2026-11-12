// 多台电脑的配对信息：纯函数，方便测试；读写 SecureStore 在 App 里做。

// hostKey 稳定标识一台电脑：优先用邀请里的 hostId，旧数据没有就用 route。
export const hostKey = p => p?.hostId || p?.route || p?.url || ''

// upsertPairing 同一台电脑只保留一条（重新配对会更新中转凭据/加密密钥）。
export function upsertPairing(list, entry) {
  const key = hostKey(entry)
  return [...list.filter(p => hostKey(p) !== key), entry]
}

// pickActive 按保存的 hostKey 找当前电脑；找不到就退回第一台。
export function pickActive(list, key) {
  return list.find(p => hostKey(p) === key) || list[0] || null
}
