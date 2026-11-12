// 设备私钥与配对信息的持久化。
// 原生端走 SecureStore（Android Keystore）；网页调试退回 localStorage 并在界面标注不安全。
import { Capacitor, registerPlugin } from '@capacitor/core'

const SecureStore = registerPlugin('SecureStore')
const prefix = 'ags:'

export function storageIsSecure() {
  return Capacitor.isNativePlatform()
}

export async function secureGet(key) {
  if (Capacitor.isNativePlatform()) {
    try {
      const res = await SecureStore.get({ key })
      return res?.value ?? null
    } catch {
      return null
    }
  }
  return localStorage.getItem(prefix + key)
}

export async function secureSet(key, value) {
  if (Capacitor.isNativePlatform()) {
    await SecureStore.set({ key, value })
    return
  }
  localStorage.setItem(prefix + key, value)
}

export async function secureRemove(key) {
  if (Capacitor.isNativePlatform()) {
    try {
      await SecureStore.remove({ key })
    } catch {
      // 忽略
    }
    return
  }
  localStorage.removeItem(prefix + key)
}
