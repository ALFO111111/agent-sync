// Android 本地通知：回合完成、等待批准时提醒；不接第三方/付费推送。
import { LocalNotifications } from '@capacitor/local-notifications'
import { notificationText } from './notify-text.js'

let granted = false
let nextID = 1

export async function ensureNotifyPermission() {
  if (granted) return true
  try {
    let res = await LocalNotifications.checkPermissions()
    if (res.display !== 'granted') res = await LocalNotifications.requestPermissions()
    granted = res.display === 'granted'
  } catch {
    granted = false
  }
  return granted
}

export async function notify(kind, ev, hostName) {
  if (!(await ensureNotifyPermission())) return
  const { title, body } = notificationText(kind, ev, hostName)
  try {
    await LocalNotifications.schedule({
      notifications: [{ id: nextID++, title, body }],
    })
  } catch {
    // 通知失败不影响聊天
  }
}
