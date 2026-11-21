import { Capacitor, registerPlugin } from '@capacitor/core'

// 原生前台服务：有活动会话时让系统尽量不要冻结 WebView/MQTT。
const KeepAlive = registerPlugin('KeepAlive')

export async function startKeepAlive() {
  if (!Capacitor.isNativePlatform()) return
  try {
    await KeepAlive.start()
  } catch {
    // 服务启动失败不影响聊天，只是锁屏后可能更容易断线。
  }
}

export async function stopKeepAlive() {
  if (!Capacitor.isNativePlatform()) return
  try {
    await KeepAlive.stop()
  } catch {
    // 忽略：系统可能已经回收了服务
  }
}
