<script setup>
import { computed, nextTick, onMounted, ref } from 'vue'
import { App as CapApp } from '@capacitor/app'
import { Capacitor } from '@capacitor/core'
import {
  fingerprint,
  initCrypto,
  newDevice,
  pair,
  parseDeepLink,
  reconnect,
} from './lib/protocol.js'
import { secureGet, secureRemove, secureSet, storageIsSecure } from './lib/storage.js'

const state = ref('idle')
const message = ref('')
const logs = ref([])
const invitationText = ref('')
const echoText = ref('hello')
const device = ref(null)
const pairing = ref(null)
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
let currentAssistant = null
let claudeSessionId = ""
let lastSeq = 0
let reconnecting = false
const mode = ref("dontAsk")
const tab = ref("home")
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

async function loadPairing() {
  const raw = await secureGet('pairing')
  pairing.value = raw ? JSON.parse(raw) : null
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
  pairing.value = {
    url: invitation.url,
    route: invitation.route,
    credential: invitation.credential,
    name: session.ready.hostName || '电脑',
    deviceId: session.ready.deviceId,
    e2eeKey: invitation.contentKey,
  }
  await secureSet('pairing', JSON.stringify(pairing.value))
  state.value = 'connected'
  message.value = `已连接到 ${pairing.value.name}`
  log('配对成功，已保存设备授权')
  attachClose()
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
}

// 网络断开时自动重连，并尝试接回原会话。
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

