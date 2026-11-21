<template>
  <div class="remote">
    <!-- 官方模式未登录：首屏直接给账号密码，不让用户先去找“编辑连接信息” -->
    <div v-if="needLogin" class="card login-first">
      <div class="card-head">
        <h3>{{ t('remote.loginFirstTitle') }}</h3>
        <span class="badge info">1 / 3</span>
      </div>
      <div class="muted small">{{ t('remote.loginFirstHint') }}</div>
      <div class="row wrap mt">
        <input v-model.trim="auth.username" autocomplete="username" :placeholder="t('remote.username')" @keyup.enter="submitAuth" />
        <input v-model="auth.password" type="password" autocomplete="current-password" :placeholder="t('remote.password')" @keyup.enter="submitAuth" />
        <button class="primary" :disabled="authBusy || !auth.username || !auth.password" @click="submitAuth">
          {{ authBusy ? t('remote.loggingIn') : t('remote.login') }}
        </button>
      </div>
      <div class="hint mt">{{ t('remote.loginCredentialHint') }}</div>
      <div v-if="authError" class="err-box mt">{{ authError }}</div>
      <div class="row wrap mt">
        <a href="#" @click.prevent="openRegister">{{ t('remote.noAccount') }}</a>
        <a href="#" @click.prevent="openDownload">{{ t('remote.downloadApp') }}</a>
      </div>
    </div>

    <!-- 连接信息：默认只读展示，右上角进入编辑；编辑时表单就在最上面 -->
    <div class="card">
      <div class="card-head">
        <h3>{{ t('remote.title') }}</h3>
        <div class="row">
          <span class="badge" :class="brokerBadgeClass">{{ st.running ? brokerStateLabel : t('remote.stopped') }}</span>
          <button v-if="!editing" class="sm ghost" :disabled="busy" @click="startEdit">
            <NavIcon name="settings" :size="13" /> {{ t('remote.editInfo') }}
          </button>
        </div>
      </div>
      <div class="muted small">{{ t('remote.subtitle') }}</div>
      <div class="steps mt">
        <span :class="{ on: stepIndex >= 1, done: stepIndex > 1 }">{{ t('remote.stepLogin') }}</span>
        <span :class="{ on: stepIndex >= 2, done: stepIndex > 2 }">{{ t('remote.stepStart') }}</span>
        <span :class="{ on: stepIndex >= 3 }">{{ t('remote.stepScan') }}</span>
      </div>
      <div v-if="st.installError" class="err-box mt">{{ t('remote.installFailed', { error: st.installError }) }}</div>
      <div v-if="st.needsRestart" class="warn-box mt">{{ t('remote.needsRestart') }}</div>
      <div v-if="st.running && st.lastError" class="err-box mt">{{ st.lastError }}</div>

      <template v-if="!editing">
        <div v-if="st.running" class="info-grid">
          <div><div class="k">{{ t('remote.hostName') }}</div><div class="v">{{ st.hostName || '—' }}</div></div>
          <div><div class="k">{{ t('remote.brokerAddr') }}</div><div class="v mono">{{ st.broker || '—' }}</div></div>
          <div class="wide"><div class="k">{{ t('remote.hostId') }}</div><div class="v mono ellipsis">{{ st.hostId || '—' }}</div></div>
        </div>
        <div v-else class="info-box mt">{{ t('remote.hostNotRunning') }}</div>

        <div v-if="st.loggedIn" class="info-grid mt">
          <div><div class="k">{{ t('remote.plan') }}</div><div class="v">{{ planLabel(st.plan) }}</div></div>
          <div><div class="k">{{ t('remote.expiry') }}</div><div class="v">{{ fmtExpiry(st.planExpiresAt) }}</div></div>
          <div class="wide">
            <div class="k">{{ t('remote.usage') }}</div>
            <div class="v">{{ fmtSize(st.usedBytes || 0) }} / {{ fmtSize(st.quotaBytes || 0) }}</div>
            <div class="usage-bar"><i :style="{ width: usagePercent + '%' }"></i></div>
          </div>
          <div class="wide"><a href="#" @click.prevent="openUpgrade">{{ t('remote.upgrade') }} →</a></div>
        </div>

        <div class="row wrap mt">
          <button v-if="!st.running" class="primary" :disabled="busy" @click="start">{{ busy ? t('common.working') : t('remote.start') }}</button>
          <button v-else class="danger" :disabled="busy" @click="stop">{{ busy ? t('common.working') : t('remote.stop') }}</button>
          <button class="primary" :disabled="busy || !st.running" @click="invite"><NavIcon name="device" :size="14" /> {{ t('remote.invite') }}</button>
          <button class="sm ghost" :disabled="busy" @click="refresh">{{ t('common.refresh') }}</button>
        </div>
      </template>

      <!-- 编辑态：就在最上面，不用翻到底 -->
      <template v-else>
        <div class="form-grid mt">
          <div class="form-row">
            <label>{{ t('remote.name') }}</label>
            <input v-model.trim="form.name" :placeholder="t('remote.nameHint')" />
          </div>
          <div class="form-row">
            <label>{{ t('remote.mode') }}</label>
            <select v-model="form.mode">
              <option value="official">{{ t('remote.modeOfficial') }}</option>
              <option value="selfhost">{{ t('remote.modeSelfHost') }}</option>
            </select>
          </div>
          <div class="form-row wide">
            <label>{{ t('remote.broker') }}</label>
            <input v-model.trim="form.broker" placeholder="wss://agent-sync.yirenzaowusuo.cn/mqtt" :disabled="form.mode === 'official'" />
          </div>
          <template v-if="form.mode === 'official'">
            <div v-if="!auth.loggedIn" class="form-row wide">
              <label>{{ t('remote.accountForm') }}</label>
              <div class="row wrap">
                <input v-model.trim="auth.username" autocomplete="username" :placeholder="t('remote.username')" />
                <input v-model="auth.password" type="password" autocomplete="current-password" :placeholder="t('remote.password')" />
                <button class="primary" :disabled="authBusy" @click="submitAuth">
                  {{ authBusy ? t('remote.loggingIn') : t('remote.login') }}
                </button>
              </div>
              <div class="hint">{{ t('remote.loginCredentialHint') }}</div>
              <div v-if="authError" class="err-box mt">{{ authError }}</div>
              <div class="hint">
                <a href="#" @click.prevent="openRegister">{{ t('remote.noAccount') }}</a>
              </div>
            </div>
            <div v-else class="form-row wide">
              <label>{{ t('remote.account') }}</label>
              <div class="row wrap">
                <span class="badge ok">{{ t('remote.loggedIn') }}</span>
                <span class="mono">{{ auth.accountId }}</span>
                <button class="sm ghost" :disabled="authBusy" @click="logout">{{ t('remote.logout') }}</button>
              </div>
            </div>
          </template>
          <template v-if="form.mode === 'selfhost'">
            <div class="form-row">
              <label>{{ t('remote.username') }}</label>
              <input v-model.trim="form.username" autocomplete="username" />
            </div>
            <div class="form-row">
              <label>{{ t('remote.password') }}</label>
              <input v-model="form.password" type="password" autocomplete="current-password" />
            </div>
            <div class="form-row wide">
              <div class="row wrap">
                <button class="sm ghost" :disabled="testBusy" @click="testBroker">
                  {{ testBusy ? t('remote.testingConnection') : t('remote.testConnection') }}
                </button>
                <span v-if="testMessage" class="small" :class="testOK ? 'muted' : 'err'">{{ testMessage }}</span>
              </div>
            </div>
          </template>
          <div class="form-row">
            <label>{{ t('remote.proxy') }}</label>
            <input v-model.trim="form.proxy" placeholder="http://127.0.0.1:7890" />
          </div>
        </div>
        <div class="row wrap mt">
          <button class="primary" :disabled="busy" @click="saveConfig">{{ t('remote.save') }}</button>
          <button class="ghost" :disabled="busy" @click="cancelEdit">{{ t('common.cancel') }}</button>
          <span v-if="saved" class="ok small">{{ t('remote.saved') }}</span>
        </div>
      </template>

      <div v-if="err" class="err-box mt">{{ err }}</div>
      <div class="warn-box mt">{{ t('remote.approvalWarn') }}</div>
      <div class="muted small mt">
        <a href="#" @click.prevent="openDownload">{{ t('remote.downloadApp') }} →</a>
      </div>
    </div>

    <!-- 配对邀请：二维码 + 可复制的原文 -->
    <div v-if="invitation" class="card invite">
      <div class="card-head">
        <h3>{{ t('remote.inviteTitle') }}</h3>
        <button class="sm ghost" @click="invitation = null">{{ t('common.close') }}</button>
      </div>
      <div class="invite-body">
        <img :src="invitation.qr" :alt="t('remote.inviteTitle')" class="qr" />
        <div class="grow">
          <div class="muted small">{{ t('remote.inviteHint') }}</div>
          <textarea class="invite-text mono" readonly rows="3" :value="invitation.text"></textarea>
          <button class="sm" @click="copyInvite">{{ t('remote.copyInvite') }}</button>
        </div>
      </div>
      <div v-if="st.mode === 'selfhost'" class="warn-box mt">{{ t('remote.selfhostInviteWarn') }}</div>
    </div>

    <!-- 待批准：手机第一次连进来，必须在这里点允许 -->
    <div class="card">
      <div class="card-head">
        <h3>{{ t('remote.pending') }}</h3>
        <span class="muted small">{{ t('remote.pendingHint') }}</span>
      </div>
      <div v-if="!st.pending?.length" class="muted small">{{ t('remote.noPending') }}</div>
      <div v-for="p in st.pending" :key="p.id" class="line">
        <div class="grow">
          <div><b>{{ p.deviceName || t('remote.unnamed') }}</b></div>
          <div class="muted small mono ellipsis">{{ p.fingerprint }}</div>
          <div v-if="p.expires" class="muted small">{{ pendingLeft(p) }}</div>
        </div>
        <button class="sm primary" :disabled="busy || pendingExpired(p)" @click="decide(p.id, true)">{{ t('remote.approve') }}</button>
        <button class="sm danger" :disabled="busy || pendingExpired(p)" @click="decide(p.id, false)">{{ t('remote.deny') }}</button>
      </div>
    </div>

    <!-- 已配对设备：可以重命名、删除 -->
    <div class="card">
      <div class="card-head">
        <h3>{{ t('remote.devices') }}</h3>
        <button class="sm ghost" @click="refresh">{{ t('common.refresh') }}</button>
      </div>
      <div v-if="!st.devices?.length" class="muted small">{{ t('remote.noDevices') }}</div>
      <div v-for="d in st.devices" :key="d.id" class="line">
        <span class="online" :class="{ on: d.online }" :title="d.online ? t('remote.online') : t('remote.offline')"></span>
        <div class="grow">
          <template v-if="renaming === d.id">
            <input v-model="newName" class="name-input" :placeholder="d.name" @keyup.enter="saveName(d)" />
          </template>
          <template v-else>
            <div>
              <b>{{ d.name || t('remote.unnamed') }}</b>
              <span v-if="d.revoked" class="badge err revoked-badge">{{ t('remote.revokedBadge') }}</span>
            </div>
            <div class="muted small">
              <span class="mono">{{ shortFP(d.fingerprint) }}</span>
              <span v-if="d.lastSeen"> · {{ t('remote.lastSeen', { when: when(d.lastSeen) }) }}</span>
            </div>
          </template>
        </div>
        <template v-if="renaming === d.id">
          <button class="sm primary" :disabled="busy" @click="saveName(d)">{{ t('remote.saveName') }}</button>
          <button class="sm ghost" @click="renaming = null">{{ t('common.cancel') }}</button>
        </template>
        <template v-else>
          <button v-if="!d.revoked" class="sm ghost" @click="startRename(d)">{{ t('remote.rename') }}</button>
          <button class="sm danger" :disabled="busy" @click="removeDevice(d)">{{ t('remote.deleteDevice') }}</button>
        </template>
      </div>
    </div>

    <!-- 正在手机上跑的会话 -->
    <div class="card">
      <div class="card-head">
        <h3>{{ t('remote.active') }}</h3>
        <span class="muted small">{{ t('remote.activeHint') }}</span>
      </div>
      <div v-if="!st.active?.length" class="muted small">{{ t('remote.noActive') }}</div>
      <div v-for="a in st.active" :key="a.key" class="line">
        <span class="badge info">{{ a.tool === 'codex' ? 'Codex' : 'Claude Code' }}</span>
        <div class="grow">
          <div class="mono ellipsis">{{ a.cwd }}</div>
          <div class="muted small">{{ t('remote.sessionMeta', { mode: modeLabel(a.mode), n: a.subscribers }) }}</div>
        </div>
        <button class="sm danger" :disabled="busy" @click="stopSession(a)">{{ t('remote.stopSession') }}</button>
      </div>
    </div>

    <Notice :notice="notice" />
  </div>
