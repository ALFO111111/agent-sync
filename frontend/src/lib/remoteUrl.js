export function normalizeBroker(raw) {
  const url = String(raw || '').trim()
  if (!url) return ''
  if (!url.startsWith('wss://') && !url.startsWith('ws://')) {
    throw new Error('Broker 必须使用 wss:// 或 ws://')
  }
  return url
}
