import type { CanonicalEvent, CanonicalRequest, CanonicalResponse, ChatUsage, Ctx, FinishReason } from "@omnigate/plugin-sdk"

// DeepSeek FIM 补全（Beta）：POST {Base URL 的源}/beta/completions（文档链接见 README）

export const DEFAULT_FIM_PATH = "/beta/completions"
export const DEFAULT_HOLE_MARKER = "<|fim_hole|>"
/** 文档写明 FIM 的最大补全长度为 4K；客户端没有给 max_tokens 时使用。 */
export const DEFAULT_MAX_TOKENS = 4096

export interface FimConfig {
  fimPath: string
  holeMarker: string
  defaultMaxTokens: number
  /** 0 = 不限制（原样转发客户端的 max_tokens）。 */
  maxTokensLimit: number
  includeSystem: boolean
}

function num(v: unknown, def: number): number {
  return typeof v === "number" && isFinite(v) && v >= 0 ? Math.floor(v) : def
}

export function fimConfig(ctx: Ctx): FimConfig {
  const c = ctx.config || {}
  return {
    fimPath: typeof c.fimPath === "string" && c.fimPath ? c.fimPath : DEFAULT_FIM_PATH,
    holeMarker: typeof c.holeMarker === "string" && c.holeMarker ? c.holeMarker : DEFAULT_HOLE_MARKER,
    defaultMaxTokens: num(c.defaultMaxTokens, DEFAULT_MAX_TOKENS),
    maxTokensLimit: num(c.maxTokensLimit, 0),
    includeSystem: c.includeSystem === true,
  }
}

