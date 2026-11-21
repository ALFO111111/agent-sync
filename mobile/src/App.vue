<script setup>
import { computed, nextTick, onMounted, onUnmounted, ref } from 'vue'
import { App as CapApp } from '@capacitor/app'
import { Capacitor, registerPlugin } from '@capacitor/core'
import AboutUpdates from './components/AboutUpdates.vue'
import mobilePackage from '../package.json'
import { createUpdateChecker, isTrustedUpdateUrl } from './lib/update.js'
import { Camera, CameraResultType, CameraSource } from '@capacitor/camera'
import { BarcodeScanner, BarcodeFormat } from '@capacitor-mlkit/barcode-scanning'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import Icon from './components/Icon.vue'
import ConnectPanel from './components/ConnectPanel.vue'
import {
  fingerprint,
  initCrypto,
  newDevice,
  pair,
  parseDeepLink,
  reconnect,
} from './lib/protocol.js'
import { createSeqGate } from './lib/seq.js'
import { shouldSendOnEnter, shouldStickToBottom, nextStick } from './lib/scroll.js'
import { ensureNotifyPermission, notify as localNotify } from './lib/notify.js'
import { secureGet, secureRemove, secureSet, storageIsSecure } from './lib/storage.js'
import { login as authLogin, mqttToken, passwordValid, usernameValid } from './lib/auth.js'
import { hostKey, pickActive, upsertPairing } from './lib/pairings.js'

const state = ref('idle')
const message = ref('')
const logs = ref([])
const toast = ref('')
const appActive = ref(true)
let toastTimer
const invitationText = ref('')
const echoText = ref('hello')
const device = ref(null)
const pairing = ref(null)
const pairings = ref([])
const conn = ref({ mode: 'official', url: 'wss://agent-sync.yirenzaowusuo.cn/mqtt', username: '', password: '' })
const auth = ref({ accountId: '', sessionToken: '', username: '', password: '' })
const authBusy = ref(false)
const ready = ref(null)
const devices = ref([])
const busy = ref(false)
let session = null

const deviceName = ref('Android')

// 会话（v2 只读）
const projects = ref([])
const sessions = ref([])
const currentProject = ref(null)
const chatOpen = ref(false)
const sessionKey = ref('')
const chat = ref([])
const historyMessages = ref([])
const historyLoading = ref(false)
const historyError = ref('')
const historyCursor = ref('')
const historyMore = ref(false)
let historyTarget = null
let historyGeneration = 0
const displayedChat = computed(() => [...historyMessages.value, ...chat.value])

let msgID = 0
const nextMsgID = () => `m${Date.now().toString(36)}-${++msgID}`
const CHAT_KEEP = 400
const CHAT_MAX = 600
function pushMsg(m) {
  chat.value.push({ id: nextMsgID(), ...m })
  if (chat.value.length > CHAT_MAX) chat.value.splice(0, chat.value.length - CHAT_KEEP)
}

let historyMsgID = 0
const withHistoryIDs = msgs => msgs.map(m => (m.id ? m : { id: `h${++historyMsgID}`, ...m }))
const chatInput = ref('')
const attachments = ref([]) // {attachId,name,size,kind}
const attachBusy = ref(false)
const attachProgress = ref(0)
const MAX_ATTACH = 3
const MAX_ATTACH_BYTES = 10 * 1024 * 1024
let currentAssistant = null
let currentThinking = null
let typeTimer = null
let pendingText = ''
let claudeSessionId = ""
const seqGate = createSeqGate()
let reconnecting = false
let manualClose = false
let reconnectEpoch = 0
const mode = ref("dontAsk")
const tab = ref("sessions")
const sessionView = ref("projects") // projects | project
const currentSession = ref(null)
const generating = ref(false)
const thinking = ref(false)
const showPerm = ref(false)
const showPlus = ref(false)
const scanning = ref(false)
const projectsLoading = ref(false)
const chatInputEl = ref(null)
const overview = ref(null)
const overviewBusy = ref(false)
const overviewErr = ref("")
const theme = ref((() => { try { return localStorage.getItem('ags:theme') || 'system' } catch { return 'system' } })())
const msgBox = ref(null)
const stickToBottom = ref(true)
let programmaticScroll = false
let userScrollUntil = 0
const filesOpen = ref(false)
const fsCwd = ref("")
const fsPath = ref("")
const fsEntries = ref([])
const fsView = ref("list")
const fsTitle = ref("")
const fsText = ref("")
const fsChanges = ref([])
const fsTruncated = ref(false)
const fsBinary = ref(false)
const tool = ref("claude")
const toolLabels = { claude: "Claude Code", codex: "Codex" }

const statusLabel = computed(
  () =>
    ({
      idle: '未连接',
      connecting: '正在连接',
      pending: '等待电脑批准',
      connected: '已连接',
      error: '出错',
      reconnecting: '正在重连',
    })[state.value],
)
const paired = computed(() => !!pairing.value)
const secure = computed(() => storageIsSecure())
const nativePlatform = computed(() => Capacitor.isNativePlatform())
const versionLabel = ref(Capacitor.isNativePlatform() ? '正在读取安装版本…' : `网页调试版 ${mobilePackage.version}`)
const update = ref({ auto: true, status: 'idle', message: '', release: null })
const openingUpdate = ref(false)
const ExternalLinks = registerPlugin('ExternalLinks')
let updateChecker
let updateTimer
let updateForegroundListener
let updateDisposed = false
let updateBooting = true
let updateBackgrounded = false

function checkUpdate() { return updateChecker?.check({ manual: true }) }
function setAutoUpdate(value) { updateChecker?.setAuto(value) }
function skipUpdate() { updateChecker?.skip() }
async function openUpdate(url) {
  if (!isTrustedUpdateUrl(url)) { notify('更新地址不可信，已拒绝打开'); return }
  if (openingUpdate.value) return
  openingUpdate.value = true
  try {
    if (Capacitor.isNativePlatform()) await ExternalLinks.open({ url })
    else window.open(url, '_blank', 'noopener,noreferrer')
  } catch { notify('无法打开浏览器，请稍后重试') }
  finally { openingUpdate.value = false }
}

// 与配对、电脑连接完全独立；版本只信任原生 App.getInfo。
async function initUpdates() {
  if (!Capacitor.isNativePlatform()) return
  let currentVersion = ''
  try {
    const info = await CapApp.getInfo()
    if (updateDisposed) return
    currentVersion = info.version
    versionLabel.value = `安装版本 ${info.version}（build ${info.build}）`
  } catch { versionLabel.value = '无法读取安装版本' }
  if (updateDisposed) return
  let storage
  try { storage = window.localStorage } catch { /* 禁用存储时仍可检查 */ }
  updateChecker = createUpdateChecker({ currentVersion, storage, onChange: value => { update.value = value } })
  updateTimer = setTimeout(() => { updateBooting = false; updateChecker?.check() }, 8000)
  try {
    const handle = await CapApp.addListener('appStateChange', ({ isActive }) => {
      appActive.value = isActive
      if (!isActive) updateBackgrounded = true
      else if (updateBackgrounded) {
        updateBackgrounded = false
        if (!updateBooting) updateChecker?.check()
      }
      // 回到前台且连接已断：立刻重连，不用等下一次心跳。
      if (isActive && pairing.value && state.value !== 'connected' && !reconnecting && !manualClose) {
        void autoReconnect()
      }
    })
    if (updateDisposed) await handle.remove()
    else updateForegroundListener = handle
  } catch { /* 系统不支持前后台监听不影响手动检查 */ }
}

onMounted(() => { void initUpdates() })
onUnmounted(() => {
  updateDisposed = true
  clearTimeout(updateTimer)
  clearTimeout(toastTimer)
  updateChecker?.dispose()
  updateForegroundListener?.remove()
})
const connected = computed(() => state.value === 'connected' && session)

function log(line) {
  const t = new Date().toLocaleTimeString()
  logs.value.unshift(`${t}  ${line}`)
  logs.value = logs.value.slice(0, 60)
}

