import { definePlugin, type UpstreamRequest } from "@omnigate/plugin-sdk"
import { STYLES, type ThinkingStyle, applyThinking, stripReasoning } from "./thinking"
import { type JsonObject, deepMerge, deletePath, isObject, isProtected, parseJSON, setPath, splitPath } from "./util"

// Only the JSON bodies of the chat-style dialects are rewritten; embeddings,
// images (multipart never reaches transformRequest), audio and anything else
// pass through untouched.
const CHAT = "openai.chat"
const RESPONSES = "openai.responses"

function str(v: unknown): string {
  return typeof v === "string" ? v.trim() : ""
}

function oneOf(v: unknown, allowed: string[], fallback: string): string {
  return typeof v === "string" && allowed.indexOf(v) >= 0 ? v : fallback
}

// The compiled modelRegex, kept across calls (runtimes are pooled and reused).
let regexCache: { src: string; re: RegExp | null } = { src: "", re: null }

/** null = invalid pattern. Matching is case-insensitive. */
function compileRegex(src: string): RegExp | null {
  if (regexCache.src !== src) {
    let re: RegExp | null = null
    try {
      re = new RegExp(src, "i")
    } catch {
      re = null
    }
    regexCache = { src, re }
  }
  return regexCache.re
}

/** A configured JSON literal ("" = not set); undefined when it is not valid JSON. */
function jsonValue(raw: string, field: string): { set: boolean; value?: unknown } | undefined {
  if (!raw) return { set: false }
  const r = parseJSON(raw)
  if (!r.ok) {
    og.log.warn(`${field} 不是合法的 JSON，已忽略：${raw}`)
    return undefined
  }
  return { set: true, value: r.value }
}

function rewrite(body: JsonObject, responses: boolean, cfg: Record<string, unknown>): void {
  // 1. model gate
  const pattern = str(cfg.modelRegex)
  if (pattern) {
    const re = compileRegex(pattern)
    if (!re) {
      og.log.warn(`modelRegex 不是合法的正则表达式，本次不改写：${pattern}`)
      return
    }
    if (typeof body.model !== "string" || !re.test(body.model)) return
  }

  // 2. removeFields
  for (const path of str(cfg.removeFields).split(/[\s,，]+/)) {
    const segs = splitPath(path)
    if (segs && !isProtected(segs)) deletePath(body, segs)
  }

  // 3. developer → system
  if (cfg.developerToSystem === true) {
    const list = responses ? body.input : body.messages
    if (Array.isArray(list)) {
      for (const msg of list) {
        if (isObject(msg) && msg.role === "developer") msg.role = "system"
      }
    }
  }

  // 4. thinking
  const mode = oneOf(cfg.thinking, ["passthrough", "on", "off"], "passthrough")
  if (mode !== "passthrough") {
    const on = mode === "on"
    const style = oneOf(cfg.thinkingStyle, STYLES, "openai") as ThinkingStyle
    if (style === "custom") {
      const segs = splitPath(str(cfg.thinkingPath))
      const v = jsonValue(str(on ? cfg.thinkingOnValue : cfg.thinkingOffValue), on ? "thinkingOnValue" : "thinkingOffValue")
      if (segs && !isProtected(segs) && v && v.set) setPath(body, segs, v.value)
    } else {
      applyThinking(body, {
        on,
        style,
        responses,
        effort: oneOf(cfg.reasoningEffort, ["low", "medium", "high"], "medium"),
        offEffort: oneOf(cfg.reasoningEffortOff, ["auto", "none", "minimal", "low"], "auto"),
      })
    }
    if (!on && cfg.stripReasoningContent === true && !responses) stripReasoning(body)
  }

  // 5. maxTokensCap
  const cap = cfg.maxTokensCap
  if (typeof cap === "number" && cap >= 1 && Math.floor(cap) === cap) {
    let present = false
    for (const f of ["max_tokens", "max_completion_tokens", "max_output_tokens"]) {
      const v = body[f]
      if (v === undefined || v === null) continue
      present = true
      if (typeof v === "number" && v > cap) body[f] = cap
    }
    if (!present && cfg.setMaxTokensWhenMissing === true) body[responses ? "max_output_tokens" : "max_tokens"] = cap
  }

  // 6. mergeBody (last)
  const merge = str(cfg.mergeBody)
  if (merge) {
    const r = parseJSON(merge)
    if (r.ok && isObject(r.value)) deepMerge(body, r.value, true)
    else og.log.warn("mergeBody 不是 JSON 对象，已忽略")
  }
}

export default definePlugin({
  transformRequest(req: UpstreamRequest, ctx) {
    if ((req.dialect === CHAT || req.dialect === RESPONSES) && isObject(req.body)) {
      rewrite(req.body, req.dialect === RESPONSES, ctx.config ?? {})
    }
    return req
  },
})
