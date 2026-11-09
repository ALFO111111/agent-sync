<template>
  <div>
    <div class="card">
      <h3>{{ t('bindings.bound') }}<Refreshing :on="!!busyKey" /></h3>
      <Skeleton v-if="!res.data" :lines="3" />
      <div v-else-if="!bound.length" class="muted">{{ t('bindings.noneBound') }}</div>
      <table v-else>
        <thead>
          <tr>
            <th>{{ t('bindings.colType') }}</th><th>{{ t('bindings.colProject') }}</th><th>{{ t('bindings.colLocalPath') }}</th>
            <th>{{ t('bindings.colPid') }}</th><th class="ops">{{ t('bindings.colOps') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="b in bound" :key="b.pid">
            <td><span class="badge">{{ kindLabel(b.kind) }}</span></td>
            <td>{{ b.display }}</td>
            <td>{{ b.root }}</td>
            <td class="muted pid" :title="b.pid">{{ b.pid }}</td>
            <td class="ops"><button class="sm ghost danger" :disabled="isBusy(b.pid)" @click="unbind(b)">{{ isBusy(b.pid) ? t('common.working') : t('bindings.stopSync') }}</button></td>
          </tr>
        </tbody>
      </table>
    </div>

    <div class="card">
      <h3>{{ t('bindings.claimable') }}<Refreshing :on="res.loading && !!res.data" /></h3>
      <!-- 读云端失败时下面照样显示上次缓存的结果，这里提一句 -->
      <div v-if="remoteError" class="err-box">{{ t('bindings.remoteError', { err: remoteError }) }}</div>
      <Skeleton v-if="!res.data" :lines="2" />
      <div v-else-if="!claimable.length" class="muted">{{ t('bindings.noClaimable') }}</div>
      <table v-else>
        <thead>
          <tr>
            <th>{{ t('bindings.colProject') }}</th><th>{{ t('bindings.colDevice') }}</th>
            <th>{{ t('bindings.colTheirPath') }}</th><th class="ops">{{ t('bindings.colOps') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="c in claimable" :key="c.pid + c.deviceId">
            <td>{{ c.display || c.pid }}</td>
            <td>{{ c.device }}</td>
            <td class="muted">{{ c.root }}</td>
            <td class="ops">
              <button class="sm primary" :disabled="isBusy(claimKey(c))" @click="claim(c)">{{ isBusy(claimKey(c)) ? t('common.working') : t('bindings.claim') }}</button>
              <button class="sm ghost danger" :disabled="isBusy(claimKey(c))" @click="ignore(c)">{{ t('bindings.ignore') }}</button>
            </td>
          </tr>
        </tbody>
      </table>
      <div class="note">{{ t('bindings.claimHint') }}</div>
    </div>

    <div class="card" v-if="ignored.length">
      <h3>{{ t('bindings.ignored') }}</h3>
      <table>
        <thead>
          <tr>
            <th>{{ t('bindings.colType') }}</th><th>{{ t('bindings.colProject') }}</th>
            <th>{{ t('bindings.colPid') }}</th><th class="ops">{{ t('bindings.colOps') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="g in ignored" :key="g.pid">
            <td><span class="badge">{{ kindLabel(g.kind) }}</span></td>
            <td>{{ g.display || '—' }}</td>
            <td class="muted pid" :title="g.pid">{{ g.pid }}</td>
            <td class="ops"><button class="sm" :disabled="isBusy(g.pid)" @click="unignore(g)">{{ isBusy(g.pid) ? t('common.working') : t('bindings.restore') }}</button></td>
          </tr>
        </tbody>
      </table>
      <div class="note">{{ t('bindings.restoreHint') }}</div>
    </div>

    <Notice :notice="notice" />
  </div>
</template>

<script setup>
import { ref, computed, onActivated } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/api'
import { kindLabel } from '@/utils/format'
import { useNotice } from '@/composables/useNotice'
import { resources } from '@/stores/data'
import Notice from '@/components/Notice.vue'
import Skeleton from '@/components/Skeleton.vue'
import Refreshing from '@/components/Refreshing.vue'

const { t } = useI18n()
// 数据来自全局缓存（stores/data.js）：启动时已在后台拉好；再进这页先显示上次的，后台悄悄刷新
const res = resources.projects
const bound = computed(() => res.data?.bound || [])
const claimable = computed(() => res.data?.claimable || [])
const ignored = computed(() => res.data?.ignored || [])
const remoteError = computed(() => res.data?.remoteError || res.error)
// 正在操作的那一行（'' = 空闲）。只禁用这一行，其他行照常可以点；
// 操作要顺带跑一轮同步（几秒到几十秒），所以先显示"正在…"，别让用户以为点了没反应。
const busyKey = ref('')
const isBusy = (key) => busyKey.value === key
const claimKey = (c) => c.pid + c.deviceId // 同一个 pid 可能来自多台设备，行要分开
const { notice, pending, ok, fail } = useNotice()

const refresh = () => res.refresh()

// act 执行一个会顺带同步的操作。failKey 是失败提示的文字 key（带 {err} 参数）。
async function act(key, pendingText, fn, okText, failKey) {
  busyKey.value = key
  pending(pendingText)
  try {
    await fn()
    ok(okText)
  } catch (e) {
    fail(t(failKey, { err: String(e) }))
  } finally {
    busyKey.value = ''
  }
  refresh()
}

const nameOf = (p) => p.display || p.pid

async function claim(c) {
  const dir = await api.PickDirectory()
  if (!dir) return
  const name = nameOf(c)
  act(claimKey(c), t('bindings.workingClaim', { name }), () => api.ClaimProject(c.pid, dir), t('bindings.claimed', { name, dir }), 'bindings.claimFailed')
}

function ignore(c) {
  const name = nameOf(c)
  act(claimKey(c), t('bindings.workingIgnore', { name }), () => api.IgnoreProject(c.pid), t('bindings.ignoredOk', { name }), 'bindings.opFailed')
}

async function unbind(b) {
  const name = nameOf(b)
  const yes = await api.Confirm(t('bindings.stopTitle'), t('bindings.stopConfirm', { name }))
  if (!yes) return
  act(b.pid, t('bindings.workingStop', { name }), () => api.IgnoreProject(b.pid), t('bindings.stopped', { name }), 'bindings.opFailed')
}

function unignore(g) {
  const name = nameOf(g)
  act(g.pid, t('bindings.workingRestore', { name }), () => api.UnignoreProject(g.pid), t('bindings.restored', { name }), 'bindings.opFailed')
}

// 每次进这页：30 秒内拉过就不重复拉（同步结束时缓存会自动刷新）
onActivated(() => res.load())
</script>

<style scoped>
/* 项目 ID 很长：按窗口宽度截断（全屏时显示得更多），鼠标悬停看全 */
.pid { font-size: 12px; max-width: 18vw; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* 操作列：表头和按钮都居中（按钮有内边距，靠左/靠右都会和表头错开） */
.ops { text-align: center; }
</style>
