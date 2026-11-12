<template>
  <div class="remote">
    <!-- 连接信息：默认只读展示，右上角进入编辑；编辑时表单就在最上面 -->
    <div class="card">
      <div class="card-head">
        <h3>{{ t('remote.title') }}</h3>
        <div class="row">
          <span class="badge" :class="st.running ? 'ok' : 'err'">{{ st.running ? t('remote.running') : t('remote.stopped') }}</span>
          <button v-if="!editing" class="sm ghost" :disabled="busy" @click="startEdit">
            <NavIcon name="settings" :size="13" /> {{ t('remote.editInfo') }}
          </button>
        </div>
      </div>
      <div class="muted small">{{ t('remote.subtitle') }}</div>
      <div class="muted small mt">{{ t('remote.encryptedHint') }}</div>

      <template v-if="!editing">
        <div v-if="st.running" class="info-grid">
          <div><div class="k">{{ t('remote.hostName') }}</div><div class="v">{{ st.hostName || '—' }}</div></div>
          <div><div class="k">{{ t('remote.relayAddr') }}</div><div class="v mono">{{ toDisplay(st.relay) || '—' }}</div></div>
          <div class="wide"><div class="k">{{ t('remote.hostId') }}</div><div class="v mono ellipsis">{{ st.hostId || '—' }}</div></div>
        </div>
        <div v-else-if="st.binPath" class="muted small mt">{{ t('remote.binFound', { path: st.binPath }) }}</div>
        <div v-else class="err-box mt">{{ t('remote.binMissing') }}</div>

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
          <div class="form-row wide">
            <label>{{ t('remote.relay') }}</label>
            <input v-model.trim="form.relay" placeholder="https://agent-sync.example.com" />
            <div class="hint">{{ t('remote.relayHint') }}</div>
          </div>
          <div class="form-row wide">
            <label>{{ t('remote.public') }}</label>
            <input v-model.trim="form.public" placeholder="https://agent-sync.example.com" />
            <div class="hint">{{ t('remote.publicHint') }}</div>
          </div>
          <div class="form-row">
            <label>{{ t('remote.proxy') }}</label>
            <input v-model.trim="form.proxy" placeholder="http://127.0.0.1:7890" />
            <div class="hint">{{ t('remote.proxyHint') }}</div>
          </div>
          <div class="form-row">
            <label>{{ t('remote.claudeBin') }}</label>
            <input v-model.trim="form.claudeBin" :placeholder="t('remote.auto')" />
            <div class="hint">{{ t('remote.claudeBinHint') }}</div>
          </div>
          <div class="form-row">
            <label>{{ t('remote.bin') }}</label>
            <input v-model.trim="form.bin" :placeholder="st.binPath || t('remote.auto')" />
            <div class="hint">{{ t('remote.binHint') }}</div>
          </div>
        </div>
        <div class="row wrap mt">
          <button class="primary" :disabled="busy" @click="saveConfig">{{ t('remote.save') }}</button>
          <button class="ghost" :disabled="busy" @click="cancelEdit">{{ t('common.cancel') }}</button>
          <span v-if="saved" class="ok small">{{ t('remote.saved') }}</span>
        </div>
        <div class="row wrap mt">
          <button class="danger" :disabled="busy" @click="rotateKey">{{ t('remote.rotateKey') }}</button>
          <span class="muted small">{{ t('remote.rotateKeyHint') }}</span>
        </div>
      </template>

      <div v-if="err" class="err-box mt">{{ err }}</div>
      <div class="warn-box mt">{{ t('remote.approvalWarn') }}</div>
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
        </div>
        <button class="sm primary" :disabled="busy" @click="decide(p.id, true)">{{ t('remote.approve') }}</button>
        <button class="sm danger" :disabled="busy" @click="decide(p.id, false)">{{ t('remote.deny') }}</button>
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
import { ref, onActivated, onDeactivated, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/api'
import { useNotice } from '@/composables/useNotice'
import { fromNow } from '@/utils/format'
import Notice from '@/components/Notice.vue'
import NavIcon from '@/components/NavIcon.vue'

const { t } = useI18n()
const { notice, ok, fail } = useNotice()

const st = ref({ running: false, pending: [], devices: [], active: [] })
const form = ref({ relay: '', public: '', name: '', claudeBin: '', bin: '', proxy: '' })
const busy = ref(false)
const err = ref('')
const saved = ref(false)
const editing = ref(false)
const invitation = ref(null)
const renaming = ref(null)
const newName = ref('')
let loaded = ''
let timer

const MODES = {
  readonly: 'remote.modes.readonly', default: 'remote.modes.default', acceptEdits: 'remote.modes.acceptEdits',
  plan: 'remote.modes.plan', auto: 'remote.modes.auto', bypassPermissions: 'remote.modes.full',
  edit: 'remote.modes.edit', full: 'remote.modes.full',
  dontAsk: 'remote.modes.readonly',
  'read-only': 'remote.modes.readonly', 'workspace-write': 'remote.modes.edit', 'danger-full-access': 'remote.modes.full',
}
const modeLabel = (m) => (MODES[m] ? t(MODES[m]) : m || '—')
const shortFP = (fp) => (fp && fp.length > 20 ? fp.slice(0, 10) + '…' + fp.slice(-8) : fp || '')
const when = (iso) => fromNow(Date.parse(iso) || 0)

// 配置里的 wss 地址在界面上显示成 https，方便填写。
function toDisplay(raw) {
  if (!raw) return ''
  try {
    const u = new URL(raw)
    if (u.protocol === 'wss:') u.protocol = 'https:'
    else if (u.protocol === 'ws:') u.protocol = 'http:'
    if (u.pathname === '/ws') u.pathname = ''
    return u.toString().replace(/\/$/, '')
  } catch {
    return raw
  }
}

async function loadConfig() {
  try {
    form.value = await api.GetRemoteConfig()
  } catch (e) {
    err.value = String(e)
  }
}

async function refresh() {
  try {
    const next = await api.GetRemoteStatus()
    st.value = next
    if (next.running) {
      if (!form.value.relay) form.value.relay = toDisplay(next.relay)
      if (!form.value.public) form.value.public = toDisplay(next.relay)
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
  if (!form.value.relay) {
    fail(t('remote.needConfig'))
    return
  }
  busy.value = true
  try {
    await api.SaveRemoteConfig(form.value)
    loaded = JSON.stringify(form.value)
    await api.StartRemote()
    await new Promise((r) => setTimeout(r, 600))
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
    const restart = wasRunning && ['relay', 'public', 'proxy'].some((k) => form.value[k] !== before[k])
    await api.SaveRemoteConfig(form.value)
    loaded = JSON.stringify(form.value)
    saved.value = true
    if (restart) {
      await api.StopRemote()
      await new Promise((r) => setTimeout(r, 800))
      await api.StartRemote()
      await new Promise((r) => setTimeout(r, 600))
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

async function rotateKey() {
  if (!(await api.Confirm(t('remote.rotateKeyTitle'), t('remote.rotateKeyConfirm')))) return
  busy.value = true
  try {
    await api.RotateRemoteKey()
    await refresh()
    ok(t('remote.rotateKeyDone'))
  } catch (e) {
    fail(String(e))
  } finally {
    busy.value = false
  }
}

let active = false
function schedule() {
  clearInterval(timer)
  if (active) timer = setInterval(refresh, 3000)
}

onActivated(async () => {
  active = true
  await loadConfig()
  await refresh()
  schedule()
})
onDeactivated(() => {
  active = false
  schedule()
})
onUnmounted(() => clearInterval(timer))
</script>

<style scoped>
.info-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 10px 18px; margin-top: 14px; }
.info-grid .wide { grid-column: 1 / -1; }
.info-grid .k { font-size: 12px; color: var(--muted); }
.info-grid .v { font-size: 14px; margin-top: 2px; }
.ellipsis { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.line { display: flex; align-items: center; gap: 10px; padding: 10px 0; }
.line + .line { border-top: 1px solid var(--border); }
.online { width: 8px; height: 8px; border-radius: 50%; background: var(--muted); flex: none; }
.online.on { background: var(--ok); }
.revoked-badge { margin-left: 8px; }
.name-input { max-width: 240px; }
.invite-body { display: flex; gap: 18px; align-items: flex-start; }
.qr { width: 200px; height: 200px; border-radius: 8px; background: #fff; padding: 6px; flex: none; }
.invite-text { width: 100%; margin: 10px 0; resize: vertical; word-break: break-all; font-size: 12px; }
@media (max-width: 640px) {
  .info-grid { grid-template-columns: 1fr; }
  .invite-body { flex-direction: column; }
}
</style>