function disconnect() {
  const finish = () => {
    if (session && session.link) session.link.onClose = null
    if (session) session.close()
    session = null
    state.value = 'idle'
    message.value = '已断开'
    chatOpen.value = false
    sessionKey.value = ''
    log('已断开')
  }
  // 先尽量结束电脑上的会话，再断开连接。
  if (session && sessionKey.value) {
    session
      .closeSession(sessionKey.value)
      .catch(() => {})
      .finally(finish)
  } else {
    finish()
  }
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

function forget() {
  run(async () => {
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
    pairing.value = null
    await secureRemove('pairing')
    state.value = 'idle'
    message.value = '已清除配对'
    log('已清除配对（设备身份仍保留，可重新扫码）')
  })
}

// ---- 会话 ----

function loadProjects() {
  run(async () => {
    projects.value = await session.projects(tool.value)
    log(`项目 ${projects.value.length} 个`)
  })
}

function openProject(p) {
  run(async () => {
    currentProject.value = p
    sessions.value = await session.sessions(tool.value, p.cwd)
    log(`会话 ${sessions.value.length} 条`)
  })
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
  chatOpen.value = true
  await session.subscribe(key, 0, onSessionEvent)
}

function newSession() {
  run(async () => {
    if (!currentProject.value) throw new Error('先选一个项目')
    await startChat(currentProject.value.cwd, '')
    log('已新建会话')
  })
}

function resumeSession(s) {
  run(async () => {
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
    chat.value.push({ role: 'toolresult', text: (ev.text || '').slice(0, 300) })
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
    chat.value.push({ role: 'notice', text: '本轮结束' })
    return
  }
  if (kind === 'exit') {
    chat.value.push({ role: 'notice', text: `会话进程退出 ${ev.text || ''}`.trim() })
  }
}

function sendChat() {
  run(async () => {
    const text = chatInput.value.trim()
    if (!text || !sessionKey.value) return
    chat.value.push({ role: 'user', text })
    chatInput.value = ''
    currentAssistant = null
    session.sendTurn(sessionKey.value, text)
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
  })
}

// 结束会话：立刻释放电脑上的会话，之后可以在电脑端打开同一个对话。
function endChat() {
  run(async () => {
    if (sessionKey.value && session) {
      await session.closeSession(sessionKey.value)
    }
    sessionKey.value = ''
    chatOpen.value = false
    log('会话已结束，电脑端可以继续打开')
  })
}

onMounted(async () => {
  applyTheme()
  try {
    await initCrypto()
    await loadOrCreateDevice()
    await loadPairing()
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
  log('切换工具：' + toolLabels[next])
}

function decideApproval(item, allow) {
  if (!sessionKey.value || item.decided) return
  session.decide(sessionKey.value, item.approvalId, allow)
  item.decided = allow ? 'allowed' : 'denied'
  chat.value.push({ role: 'notice', text: allow ? '已允许该操作' : '已拒绝该操作' })
}
</script>

<template>
  <main class="app">
    <header class="top">
      <div>
        <p class="eyebrow">AGENT SYNC</p>
        <h1>远程连接</h1>
      </div>
      <span class="badge" :data-state="state">{{ statusLabel }}</span>
    </header>

    <section v-show="tab === 'home'" class="card status">
      <p class="line">{{ message || '把电脑上生成的配对邀请粘贴到下面，或用手机相机扫码。' }}</p>
      <dl v-if="device" class="meta">
        <div><dt>本机设备</dt><dd>{{ deviceName }}</dd></div>
        <div><dt>指纹</dt><dd class="mono">{{ fingerprint(device.publicKey).slice(0, 16) }}…</dd></div>
        <div v-if="pairing"><dt>已配对电脑</dt><dd>{{ pairing.name }}</dd></div>
      </dl>
      <div class="actions">
        <button v-if="!pairing" class="primary" :disabled="busy" @click="$refs.invite.focus()">粘贴邀请</button>
        <button v-if="pairing" class="primary" :disabled="busy" @click="connectSaved">重新连接</button>
        <button v-if="pairing" :disabled="busy" @click="disconnect">断开</button>
        <button v-if="pairing" class="ghost" :disabled="busy" @click="forget">清除配对</button>
      </div>
    </section>

    <section v-if="!pairing" class="card">
      <h2>配对邀请</h2>
      <textarea
        ref="invite"
        v-model="invitationText"
        rows="5"
        spellcheck="false"
        placeholder="在电脑上运行 agent-sync-host invite，粘贴这里；或让电脑显示二维码用手机相机扫码。"
      />
      <button class="primary wide" :disabled="busy" @click="submitInvitation">开始配对</button>
      <p v-if="nativePlatform" class="hint">手机上也可以直接用系统相机扫电脑显示的二维码。</p>
    </section>

    <section v-if="filesOpen" class="card files">
      <div class="chat-head">
        <h2>文件</h2>
        <div class="actions">
          <button @click="upDir">上一级</button>
          <button @click="fsRoot">项目根</button>
          <button @click="showChanges">改动</button>
          <button class="ghost" @click="closeFiles">关闭</button>
        </div>
      </div>
      <p class="hint mono">{{ fsCwd }}<span v-if="fsPath">/{{ fsPath }}</span></p>
      <template v-if="fsView === 'list'">
        <ul class="devices">
          <li v-for="e in fsEntries" :key="e.name">
            <button class="row" @click="enterEntry(e)">
              <span>{{ e.name }}{{ e.dir ? '/' : '' }}</span>
              <span v-if="!e.dir" class="small">{{ e.size }} B</span>
            </button>
          </li>
        </ul>
        <p v-if="!fsEntries.length" class="hint">空目录</p>
      </template>
      <template v-else-if="fsView === 'changes'">
        <ul class="devices">
          <li v-for="c in fsChanges" :key="c.path">
            <button class="row" @click="openChange(c)">
              <span class="mono small">{{ c.status }} {{ c.path }}</span>
            </button>
          </li>
        </ul>
        <p v-if="!fsChanges.length" class="hint">没有改动（或不是 git 仓库）</p>
      </template>
      <template v-else>
        <p class="hint mono">{{ fsTitle }}<span v-if="fsTruncated">（已截断）</span></p>
        <p v-if="fsBinary" class="hint">二进制文件，不显示内容。</p>
        <pre v-else class="filetext">{{ fsText }}</pre>
        <button class="ghost" @click="fsView = 'list'">返回列表</button>
      </template>
    </section>

    <section v-show="tab === 'sessions' && !filesOpen" v-else-if="!chatOpen" class="card">
      <h2>会话</h2>
      <div class="mode-row">
        <span>工具</span>
        <div class="mode-chips">
          <button
            v-for="(label, key) in toolLabels"
            :key="key"
            :class="{ active: tool === key }"
            :disabled="busy || !connected"
            @click="pickTool(key)"
          >
            {{ label }}
          </button>
        </div>
      </div>
      <p v-if="tool === 'codex'" class="hint">Codex 按沙箱模式执行，暂不支持逐操作审批。</p>
      <div class="actions">
        <div class="mode-row">
          <span>新建权限</span>
          <div class="mode-chips">
            <button
              v-for="(label, key) in modeLabels"
              :key="key"
              :class="{ active: mode === key }"
              :disabled="busy || !connected"
              @click="mode = key"
            >
              {{ label }}
            </button>
          </div>
        </div>
        <button class="primary" :disabled="busy || !connected" @click="loadProjects">加载项目</button>
        <button v-if="currentProject" :disabled="busy || !connected" @click="newSession">新建会话</button>
        <button v-if="currentProject" :disabled="busy || !connected" @click="openFiles(currentProject.cwd)">浏览文件</button>
      </div>
      <ul v-if="projects.length" class="devices">
        <li v-for="p in projects" :key="p.path">
          <button class="row" :disabled="busy || !connected" @click="openProject(p)">
            <span class="mono small">{{ p.cwd }}</span>
            <span class="tag">{{ p.sessions }} 个会话</span>
          </button>
        </li>
      </ul>
      <template v-if="currentProject">
        <h2 class="sub">项目：{{ currentProject.cwd }}</h2>
        <ul v-if="sessions.length" class="devices">
          <li v-for="s in sessions" :key="s.id">
            <button class="row" :disabled="busy || !connected" @click="resumeSession(s)">
              <span>{{ s.title || s.id.slice(0, 8) }}</span>
              <span class="small">{{ new Date(s.updatedAt).toLocaleString() }}</span>
            </button>
          </li>
        </ul>
        <p v-else class="hint">这个项目还没有会话，点“新建会话”。</p>
      </template>
    </section>

    <section v-show="tab === 'sessions' && !filesOpen" v-else class="card chat">
      <div class="chat-head">
        <h2>会话 · {{ toolLabels[tool] }}</h2>
        <div class="actions">
          <button :disabled="!connected" @click="stopChat">停止回合</button>
          <button :disabled="!connected" @click="endChat">结束会话</button>
          <button v-if="currentProject" :disabled="!connected" @click="openFiles(currentProject.cwd)">文件</button>
          <button class="ghost" @click="closeChat">返回</button>
        </div>
      </div>
      <div class="mode-row">
        <span>权限</span>
        <div class="mode-chips">
          <button
            v-for="(label, key) in modeLabels"
            :key="key"
            :class="{ active: mode === key }"
            :disabled="busy || !connected"
            @click="switchMode(key)"
          >
            {{ label }}
          </button>
        </div>
      </div>
      <ol class="messages">
        <li v-for="(m, i) in chat" :key="i" :data-role="m.role">
          <template v-if="m.role === 'approval'">
            <span class="who">需要批准</span>
            <span class="text">{{ m.text }}</span>
            <div v-if="m.decided === null" class="actions">
              <button class="primary" :disabled="!connected" @click="decideApproval(m, true)">允许</button>
              <button :disabled="!connected" @click="decideApproval(m, false)">拒绝</button>
            </div>
            <span v-else class="small">{{ m.decided === 'allowed' ? '已允许' : '已拒绝' }}</span>
          </template>
          <template v-else>
            <span class="who">{{ m.role === 'user' ? '我' : m.role === 'assistant' ? toolLabels[tool] : '' }}</span>
            <span class="text">{{ m.text }}</span>
          </template>
        </li>
      </ol>
      <div class="composer">
        <input v-model="chatInput" placeholder="直接问 Claude；写文件、执行命令受当前权限模式限制" @keyup.enter="sendChat" />
        <button class="primary" :disabled="!connected" @click="sendChat">发送</button>
      </div>
    </section>

    <section v-show="tab === 'home'" v-if="paired && !chatOpen" class="card">
      <h2>设备</h2>
      <div class="actions">
        <button :disabled="busy || !connected" @click="loadDevices">设备列表</button>
        <button :disabled="busy || !connected" @click="sendEcho">发送回显</button>
      </div>
      <ul v-if="devices.length" class="devices">
        <li v-for="d in devices" :key="d.id">
          <span>{{ d.name }}</span>
          <span class="mono small">{{ d.fingerprint.slice(0, 12) }}…</span>
          <span class="tag" :data-online="d.online">{{ d.online ? '在线' : '离线' }}</span>
        </li>
      </ul>
    </section>

    <section v-show="tab === 'home'" class="card log">
      <h2>日志</h2>
      <p v-if="!logs.length" class="hint">暂无</p>
      <ol>
        <li v-for="(line, i) in logs" :key="i">{{ line }}</li>
      </ol>
    </section>
    <section v-show="tab === 'settings'" class="card">
      <h2>设置</h2>
      <div class="mode-row">
        <span>主题</span>
        <div class="mode-chips">
          <button :class="{ active: theme === 'system' }" @click="setTheme('system')">跟随系统</button>
          <button :class="{ active: theme === 'light' }" @click="setTheme('light')">浅色</button>
          <button :class="{ active: theme === 'dark' }" @click="setTheme('dark')">深色</button>
        </div>
      </div>
      <dl class="meta">
        <div><dt>已配对电脑</dt><dd>{{ pairing ? pairing.name : '未配对' }}</dd></div>
        <div v-if="device"><dt>设备指纹</dt><dd class="mono">{{ fingerprint(device.publicKey).slice(0, 16) }}…</dd></div>
      </dl>
      <div class="actions">
        <button v-if="pairing" @click="disconnect">断开</button>
        <button v-if="pairing" class="ghost" @click="forget">清除配对</button>
      </div>
      <p class="hint">Agent Sync · 协议 v1 · AI 在所选电脑上执行</p>
    </section>

    <nav v-if="!chatOpen" class="tabbar">
      <button :class="{ active: tab === 'home' }" @click="tab = 'home'">连接</button>
      <button :class="{ active: tab === 'sessions' }" @click="tab = 'sessions'">会话</button>
      <button :class="{ active: tab === 'settings' }" @click="tab = 'settings'">设置</button>
    </nav>
  </main>
</template>
