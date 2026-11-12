<template>
  <div class="connect" :class="{ manage }">
    <div v-if="!manage" class="hero">
      <div class="orb" :data-state="state"><i></i><i></i><i></i></div>
      <div class="hero-text">
        <h1>{{ store && state === 'connected' ? store.name : '连接电脑' }}</h1>
        <p>{{ state === 'connected' ? statusLabel : message || '选择已配对的电脑，或扫码添加一台' }}</p>
      </div>
      <span class="state-pill" :data-state="state">{{ statusLabel }}</span>
    </div>

    <div v-if="pairings.length" class="host-list">
      <div v-for="p in pairings" :key="key(p)" class="host-card" :class="{ active: isActive(p) }">
        <div class="host-dot" :data-online="isActive(p) && state === 'connected'"></div>
        <div class="host-info">
          <div class="host-name">{{ p.name || '电脑' }}</div>
          <div class="host-sub">{{ shortHost(p) }}</div>
        </div>
        <span v-if="isActive(p)" class="chip">{{ state === 'connected' ? '当前' : '已选择' }}</span>
        <button v-if="!isActive(p)" class="pill" :disabled="busy" @click="$emit('switch', p)">连接</button>
        <button v-else-if="state !== 'connected'" class="pill" :disabled="busy" @click="$emit('connect')">重连</button>
        <button class="icon-x" :disabled="busy" :aria-label="'删除 ' + p.name" @click="$emit('remove', p)">
          <Icon name="x" :size="15" />
        </button>
      </div>
    </div>

    <div class="add-box">
      <button class="add-toggle" @click="open = !open">
        <span class="add-plus"><Icon name="plus" :size="16" /></span>
        <span class="add-label">添加新电脑</span>
        <span class="add-hint">扫码或粘贴邀请</span>
      </button>
      <div v-if="open" class="add-body">
        <textarea v-model="text" rows="3" spellcheck="false" placeholder="粘贴电脑上生成的配对邀请，也可以用旁边的扫码"></textarea>
        <div class="add-actions">
          <button class="primary grow" :disabled="busy || !text.trim()" @click="submit">开始配对</button>
          <button class="scan" :disabled="busy" @click="$emit('scan')">
            <Icon name="camera" :size="18" /><span>扫码</span>
          </button>
        </div>
      </div>
    </div>

    <p v-if="!manage && !pairings.length && !open" class="empty">
      还没有配对的电脑。点上面的“添加新电脑”，用手机扫电脑上的二维码最方便。
    </p>
  </div>
</template>

<script setup>
import { ref, watch } from 'vue'
import Icon from './Icon.vue'

const props = defineProps({
  state: { type: String, default: 'idle' },
  message: { type: String, default: '' },
  statusLabel: { type: String, default: '' },
  store: { type: Object, default: null },
  pairings: { type: Array, default: () => [] },
  busy: { type: Boolean, default: false },
  manage: { type: Boolean, default: false },
})
const emit = defineEmits(['connect', 'switch', 'remove', 'pair', 'scan'])

const open = ref(props.pairings.length === 0)
const text = ref('')
watch(() => props.pairings.length, (n) => {
  if (n === 0) open.value = true
})

const key = p => p?.hostId || p?.route || p?.url || ''
const isActive = p => props.store && key(p) === key(props.store)
const shortHost = p => (p?.hostId ? p.hostId.slice(0, 10) : p?.name || '电脑')

function submit() {
  if (!text.value.trim()) return
  emit('pair', text.value.trim())
  text.value = ''
}
</script>

<style scoped>
.connect { display: grid; gap: 14px; }
.hero { display: flex; align-items: center; gap: 14px; padding: 10px 2px 4px; }
.orb { position: relative; width: 42px; height: 42px; flex: none; }
.orb i { position: absolute; inset: 0; border-radius: 50%; border: 1.5px solid var(--accent); opacity: .3; }
.orb i:nth-child(2) { inset: 7px; opacity: .55; }
.orb i:nth-child(3) { inset: 14px; background: var(--accent); border: 0; opacity: 1; }
.orb[data-state='connecting'] i, .orb[data-state='reconnecting'] i { animation: pulse 1.4s ease-in-out infinite; }
@keyframes pulse { 50% { transform: scale(.82); opacity: .9; } }
.hero-text { flex: 1; min-width: 0; }
.hero h1 { margin: 0; font-size: 21px; letter-spacing: -.01em; }
.hero p { margin: 4px 0 0; color: var(--muted); font-size: 13px; line-height: 1.45; }
.state-pill { font-size: 11px; padding: 4px 10px; border-radius: 999px; border: 1px solid var(--line); color: var(--muted); flex: none; }
.state-pill[data-state='connected'] { color: var(--ok); border-color: color-mix(in srgb, var(--ok) 35%, transparent); }
.state-pill[data-state='error'] { color: var(--err); border-color: color-mix(in srgb, var(--err) 35%, transparent); }
.state-pill[data-state='pending'], .state-pill[data-state='connecting'], .state-pill[data-state='reconnecting'] { color: var(--accent); }

.host-list { display: grid; gap: 8px; }
.host-card { display: flex; align-items: center; gap: 10px; padding: 12px 14px; background: var(--surface); border: 1px solid var(--line); border-radius: 16px; }
.host-card.active { border-color: color-mix(in srgb, var(--accent) 45%, var(--line)); background: color-mix(in srgb, var(--accent) 7%, var(--surface)); }
.host-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--muted); flex: none; }
.host-dot[data-online='true'] { background: var(--ok); box-shadow: 0 0 0 3px color-mix(in srgb, var(--ok) 18%, transparent); }
.host-info { flex: 1; min-width: 0; }
.host-name { font-size: 15px; font-weight: 600; }
.host-sub { font-size: 11px; color: var(--muted); font-family: ui-monospace, monospace; margin-top: 2px; }
.chip { font-size: 11px; color: var(--accent); border: 1px solid color-mix(in srgb, var(--accent) 40%, transparent); padding: 2px 8px; border-radius: 999px; }
.pill { font-size: 13px; border: 1px solid var(--line); background: var(--surface-2); color: var(--text); border-radius: 999px; padding: 6px 14px; }
.icon-x { border: 0; background: transparent; color: var(--muted); padding: 4px; display: grid; place-items: center; }

.add-box { border: 1px dashed var(--line); border-radius: 16px; overflow: hidden; background: color-mix(in srgb, var(--surface) 60%, transparent); }
.add-toggle { width: 100%; display: flex; align-items: center; gap: 10px; padding: 14px; background: transparent; border: 0; color: var(--text); }
.add-plus { width: 26px; height: 26px; border-radius: 50%; display: grid; place-items: center; background: var(--accent-soft); color: var(--accent); }
.add-label { font-size: 14px; font-weight: 600; }
.add-hint { margin-left: auto; font-size: 11px; color: var(--muted); }
.add-body { padding: 0 14px 14px; display: grid; gap: 10px; }
textarea { width: 100%; resize: none; border: 1px solid var(--line); background: var(--input); color: var(--text); border-radius: 12px; padding: 10px 12px; font-size: 12px; line-height: 1.5; }
.add-actions { display: flex; gap: 8px; }
.scan { display: inline-flex; align-items: center; gap: 6px; border: 1px solid var(--line); background: var(--surface-2); color: var(--text); border-radius: 12px; padding: 0 14px; font-size: 13px; }
.empty { color: var(--muted); font-size: 13px; line-height: 1.6; margin: 4px 2px; }
</style>
