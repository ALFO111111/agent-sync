export const LATEST_URL = 'https://api.github.com/repos/ALFO111111/agent-sync/releases/latest'
export const DAY_MS = 24 * 60 * 60 * 1000
const ROOT = 'https://github.com/ALFO111111/agent-sync/releases'
const VERSION = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?![\s\S])/
const STORE_KEY = 'ags:updates:v1'

export function parseVersion(value) {
  if (typeof value !== 'string' || !VERSION.test(value)) return null
  return value.split('.').map(BigInt)
}

export function compareVersions(a, b) {
  const left = parseVersion(a), right = parseVersion(b)
  if (!left || !right) throw new Error('版本号格式不正确')
  for (let i = 0; i < 3; i++) {
    if (left[i] !== right[i]) return left[i] > right[i] ? 1 : -1
  }
  return 0
}

export function releaseLinks(version) {
  if (!parseVersion(version)) throw new Error('版本号格式不正确')
  const filename = `agent-sync-${version}-android.apk`
  return { filename, pageUrl: `${ROOT}/tag/v${version}`, downloadUrl: `${ROOT}/download/v${version}/${filename}` }
}

export function isTrustedUpdateUrl(url) {
  if (typeof url !== 'string') return false
  const match = url.match(/^https:\/\/github\.com\/ALFO111111\/agent-sync\/releases\/(?:tag\/v([^/]+)|download\/v([^/]+)\/([^/]+))$/)
  if (!match) return false
  const version = match[1] || match[2]
  if (!parseVersion(version)) return false
  const links = releaseLinks(version)
  return url === links.pageUrl || url === links.downloadUrl
}

export function parseRelease(data) {
  if (!data || data.draft || data.prerelease) throw new Error('尚无可用的正式版本')
  const version = typeof data.tag_name === 'string' && data.tag_name.startsWith('v') ? data.tag_name.slice(1) : ''
  const links = releaseLinks(version)
  if (data.html_url !== links.pageUrl) throw new Error('更新地址不可信，已拒绝打开')
  const asset = Array.isArray(data.assets) && data.assets.find(item => item.name === links.filename)
  if (asset && asset.browser_download_url !== links.downloadUrl) throw new Error('安装包地址不可信，已拒绝打开')
  return { version, notes: typeof data.body === 'string' ? data.body.slice(0, 20000) : '', ...links, hasApk: !!asset }
}

export async function fetchLatest({ fetchImpl = globalThis.fetch, timeoutMs = 12000, signal } = {}) {
  const controller = new AbortController()
  const abort = () => controller.abort()
  signal?.addEventListener('abort', abort, { once: true })
  if (signal?.aborted) controller.abort()
  let timeout
  let timedOut = false
  const cancelled = new Promise((_, reject) => {
    controller.signal.addEventListener('abort', () => reject(new Error(timedOut ? '检查超时，请稍后重试' : '检查已取消')), { once: true })
    if (controller.signal.aborted) reject(new Error('检查已取消'))
  })
  timeout = setTimeout(() => { timedOut = true; controller.abort() }, timeoutMs)
  try {
    return await Promise.race([(async () => {
      const response = await fetchImpl(LATEST_URL, { signal: controller.signal, headers: { Accept: 'application/vnd.github+json' }, redirect: 'error', credentials: 'omit' })
      if (response.status === 403 || response.status === 429) throw new Error('GitHub 请求限流，请稍后重试')
      if (!response.ok) throw new Error(`检查失败（HTTP ${response.status}）`)
      return parseRelease(await response.json())
    })(), cancelled])
  } catch (error) {
    if (controller.signal.aborted) throw new Error(timedOut ? '检查超时，请稍后重试' : '检查已取消')
    throw new Error(error?.message || '网络不可用，请稍后重试')
  } finally {
    clearTimeout(timeout)
    signal?.removeEventListener('abort', abort)
  }
}

function readStore(storage) {
  try {
    const value = JSON.parse(storage?.getItem(STORE_KEY) || '{}')
    return value && typeof value === 'object' ? value : {}
  } catch { return {} }
}

// 注入时钟、网络和存储，保持与 Vue / 原生插件无关，方便直接测试。
export function createUpdateChecker({ currentVersion, storage, now = Date.now, fetchRelease = fetchLatest, onChange = () => {} }) {
  const saved = readStore(storage)
  const timestamp = value => Number.isFinite(value) && value >= 0 ? value : 0
  const state = {
    auto: saved.auto !== false, lastSuccess: timestamp(saved.lastSuccess), retryAt: timestamp(saved.retryAt),
    failures: Number.isInteger(saved.failures) && saved.failures > 0 ? Math.min(saved.failures, 10) : 0,
    skipped: parseVersion(saved.skipped) ? saved.skipped : '', status: 'idle', message: '', release: null,
  }
  let pending = null
  let disposed = false
  const controller = new AbortController()
  const publish = () => { if (!disposed) onChange({ ...state }) }
  const save = () => {
    try { storage?.setItem(STORE_KEY, JSON.stringify({ auto: state.auto, lastSuccess: state.lastSuccess, retryAt: state.retryAt, failures: state.failures, skipped: state.skipped })) } catch { /* 存储不可用不影响更新 */ }
  }
  function check({ manual = false } = {}) {
    if (disposed) return Promise.resolve(null)
    if (pending) return pending
    if (!manual && (!state.auto || (state.lastSuccess && now() - state.lastSuccess < DAY_MS) || now() < state.retryAt)) return Promise.resolve(null)
    state.status = 'checking'; state.message = '正在检查更新…'; publish()
    pending = Promise.resolve().then(async () => {
      try {
        if (!parseVersion(currentVersion)) throw new Error('无法读取当前安装版本，请重新启动后再检查')
        const release = await fetchRelease({ signal: controller.signal })
        if (disposed) return null
        state.lastSuccess = now(); state.retryAt = 0; state.failures = 0
        const newer = compareVersions(release.version, currentVersion) > 0
        state.release = newer ? release : null
        state.status = !newer ? 'latest' : !manual && state.skipped === release.version ? 'skipped' : release.hasApk ? 'available' : 'no-apk'
        state.message = !newer ? '当前已是最新正式版' : state.status === 'skipped' ? `已暂缓 v${release.version}，可手动检查重新查看` : !release.hasApk ? `发现 v${release.version}，Android 安装包尚未发布` : `发现新版本 v${release.version}`
        save(); publish()
        return release
      } catch (error) {
        if (disposed) return null
        state.failures = Math.min(state.failures + 1, 10)
        state.retryAt = now() + Math.min(15 * 60 * 1000 * 2 ** (state.failures - 1), DAY_MS)
        state.status = 'error'; state.message = error?.message || '检查更新失败'; save(); publish()
        return null
      } finally { pending = null }
    })
    return pending
  }
  publish()
  return {
    check,
    setAuto(value) { state.auto = !!value; save(); publish() },
    skip() {
      if (!state.release) return
      state.skipped = state.release.version; state.status = 'skipped'; state.message = `已暂缓 v${state.skipped}，可手动检查重新查看`; save(); publish()
    },
    dispose() { disposed = true; controller.abort() },
  }
}
