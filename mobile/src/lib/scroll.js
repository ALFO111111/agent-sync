// 聊天滚动策略：只有用户本来就在底部附近时才自动跟随新消息。
export function shouldStickToBottom({ scrollTop, scrollHeight, clientHeight, threshold = 80 }) {
  return scrollHeight - scrollTop - clientHeight < threshold
}

// nextStick 决定下一次内容增长后是否继续跟随。
// 关键：内容变长会触发 scroll 事件，但这不是用户在滑；只有用户手势才能关闭跟随。
export function nextStick({ near, programmatic, userScrolling, stick }) {
  if (programmatic) return stick
  if (near) return true
  if (userScrolling) return false
  return stick
}

// 中文输入法选词时按回车（isComposing / keyCode 229）不能发送消息。
export function shouldSendOnEnter(event) {
  return !event.isComposing && event.keyCode !== 229
}