/** 渠道 baseUrl 的源（scheme://host[:port]），忽略路径：baseUrl 写成 …/v1 也能用。 */
export function origin(baseUrl: string): string {
  const m = /^(https?:\/\/[^/?#]+)/i.exec(baseUrl.trim())
  if (!m) throw new Error("渠道 Base URL 无效：" + baseUrl)
  return m[1]
}

export function fimUrl(ctx: Ctx, cfg: FimConfig): string {
  const p = cfg.fimPath.trim()
  if (p.charAt(0) !== "/" || p.charAt(1) === "/") {
    throw new Error("fimPath 必须是以 / 开头的路径（相对 Base URL 的源），例如 " + DEFAULT_FIM_PATH)
  }
  return origin(ctx.channel.baseUrl) + p
}

function textOf(content: unknown): string {
  if (typeof content === "string") return content
  if (Array.isArray(content)) {
    return content.map((p: any) => (p && p.type === "text" && typeof p.text === "string" ? p.text : "")).join("")
  }
  return ""
}

/**
 * 取得 FIM 的 prompt / suffix：
 * 1. 请求体顶层的 prompt（字符串）优先；
 * 2. 否则取最后一条 user 消息的文本（多段文本直接拼接，图片忽略），includeSystem 时把 system / developer 消息放在前面；
 * 顶层 suffix（字符串）存在时直接使用，否则在文本中找第一个 holeMarker：之前为 prompt，之后为 suffix；没有标记时整段续写。
 */
export function promptAndSuffix(req: CanonicalRequest, cfg: FimConfig): { prompt: string; suffix?: string } {
  let text: string
  if (typeof req.prompt === "string") {
    text = req.prompt
  } else {
    const msgs = Array.isArray(req.messages) ? req.messages : []
    let last = ""
    for (let i = msgs.length - 1; i >= 0; i--) {
      if (msgs[i] && msgs[i].role === "user") {
        last = textOf(msgs[i].content)
        break
      }
    }
    text = last
    if (cfg.includeSystem) {
      const sys = msgs.filter((m) => m && (m.role === "system" || m.role === "developer")).map((m) => textOf(m.content)).filter((s) => s)
      if (sys.length) text = sys.join("\n") + "\n" + text
    }
  }
  if (typeof req.suffix === "string") return { prompt: text, suffix: req.suffix }
  const at = text.indexOf(cfg.holeMarker)
  if (at < 0) return { prompt: text }
  return { prompt: text.slice(0, at), suffix: text.slice(at + cfg.holeMarker.length) }
}

export function toFimBody(req: CanonicalRequest, cfg: FimConfig): Record<string, unknown> {
  const { prompt, suffix } = promptAndSuffix(req, cfg)
  const body: Record<string, unknown> = { model: req.model, prompt }
  if (suffix !== undefined) body.suffix = suffix
  let max = req.max_tokens != null ? req.max_tokens : req.max_completion_tokens
  if (max == null && cfg.defaultMaxTokens > 0) max = cfg.defaultMaxTokens
  if (max != null) {
    if (cfg.maxTokensLimit > 0 && max > cfg.maxTokensLimit) max = cfg.maxTokensLimit
    body.max_tokens = max
  }
  if (req.temperature != null) body.temperature = req.temperature
  if (req.top_p != null) body.top_p = req.top_p
  if (req.stop != null) body.stop = req.stop
  // frequency_penalty / presence_penalty 已被 DeepSeek 废弃（传了也不生效），logprobs / echo 无法放进 Chat 响应：都不转发。
  // tools / tool_choice / 图片对 FIM 没有意义：忽略。
  if (req.stream) {
    body.stream = true
    body.stream_options = { include_usage: true }
  } else {
    body.stream = false
  }
  return body
}

// DeepSeek finish_reason → Chat Completions finish_reason。
// insufficient_system_resource / aborted 表示生成被中断，按上游错误处理（见 ABORTED）。
export const FINISH: Record<string, FinishReason> = { stop: "stop", length: "length", content_filter: "content_filter" }

/** 生成被中断的结束原因 → 网关错误状态（503 / 502 都会换渠道重试）。 */
export const ABORTED: Record<string, { status: number; message: string }> = {
  insufficient_system_resource: { status: 503, message: "DeepSeek 推理资源不足（insufficient_system_resource），生成被中断" },
  aborted: { status: 502, message: "DeepSeek 生成被中断（aborted）" },
}

export function toChatUsage(u: any): ChatUsage | undefined {
  if (!u || typeof u !== "object") return undefined
  const prompt = Number(u.prompt_tokens) || 0
  const completion = Number(u.completion_tokens) || 0
  const out: ChatUsage = { prompt_tokens: prompt, completion_tokens: completion, total_tokens: Number(u.total_tokens) || prompt + completion }
  const d = u.prompt_tokens_details || {}
  // 文档：cached_tokens 与 prompt_cache_hit_tokens 相同；两处都可能出现。
  const cached = d.cached_tokens != null ? d.cached_tokens : u.prompt_cache_hit_tokens != null ? u.prompt_cache_hit_tokens : d.prompt_cache_hit_tokens
  if (cached != null) out.prompt_tokens_details = { cached_tokens: Number(cached) || 0 }
  const reasoning = u.completion_tokens_details && u.completion_tokens_details.reasoning_tokens
  if (reasoning) out.completion_tokens_details = { reasoning_tokens: Number(reasoning) || 0 }
  return out
}

function errorMessage(e: any, fallback: string): string {
  if (e && typeof e === "object") {
    const msg = typeof e.message === "string" ? e.message : fallback
    const code = typeof e.code === "string" ? e.code : typeof e.type === "string" ? e.type : ""
    return code && msg.indexOf(code) < 0 ? code + ": " + msg : msg
  }
  return typeof e === "string" ? e : fallback
}

function errorStatus(e: any): number {
  const s = e && Number(e.status != null ? e.status : e.code)
  return s >= 400 && s <= 599 ? s : 502
}

export function parseCompletion(body: string): CanonicalResponse {
  let j: any
  try {
    j = JSON.parse(body)
  } catch (_e) {
    return { error: { status: 502, message: "DeepSeek 返回的不是 JSON：" + body.slice(0, 200) } }
  }
  if (j && j.error) return { error: { status: errorStatus(j.error), message: errorMessage(j.error, "DeepSeek 返回错误") } }
  const c = j && Array.isArray(j.choices) ? j.choices[0] : undefined
  if (!c) return { error: { status: 502, message: "DeepSeek 响应缺少 choices" } }
  const reason = c.finish_reason
  if (reason && ABORTED[reason]) return { error: ABORTED[reason] }
  return {
    id: j.id,
    choices: [{ index: 0, message: { role: "assistant", content: typeof c.text === "string" ? c.text : "" }, finish_reason: FINISH[reason] || "stop" }],
    usage: toChatUsage(j.usage),
  }
}

/** 一行 SSE → CanonicalEvent。注释行（": keep-alive"）、event: / id: 行与 [DONE] 都不产生事件。 */
export function lineEvents(raw: string, state: any): CanonicalEvent[] {
  const line = raw.replace(/\r$/, "")
  if (line.slice(0, 5) !== "data:") return []
  const data = line.slice(5).trim()
  if (!data) return []
  if (data === "[DONE]") {
    state.done = true
    return []
  }
  let j: any
  try {
    j = JSON.parse(data)
  } catch (_e) {
    return [{ type: "error", message: "DeepSeek 流式数据不是 JSON：" + data.slice(0, 200) }]
  }
  if (j && j.error) return [{ type: "error", status: errorStatus(j.error), message: errorMessage(j.error, "DeepSeek 返回错误") }]
  const out: CanonicalEvent[] = []
  const c = j && Array.isArray(j.choices) ? j.choices[0] : undefined
  if (c) {
    if (typeof c.text === "string" && c.text !== "") out.push({ type: "delta", content: c.text })
    const reason = c.finish_reason
    if (reason && ABORTED[reason]) {
      out.push({ type: "error", status: ABORTED[reason].status, message: ABORTED[reason].message })
      return out
    }
    if (reason && !state.finished) {
      state.finished = true
      out.push({ type: "finish", reason: FINISH[reason] || "stop" })
    }
  }
  // include_usage 时每个 chunk 都带 usage（只有最后一个非 null）；也可能是单独的用量 chunk。
  const usage = toChatUsage(j && j.usage)
  if (usage && !state.usage) {
    state.usage = true
    out.push({ type: "usage", usage })
  }
  return out
}