</template>

<script setup>
import { ref, computed, onActivated, onDeactivated, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, BrowserOpenURL } from '@/api'
import { useNotice } from '@/composables/useNotice'
import { fromNow, fmtSize } from '@/utils/format'
import Notice from '@/components/Notice.vue'
import NavIcon from '@/components/NavIcon.vue'

const { t } = useI18n()
const { notice, ok, fail } = useNotice()

const st = ref({ running: false, pending: [], devices: [], active: [] })
const form = ref({ mode: 'official', broker: '', username: '', password: '', name: '', claudeBin: '', bin: '', proxy: '' })
const auth = ref({ loggedIn: false, accountId: '', username: '', password: '' })
const authBusy = ref(false)
const authError = ref('')
const testBusy = ref(false)
const testMessage = ref('')
const testOK = ref(false)
const busy = ref(false)
const err = ref('')
const saved = ref(false)
const editing = ref(false)
const invitation = ref(null)
const renaming = ref(null)
const newName = ref('')
const now = ref(Date.now())
let loaded = ''
let timer
let nowTimer

const MODES = {
  readonly: 'remote.modes.readonly', default: 'remote.modes.default', acceptEdits: 'remote.modes.acceptEdits',
  plan: 'remote.modes.plan', auto: 'remote.modes.auto', bypassPermissions: 'remote.modes.full',
  edit: 'remote.modes.edit', full: 'remote.modes.full',
  dontAsk: 'remote.modes.readonly',
  'read-only': 'remote.modes.readonly', 'workspace-write': 'remote.modes.edit', 'danger-full-access': 'remote.modes.full',
}
const modeLabel = (m) => (MODES[m] ? t(MODES[m]) : m || '—')
const shortFP = (fp) => (fp && fp.length > 20 ? fp.slice(0, 10) + '…' + fp.slice(-8) : fp || '')
const pendingExpired = (p) => !!p?.expires && new Date(p.expires).getTime() <= now.value
const pendingLeft = (p) => {
  if (!p?.expires) return ''
  if (pendingExpired(p)) return t('remote.pendingExpired')
  const total = Math.max(0, Math.ceil((new Date(p.expires).getTime() - now.value) / 1000))
  return t('remote.pendingLeft', { m: Math.floor(total / 60), s: total % 60 })
}
const when = (iso) => fromNow(Date.parse(iso) || 0)
const usagePercent = computed(() => {
  const q = Number(st.value.quotaBytes) || 0
  return q > 0 ? Math.min(100, Math.round((Number(st.value.usedBytes) || 0) / q * 100)) : 0
})
const planLabel = (p) => (p === 'pro' ? 'Pro' : p === 'free' ? t('remote.planFree') : p || '—')
const fmtExpiry = (ts) => (ts ? new Date(ts * 1000).toLocaleDateString() : '—')
const openUpgrade = () => BrowserOpenURL('https://agent-sync.yirenzaowusuo.cn/app/upgrade')
const openDownload = () => BrowserOpenURL('https://agent-sync.yirenzaowusuo.cn/download')
const needLogin = computed(() => !auth.value.loggedIn && (st.value.mode || form.value.mode || 'official') === 'official')
const stepIndex = computed(() => {
  const mode = st.value.mode || form.value.mode || 'official'
  if (mode === 'official' && !auth.value.loggedIn) return 1
  if (!st.value.running) return 2
  return st.value.brokerState === 'connected' ? 3 : 2
})
const brokerStateLabel = computed(() => {
  const map = { connected: 'remote.brokerConnected', connecting: 'remote.brokerConnecting', error: 'remote.brokerError' }
  return t(map[st.value.brokerState] || 'remote.brokerDisconnected')
})
const brokerBadgeClass = computed(() => {
  if (!st.value.running) return 'err'
  if (st.value.brokerState === 'connected') return 'ok'
  if (st.value.brokerState === 'error') return 'err'
  return 'info'
})

