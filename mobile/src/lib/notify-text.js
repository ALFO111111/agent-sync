// 通知文案（纯函数，Node 可直接测试）。
export function notificationText(kind, ev, hostName) {
  const host = hostName || '电脑'
  if (kind === 'approval') {
    return { title: `${host}：等待批准`, body: `${ev.tool || '工具'}：${(ev.text || '').slice(0, 120)}` }
  }
  const preview = String(ev.text || '').replace(/\s+/g, ' ').slice(0, 120)
  return { title: `${host}：回复完成`, body: preview || '可以回来看结果了' }
}