function notify(text) {
  toast.value = String(text)
  clearTimeout(toastTimer)
  toastTimer = setTimeout(() => { toast.value = '' }, 3600)
}

async function loadOrCreateDevice() {
  const raw = await secureGet('device')
  if (raw) {
    device.value = JSON.parse(raw)
    return
  }
  const created = newDevice()
  device.value = created
  await secureSet('device', JSON.stringify(created))
  log('已生成新的设备身份')
}

const PAIRINGS_KEY = 'pairings'
const ACTIVE_KEY = 'activeHost'

async function savePairingsStore(list, active) {
  await secureSet(PAIRINGS_KEY, JSON.stringify(list))
  await secureSet(ACTIVE_KEY, active ? hostKey(active) : '')
}

// 读取多台电脑；老版本只有一条 pairing，自动迁移过来。
async function loadPairings() {
  const raw = await secureGet(PAIRINGS_KEY)
  if (raw) {
    try {
      const list = JSON.parse(raw)
      pairings.value = list
      pairing.value = pickActive(list, (await secureGet(ACTIVE_KEY)) || '')
      return
    } catch {
      // 数据坏了就当没有
    }
  }
  const old = await secureGet('pairing')
  if (old) {
    try {
      const p = JSON.parse(old)
      pairings.value = [p]
      pairing.value = p
      await savePairingsStore(pairings.value, p)
      await secureRemove('pairing')
      return
    } catch {
      // 忽略
    }
  }
  pairings.value = []
  pairing.value = null
}

function onPending(id) {
  state.value = 'pending'
  message.value = `等待电脑批准（请求 ${id.slice(0, 8)}）`
  log('配对请求已提交，请在电脑上运行 pending 后 approve')
}

async function saveConn() {
  await secureSet('mqttConn', JSON.stringify(conn.value))
  message.value = 'MQTT 连接设置已保存'
  log('MQTT 连接设置已保存')
}

async function saveAuth() {
  await secureSet('authSession', JSON.stringify({
    accountId: auth.value.accountId,
    sessionToken: auth.value.sessionToken,
    username: auth.value.username,
  }))
}

async function loadAuth() {
  try {
    const raw = await secureGet('authSession')
    if (raw) Object.assign(auth.value, JSON.parse(raw))
  } catch {
    // 忽略损坏的登录状态
  }
}

async function submitAuth() {
  if (!usernameValid(auth.value.username)) throw new Error('用户名 3–32 位，只允许字母数字和 _.-')
  if (!passwordValid(auth.value.password)) throw new Error('密码至少 10 位')
  const out = await authLogin(auth.value.username, auth.value.password)
  auth.value.accountId = out.accountId
  auth.value.sessionToken = out.sessionToken
  auth.value.password = ''
  await saveAuth()
  message.value = `登录成功，账号 ${out.username || out.accountId.slice(0, 8)}`
  notify(message.value)
  log(message.value)
}

async function openRegister() {
  const url = 'https://agent-sync.yirenzaowusuo.cn/register'
  try {
    if (Capacitor.isNativePlatform()) await ExternalLinks.open({ url })
    else window.open(url, '_blank', 'noopener,noreferrer')
  } catch {
    notify('无法打开浏览器，请稍后重试')
  }
}

async function submitAuthWrap() {
  authBusy.value = true
  try {
    await submitAuth()
  } catch (err) {
    message.value = err.message || String(err)
    notify(message.value)
    log(message.value)
  } finally {
    authBusy.value = false
  }
}

async function credentialsFor(hostId, did, scope, opts = {}) {
  if (conn.value.mode !== 'official') {
    const saved = opts.saved
    if (saved?.mqttUser && saved?.mqttPassword) {
      return { url: saved.broker || conn.value.url || opts.invitation?.url, username: saved.mqttUser, password: saved.mqttPassword }
    }
    if (scope === 'pair' && opts.invitation?.mqttUser && opts.invitation?.mqttPassword) {
      return { url: opts.invitation.url || conn.value.url, username: opts.invitation.mqttUser, password: opts.invitation.mqttPassword }
    }
    return { url: conn.value.url || opts.invitation?.url, username: conn.value.username, password: conn.value.password }
  }
  const saved = opts.saved
  if (saved?.mqttUser && saved?.mqttPassword) {
    // 0.7.0 旧版保存的是账号级固定凭据（用户名不带点）；新版是 <账号>.<设备> 的 JWT。
    if (!saved.mqttUser.includes('.')) throw new Error('配对凭据已升级，请重新扫码配对')
    return { url: saved.broker || conn.value.url || 'wss://agent-sync.yirenzaowusuo.cn/mqtt', username: saved.mqttUser, password: saved.mqttPassword }
  }
  if (scope === 'pair' && opts.invitation?.mqttUser && opts.invitation?.mqttPassword) {
    return { url: opts.invitation.url || conn.value.url || 'wss://agent-sync.yirenzaowusuo.cn/mqtt', username: opts.invitation.mqttUser, password: opts.invitation.mqttPassword }
  }
  if (!auth.value.sessionToken) throw new Error('请先登录官方账号')
  const t = await mqttToken(auth.value.sessionToken, did, hostId, 'phone', scope)
  return { url: conn.value.url || 'wss://agent-sync.yirenzaowusuo.cn/mqtt', username: t.username, password: t.password }
}

async function doPair(invitation) {
  if (!invitation?.hostId || !invitation?.contentKey) throw new Error('邀请格式不对')
  manualClose = false
  reconnectEpoch++
  if (invitation.mode === 'selfhost') {
    conn.value.mode = 'selfhost'
    if (invitation.url) conn.value.url = invitation.url
    if (invitation.mqttUser) {
      conn.value.username = invitation.mqttUser
      conn.value.password = invitation.mqttPassword || ''
    }
    await saveConn()
  }
  state.value = 'connecting'
  message.value = '正在连接 Broker…'
  const did = fingerprint(device.value.publicKey)
  const creds = await credentialsFor(invitation.hostId, did, 'pair', { invitation })
  session = await pair(invitation, device.value, deviceName.value, onPending, creds, handleLinkClose)
  ready.value = session.ready
  const entry = {
    hostId: invitation.hostId || session.ready.hostId || '',
    uid: invitation.uid || 'local',
    broker: conn.value.url || invitation.url,
    name: session.ready.hostName || '电脑',
    deviceId: did,
    e2eeKey: session.deviceKey || invitation.contentKey,
    pairKey: session.pairKey,
    deviceKey: session.deviceKey || '',
    seq: session.link.seq,
    mqttUser: session.ready.mqttUser || invitation.mqttUser || '',
    mqttPassword: session.ready.mqttPassword || invitation.mqttPassword || '',
  }
  pairings.value = upsertPairing(pairings.value, entry)
  pairing.value = entry
  await savePairingsStore(pairings.value, entry)
  if (conn.value.mode === 'official') {
    // 配对 token 只能访问 pair 主题；换正式 token 重新连接。
    manualClose = true // 这次关闭是主动的，不要触发自动重连循环
    session.close()
    await doReconnect()
    return
  }
  await session.link.switchToSession()
  state.value = 'connected'
  message.value = `已连接到 ${pairing.value.name}`
  log('配对成功，已保存设备授权')
  void ensureNotifyPermission()
  loadOverview()
  loadProjects()
  tab.value = 'sessions'
}

async function doReconnect() {
  if (!pairing.value) return
  manualClose = false
  reconnectEpoch++
  state.value = 'connecting'
  message.value = '正在重连…'
  const did = pairing.value.deviceId || fingerprint(device.value.publicKey)
  const creds = await credentialsFor(pairing.value.hostId, did, 'session', { saved: pairing.value })
  session = await reconnect(pairing.value, device.value, onPending, creds, handleLinkClose)
  ready.value = session.ready
  pairing.value.pairKey = session.pairKey
  pairing.value.deviceKey = session.deviceKey || ''
  pairing.value.e2eeKey = session.deviceKey || session.pairKey
  pairing.value.seq = session.link.seq
  await savePairingsStore(pairings.value, pairing.value)
  state.value = 'connected'
  message.value = `已连接到 ${pairing.value.name}`
  log('重连成功')
  void ensureNotifyPermission()
  loadOverview()
  loadProjects()
}