async function loadConfig() {
  try {
    form.value = await api.GetRemoteConfig()
    if (form.value.mode === 'official' && !form.value.broker) {
      form.value.broker = 'wss://agent-sync.yirenzaowusuo.cn/mqtt'
    }
  } catch (e) {
    err.value = String(e)
  }
}

async function loadAuth() {
  try {
    const a = await api.RemoteAuthStatus()
    auth.value.loggedIn = !!a.loggedIn
    auth.value.accountId = a.accountId || ''
  } catch {
    auth.value.loggedIn = false
  }
}

async function submitAuth() {
  authError.value = ''
  authBusy.value = true
  try {
    await api.LoginRemote(auth.value.username, auth.value.password, form.value.broker || 'wss://agent-sync.yirenzaowusuo.cn/mqtt')
    auth.value.password = ''
    await loadAuth()
    ok(t('remote.loginSuccess', { account: auth.value.accountId || auth.value.username }))
  } catch (e) {
    authError.value = String(e)
    fail(String(e))
  } finally {
    authBusy.value = false
  }
}

function openRegister() {
  BrowserOpenURL('https://agent-sync.yirenzaowusuo.cn/register')
}

async function testBroker() {
  testMessage.value = ''
  testBusy.value = true
  try {
    await api.TestRemoteBroker({ ...form.value })
    testOK.value = true
    testMessage.value = t('remote.testOk')
  } catch (e) {
    testOK.value = false
    testMessage.value = String(e)
  } finally {
    testBusy.value = false
  }
}

