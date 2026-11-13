import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, existsSync } from 'node:fs'

const read = path => readFileSync(new URL(path, import.meta.url), 'utf8')

test('移动端不注册录音权限或原生语音插件', () => {
  const manifest = read('../android/app/src/main/AndroidManifest.xml')
  assert.doesNotMatch(manifest, /RECORD_AUDIO|RecognitionService/)
  assert.doesNotMatch(read('../android/app/src/main/java/com/alfo/agentsync/MainActivity.java'), /VoiceRecognition|SpeechRecognition/)
  assert.equal(existsSync(new URL('../src/lib/voice.js', import.meta.url)), false)
  assert.equal(existsSync(new URL('../android/app/src/main/java/com/alfo/agentsync/VoiceRecognitionPlugin.java', import.meta.url)), false)
})

test('聊天输入仅保留文字和附件入口，不含语音控制或识别依赖', () => {
  assert.doesNotMatch(read('../src/App.vue'), /VoiceRecognition|SpeechRecognition|cancelVoice|voiceOn|name="mic"/)
  assert.doesNotMatch(read('../package.json'), /speech-recognition/)
  assert.doesNotMatch(read('../package-lock.json'), /speech-recognition/)
  assert.doesNotMatch(read('../android/capacitor.settings.gradle'), /speech-recognition/)
  assert.doesNotMatch(read('../android/app/capacitor.build.gradle'), /speech-recognition/)
})