// 网络断开时自动重连，并尝试接回原会话。
// 电脑概况：连接成功后拉一次；用户也可以手动刷新。
async function loadOverview() {
  if (!session) return
  overviewBusy.value = true
  try {
    overview.value = await session.overview()
    overviewErr.value = ''
  } catch (err) {
    overviewErr.value = err.message || String(err)
  } finally {
    overviewBusy.value = false
  }
}

const CATEGORY_LABELS = {
  rules: '规则', plans: '计划', skills: '技能', agents: '代理', commands: '命令',
  tasks: '任务', settings: '设置', mcp: 'MCP', 'legacy-skills': '旧技能', sessions: '会话',
}
const catLabel = c => CATEGORY_LABELS[c] || c
const fmtTime = iso => (iso ? new Date(iso).toLocaleString() : '从未')

// 电脑端连接断开时由 MQTTLink 回调；界面立刻变成“重连中”，不要继续假装已连接。
function handleLinkClose() {
  if (!pairing.value || manualClose) return
  if (reconnecting) return
  state.value = 'reconnecting'
  message.value = '连接已断开，正在自动重连…'
  void autoReconnect()
}

// 横幅按钮：即使旧的重连循环卡在某个网络请求上，也能强制开一轮新的。
function forceReconnect() {
  if (!pairing.value) return
  reconnectEpoch++
  reconnecting = false
  manualClose = false
  state.value = 'reconnecting'
  message.value = '正在重新连接…'
  void autoReconnect()
}

async function autoReconnect() {
  if (reconnecting || !pairing.value || manualClose) return
  reconnecting = true
  const epoch = ++reconnectEpoch
  let attempt = 0
  try {
    while (pairing.value && !manualClose && epoch === reconnectEpoch) {
      if (attempt > 0) await new Promise(r => setTimeout(r, Math.min(5000, 300 * 2 ** attempt)))
      attempt++
      state.value = 'reconnecting'
      try {
        const did = pairing.value.deviceId || fingerprint(device.value.publicKey)
        const creds = await credentialsFor(pairing.value.hostId, did, 'session', { saved: pairing.value })
        session = await reconnect(pairing.value, device.value, onPending, creds, handleLinkClose)
        ready.value = session.ready
        pairing.value.pairKey = session.pairKey
        pairing.value.deviceKey = session.deviceKey || ''
        pairing.value.e2eeKey = session.deviceKey || session.pairKey
        pairing.value.seq = session.link.seq
        await savePairingsStore(pairings.value, pairing.value)
        state.value = 'connected'
        message.value = `已重新连接 ${pairing.value.name}`
        log('自动重连成功')
        loadOverview()
        loadProjects()
        if (chatOpen.value && sessionKey.value && claudeSessionId) {
          try {
            const list = await session.activeSessions()
            const hit = list.find(a => a.claudeSessionId === claudeSessionId)
            if (hit) {
              sessionKey.value = hit.key
              seqGate.reset()
              await session.subscribe(hit.key, 0, onSessionEvent)
              pushMsg({ role: 'notice', text: '已接回原会话' })
              log('已接回原会话')
            } else {
              pushMsg({ role: 'notice', text: '原会话已结束，可返回后重新开始' })
            }
          } catch {
            // 接回失败时保持已连接状态即可
          }
        }
        if (chatOpen.value && historyTarget && !historyMessages.value.length) {
          historyLoading.value = false
          void loadHistory()
        }
        return
      } catch (err) {
        log('重连失败：' + (err.message || err))
      }
    }
  } finally {
    if (epoch === reconnectEpoch) reconnecting = false
  }
}

async function run(action) {
  if (busy.value) return
  busy.value = true
  try {
    await action()
  } catch (err) {
    state.value = 'error'
    message.value = err.message || String(err)
    log('失败：' + message.value)
    if (session) {
      // 关掉旧链路；如果是网络问题，onClose 会自动进入重连，不要在这里把重连停掉。
      session.close()
      session = null
    }
  } finally {
    busy.value = false
  }
}

function submitInvitationText(text) {
  invitationText.value = text
  submitInvitation()
}

function submitInvitation() {
  run(async () => {
    let invitation
    try {
      invitation = JSON.parse(invitationText.value.trim())
    } catch {
      throw new Error('邀请内容不是合法 JSON')
    }
    await doPair(invitation)
    invitationText.value = ''
  })
}

function connectSaved() {
  run(() => doReconnect())
}

// 断开并清掉当前会话（切换电脑、删除电脑、手动断开共用）。
async function teardown() {
  manualClose = true
  reconnectEpoch++
  resetHistory()
  if (session && sessionKey.value) {
    try {
      await session.closeSession(sessionKey.value)
    } catch {
      // 连接已断时忽略
    }
  }
  if (session) session.close()
  session = null
  chatOpen.value = false
  sessionKey.value = ''
  overview.value = null
  for (const a of attachments.value) revokePreview(a)
  attachments.value = []
  state.value = 'idle'
}

function disconnect() {
  run(async () => {
    await teardown()
    message.value = '已断开'
    log('已断开')
  })
}

// 切换到另一台电脑：先断开当前连接，再用保存的凭据重连。
function switchTo(p) {
  if (!p) return
  run(async () => {
    await teardown()
    pairing.value = p
    await savePairingsStore(pairings.value, p)
    await doReconnect()
    log(`已切换到 ${p.name}`)
    tab.value = 'sessions'
  })
}

function removeComputer(p) {
  run(async () => {
    const active = pairing.value && hostKey(pairing.value) === hostKey(p)
    if (active) await teardown()
    pairings.value = pairings.value.filter(x => hostKey(x) !== hostKey(p))
    if (active) {
      pairing.value = pairings.value[0] || null
      message.value = pairing.value ? `已删除，当前选择 ${pairing.value.name}，点“连接”` : '已删除全部配对'
    }
    await savePairingsStore(pairings.value, pairing.value)
    log(`已删除电脑 ${p.name}`)
  })
}

function sendEcho() {
  run(async () => {
    if (!session) throw new Error('还没有连接')
    const reply = await session.echo(echoText.value)
    log(`回显：${reply}`)
  })
}

function loadDevices() {
  run(async () => {
    if (!session) throw new Error('还没有连接')
    devices.value = await session.devices()
    log(`设备列表 ${devices.value.length} 条`)
  })
}

// ---- 会话 ----

async function loadProjects() {
  if (!connected.value || !session) return
  projectsLoading.value = true
  try {
    projects.value = await session.projects(tool.value)
    log(`项目 ${projects.value.length} 个`)
  } catch (err) {
    log('加载项目失败：' + (err.message || err))
  } finally {
    projectsLoading.value = false
  }
}

function openProjectPage(p) {
  run(async () => {
    currentProject.value = p
    currentSession.value = null
    sessionView.value = 'project'
    await loadSessions()
    log(`会话 ${sessions.value.length} 条`)
  })
}

async function loadSessions() {
  if (!currentProject.value) return
  const list = await session.sessions(tool.value, currentProject.value.cwd)
  sessions.value = list.map(s => ({ ...s, timeLabel: sessionTime(s.updatedAt) }))
}

function reloadSessions() {
  run(loadSessions)
}

function backToProjects() {
  sessionView.value = 'projects'
  sessions.value = []
  currentProject.value = null
  currentSession.value = null
}

function resetHistory() {
  historyGeneration++
  historyTarget = null
  historyMessages.value = []
  historyLoading.value = false
  historyError.value = ''
  historyCursor.value = ''
  historyMore.value = false
  finishTypewriter()
  currentThinking = null
  thinking.value = false
}

