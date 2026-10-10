// Thinking on/off for the upstreams' different switches. Sources for every
// style are listed in README.md (verified against official docs / source).
import { type JsonObject, isObject } from "./util"

export type ThinkingStyle =
  | "openai"
  | "qwen_dashscope"
  | "chat_template_kwargs"
  | "zhipu"
  | "ollama"
  | "qwen_soft_switch"
  | "custom"

export const STYLES: ThinkingStyle[] = ["openai", "qwen_dashscope", "chat_template_kwargs", "zhipu", "ollama", "qwen_soft_switch", "custom"]

export interface ThinkingOptions {
  on: boolean
  style: ThinkingStyle
  responses: boolean
  /** low | medium | high: the effort for "on" (openai / ollama). */
  effort: string
  /** "auto" or an explicit effort for "off" (openai). */
  offEffort: string
}

/**
 * The lowest reasoning effort OpenAI accepts for a model family ("off"), or
 * null when the model only accepts one value (leave the body alone):
 *  - gpt-5-pro: only "high"
 *  - gpt-6-astra, gpt-6.1-sol: lowest "low" (no "none" / "minimal")
 *  - gpt-5, gpt-5-mini, gpt-5-nano: "minimal" ("none" arrived with gpt-5.1)
 *  - o-series (o1, o3, o4-mini…): "low"
 *  - everything else (gpt-5.1+, gpt-6-sol / luna, other OpenAI-compatible servers): "none"
 */
export function openAIOffEffort(model: string): string | null {
  const m = model.toLowerCase().replace(/^.*\//, "")
  if (/^gpt-5-pro(-|$)/.test(m)) return null
  if (/^gpt-6-astra(-|$)/.test(m) || /^gpt-6\.1-sol(-|$)/.test(m)) return "low"
  if (/^gpt-5(-mini|-nano)?(-\d{4}-\d{2}-\d{2})?$/.test(m)) return "minimal"
  if (/^o\d/.test(m)) return "low"
  return "none"
}

/** Chat: top-level reasoning_effort; Responses: reasoning.effort (merged into an existing object). */
function setEffort(body: JsonObject, responses: boolean, effort: string): void {
  if (responses) {
    if (!isObject(body.reasoning)) body.reasoning = {}
    body.reasoning.effort = effort
  } else {
    body.reasoning_effort = effort
  }
}

function subObject(body: JsonObject, key: string): JsonObject {
  if (!isObject(body[key])) body[key] = {}
  return body[key]
}

const SWITCH_RE = /\s*\/(no_)?think\s*$/

/** Replaces a trailing /think or /no_think (if any) with tag, so applying twice changes nothing. */
function withSwitch(text: string, tag: string): string {
  const base = text.replace(SWITCH_RE, "")
  return base ? base + " " + tag : tag
}

/** Appends the Qwen3 soft switch to the last user message (string content or its last text part). */
function softSwitch(body: JsonObject, responses: boolean, tag: string): void {
  const partType = responses ? "input_text" : "text"
  let list: any[]
  if (responses) {
    if (typeof body.input === "string") {
      body.input = withSwitch(body.input, tag)
      return
    }
    if (!Array.isArray(body.input)) return
    list = body.input
  } else {
    if (!Array.isArray(body.messages)) return
    list = body.messages
  }
  for (let i = list.length - 1; i >= 0; i--) {
    const msg = list[i]
    if (!isObject(msg) || msg.role !== "user") continue
    if (responses && msg.type !== undefined && msg.type !== "message") continue
    const content = msg.content
    if (typeof content === "string") {
      msg.content = withSwitch(content, tag)
    } else if (Array.isArray(content)) {
      for (let j = content.length - 1; j >= 0; j--) {
        const part = content[j]
        if (isObject(part) && part.type === partType && typeof part.text === "string") {
          part.text = withSwitch(part.text, tag)
          return
        }
      }
      content.push({ type: partType, text: tag })
    } else {
      msg.content = tag
    }
    return
  }
}

/** Applies thinking on/off for every style except "custom" (handled by the caller). */
export function applyThinking(body: JsonObject, o: ThinkingOptions): void {
  switch (o.style) {
    case "openai": {
      let effort: string | null = o.effort
      if (!o.on) effort = o.offEffort !== "auto" ? o.offEffort : openAIOffEffort(typeof body.model === "string" ? body.model : "")
      if (effort) setEffort(body, o.responses, effort)
      return
    }
    case "ollama": {
      // Ollama maps reasoning_effort / reasoning.effort to its think value ("none" → false).
      // reasoning.effort wins over reasoning_effort on /v1/chat/completions, and the
      // `think` extension wins over reasoning.effort on /v1/responses: keep them consistent.
      const effort = o.on ? o.effort : "none"
      setEffort(body, o.responses, effort)
      if (o.responses) delete body.think
      else if (isObject(body.reasoning)) body.reasoning.effort = effort
      return
    }
    case "qwen_dashscope":
      body.enable_thinking = o.on
      return
    case "chat_template_kwargs": {
      // Qwen3 templates read enable_thinking, DeepSeek-V3.1+ templates read thinking;
      // vLLM drops kwargs the template does not use, Jinja ignores unused ones.
      const kw = subObject(body, "chat_template_kwargs")
      kw.enable_thinking = o.on
      kw.thinking = o.on
      return
    }
    case "zhipu":
      subObject(body, "thinking").type = o.on ? "enabled" : "disabled"
      return
    case "qwen_soft_switch":
      softSwitch(body, o.responses, o.on ? "/think" : "/no_think")
      return
  }
}

/** Removes prior-turn reasoning from assistant messages (Chat bodies). */
export function stripReasoning(body: JsonObject): void {
  if (!Array.isArray(body.messages)) return
  for (const msg of body.messages) {
    if (isObject(msg) && msg.role === "assistant") {
      delete msg.reasoning_content
      delete msg.reasoning
    }
  }
}
