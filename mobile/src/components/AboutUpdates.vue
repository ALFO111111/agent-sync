<script setup>
defineProps({ versionLabel: String, update: Object, native: Boolean, opening: Boolean })
defineEmits(['check', 'auto', 'open', 'skip'])
</script>

<template>
  <div class="section-title">关于</div>
  <div class="card about-card">
    <div class="about-name">agent-sync</div>
    <div class="muted small">手机遥控 · 协议 v1 · 内容端到端加密</div>
    <p class="hint">{{ versionLabel }}</p>
    <template v-if="native">
      <label class="setting-row">
        <span>自动检查更新</span>
        <input type="checkbox" :checked="update.auto" @change="$emit('auto', $event.target.checked)">
      </label>
      <div class="row wrap">
        <button class="sm" :disabled="update.status === 'checking'" @click="$emit('check')">{{ update.status === 'checking' ? '正在检查…' : '检查更新' }}</button>
        <span class="hint" role="status">{{ update.message || '仅检查官方正式版本，不自动安装' }}</span>
      </div>
      <div v-if="update.release && ['available', 'no-apk'].includes(update.status)" class="release-details">
        <b>v{{ update.release.version }} 版本说明</b>
        <p class="release-notes">{{ update.release.notes || '暂无版本说明' }}</p>
        <div class="row wrap">
          <button v-if="update.release.hasApk" class="primary" :disabled="opening" @click="$emit('open', update.release.downloadUrl)">下载 Android 安装包</button>
          <button class="sm" :disabled="opening" @click="$emit('open', update.release.pageUrl)">官方发布页</button>
          <button class="ghost" @click="$emit('skip')">以后再说</button>
        </div>
        <p class="hint">将用系统浏览器打开 GitHub；下载后由你确认安装。不会自动安装。</p>
      </div>
    </template>
    <p v-else class="hint">网页调试模式：这里的版本不是手机已安装版本，不提供 Android 更新检查。</p>
  </div>
</template>

<style scoped>
.release-details { margin-top: 14px; padding-top: 14px; border-top: 1px solid var(--line); }
.release-notes { white-space: pre-wrap; overflow-wrap: anywhere; max-height: 260px; overflow-y: auto; font-size: 13px; line-height: 1.65; text-align: left; }
</style>