async function loadHistory(older = false) {
  if (!historyTarget || !session || historyLoading.value) return
  const generation = historyGeneration
  const target = historyTarget
  const linkSession = session
  const beforeHeight = msgBox.value?.scrollHeight || 0
  historyLoading.value = true
  historyError.value = ''
  try {
    const page = await linkSession.historyPage(target.tool, target.cwd, target.id, older ? historyCursor.value : '')
    if (generation !== historyGeneration || linkSession !== session) return
    historyMessages.value = older
      ? [...withHistoryIDs(page.messages), ...historyMessages.value]
      : withHistoryIDs(page.messages)
    historyCursor.value = page.cursor
    historyMore.value = page.hasMore
    if (older) {
      await nextTick()
      if (msgBox.value) msgBox.value.scrollTop += msgBox.value.scrollHeight - beforeHeight
    } else scrollChat(true)
  } catch (err) {
    if (generation === historyGeneration && linkSession === session) historyError.value = err.message || String(err)
  } finally {
    if (generation === historyGeneration) historyLoading.value = false
  }
}

async function startChat(cwd, resumeId) {
  // 开新会话前先结束上一个，避免占用电脑上的会话名额。
  if (session && sessionKey.value) {
    try {
      await session.closeSession(sessionKey.value)
    } catch {
      // 旧会话可能已结束
    }
  }
  resetHistory()
  const key = await session.startSession(tool.value, cwd, resumeId || '', mode.value)
  seqGate.reset()
  claudeSessionId = ''
  sessionKey.value = key
  chat.value = []
  currentAssistant = null
  generating.value = false
  chatOpen.value = true
  stickToBottom.value = true
  // 历史请求和 session.subscribe 互不依赖，同时发出，少等一个往返。
  if (resumeId) {
    historyTarget = { tool: tool.value, cwd, id: resumeId }
    void loadHistory()
  }
  await session.subscribe(key, 0, onSessionEvent)
}

function newSession() {
  run(async () => {
    if (!currentProject.value) throw new Error('先选一个项目')
    currentSession.value = { title: '新会话' }
    await startChat(currentProject.value.cwd, '')
    log('已新建会话')
  })
}

function resumeSession(s) {
  run(async () => {
    currentSession.value = s
    await startChat(s.cwd, s.id)
    log('已继续会话')
  })
}

function ensureAssistant() {
  if (!currentAssistant) {
    pushMsg({ role: 'assistant', text: '', streaming: true })
    currentAssistant = chat.value[chat.value.length - 1]
  }
  return currentAssistant
}

// 模型（尤其带思考的模型）常在最后一次性吐出一大段。积压文本按约 1 秒的节奏
// 逐字显示；真实流式时每帧只有一两个字，几乎无额外延迟。
function startTypewriter() {
  if (typeTimer) return
  typeTimer = setInterval(() => {
    if (!pendingText) {
      clearInterval(typeTimer)
      typeTimer = null
      if (!generating.value) {
        if (currentAssistant) currentAssistant.streaming = false
        currentAssistant = null
      }
      return
    }
    const bubble = ensureAssistant()
    const n = Math.max(1, Math.ceil(pendingText.length / 20))
    bubble.text += pendingText.slice(0, n)
    pendingText = pendingText.slice(n)
    scheduleScroll()
  }, 16)
}

function flushPending() {
  if (pendingText && currentAssistant) {
    currentAssistant.text += pendingText
    pendingText = ''
  }
}

function finishTypewriter() {
  flushPending()
  if (typeTimer) {
    clearInterval(typeTimer)
    typeTimer = null
  }
  currentAssistant = null
}

// 丢弃尚未显示完的积压文本（会话重启/切换模式时用，不要 flush 到旧气泡）。
function clearTypewriter() {
  if (typeTimer) {
    clearInterval(typeTimer)
    typeTimer = null
  }
  pendingText = ''
}

function onSessionEvent(ev) {
  if (!seqGate.accept(ev.seq)) return
  handleSessionEvent(ev)
  scheduleScroll()
}

function handleSessionEvent(ev) {
  if (ev.kind === 'init' && ev.sessionId) {
    claudeSessionId = ev.sessionId
    return
  }
  const kind = ev.kind
  if (kind === 'init') return
  if (kind === 'thinking') {
    // 思考内容也是流式的；和正文分开显示，答案开始后下一次思考再新开一块。
    thinking.value = false
    if (!currentThinking) {
      pushMsg({ role: 'thinking', text: '', open: true })
      currentThinking = chat.value[chat.value.length - 1]
    }
    currentThinking.text += ev.text || ''
    return
  }
  if (kind === 'text_delta') {
    currentThinking = null
    thinking.value = false
    pendingText += ev.text || ''
    ensureAssistant()
    startTypewriter()
    return
  }
  if (kind === 'text') {
    // 完整的 assistant 消息会跟在 text_delta 后面，覆盖到同一个气泡里，
    // 不能新建气泡，否则同一段回复会显示两遍。
    currentThinking = null
    const bubble = ensureAssistant()
    flushPending()
    bubble.text = ev.text || bubble.text
    bubble.streaming = false
    if (typeTimer) {
      clearInterval(typeTimer)
      typeTimer = null
    }
    currentAssistant = null
    return
  }
  if (kind === 'tool_use') {
    thinking.value = false
    currentThinking = null
    flushPending()
    pushMsg({ role: 'tool', text: `${ev.tool || '工具'} ${ev.text || ''}`.trim() })
    return
  }
  if (kind === 'tool_result') {
    return
  }
  if (kind === 'permission_denied') {
    pushMsg({ role: 'notice', text: `电脑端已拒绝：${ev.tool || '工具'}` })
    return
  }
  if (kind === 'approval') {
    thinking.value = false
    currentThinking = null
    flushPending()
    pushMsg({
      role: 'approval',
      text: `${ev.tool || '工具'}：${ev.text || ''}`.trim(),
      approvalId: ev.approvalId,
      decided: null,
    })
    if (!appActive.value) void localNotify('approval', ev, pairing.value?.name)
    return
  }
  if (kind === 'result') {
    thinking.value = false
    currentThinking = null
    generating.value = false
    if (!appActive.value) void localNotify('result', ev, pairing.value?.name)
    // 还有积压文本时让打字机继续吐完，全部显示后再结束这一段。
    if (!pendingText) currentAssistant = null
    return
  }
  if (kind === 'exit') {
    thinking.value = false
    currentThinking = null
    flushPending()
    generating.value = false
    pushMsg({ role: 'notice', text: `会话进程退出 ${ev.text || ''}`.trim() })
    return
  }
  if (kind === 'error') {
    thinking.value = false
    currentThinking = null
    flushPending()
    generating.value = false
    pushMsg({ role: 'notice', text: ev.text || '出错了' })
  }
}

// 打开系统相机/相册可能让 WebView 暂停、连接断开；等自动重连后再继续。
async function waitConnected(timeout = 20000) {
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    if (state.value === 'connected' && session) return true
    await new Promise(r => setTimeout(r, 300))
  }
  return false
}

function revokePreview(a) {
  if (a?.preview?.startsWith('blob:')) URL.revokeObjectURL(a.preview)
}

function removeAttachment(id) {
  const hit = attachments.value.find(a => a.attachId === id)
  revokePreview(hit)
  attachments.value = attachments.value.filter(a => a.attachId !== id)
}

function onEnter(e) {
  if (!shouldSendOnEnter(e)) return
  e.preventDefault()
  sendChat()
}

function sendChat() {
  run(async () => {
    const text = chatInput.value.trim()
    const originalAttachments = [...attachments.value]
    const ids = originalAttachments.map(a => a.attachId)
    if ((!text && !ids.length) || !sessionKey.value || !connected.value) return
    finishTypewriter()
    pushMsg({ role: 'user', text, files: originalAttachments.map(a => a.name) })
    chatInput.value = ''
    attachments.value = []
    currentAssistant = null
    currentThinking = null
    generating.value = true
    thinking.value = true
    try {
      await session.sendTurn(sessionKey.value, text, ids)
    } catch (err) {
      generating.value = false
      thinking.value = false
      chat.value.pop() // 去掉乐观气泡，避免重发时出现两条
      chatInput.value = text
      attachments.value = originalAttachments
      notify('消息发送失败：' + (err.message || err))
      return
    }
    for (const a of originalAttachments) revokePreview(a)
    if (chatInputEl.value) chatInputEl.value.style.height = 'auto'
    scrollChat(true)
  })
}

