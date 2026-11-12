<script setup>
import { computed, nextTick, onMounted, ref } from 'vue'
import { App as CapApp } from '@capacitor/app'
import { Capacitor } from '@capacitor/core'
import { Camera, CameraResultType, CameraSource } from '@capacitor/camera'
import { BarcodeScanner, BarcodeFormat } from '@capacitor-mlkit/barcode-scanning'
import { SpeechRecognition } from '@capacitor-community/speech-recognition'
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
import { secureGet, secureRemove, secureSet, storageIsSecure } from './lib/storage.js'
import { hostKey, pickActive, upsertPairing } from './lib/pairings.js'

const state = ref('idle')
const message = ref('')
const logs = ref([])
const toast = ref('')
let toastTimer
const invitationText = ref('')
const echoText = ref('hello')
const device = ref(null)
const pairing = ref(null)
const pairings = ref([])
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
const chatInput = ref('')
const fileInput = ref(null)
const attachments = ref([]) // {attachId,name,size,kind}
const attachBusy = ref(false)
const attachProgress = ref(0)
const MAX_ATTACH = 3
const MAX_ATTACH_BYTES = 10 * 1024 * 1024
const TEXT_EXT = /\.(txt|md|markdown|json|jsonl|js|mjs|cjs|ts|tsx|jsx|vue|css|scss|html|xml|yml|yaml|toml|ini|cfg|conf|log|csv|tsv|py|go|rs|java|kt|kts|c|h|cpp|hpp|cs|php|rb|sh|bash|zsh|ps1|bat|cmd|sql|graphql|proto|tex)$/i
let currentAssistant = null
let claudeSessionId = ""
let lastSeq = 0
let reconnecting = false
const mode = ref("dontAsk")
const tab = ref("sessions")
const sessionView = ref("projects") // projects | project
const currentSession = ref(null)
const generating = ref(false)
const showPerm = ref(false)
const showPlus = ref(false)
const voiceOn = ref(false)
const scanning = ref(false)
const projectsLoading = ref(false)
const chatInputEl = ref(null)
const overview = ref(null)
const overviewBusy = ref(false)
const overviewErr = ref("")
const theme = ref(localStorage.getItem("ags:theme") || "system")
const msgBox = ref(null)
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

async function doPair(invitation) {
  if (!invitation?.url || !invitation?.route) throw new Error('邀请格式不对')
  state.value = 'connecting'
  message.value = '正在连接中转…'
  session = await pair(invitation, device.value, deviceName.value, onPending)
  ready.value = session.ready
  const entry = {
    hostId: invitation.hostId || session.ready.hostId || '',
    url: invitation.url,
    route: invitation.route,
    credential: invitation.credential,
    name: session.ready.hostName || '电脑',
    deviceId: session.ready.deviceId,
    e2eeKey: invitation.contentKey,
  }
  pairings.value = upsertPairing(pairings.value, entry)
  pairing.value = entry
  await savePairingsStore(pairings.value, entry)
  state.value = 'connected'
  message.value = `已连接到 ${pairing.value.name}`
  log('配对成功，已保存设备授权')
  attachClose()
  loadOverview()
  loadProjects()
  tab.value = 'sessions'
}

async function doReconnect() {
  if (!pairing.value) return
  state.value = 'connecting'
  message.value = '正在重连…'
  session = await reconnect(pairing.value, device.value, onPending)
  ready.value = session.ready
  state.value = 'connected'
  message.value = `已连接到 ${pairing.value.name}`
  log('重连成功')
  attachClose()
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

function attachClose() {
  if (!session || !session.link) return
  session.link.onClose = () => {
    if (reconnecting) return
    autoReconnect()
  }
}

async function autoReconnect() {
  if (reconnecting || !pairing.value) return
  reconnecting = true
  try {
    for (let attempt = 0; attempt < 6; attempt++) {
      if (attempt > 0) await new Promise(r => setTimeout(r, Math.min(30000, 1000 * 2 ** attempt)))
      state.value = 'reconnecting'
      try {
        session = await reconnect(pairing.value, device.value, onPending)
        attachClose()
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
              await session.subscribe(hit.key, lastSeq, onSessionEvent)
              chat.value.push({ role: 'notice', text: '已接回原会话' })
              log('已接回原会话')
            } else {
              chat.value.push({ role: 'notice', text: '原会话已结束，可返回后重新开始' })
            }
          } catch {
            // 接回失败时保持已连接状态即可
          }
        }
        return
      } catch {
        // 继续退避重试
      }
    }
    state.value = 'error'
    message.value = '自动重连失败，请手动重连'
    log('自动重连失败')
  } finally {
    reconnecting = false
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
      session.close()
      session = null
    }
  } finally {
    busy.value = false
  }
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
  if (session && sessionKey.value) {
    try {
      await session.closeSession(sessionKey.value)
    } catch {
      // 连接已断时忽略
    }
  }
  if (session && session.link) session.link.onClose = null
  if (session) session.close()
  session = null
  chatOpen.value = false
  sessionKey.value = ''
  overview.value = null
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

