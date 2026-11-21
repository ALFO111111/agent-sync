// 电脑在线状态是 retained 明文：{"online":true|false}。
// 它不加密，不能和业务帧一起解密，必须先按 topic 分流。
export function isStatusTopic(topic) {
  return typeof topic === 'string' && topic.endsWith('/status')
}

export function parseHostStatus(payload) {
  try {
    const text = typeof payload === 'string' ? payload : new TextDecoder().decode(payload)
    const v = JSON.parse(text)
    return typeof v?.online === 'boolean' ? v.online : null
  } catch {
    return null
  }
}