async function logout() {
  authBusy.value = true
  try {
    await api.LogoutRemote()
    await loadAuth()
    ok(t('remote.loggedOut'))
  } catch (e) {
    fail(String(e))
  } finally {
    authBusy.value = false
  }
}

async function refresh() {
  try {
    const next = await api.GetRemoteStatus()
    st.value = next
    if (next.running) {
      if (!form.value.broker) form.value.broker = next.broker || ''
      if (!form.value.mode) form.value.mode = next.mode || 'official'
      if (!form.value.name) form.value.name = next.hostName || ''
    }
    if (!loaded) loaded = JSON.stringify(form.value)
    err.value = ''
  } catch (e) {
    err.value = String(e)
  }
}

function startEdit() {
  loadConfig()
  saved.value = false
  editing.value = true
}

function cancelEdit() {
  editing.value = false
  loadConfig()
}

async function start() {
  if (!form.value.broker) {
    fail(t('remote.needConfig'))
    return
  }
  if (form.value.mode === 'official' && !auth.value.loggedIn) {
    fail(t('remote.needLogin'))
    return
  }
  busy.value = true
  try {
    await api.SaveRemoteConfig(form.value)
    loaded = JSON.stringify(form.value)
    await api.StartRemote()
    await refresh()
    ok(t('remote.started'))
  } catch (e) {
    fail(String(e))
  } finally {
    busy.value = false
  }
}