async function startChat(cwd, resumeId) {
  // 开新会话前先结束上一个，避免占用电脑上的会话名额。
  if (session && sessionKey.value) {
    try {
      await session.closeSession(sessionKey.value)
    } catch {
      // 旧会话可能已结束
    }
  }
  const key = await session.startSession(tool.value, cwd, resumeId || '', mode.value)
  lastSeq = 0
  claudeSessionId = ''
  sessionKey.value = key
  chat.value = []
  currentAssistant = null
  generating.value = false
  chatOpen.value = true
  await session.subscribe(key, 0, onSessionEvent)
  if (resumeId) {
    try {
      const msgs = await session.history(tool.value, cwd, resumeId)
      chat.value = msgs.map(m => ({ role: m.role, text: m.text }))
      if (!msgs.length) chat.value.push({ role: 'notice', text: '这段会话没有可显示的历史' })
    } catch (err) {
      chat.value.push({ role: 'notice', text: '历史加载失败：' + (err.message || err) })
    }
    scrollChat()
  }
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

function onSessionEvent(ev) {
  handleSessionEvent(ev)
  scrollChat()
}

function handleSessionEvent(ev) {
  if (ev.seq && ev.seq > lastSeq) lastSeq = ev.seq
  if (ev.kind === 'init' && ev.sessionId) {
    claudeSessionId = ev.sessionId
    return
  }
  const kind = ev.kind
  if (kind === 'init') return
  if (kind === 'text_delta') {
    if (!currentAssistant) {
      chat.value.push({ role: 'assistant', text: '' })
      currentAssistant = chat.value[chat.value.length - 1]
    }
    currentAssistant.text += ev.text || ''
    return
  }
  if (kind === 'text') {
    if (currentAssistant) {
      currentAssistant.text = ev.text || currentAssistant.text
    } else {
      chat.value.push({ role: 'assistant', text: ev.text || '' })
    }
    currentAssistant = null
    return
  }
  if (kind === 'tool_use') {
    chat.value.push({ role: 'tool', text: `${ev.tool || '工具'} ${ev.text || ''}`.trim() })
    return
  }
  if (kind === 'tool_result') {
    return
  }
  if (kind === 'permission_denied') {
    chat.value.push({ role: 'notice', text: `电脑端已拒绝：${ev.tool || '工具'}` })
    return
  }
  if (kind === 'approval') {
    chat.value.push({
      role: 'approval',
      text: `${ev.tool || '工具'}：${ev.text || ''}`.trim(),
      approvalId: ev.approvalId,
      decided: null,
    })
    return
  }
  if (kind === 'result') {
    currentAssistant = null
    generating.value = false
    return
  }
  if (kind === 'exit') {
    generating.value = false
    chat.value.push({ role: 'notice', text: `会话进程退出 ${ev.text || ''}`.trim() })
    return
  }
  if (kind === 'error') {
    generating.value = false
    chat.value.push({ role: 'notice', text: ev.text || '出错了' })
  }
}

function fileKind(file) {
  const type = file.type || ''
  if (type.startsWith('image/')) return 'image'
  if (type.startsWith('text/') || TEXT_EXT.test(file.name || '')) return 'text'
  return 'binary'
}

// 图片先在手机压缩，避免几 MB 的截图占用太长时间。
async function compressImage(file) {
  const bitmap = await createImageBitmap(file)
  const scale = Math.min(1, 2048 / Math.max(bitmap.width, bitmap.height))
  const canvas = document.createElement('canvas')
  canvas.width = Math.max(1, Math.round(bitmap.width * scale))
  canvas.height = Math.max(1, Math.round(bitmap.height * scale))
  canvas.getContext('2d').drawImage(bitmap, 0, 0, canvas.width, canvas.height)
  const blob = await new Promise(r => canvas.toBlob(r, 'image/jpeg', 0.85))
  if (!blob) throw new Error('compress failed')
  return {
    data: new Uint8Array(await blob.arrayBuffer()),
    name: (file.name || 'image').replace(/\.[^.]+$/, '') + '.jpg',
  }
}

// 打开系统文件选择器可能让 WebView 暂停、连接断开；等自动重连后再继续。
async function waitConnected(timeout = 20000) {
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    if (state.value === 'connected' && session) return true
    await new Promise(r => setTimeout(r, 300))
  }
  return false
}

