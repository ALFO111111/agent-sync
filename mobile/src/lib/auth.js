const BASE = 'https://agent-sync.yirenzaowusuo.cn/api'

export function usernameValid(u) {
  const s = String(u || '').trim().toLowerCase()
  return /^[a-z0-9_.-]{3,32}$/.test(s) && !['admin', 'root', 'system'].includes(s)
}

export function passwordValid(p) {
  return typeof p === 'string' && p.length >= 10 && p.length <= 128
}

async function post(path, body) {
  const res = await fetch(BASE + path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(data.message || `HTTP ${res.status}`)
  return data
}

export const login = (username, password) => post('/v1/auth/login', { username, password })
export const mqttToken = (sessionToken, deviceId, hostId, role, scope) =>
  post('/v1/mqtt/token', { sessionToken, deviceId, hostId, role, scope })