async function stop() {
  busy.value = true
  try {
    await api.StopRemote()
    await refresh()
    ok(t('remote.stoppedOk'))
  } catch (e) {
    fail(String(e))
  } finally {
    busy.value = false
  }
}

async function invite() {
  busy.value = true
  try {
    invitation.value = await api.CreateRemoteInvite()
    await refresh()
  } catch (e) {
    fail(String(e))
  } finally {
    busy.value = false
  }
}

async function copyInvite() {
  try {
    await api.CopyText(invitation.value.text)
    ok(t('remote.copied'))
  } catch (e) {
    fail(String(e))
  }
}

async function decide(id, approve) {
  busy.value = true
  try {
    await api.DecideRemote(id, approve)
    await refresh()
    ok(approve ? t('remote.approved') : t('remote.denied'))
  } catch (e) {
    fail(String(e))
  } finally {
    busy.value = false
  }
}

function startRename(d) {
  renaming.value = d.id
  newName.value = d.name || ''
}

async function saveName(d) {
  if (!newName.value.trim()) return
  busy.value = true
  try {
    await api.RenameRemote(d.id, newName.value.trim())
    renaming.value = null
    await refresh()
    ok(t('remote.renamed'))
  } catch (e) {
    fail(String(e))
  } finally {
    busy.value = false
  }
}

