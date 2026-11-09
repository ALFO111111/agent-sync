import { ref } from 'vue'

// useNotice 页面底部"操作成功/失败"提示的状态，配合 <Notice :notice="notice" /> 使用。
export function useNotice() {
  const notice = ref(null) // { text, error, pending } 或 null
  return {
    notice,
    // pending 操作进行中的提示（灰色），避免用户以为点了没反应
    pending: (text) => { notice.value = { text, error: false, pending: true } },
    ok: (text) => { notice.value = { text, error: false } },
    fail: (text) => { notice.value = { text: String(text), error: true } },
    clear: () => { notice.value = null },
  }
}
