// Display labels (Simplified Chinese) for Phase 1 enums.
import type { CapabilityOutput, ChannelScope, ChannelType, CompatMode, HealthState, LedgerKind, PluginApproval, PluginSource, PluginTemplate, PriceKind } from './types'

export const CHANNEL_TYPE_LABELS: Record<ChannelType, string> = {
  openai: 'OpenAI 兼容',
  anthropic: 'Anthropic',
  custom: '自定义协议',
}

/** baseUrl convention per type (contract §1). */
export const CHANNEL_BASE_URL_HINTS: Record<ChannelType, { example: string, hint: string }> = {
  openai: {
    example: 'https://api.openai.com/v1',
    hint: '包含版本路径，如 https://api.openai.com/v1；请求时拼接 /chat/completions，健康探测使用 GET {baseUrl}/models。',
  },
  anthropic: {
    example: 'https://api.anthropic.com',
    hint: '不含版本路径，如 https://api.anthropic.com；请求时拼接 /v1/messages，健康探测使用 GET {baseUrl}/v1/models。',
  },
  custom: {
    example: 'https://api.example.com',
    hint: '上游请求地址由插件的 buildRequest 决定，通常填写厂商 API 的根地址（以插件说明为准）。',
  },
}

export const SCOPE_LABELS: Record<ChannelScope, string> = {
  private: '私有',
  shared: '共享',
  global: '全局',
}

export const SCOPE_DESCRIPTIONS: Record<ChannelScope, string> = {
  private: '仅自己的 API Key 可以使用。',
  shared: '自己、指定的用户以及指定用户组的成员可以使用。',
  global: '所有用户都可以使用（需要渠道管理权限）。',
}

export const HEALTH_LABELS: Record<HealthState, string> = {
  healthy: '健康',
  degraded: '降级',
  open: '熔断',
}

export const INBOUND_LABELS: Record<string, string> = {
  'openai.chat': 'OpenAI Chat',
  'openai.responses': 'OpenAI Responses',
  'anthropic.messages': 'Anthropic Messages',
  'openai.models': '模型列表',
  'openai.embeddings': 'OpenAI Embeddings',
  'openai.images.generations': 'Images 生成',
  'openai.images.edits': 'Images 编辑',
  'openai.images.variations': 'Images 变体',
  'openai.audio.transcriptions': '语音转写',
  'openai.audio.translations': '语音翻译',
  'openai.audio.speech': '语音合成',
  'openai.completions': 'OpenAI Completions',
}

/** Explanations for gateway error classes shown in request logs. */
export const ERROR_CLASS_HINTS: Record<string, string> = {
  quota_exceeded: '套餐额度已达上限（429），窗口重置后恢复',
  quota_exhausted: '套餐订阅期内总量已用尽（429）',
  rate_limited: '超过每分钟请求数上限（429，API Key 或用户组 RPM），按 Retry-After 稍后重试',
  user_request_limit: '超过用户组的每日请求数上限（429），次日零点（按用户组时区）重置',
  spend_limit_exceeded: '已达到消费限额（429）：用户组的每日 / 每月消费上限或 API Key 的消费上限，窗口重置后恢复；自有 / 共享渠道不受消费限额约束',
}

export const LEDGER_KIND_LABELS: Record<LedgerKind, string> = {
  grant: '充值',
  charge: '扣费',
  refund: '退款',
  adjust: '调整',
}

export const REF_TYPE_LABELS: Record<string, string> = {
  request: '请求',
  redeem: '兑换码',
  admin: '管理员',
}

export const PRICE_KIND_LABELS: Record<PriceKind, string> = {
  sell: '售价',
  cost: '成本价',
}

export const COMPAT_MODE_LABELS: Record<CompatMode, string> = {
  strict: '严格（strict）',
  lenient: '宽松（lenient）',
}

export const COMPAT_MODE_DESCRIPTIONS: Record<CompatMode, string> = {
  strict: '跨协议转换时遇到无法表达的字段，直接返回 400 错误。',
  lenient: '丢弃无法转换的字段继续请求，并在响应头 X-OmniGate-Compat-Warnings 中列出被丢弃的字段。',
}

// ---------------------------------------------------------------------------
// Phase 2: plugins
// ---------------------------------------------------------------------------

export const PLUGIN_SOURCE_LABELS: Record<PluginSource, string> = {
  builtin: '内置',
  bundled: '随附',
  upload: '上传',
  editor: '编辑器',
}

export const PLUGIN_SOURCE_DESCRIPTIONS: Record<PluginSource, string> = {
  builtin: '随 OmniGate 内置的原生协议实现，不可编辑或导出。',
  bundled: '随 OmniGate 二进制分发的示例插件。',
  upload: '通过 ZIP 导入的插件。',
  editor: '在线编辑器中创建的插件。',
}

export const APPROVAL_LABELS: Record<PluginApproval, string> = {
  pending: '待审批',
  approved: '已批准',
  rejected: '已拒绝',
}

export const EXTENDS_LABELS: Record<string, string> = {
  'openai.chat': 'OpenAI Chat',
  'anthropic.messages': 'Anthropic Messages',
}

/** Channel type implied by a plugin's `extends`. */
export const EXTENDS_CHANNEL_TYPE: Record<string, ChannelType> = {
  'openai.chat': 'openai',
  'anthropic.messages': 'anthropic',
}

/** Channel type implied by a plugin manifest: `custom` for custom-protocol plugins (phase9 §2), else by `extends`. */
export function manifestChannelType(m: { protocol?: string, extends?: string } | null | undefined): ChannelType | undefined {
  if (m?.protocol === 'custom')
    return 'custom'
  return EXTENDS_CHANNEL_TYPE[m?.extends ?? '']
}

export const CAPABILITY_LABELS: Record<string, string> = {
  'models.list': '模型列表',
  'balance.get': '余额',
  'usage.query': '用量',
  'quota.get': '配额',
  'health.check': '健康检查',
}

export const CAPABILITY_OUTPUT_LABELS: Record<CapabilityOutput, string> = {
  balance: '余额',
  quota: '配额',
  models: '模型',
  usage: '用量',
  health: '健康',
  json: 'JSON',
}

export const HOOK_LABELS: Record<string, string> = {
  transformRequest: '改写请求（transformRequest）',
  signRequest: '自定义签名（signRequest）',
  // phase9 §2: custom protocol
  buildRequest: '构造上游请求（buildRequest）',
  parseResponse: '解析响应（parseResponse）',
  parseStream: '解析流（parseStream）',
  endStream: '流结束（endStream）',
  normalizeError: '错误归一化（normalizeError）',
}

export const PLUGIN_TEMPLATE_LABELS: Record<PluginTemplate, { label: string, description: string }> = {
  'openai-compatible': { label: 'OpenAI 兼容', description: '继承 openai.chat，附带模型列表与健康检查能力及测试用例。' },
  'anthropic-compatible': { label: 'Anthropic 兼容', description: '继承 anthropic.messages，附带模型列表与健康检查能力及测试用例。' },
  'custom-protocol': { label: '自定义协议', description: '上游既不兼容 OpenAI 也不兼容 Anthropic 时使用：实现 buildRequest / parseResponse / parseStream，以虚构的 JSON Lines 流式协议为例，附带流式测试用例。首次发布需要审批。' },
  'blank': { label: '空白', description: '继承 openai.chat，不含任何能力，从零开始。' },
}