async function removeDevice(d) {
  if (!(await api.Confirm(t('remote.deleteTitle'), t('remote.deleteConfirm', { name: d.name || t('remote.unnamed') })))) return
  busy.value = true
  try {
    await api.RemoveRemoteDevice(d.id)
    await refresh()
    ok(t('remote.deletedDevice'))
  } catch (e) {
    fail(String(e))
  } finally {
    busy.value = false
  }
}

async function stopSession(a) {
  busy.value = true
  try {
    await api.StopRemoteSession(a.key)
    await refresh()
    ok(t('remote.sessionStopped'))
  } catch (e) {
    fail(String(e))
  } finally {
    busy.value = false
  }
}

async function saveConfig() {
  busy.value = true
  try {
    const before = JSON.parse(loaded || '{}')
    const wasRunning = st.value.running
    const restart = wasRunning && ['mode', 'broker', 'username', 'password', 'proxy'].some((k) => form.value[k] !== before[k])
    await api.SaveRemoteConfig(form.value)
    loaded = JSON.stringify(form.value)
    saved.value = true
    if (restart) {
      await api.StopRemote()
      await api.StartRemote()
      await refresh()
    }
    await loadConfig()
    editing.value = false
    ok(restart ? t('remote.savedOk') : t('remote.saved'))
  } catch (e) {
    fail(String(e))
  } finally {
    busy.value = false
  }
}

let active = false
function schedule() {
  clearInterval(timer)
  clearInterval(nowTimer)
  if (active) {
    timer = setInterval(refresh, 3000)
    nowTimer = setInterval(() => { now.value = Date.now() }, 1000)
  }
}

onActivated(async () => {
  active = true
  now.value = Date.now()
  await loadConfig()
  await loadAuth()
  await refresh()
  schedule()
})
onDeactivated(() => {
  active = false
  schedule()
})
onUnmounted(() => { clearInterval(timer); clearInterval(nowTimer) })
</script>

<style scoped>
.info-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 10px 18px; margin-top: 14px; }
.info-grid .wide { grid-column: 1 / -1; }
.info-grid .k { font-size: 12px; color: var(--muted); }
.info-grid .v { font-size: 14px; margin-top: 2px; }
.usage-bar { height: 5px; background: var(--surface-2, var(--border)); border-radius: 999px; overflow: hidden; margin-top: 6px; }
.usage-bar i { display: block; height: 100%; background: var(--accent, #0b6b60); }
.ellipsis { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.line { display: flex; align-items: center; gap: 10px; padding: 10px 0; }
.line + .line { border-top: 1px solid var(--border); }
.online { width: 8px; height: 8px; border-radius: 50%; background: var(--muted); flex: none; }
.online.on { background: var(--ok); }
.revoked-badge { margin-left: 8px; }
.name-input { max-width: 240px; }
.steps { display: flex; gap: 8px; flex-wrap: wrap; }
.steps span { padding: 3px 10px; border-radius: 999px; border: 1px solid var(--border); color: var(--muted); font-size: 12px; }
.steps span.on { border-color: var(--accent); color: var(--accent); }
.steps span.done { background: color-mix(in srgb, var(--accent) 12%, transparent); }
.login-first { border-color: var(--accent); }
.invite-body { display: flex; gap: 18px; align-items: flex-start; }
.qr { width: 300px; height: 300px; border-radius: 8px; background: #fff; padding: 6px; flex: none; }
.invite-text { width: 100%; margin: 10px 0; resize: vertical; word-break: break-all; font-size: 12px; }
@media (max-width: 640px) {
  .info-grid { grid-template-columns: 1fr; }
  .qr { width: 240px; height: 240px; }
  .invite-body { flex-direction: column; }
}
</style>