function stopChat() {
  thinking.value = false
  if (sessionKey.value) {
    session.stopTurn(sessionKey.value).catch(err => notify('停止失败：' + (err.message || err)))
  }
}

// 返回 = 离开会话并结束电脑上的进程（对话历史仍保留，可再“继续”）。
function closeChat() {
  resetHistory()
  run(async () => {
    if (session && sessionKey.value) {
      try {
        session.unsubscribe(sessionKey.value)
        await session.closeSession(sessionKey.value)
      } catch {
        // 忽略：连接已断时电脑端会自动回收
      }
    }
    sessionKey.value = ''
    chatOpen.value = false
    for (const a of attachments.value) revokePreview(a)
    attachments.value = []
    generating.value = false
    thinking.value = false
    currentThinking = null
    finishTypewriter()
    currentSession.value = null
    sessionView.value = 'project'
    if (currentProject.value) reloadSessions()
  })
}

// 结束会话 = 返回上一级；电脑上的进程会立即释放，历史仍在。
function endChat() {
  closeChat()
}

onMounted(async () => {
  applyTheme()
  try {
    await initCrypto()
    await loadOrCreateDevice()
    await loadPairings()
    if (pairing.value) void ensureNotifyPermission()
    try {
      const rawConn = await secureGet('mqttConn')
      if (rawConn) conn.value = { ...conn.value, ...JSON.parse(rawConn) }
      await loadAuth()
    } catch {
      // 配置损坏时用默认值
    }
    if (pairing.value && (!pairing.value.e2eeKey || !pairing.value.hostId)) {
      log('这台电脑需要重新配对（升级后首次）')
      pairing.value = null
    }
    log(`设备指纹 ${fingerprint(device.value.publicKey).slice(0, 12)}`)
    if (!secure.value) log('网页调试模式：密钥仅存于浏览器，非正式安全存储')
    if (Capacitor.isNativePlatform()) {
      const handleLink = url => {
        const inv = parseDeepLink(url)
        if (inv) {
          log('收到扫码邀请')
          run(() => doPair(inv))
        }
      }
      // 运行中扫码
      CapApp.addListener('appUrlOpen', event => handleLink(event.url))
      // App 没开时扫码/点链接：冷启动要主动取一次 launch URL。
      const launch = await CapApp.getLaunchUrl()
      if (launch?.url) handleLink(launch.url)
      CapApp.addListener('backButton', () => {
        if (scanning.value) { stopScanQR(); return }
        if (showPlus.value) { showPlus.value = false; return }
        if (showPerm.value) { showPerm.value = false; return }
        if (filesOpen.value) { closeFiles(); return }
        if (chatOpen.value) { closeChat(); return }
        if (tab.value === 'sessions' && sessionView.value === 'project') { backToProjects(); return }
        CapApp.exitApp()
      })
    }
    if (pairing.value) {
      try {
        await doReconnect()
      } catch {
        log('首次连接失败，自动重试中')
        autoReconnect()
      }
    }
  } catch (err) {
    state.value = 'error'
    message.value = err.message || String(err)
  }
})

// ---- 权限模式 ----
const modeLabels = computed(() => tool.value === 'codex' ? {
  dontAsk: '只读沙箱',
  acceptEdits: '项目内可写',
  bypassPermissions: '完全访问',
} : {
  dontAsk: '只读',
  default: '手动',
  acceptEdits: '自动编辑',
  plan: '计划',
  auto: '自动',
  bypassPermissions: '完全',
})

// 用按钮组而不是原生 select：部分 Android WebView 的原生下拉只改值、不派发 change。
async function switchMode(next) {
  if (!session || next === mode.value) return
  log(`切换权限：${modeLabels.value[next] || next}`)
  if (next === 'bypassPermissions' && !window.confirm(`完全模式允许 ${toolLabels[tool.value]} 执行任意命令、修改任意文件。确定切换？`)) {
    return
  }
  if (busy.value) return
  busy.value = true
  try {
    const key = await session.setMode(sessionKey.value, next)
    const moved = key !== sessionKey.value
    sessionKey.value = key
    if (moved) {
      // 新 Runner 的事件序号从 1 重新开始；不重置就会被当成重复全部丢弃。
      clearTypewriter()
      seqGate.reset()
      currentAssistant = null
      currentThinking = null
      pendingText = ''
      await session.subscribe(key, 0, onSessionEvent)
    }
    pushMsg({
      role: 'notice',
      text: `权限模式：${modeLabels.value[next]}${moved ? '（会话已重启，上下文保留）' : ''}`,
    })
    mode.value = next
    log('权限模式切换为 ' + modeLabels.value[next])
  } catch (err) {
    message.value = '切换模式失败：' + (err.message || err)
    log(message.value)
  } finally {
    busy.value = false
  }
}

function applyTheme() {
  const el = document.documentElement
  if (theme.value === 'system') delete el.dataset.theme
  else el.dataset.theme = theme.value
  try { localStorage.setItem('ags:theme', theme.value) } catch { /* 存储被禁用时仅本次生效 */ }
}

function setTheme(next) {
  theme.value = next
  applyTheme()
}

// ---- 文件浏览 / diff ----
function joinPath(dir, name) {
  return dir ? `${dir}/${name}` : name
}

function parentPath(p) {
  const i = p.lastIndexOf('/')
  return i < 0 ? '' : p.slice(0, i)
}

async function fsRun(fn) {
  try {
    await fn()
  } catch (err) {
    log('文件浏览失败：' + (err.message || err))
  }
}

function openFiles(cwd) {
  fsRun(async () => {
    filesOpen.value = true
    fsCwd.value = cwd
    fsPath.value = ''
    fsView.value = 'list'
    fsText.value = ''
    fsChanges.value = []
    fsEntries.value = await session.fsList(cwd, '')
  })
}

function loadDir() {
  fsRun(async () => {
    fsEntries.value = await session.fsList(fsCwd.value, fsPath.value)
    fsView.value = 'list'
  })
}

function enterEntry(e) {
  fsRun(async () => {
    if (e.dir) {
      fsPath.value = joinPath(fsPath.value, e.name)
      loadDir()
      return
    }
    const rel = joinPath(fsPath.value, e.name)
    const res = await session.fsRead(fsCwd.value, rel)
    fsTitle.value = rel
    fsText.value = res.text
    fsTruncated.value = res.truncated
    fsBinary.value = res.binary
    fsView.value = 'text'
  })
}

function upDir() {
  fsPath.value = parentPath(fsPath.value)
  loadDir()
}

function fsRoot() {
  fsPath.value = ''
  loadDir()
}

function showChanges() {
  fsRun(async () => {
    fsChanges.value = await session.gitStatus(fsCwd.value)
    fsView.value = 'changes'
  })
}

function openChange(c) {
  fsRun(async () => {
    const r = await session.gitDiff(fsCwd.value, c.path)
    fsTitle.value = c.path
    fsText.value = r.text
    fsTruncated.value = r.truncated
    fsBinary.value = false
    fsView.value = 'diff'
  })
}

function closeFiles() {
  filesOpen.value = false
}

async function scrollChat(force = false) {
  await nextTick()
  const el = msgBox.value
  if (!el || (!force && !stickToBottom.value)) return
  // 程序化滚动会触发 scroll 事件；不屏蔽的话，内容突然变长时会被误判成“用户上滑”。
  programmaticScroll = true
  el.scrollTop = el.scrollHeight
  requestAnimationFrame(() => { programmaticScroll = false })
}

function noteUserScroll() {
  userScrollUntil = Date.now() + 800
}

