import test from 'node:test'
import assert from 'node:assert/strict'
import { compareVersions, parseVersion, releaseLinks, parseRelease, isTrustedUpdateUrl, fetchLatest, createUpdateChecker, DAY_MS, LATEST_URL } from '../src/lib/update.js'

function release(version = '0.6.2', extra = {}) {
  const links = releaseLinks(version)
  return { tag_name: `v${version}`, html_url: links.pageUrl, body: '<script>alert(1)</script>\n版本说明', assets: [{ name: links.filename, browser_download_url: links.downloadUrl }], ...extra }
}
function harness(options = {}) {
  let time = DAY_MS * 10, state, calls = 0
  const values = new Map()
  const storage = { getItem: key => values.get(key), setItem: (key, value) => values.set(key, value) }
  const checker = createUpdateChecker({ currentVersion: '0.6.1', storage, now: () => time,
    fetchRelease: async () => { calls++; return parseRelease(release()) },
    ...options, onChange: next => { state = next },
  })
  return { checker, storage, get state() { return state }, get calls() { return calls }, advance: ms => { time += ms } }
}

test('数字三段比较，不按字符串排序；拒绝预发布及非法版本', () => {
  assert.equal(compareVersions('0.10.0', '0.9.99'), 1)
  assert.equal(compareVersions('1.0.0', '1.0.1'), -1)
  assert.equal(compareVersions('0.6.1', '0.6.1'), 0)
  for (const bad of ['1.2', 'v1.2.3', '1.2.3-beta', '01.2.3', '1.2.3\n', '', null]) assert.equal(parseVersion(bad), null)
  assert.throws(() => compareVersions('bad', '0.6.1'))
})

test('只取正式版本、精确 APK，说明保留为纯文本', () => {
  const result = parseRelease(release())
  assert.equal(result.hasApk, true)
  assert.equal(result.notes, '<script>alert(1)</script>\n版本说明')
  assert.throws(() => parseRelease(release('0.6.2', { prerelease: true })))
  assert.throws(() => parseRelease(release('0.6.2', { draft: true })))
  assert.throws(() => parseRelease(release('0.6.2', { tag_name: 'v0.6.2-beta' })))
  for (const name of ['agent-sync-v0.6.2-android.apk', 'agent-sync-0.6.1-android.apk', 'agent-sync-0.6.2-android-debug.apk', 'other.apk']) {
    assert.equal(parseRelease(release('0.6.2', { assets: [{ name }] })).hasApk, false)
  }
})

test('严格拒绝非官方链接、查询参数、编码、重定向目标与错版本 APK', () => {
  const links = releaseLinks('0.6.2')
  assert.equal(isTrustedUpdateUrl(links.pageUrl), true)
  assert.equal(isTrustedUpdateUrl(links.downloadUrl), true)
  for (const url of [links.downloadUrl + '?x=1', links.pageUrl + '#x', links.pageUrl + '\n', links.pageUrl.replace('https:', 'http:'), links.pageUrl.replace('github.com', 'github.com.evil.test'), links.pageUrl.replace('github.com', 'evil@github.com'), links.pageUrl.replace('v0.6.2', 'v0.6.2-beta'), links.downloadUrl.replace('agent-sync-0.6.2', 'agent-sync-0.6.1'), 'javascript:alert(1)', 'https://github.com/ALFO111111/agent-sync/releases/tag/v%30.6.2']) assert.equal(isTrustedUpdateUrl(url), false, url)
  assert.throws(() => parseRelease(release('0.6.2', { html_url: 'https://evil.test' })), /不可信/)
  assert.throws(() => parseRelease(release('0.6.2', { assets: [{ name: links.filename, browser_download_url: 'https://evil.test/app.apk' }] })), /不可信/)
})