function pickFiles() {
  fileInput.value?.click()
}

async function onPickFiles(ev) {
  const files = [...(ev.target.files || [])]
  ev.target.value = ''
  if (!files.length) return
  let slots = MAX_ATTACH - attachments.value.length
  for (const file of files) {
    if (slots <= 0) { log(`每条消息最多 ${MAX_ATTACH} 个附件`); break }
    let data
    let name = file.name || 'attachment'
    let mediaType = file.type || 'application/octet-stream'
    const kind = fileKind(file)
    let preview = ''
    if (kind === 'image') {
      try {
        const small = await compressImage(file)
        data = small.data
        name = small.name
        mediaType = 'image/jpeg'
        preview = URL.createObjectURL(new Blob([data], { type: mediaType }))
      } catch {
        data = new Uint8Array(await file.arrayBuffer())
        preview = URL.createObjectURL(file)
      }
    } else {
      data = new Uint8Array(await file.arrayBuffer())
    }
    if (data.byteLength > MAX_ATTACH_BYTES) {
      log(`附件 ${name} 超过 10 MB，已跳过`)
      continue
    }
    await addAttachmentData({ name, mediaType, kind, data, preview })
    slots--
  }
}

function removeAttachment(id) {
  attachments.value = attachments.value.filter(a => a.attachId !== id)
}

function sendChat() {
  run(async () => {
    const text = chatInput.value.trim()
    const ids = attachments.value.map(a => a.attachId)
    if ((!text && !ids.length) || !sessionKey.value || !connected.value) return
    chat.value.push({ role: 'user', text, files: attachments.value.map(a => a.name) })
    chatInput.value = ''
    attachments.value = []
    currentAssistant = null
    generating.value = true
    session.sendTurn(sessionKey.value, text, ids)
    if (chatInputEl.value) chatInputEl.value.style.height = 'auto'
    scrollChat()
  })
}

function stopChat() {
  if (sessionKey.value) session.stopTurn(sessionKey.value)
}