function onChatScroll() {
  const el = msgBox.value
  if (!el) return
  stickToBottom.value = nextStick({
    near: shouldStickToBottom(el),
    programmatic: programmaticScroll,
    userScrolling: Date.now() < userScrollUntil,
    stick: stickToBottom.value,
  })
}

let scrollScheduled = false
function scheduleScroll() {
  if (scrollScheduled) return
  scrollScheduled = true
  requestAnimationFrame(() => {
    scrollScheduled = false
    void scrollChat()
  })
}

function pickTool(next) {
  if (next === tool.value) return
  tool.value = next
  // 不把 Claude 的“手动审批”等模式误当成 Codex 已支持的交互权限。
  mode.value = 'dontAsk'
  resetHistory()
  projects.value = []
  sessions.value = []
  currentProject.value = null
  sessionView.value = 'projects'
  log('切换工具：' + toolLabels[next])
  loadProjects()
}

async function decideApproval(item, allow) {
  if (!sessionKey.value || item.decided) return
  try {
    await session.decide(sessionKey.value, item.approvalId, allow)
    item.decided = allow ? 'allowed' : 'denied'
    pushMsg({ role: 'notice', text: allow ? '已允许该操作' : '已拒绝该操作' })
  } catch (err) {
    notify('审批发送失败：' + (err.message || err))
  }
}
// ---------- 新版界面 ----------
const pageTitle = computed(() => ({ home: '首页', sessions: '会话', settings: '设置' })[tab.value] || '')
const chatTitle = computed(() => currentSession.value?.title || '新会话')
const baseName = (p) => (p || '').split(/[\\/]/).filter(Boolean).pop() || p || ''
const initials = (s) => (s || '?').replace(/[^A-Za-z0-9\u4e00-\u9fa5]/g, '').slice(0, 2).toUpperCase() || '?'
const pct = (a, b) => (b ? Math.round((a / b) * 100) : 0)

function sessionTime(iso) {
  if (!iso) return ''
  const t = Date.parse(iso)
  if (!t) return ''
  const s = Math.round((Date.now() - t) / 1000)
  if (s < 60) return '刚刚'
  if (s < 3600) return Math.floor(s / 60) + ' 分钟前'
  if (s < 86400) return Math.floor(s / 3600) + ' 小时前'
  return Math.floor(s / 86400) + ' 天前'
}

const mdCache = new Map()
function renderMd(text) {
  if (!text) return ''
  const hit = mdCache.get(text)
  if (hit) return hit
  let html
  try {
    html = DOMPurify.sanitize(marked.parse(text, { breaks: true, gfm: true }))
  } catch {
    html = text
  }
  mdCache.set(text, html)
  if (mdCache.size > 200) mdCache.delete(mdCache.keys().next().value)
  return html
}

function growInput() {
  const el = chatInputEl.value
  if (!el) return
  el.style.height = 'auto'
  el.style.height = Math.min(120, el.scrollHeight) + 'px'
}

function go(next) {
  tab.value = next
  if (next === 'sessions' && connected.value && !projects.value.length) loadProjects()
}

function openChatFiles() {
  if (currentProject.value) openFiles(currentProject.value.cwd)
}

async function addAttachmentData({ name, mediaType, kind, data, preview }) {
  if (!session) return
  if (attachments.value.length >= MAX_ATTACH) {
    notify(`每条消息最多 ${MAX_ATTACH} 张图片`)
    return
  }
  attachBusy.value = true
  try {
    attachProgress.value = 0
    let attachId = ''
    for (let attempt = 0; attempt < 2; attempt++) {
      try {
        attachId = await session.uploadAttachment(
          { name, mediaType, kind, data },
          p => { attachProgress.value = p },
        )
        break
      } catch (err) {
        const msg = String(err?.message || err)
        if (attempt === 0 && msg.includes('disconnected') && await waitConnected(20000)) {
          log('连接已恢复，重试上传…')
          continue
        }
        throw err
      }
    }
    attachments.value.push({ attachId, name, size: data.byteLength, kind, preview })
    log(`附件已上传：${name}`)
  } catch (err) {
    notify('附件上传失败：' + (err.message || err))
  } finally {
    attachBusy.value = false
    attachProgress.value = 0
  }
}

async function scanQR() {
  try {
    const perm = await BarcodeScanner.checkPermissions()
    if (perm.camera !== 'granted') {
      const asked = await BarcodeScanner.requestPermissions()
      if (asked.camera !== 'granted') { notify('没有相机权限'); return }
    }
    if (!(await BarcodeScanner.isSupported()).supported) {
      notify('这台设备不支持扫码')
      return
    }
    scanning.value = true
    document.body.classList.add('barcode-scanner-active')
    await BarcodeScanner.removeAllListeners()
    BarcodeScanner.addListener('barcodeScanned', async result => {
      const raw = result?.barcode?.rawValue
      if (!raw) return
      await stopScanQR()
      try {
        const inv = parseDeepLink(raw) || JSON.parse(raw)
        if (!inv?.url) throw new Error('二维码不是配对邀请')
        await doPair(inv)
      } catch (err) {
        notify('扫码失败：' + (err.message || err))
      }
    })
    await BarcodeScanner.startScan({ formats: [BarcodeFormat.QrCode] })
  } catch (err) {
    await stopScanQR()
    notify('扫码失败：' + (err.message || err))
  }
}

async function stopScanQR() {
  if (!scanning.value) return
  scanning.value = false
  document.body.classList.remove('barcode-scanner-active')
  try {
    await BarcodeScanner.removeAllListeners()
    await BarcodeScanner.stopScan()
  } catch {
    // 忽略：可能已经停止
  }
}

async function cameraPick(source) {
  try {
    if (source === CameraSource.Camera && Capacitor.getPlatform() === 'web') {
      throw new Error('网页模式不支持直接拍照')
    }
    const photo = await Camera.getPhoto({
      source,
      resultType: CameraResultType.DataUrl,
      quality: 85,
      width: 2048,
      correctOrientation: true,
    })
    if (!photo.dataUrl) return
    const res = await fetch(photo.dataUrl)
    const data = new Uint8Array(await res.arrayBuffer())
    const format = photo.format || 'jpeg'
    await addAttachmentData({
      name: 'photo-' + Date.now() + '.' + format,
      mediaType: 'image/' + format,
      kind: 'image',
      data,
      preview: photo.dataUrl,
    })
  } catch (err) {
    notify('拍照/相册失败：' + (err.message || err))
  }
}

async function takePhoto() {
  showPlus.value = false
  await cameraPick(CameraSource.Camera)
}

async function pickGallery() {
  showPlus.value = false
  await cameraPick(CameraSource.Photos)
}

</script>

