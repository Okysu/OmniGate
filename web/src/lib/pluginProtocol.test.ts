import { describe, expect, it } from 'vitest'
import { isCustomProtocol, protocolInfo } from './pluginProtocol'

describe('protocolInfo', () => {
  it('labels custom and inherited protocols', () => {
    expect(protocolInfo({ protocol: 'custom', extends: '' }).label).toBe('自定义协议')
    expect(isCustomProtocol({ protocol: 'custom' })).toBe(true)
    expect(protocolInfo({ extends: 'openai.chat' }).label).toBe('继承 OpenAI')
    expect(protocolInfo({ extends: 'anthropic.messages' }).label).toBe('继承 Anthropic')
    expect(protocolInfo({ inherits: 'anthropic' }).kind).toBe('anthropic')
    expect(protocolInfo({ extends: '', kind: ['billing'] }).label).toBe('计费插件')
    expect(protocolInfo({ extends: '' }).label).toBe('—')
    expect(protocolInfo(null).kind).toBe('unknown')
  })
})
