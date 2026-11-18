// 会话事件序号门：丢弃重复/旧事件；会话重启后 reset，让新序号从 1 重新算。
export function createSeqGate() {
  let last = 0
  return {
    accept(seq) {
      if (!seq) return true
      if (seq <= last) return false
      last = seq
      return true
    },
    reset() {
      last = 0
    },
    get last() {
      return last
    },
  }
}
