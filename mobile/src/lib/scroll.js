// 聊天滚动策略：只有用户本来就在底部附近时才自动跟随新消息。
export function shouldStickToBottom({ scrollTop, scrollHeight, clientHeight, threshold = 80 }) {
  return scrollHeight - scrollTop - clientHeight < threshold
}

// 中文输入法选词时按回车（isComposing / keyCode 229）不能发送消息。
export function shouldSendOnEnter(event) {
  return !event.isComposing && event.keyCode !== 229
}