// 返回 = 离开会话并结束电脑上的进程（对话历史仍保留，可再“继续”）。
function closeChat() {
  run(async () => {
    if (session && sessionKey.value) {
      try {
        await session.closeSession(sessionKey.value)
      } catch {
        // 忽略：连接已断时电脑端会自动回收
      }
    }
    sessionKey.value = ''
    chatOpen.value = false
    attachments.value = []
    generating.value = false
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
    if (pairing.value && !pairing.value.e2eeKey) {
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
        if (voiceOn.value) { toggleVoice(); return }
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
const modeLabels = {
  dontAsk: '只读',
  default: '手动',
  acceptEdits: '自动编辑',
  plan: '计划',
  auto: '自动',
  bypassPermissions: '完全',
}

// 用按钮组而不是原生 select：部分 Android WebView 的原生下拉只改值、不派发 change。
async function switchMode(next) {
  if (!session || next === mode.value) return
  log(`切换权限：${modeLabels[next] || next}`)
  if (next === 'bypassPermissions' && !window.confirm('完全模式允许 Claude 执行任意命令、修改任意文件。确定切换？')) {
    return
  }
  if (busy.value) return
  busy.value = true
  try {
    const key = await session.setMode(sessionKey.value, next)
    const moved = key !== sessionKey.value
    sessionKey.value = key
    if (moved) {
      currentAssistant = null
      await session.subscribe(key, 0, onSessionEvent)
    }
    chat.value.push({
      role: 'notice',
      text: `权限模式：${modeLabels[next]}${moved ? '（会话已重启，上下文保留）' : ''}`,
    })
    mode.value = next
    log('权限模式切换为 ' + modeLabels[next])
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
  localStorage.setItem('ags:theme', theme.value)
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

async function scrollChat() {
  await nextTick()
  const el = msgBox.value
  if (el) el.scrollTop = el.scrollHeight
}

function pickTool(next) {
  if (next === tool.value) return
  tool.value = next
  projects.value = []
  sessions.value = []
  currentProject.value = null
  sessionView.value = 'projects'
  log('切换工具：' + toolLabels[next])
  loadProjects()
}

function decideApproval(item, allow) {
  if (!sessionKey.value || item.decided) return
  session.decide(sessionKey.value, item.approvalId, allow)
  item.decided = allow ? 'allowed' : 'denied'
  chat.value.push({ role: 'notice', text: allow ? '已允许该操作' : '已拒绝该操作' })
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

function renderMd(text) {
  if (!text) return ''
  try {
    return DOMPurify.sanitize(marked.parse(text, { breaks: true, gfm: true }))
  } catch {
    return text
  }
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

async function toggleVoice() {
  try {
    if (voiceOn.value) {
      await SpeechRecognition.stop()
      voiceOn.value = false
      return
    }
    const { available } = await SpeechRecognition.available()
    if (!available) { notify('这台手机的语音识别不可用'); return }
    const perm = await SpeechRecognition.requestPermissions()
    if (perm.speechRecognition !== 'granted') { notify('没有麦克风权限'); return }
    SpeechRecognition.removeAllListeners?.()
    SpeechRecognition.addListener('partialResults', (data) => {
      const text = data?.matches?.[0]
      if (text) {
        chatInput.value = text
        growInput()
      }
    })
    SpeechRecognition.addListener('listeningState', (data) => {
      if (data?.status === 'stopped') voiceOn.value = false
    })
    await SpeechRecognition.start({ language: 'zh-CN', partialResults: true, popup: false })
    voiceOn.value = true
  } catch (err) {
    voiceOn.value = false
    notify('语音识别失败：' + (err.message || err))
  }
}

</script>

<template>
  <main class="app">
    <!-- 未连接且不在聊天里：显示可复用的连接面板 -->
    <div v-if="!connected && !chatOpen" class="page connect-gate">
      <ConnectPanel
        :state="state" :message="message" :status-label="statusLabel"
        :store="pairing" :pairings="pairings" :busy="busy"
        @connect="connectSaved" @switch="switchTo" @remove="removeComputer"
        @pair="submitInvitationText" @scan="scanQR"
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

        <div class="section-title">关于</div>
        <div class="card about-card">
          <div class="about-name">agent-sync</div>
          <div class="muted small">手机遥控 · 协议 v1 · 内容端到端加密</div>
        </div>
      </section>

      <nav v-if="!chatOpen && !filesOpen" class="tabbar">
        <button :class="{ active: tab === 'home' }" @click="go('home')"><Icon name="home" :size="22" /><span>首页</span></button>
        <button :class="{ active: tab === 'sessions' }" @click="go('sessions')"><Icon name="chat" :size="22" /><span>会话</span></button>
        <button :class="{ active: tab === 'settings' }" @click="go('settings')"><Icon name="settings" :size="22" /><span>设置</span></button>
      </nav>
    </template>

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
        <span v-if="!connected" class="chat-offline">连接已断开</span>
      </div>
      <div v-if="showPerm" class="perm-sheet">
        <button v-for="(label, key) in modeLabels" :key="key" :class="{ on: mode === key }" @click="switchMode(key); showPerm = false">
          {{ label }}
        </button>
      </div>

      <div ref="msgBox" class="messages">
        <template v-for="(m, i) in chat" :key="i">
          <div v-if="m.role === 'user'" class="msg user">
            <div class="bubble">
              <div v-if="m.text" class="bubble-text">{{ m.text }}</div>
              <div v-if="m.files && m.files.length" class="bubble-files">
                <span v-for="f in m.files" :key="f" class="file-chip"><Icon name="file" :size="12" />{{ f }}</span>
              </div>
            </div>
          </div>
          <div v-else-if="m.role === 'assistant'" class="msg assistant">
            <div class="assistant-who">{{ toolLabels[tool] }}</div>
            <div class="md" v-html="renderMd(m.text)"></div>
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
        </template>
        <p v-if="!chat.length" class="empty chat-empty">问点什么，或者点左下角的 + 发送图片和文件。</p>
      </div>

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
          <button @click="pickFiles"><span class="plus-ico"><Icon name="file" :size="22" /></span>文件</button>
        </div>
        <div class="composer">
          <button class="round-btn" :disabled="!connected || attachBusy" @click="showPlus = !showPlus"><Icon name="plus" /></button>
          <textarea
            ref="chatInputEl" v-model="chatInput" rows="1" placeholder="发消息…"
            @keydown.enter.exact.prevent="sendChat" @input="growInput"
          ></textarea>
          <button class="round-btn mic" :class="{ on: voiceOn }" :disabled="!connected" @click="toggleVoice"><Icon name="mic" /></button>
          <button v-if="generating" class="round-btn stop" @click="stopChat"><Icon name="stop" /></button>
          <button v-else class="round-btn send" :disabled="!connected || attachBusy || (!chatInput.trim() && !attachments.length)" @click="sendChat">
            <Icon name="send" />
          </button>
        </div>
      </div>
      <input ref="fileInput" type="file" multiple class="hidden-file" @change="onPickFiles" />
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
    <div v-if="toast" class="toast">{{ toast }}</div>
  </main>
</template>

