import { describe, it, expect } from 'vitest'
import { normalizeBroker } from '../remoteUrl.js'

describe('normalizeBroker', () => {
  it('keeps wss url', () => expect(normalizeBroker('wss://a.cn/mqtt')).toBe('wss://a.cn/mqtt'))
  it('keeps ws url for self-host', () => expect(normalizeBroker('ws://127.0.0.1:8083/mqtt')).toBe('ws://127.0.0.1:8083/mqtt'))
  it('rejects http', () => expect(() => normalizeBroker('http://a.cn')).toThrow())
})
