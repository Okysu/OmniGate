// phase9-api.md §2: how a plugin talks to its upstream. Either it inherits a
// built-in protocol (`extends`: openai.chat / anthropic.messages, called
// `inherits: openai | anthropic` in the contract prose) and only rewrites /
// signs requests, or it implements the whole protocol (`protocol: "custom"`).
import type { InboundProtocol } from './types'
import { isBillingPlugin, isChannelPlugin } from './customMeters'

export interface ProtocolSource {
  protocol?: string | null
  extends?: string | null
  inherits?: string | null
  kind?: unknown
}

export type ProtocolKind = 'custom' | 'openai' | 'anthropic' | 'billing' | 'unknown'

export interface ProtocolInfo {
  kind: ProtocolKind
  /** Badge text: "自定义协议" / "继承 OpenAI" / "继承 Anthropic" / "计费插件". */
  label: string
  description: string
}

const INHERITS: Record<string, 'openai' | 'anthropic'> = {
  'openai.chat': 'openai',
  'openai': 'openai',
  'anthropic.messages': 'anthropic',
  'anthropic': 'anthropic',
}

export function isCustomProtocol(src: ProtocolSource | null | undefined): boolean {
  return src?.protocol === 'custom'
}

export function protocolInfo(src: ProtocolSource | null | undefined): ProtocolInfo {
  if (isCustomProtocol(src))
    return { kind: 'custom', label: '自定义协议', description: '插件实现完整的上游协议（buildRequest / parseResponse / parseStream），网关把结果转换为客户端协议。' }
  const inh = INHERITS[src?.extends || src?.inherits || '']
  if (inh === 'openai')
    return { kind: 'openai', label: '继承 OpenAI', description: '上游兼容 OpenAI Chat Completions，插件只改写 / 签名请求。' }
  if (inh === 'anthropic')
    return { kind: 'anthropic', label: '继承 Anthropic', description: '上游兼容 Anthropic Messages，插件只改写 / 签名请求。' }
  if (src && isBillingPlugin(src) && !isChannelPlugin(src))
    return { kind: 'billing', label: '计费插件', description: '只提供套餐计量，不能用于渠道。' }
  return { kind: 'unknown', label: src?.extends || src?.inherits || '—', description: '' }
}

/** Client protocols a custom-protocol channel can serve (via conversion from Chat Completions). */
export const CUSTOM_PROTOCOL_ACCEPTS: InboundProtocol[] = ['openai.chat', 'anthropic.messages', 'openai.responses']
/** Client endpoints a custom-protocol channel never serves (they are skipped during routing). */
export const CUSTOM_PROTOCOL_REJECTS = ['Embeddings', '图片', '音频'] as const

/** phase9 §2 runtime limits of parseStream. */
export const STREAM_CALL_LIMIT_MS = 50
export const STREAM_TOTAL_LIMIT_MS = 5000
/** Editor tests relax hook timeouts by this factor (server plugin/runtime.go testScale). */
export const TEST_TIMEOUT_SCALE = 4