<template>
  <main class="app">
    <!-- 未连接且不在聊天里：显示可复用的连接面板 -->
    <div v-if="!connected && !chatOpen && tab !== 'settings'" class="page connect-gate">
      <ConnectPanel
        :state="state" :message="message" :status-label="statusLabel"
        :store="pairing" :pairings="pairings" :busy="busy"
        @connect="connectSaved" @switch="switchTo" @remove="removeComputer"
        @pair="submitInvitationText" @scan="scanQR"
      />
      <AboutUpdates
        :version-label="versionLabel" :update="update" :native="nativePlatform" :opening="openingUpdate"
        @check="checkUpdate" @auto="setAutoUpdate" @open="openUpdate" @skip="skipUpdate"
      />
    </div>

    <template v-else>
      <header v-if="!chatOpen" class="appbar">
        <div class="appbar-main">
          <span class="appbar-title">{{ pageTitle }}</span>
          <span v-if="pairing" class="appbar-sub">{{ pairing.name }}</span>
        </div>
        <span class="conn-state" :data-state="state"><i></i>{{ statusLabel }}</span>
      </header>

      <!-- 首页 -->
      <section v-if="!chatOpen && tab === 'home'" class="page">
        <div class="hero-card">
          <div class="hero-orb"></div>
          <div class="hero-main">
            <div class="hero-kicker">电脑概况</div>
            <div class="hero-name">{{ overview?.hostName || pairing?.name || '—' }}</div>
            <div class="hero-meta">{{ overview ? `${overview.projects} 个项目 · ${overview.sessions} 个会话` : '正在读取…' }}</div>
          </div>
          <button class="icon-btn" :disabled="overviewBusy" @click="loadOverview"><Icon name="refresh" /></button>
        </div>

        <div v-if="overview" class="tile-grid">
          <div class="tile"><span class="tile-label">上次同步</span><span class="tile-value small-value">{{ fmtTime(overview.lastSync) }}</span></div>
          <div class="tile"><span class="tile-label">上传 / 下载</span><span class="tile-value">{{ overview.stats?.pushed || 0 }} / {{ overview.stats?.pulled || 0 }}</span></div>
          <div class="tile"><span class="tile-label">失败</span><span class="tile-value">{{ overview.stats?.errors || 0 }}</span></div>
          <div class="tile"><span class="tile-label">同步设备</span><span class="tile-value small-value">{{ overview.deviceName || '—' }}</span></div>
        </div>

        <div class="section-title">工具</div>
        <div v-if="overview" class="tool-cards">
          <div v-for="t in overview.tools" :key="t.id" class="tool-card">
            <div class="tool-top"><span class="tool-name">{{ t.name }}</span><span class="tool-state" :data-on="t.installed">{{ t.installed ? '已安装' : '未安装' }}</span></div>
            <div class="tool-sub">{{ t.installed ? `${t.projects} 个项目 · ${t.sessionCount} 个会话` : '这台电脑上还没有' }}</div>
            <div class="meter"><span :style="{ width: pct(t.contentOn, t.contentTotal) + '%' }"></span></div>
            <div class="tool-foot">同步内容 {{ t.contentOn }}/{{ t.contentTotal }}</div>
          </div>
        </div>

        <div class="section-title">同步内容</div>
        <div class="chips-grid">
          <span v-for="c in overview?.content" :key="c.tool + c.category" class="stat-chip" :data-on="c.enabled">
            {{ catLabel(c.category) }} · {{ c.tool === 'codex' ? 'Codex' : 'Claude' }}
          </span>
        </div>
        <p v-if="overviewErr" class="hint">{{ overviewErr }}</p>
      </section>

      <!-- 会话 -->
      <section v-if="!chatOpen && tab === 'sessions'" class="page">
        <template v-if="sessionView === 'project' && currentProject">
          <div class="subbar">
            <button class="icon-btn" @click="backToProjects"><Icon name="back" /></button>
            <div class="subbar-title">{{ baseName(currentProject.cwd) }}</div>
            <button class="icon-btn" :disabled="busy" @click="reloadSessions"><Icon name="refresh" /></button>
          </div>
          <div class="project-path mono">{{ currentProject.cwd }}</div>
          <button class="primary wide" @click="newSession">新建会话</button>
          <div class="session-list">
            <button v-for="s in sessions" :key="s.id" class="session-row" @click="resumeSession(s)">
              <div class="session-main">
                <div class="session-title">{{ s.title || '未命名会话' }}</div>
                <div class="session-sub">{{ s.timeLabel }}</div>
              </div>
              <Icon name="chevron" :size="16" class="chev" />
            </button>
            <p v-if="!sessions.length && !busy" class="empty">这个项目还没有会话，点上面新建一个。</p>
          </div>
        </template>

        <template v-else>
          <div class="toolbar">
            <div class="segmented">
              <button :class="{ on: tool === 'claude' }" @click="pickTool('claude')">Claude Code</button>
              <button :class="{ on: tool === 'codex' }" @click="pickTool('codex')">Codex</button>
            </div>
            <button class="icon-btn" :disabled="busy || !connected" @click="loadProjects"><Icon name="refresh" /></button>
          </div>
          <div class="project-list">
            <button v-for="p in projects" :key="p.cwd" class="project-card" @click="openProjectPage(p)">
              <div class="project-badge">{{ initials(baseName(p.cwd)) }}</div>
              <div class="project-main">
                <div class="project-name">{{ baseName(p.cwd) }}</div>
                <div class="project-sub mono">{{ p.cwd }}</div>
              </div>
              <span class="project-count">{{ p.sessions }}</span>
              <Icon name="chevron" :size="15" class="chev" />
            </button>
            <p v-if="projectsLoading" class="hint">正在加载项目…</p>
            <p v-else-if="!projects.length" class="empty">{{ connected ? '没有找到项目' : '连接后自动加载项目' }}</p>
          </div>
        </template>
      </section>

      <!-- 设置 -->
      <section v-if="!chatOpen && tab === 'settings'" class="page">
        <div class="section-title">外观</div>
        <div class="card">
          <div class="setting-row">
            <span>主题</span>
            <div class="segmented small">
              <button v-for="o in ['system', 'light', 'dark']" :key="o" :class="{ on: theme === o }" @click="setTheme(o)">
                {{ { system: '跟随系统', light: '浅色', dark: '深色' }[o] }}
              </button>
            </div>
          </div>
        </div>

        <div class="section-title">电脑</div>
        <ConnectPanel
          manage :state="state" :message="message" :status-label="statusLabel"
          :store="pairing" :pairings="pairings" :busy="busy"
          @connect="connectSaved" @switch="switchTo" @remove="removeComputer"
          @pair="submitInvitationText" @scan="scanQR"
        />
        <div v-if="overview" class="card">
          <div class="setting-row"><span>电脑名</span><b>{{ overview.hostName }}</b></div>
          <div class="setting-row"><span>同步设备名</span><b>{{ overview.deviceName }}</b></div>
          <div class="setting-row"><span>系统</span><b>{{ overview.system }}</b></div>
          <div class="setting-row"><span>语言</span><b>{{ overview.language }}</b></div>
          <div class="setting-row"><span>数据目录</span><b class="mono small">{{ overview.dataDir }}</b></div>
        </div>

        <AboutUpdates
          :version-label="versionLabel" :update="update" :native="nativePlatform" :opening="openingUpdate"
          @check="checkUpdate" @auto="setAutoUpdate" @open="openUpdate" @skip="skipUpdate"
        />
      </section>

    </template>

    <nav v-if="!chatOpen && !filesOpen" class="tabbar">
      <button :class="{ active: tab === 'home' }" @click="go('home')"><Icon name="home" :size="22" /><span>首页</span></button>
      <button :class="{ active: tab === 'sessions' }" @click="go('sessions')"><Icon name="chat" :size="22" /><span>会话</span></button>
      <button :class="{ active: tab === 'settings' }" @click="go('settings')"><Icon name="settings" :size="22" /><span>设置</span></button>
    </nav>

    <!-- 聊天：全屏，参考 DeepSeek 的对话页 -->
    <section v-if="chatOpen && !filesOpen" class="chat-page">
      <header class="chat-head">
        <button class="icon-btn" @click="closeChat"><Icon name="back" /></button>
        <div class="chat-title">{{ chatTitle }}</div>
        <button class="icon-btn" :disabled="!currentProject" @click="openChatFiles"><Icon name="folder" /></button>
      </header>
      <div class="chat-subbar">
        <button class="perm-pill" @click="showPerm = !showPerm">
          {{ modeLabels[mode] || mode }} <Icon name="chevron" :size="12" />
        </button>
        <span class="chat-tool">{{ toolLabels[tool] }}</span>
      </div>
      <button v-if="state !== 'connected'" class="reconnect-bar" @click="forceReconnect">
        <Icon name="refresh" :size="14" /> 连接已断开，点此重连
      </button>
      <div v-if="showPerm" class="perm-sheet">
        <p v-if="tool === 'codex'" class="small muted">Codex 使用沙箱限制操作，不提供逐操作审批。项目内可写允许修改项目文件；完全访问取消沙箱限制。</p>
        <button v-for="(label, key) in modeLabels" :key="key" :class="{ on: mode === key }" @click="switchMode(key); showPerm = false">
          {{ label }}
        </button>
      </div>

      <div ref="msgBox" class="messages" @scroll="onChatScroll"
        @touchstart.passive="noteUserScroll" @touchmove.passive="noteUserScroll"
        @wheel.passive="noteUserScroll" @pointerdown="noteUserScroll">
        <div class="history-actions">
          <span v-if="historyLoading">正在加载历史…</span>
          <template v-else-if="historyError"><span>{{ historyError }}</span><button class="sm" :disabled="!connected" @click="loadHistory()">重试</button></template>
          <button v-else-if="historyMore" class="sm" :disabled="!connected" @click="loadHistory(true)">加载更早的消息</button>
        </div>
        <div v-for="m in displayedChat" :key="m.id" class="chat-row">
          <div v-if="m.role === 'user'" class="msg user">
            <div class="bubble">
              <div v-if="m.text" class="bubble-text">{{ m.text }}</div>
              <div v-if="m.files && m.files.length" class="bubble-files">
                <span v-for="f in m.files" :key="f" class="file-chip"><Icon name="file" :size="12" />{{ f }}</span>
              </div>
            </div>
          </div>
          <div v-else-if="m.role === 'thinking'" class="thinking-block">
            <button class="thinking-head" @click="m.open = !m.open">
              <span class="thinking-dot"></span>
              <span>思考过程</span>
              <span class="thinking-toggle">{{ m.open ? '收起' : '展开' }}</span>
            </button>
            <div v-if="m.open" class="thinking-body">{{ m.text }}</div>
          </div>
          <div v-else-if="m.role === 'assistant'" class="msg assistant">
            <div class="assistant-who">{{ toolLabels[tool] }}</div>
            <div v-if="m.streaming" class="md md-stream">{{ m.text }}</div>
            <div v-else class="md" v-html="renderMd(m.text)"></div>
          </div>
          <div v-else-if="m.role === 'tool'" class="tool-line"><Icon name="search" :size="12" />{{ m.text }}</div>
          <div v-else-if="m.role === 'approval'" class="approval">
            <div class="approval-title"><Icon name="key" :size="14" /> 需要批准 · {{ m.text }}</div>
            <div v-if="m.decided === null" class="row">
              <button class="primary sm" @click="decideApproval(m, true)">允许</button>
              <button class="sm" @click="decideApproval(m, false)">拒绝</button>
            </div>
            <div v-else class="muted small">{{ m.decided === 'allowed' ? '已允许' : '已拒绝' }}</div>
          </div>
          <div v-else class="notice">{{ m.text }}</div>
        </div>
        <div v-if="thinking" class="thinking-line"><span class="thinking-dot"></span>思考中</div>
        <p v-if="!displayedChat.length && !historyLoading && !historyError" class="empty chat-empty">问点什么，或者点左下角的 + 发送图片。</p>
      </div>

      <button v-if="!stickToBottom" class="jump-bottom" @click="stickToBottom = true; scrollChat(true)">
        回到底部
      </button>

      <div class="composer-wrap">
        <div v-if="attachments.length || attachBusy" class="attach-row">
          <div v-for="a in attachments" :key="a.attachId" class="attach-thumb">
            <img v-if="a.preview" :src="a.preview" alt="" />
            <span v-else class="attach-file"><Icon name="file" :size="14" />{{ a.name }}</span>
            <button @click="removeAttachment(a.attachId)"><Icon name="x" :size="12" /></button>
          </div>
          <span v-if="attachBusy" class="upload-pct">{{ Math.round(attachProgress * 100) }}%</span>
        </div>
        <div v-if="showPlus" class="plus-sheet">
          <button @click="takePhoto"><span class="plus-ico"><Icon name="camera" :size="22" /></span>拍照</button>
          <button @click="pickGallery"><span class="plus-ico"><Icon name="image" :size="22" /></span>相册</button>
        </div>
        <div class="composer">
          <button class="round-btn" :disabled="!connected || attachBusy" @click="showPlus = !showPlus"><Icon name="plus" /></button>
          <textarea
            ref="chatInputEl" v-model="chatInput" rows="1" placeholder="发消息…"
            enterkeyhint="send"
            @keydown.enter.exact="onEnter" @input="growInput"
          ></textarea>
          <button v-if="generating" class="round-btn stop" @click="stopChat"><Icon name="stop" /></button>
          <button v-else class="round-btn send" :disabled="!connected || attachBusy || (!chatInput.trim() && !attachments.length)" @click="sendChat">
            <Icon name="send" />
          </button>
        </div>
      </div>
    </section>

    <!-- 文件浏览：全屏 -->
    <section v-if="filesOpen" class="files-page">
      <header class="subbar">
        <button class="icon-btn" @click="closeFiles"><Icon name="back" /></button>
        <div class="subbar-title">文件</div>
        <button class="icon-btn" @click="showChanges"><Icon name="search" /></button>
      </header>
      <div class="file-path mono">{{ fsCwd }}<span v-if="fsPath">/{{ fsPath }}</span></div>
      <div class="row wrap file-actions">
        <button class="sm" @click="upDir">上一级</button>
        <button class="sm" @click="fsRoot">项目根</button>
        <button class="sm" @click="showChanges">改动</button>
      </div>
      <template v-if="fsView === 'list'">
        <div class="file-list">
          <button v-for="e in fsEntries" :key="e.name" class="file-row" @click="enterEntry(e)">
            <Icon :name="e.dir ? 'folder' : 'file'" :size="16" />
            <span class="grow">{{ e.name }}</span>
            <span v-if="!e.dir" class="muted small">{{ e.size }} B</span>
          </button>
        </div>
        <p v-if="!fsEntries.length" class="empty">空目录</p>
      </template>
      <template v-else-if="fsView === 'changes'">
        <div class="file-list">
          <button v-for="c in fsChanges" :key="c.path" class="file-row" @click="openChange(c)">
            <span class="mono small">{{ c.status }}</span>
            <span class="grow mono small">{{ c.path }}</span>
          </button>
        </div>
        <p v-if="!fsChanges.length" class="empty">没有改动（或不是 git 仓库）</p>
      </template>
      <template v-else>
        <p class="file-path mono">{{ fsTitle }}<span v-if="fsTruncated">（已截断）</span></p>
        <p v-if="fsBinary" class="empty">二进制文件，不显示内容。</p>
        <pre v-else class="filetext">{{ fsText }}</pre>
        <button class="sm" @click="fsView = 'list'">返回列表</button>
      </template>
    </section>

    <div v-if="scanning" class="scan-overlay">
      <div class="scan-frame"></div>
      <div class="scan-tip">对准电脑上的配对二维码</div>
      <button class="scan-cancel" @click="stopScanQR">取消</button>
    </div>
    <div v-if="connected && (chatOpen || tab !== 'settings') && update.status === 'available'" class="update-overlay">
      <section class="card update-prompt" role="dialog" aria-modal="true" aria-labelledby="update-title">
        <h3 id="update-title">发现新版本 v{{ update.release.version }}</h3>
        <p class="update-notes">{{ update.release.notes || '暂无版本说明' }}</p>
        <div class="row wrap">
          <button class="primary" :disabled="openingUpdate" @click="openUpdate(update.release.downloadUrl)">下载 Android 安装包</button>
          <button class="ghost" @click="skipUpdate">以后再说</button>
        </div>
        <p class="hint">将用系统浏览器打开官方 GitHub 下载，不会自动安装。</p>
      </section>
    </div>
    <div v-if="toast" class="toast">{{ toast }}</div>
  </main>
</template>

<style scoped>
.history-actions { display: flex; justify-content: center; align-items: center; flex-wrap: wrap; gap: 8px; font-size: 13px; overflow-wrap: anywhere; }
.update-overlay { position: fixed; inset: 0; z-index: 200; background: #0008; display: flex; align-items: center; justify-content: center; padding: 16px; }
.update-prompt { width: 100%; max-width: 480px; max-height: 80dvh; overflow-y: auto; margin: 0; }
.update-prompt h3 { margin-top: 0; }
.update-notes { white-space: pre-wrap; overflow-wrap: anywhere; max-height: 40dvh; overflow-y: auto; font-size: 13px; line-height: 1.65; }
</style>