test('仅请求官方 latest API，并处理 HTTP、限流、JSON 与网络错误', async () => {
  const result = await fetchLatest({ fetchImpl: async (url, options) => {
    assert.equal(url, LATEST_URL); assert.equal(options.redirect, 'error'); assert.ok(options.signal)
    return { ok: true, json: async () => release() }
  } })
  assert.equal(result.version, '0.6.2')
  for (const status of [403, 429, 404, 500]) {
    await assert.rejects(fetchLatest({ fetchImpl: async () => ({ ok: false, status }) }), status === 403 || status === 429 ? /限流/ : /HTTP/)
  }
  await assert.rejects(fetchLatest({ fetchImpl: async () => { throw new Error('offline') } }), /offline/)
  await assert.rejects(fetchLatest({ fetchImpl: async () => ({ ok: true, json: async () => { throw new Error('invalid JSON') } }) }), /JSON/)
})

test('超时主动 abort，即使网络不响应取消也能退出', async () => {
  let signal
  await assert.rejects(fetchLatest({ timeoutMs: 5, fetchImpl: async (_, options) => { signal = options.signal; return new Promise(() => {}) } }), /超时/)
  assert.equal(signal.aborted, true)
})

test('并发请求去重，成功后自动检查严格节流 24 小时', async () => {
  const h = harness()
  const first = h.checker.check()
  assert.equal(h.checker.check({ manual: true }), first)
  await first
  assert.equal(h.calls, 1)
  assert.equal(h.state.status, 'available')
  h.advance(DAY_MS - 1); await h.checker.check(); assert.equal(h.calls, 1)
  h.advance(1); await h.checker.check(); assert.equal(h.calls, 2)
})

test('以后再说保存跳过版本，手动检查绕过跳过、24h 与关闭自动检查', async () => {
  const h = harness()
  await h.checker.check(); h.checker.skip(); h.checker.setAuto(false)
  assert.equal(h.state.status, 'skipped')
  await h.checker.check(); assert.equal(h.calls, 1)
  await h.checker.check({ manual: true }); assert.equal(h.state.status, 'available'); assert.equal(h.calls, 2)
  h.advance(DAY_MS); h.checker.setAuto(true)
  await h.checker.check(); assert.equal(h.state.status, 'skipped')
  const restored = harness({ storage: h.storage })
  await restored.checker.check(); assert.equal(restored.calls, 0)
})

test('失败指数退避，手动检查可绕过，成功清除失败计数', async () => {
  let calls = 0
  const h = harness({ fetchRelease: async () => { calls++; if (calls <= 2) throw new Error('offline'); return parseRelease(release()) } })
  await h.checker.check(); assert.equal(h.state.status, 'error')
  h.advance(15 * 60 * 1000 - 1); await h.checker.check(); assert.equal(calls, 1)
  h.advance(1); await h.checker.check(); assert.equal(calls, 2)
  h.advance(15 * 60 * 1000); await h.checker.check(); assert.equal(calls, 2)
  await h.checker.check({ manual: true }); assert.equal(calls, 3); assert.equal(h.state.failures, 0)
})

test('无 APK 不能下载；相等或更老版本不提示升级', async () => {
  for (const [version, status] of [['0.6.2', 'no-apk'], ['0.6.1', 'latest'], ['0.6.0', 'latest']]) {
    const h = harness({ fetchRelease: async () => parseRelease(release(version, { assets: [] })) })
    await h.checker.check({ manual: true }); assert.equal(h.state.status, status)
  }
})

test('localStorage 异常不影响检查；未知安装版本不发请求', async () => {
  const storage = { getItem() { throw new Error('blocked') }, setItem() { throw new Error('quota') } }
  const h = harness({ storage }); await h.checker.check(); assert.equal(h.state.status, 'available')
  const unknown = harness({ currentVersion: '' }); await unknown.checker.check(); assert.equal(unknown.calls, 0); assert.equal(unknown.state.status, 'error')
})

test('清理中止在途请求，不再发布状态或新建请求', async () => {
  let signal, finish
  const h = harness({ fetchRelease: options => { signal = options.signal; return new Promise(resolve => { finish = resolve }) } })
  const pending = h.checker.check(); await Promise.resolve()
  h.checker.dispose(); assert.equal(signal.aborted, true)
  finish(parseRelease(release())); await pending
  assert.equal(h.state.status, 'checking')
  assert.equal(await h.checker.check({ manual: true }), null)
})
