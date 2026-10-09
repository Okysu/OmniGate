package plugin

import "encoding/json"

// customProtocolTemplate is the editor's "自定义协议" template: a complete
// upstream protocol for a fictional JSON Lines streaming API (phase9-api.md
// §2), with unit tests for every hook.
func customProtocolTemplate(id, name string) map[string]string {
	manifest := map[string]any{
		"id": id, "name": name, "version": "0.1.0", "sdk": SDKVersion,
		"description": "自定义协议示例：虚构的 Acme JSON Lines 流式协议（替换为真实上游的协议）。",
		"kind":        []string{KindChannel}, "protocol": ProtocolCustom, "entry": "src/index.ts",
		"defaults":    map[string]any{"baseUrl": "https://api.example.com", "models": []any{}},
		"permissions": map[string]any{"network": []string{"$baseUrl"}, "secrets": []string{"apiKey"}, "schedule": []string{}},
		"capabilities": map[string]any{
			"models.list":  map[string]any{"output": "models", "userTriggerable": true, "label": "模型列表"},
			"health.check": map[string]any{"output": "health", "userTriggerable": true, "label": "健康检查"},
		},
		"hooks": []string{"buildRequest", "parseResponse", "parseStream", "endStream", "normalizeError"},
		"uiContributions": []any{map[string]any{
			"slot": "channel.detail.capabilities", "title": "上游模型",
			"component": map[string]any{"type": "table", "rowsBind": "models.list:/models", "columns": []any{map[string]any{"key": "id", "label": "模型 ID"}}},
			"actions":   []any{map[string]any{"label": "同步模型列表", "capability": "models.list"}},
		}},
	}
	mj, _ := json.MarshalIndent(manifest, "", "  ")
	return map[string]string{
		"manifest.json":              string(mj) + "\n",
		"src/index.ts":               customProtocolSource,
		"src/acme.ts":                customProtocolAcme,
		"README.md":                  "# " + name + customProtocolReadme,
		"tests/build-request.json":   customTestBuild,
		"tests/parse-response.json":  customTestResponse,
		"tests/parse-stream.json":    customTestStream,
		"tests/normalize-error.json": customTestError,
		"tests/models.json":          customTestModels,
	}
}

const customProtocolReadme = `

自定义协议插件：上游既不兼容 OpenAI 也不兼容 Anthropic 时，由插件实现完整的上游协议。
网关把客户端的 Chat Completions / Messages / Responses 请求统一转换为 Chat Completions（CanonicalRequest）交给
` + "`buildRequest`" + `，再把插件返回的 Chat Completions 响应或流式事件转换回客户端协议。

本模板以一个虚构的 “Acme” 协议为例：

- 请求：` + "`POST {baseUrl}/v1/generate`" + `，请求头 ` + "`X-Acme-Key`" + `，请求体
  ` + "`{model, input: [{role, text, calls?, callId?}], maxTokens?, temperature?, tools?, stream}`" + `
- 非流式响应：` + "`{id, output: {text, calls: [{id, name, args}]}, stop: \"end\" | \"length\" | \"call\", usage: {in, out}}`" + `
- 流式响应：JSON Lines，每行一个事件：` + "`text`" + ` / ` + "`think`" + ` / ` + "`call`" + ` / ` + "`done`" + `（带 stop 与 usage）/ ` + "`error`" + `
- 错误响应：非 2xx，响应体 ` + "`{code, message}`" + `

测试用例在 tests/ 目录，可在编辑器的“测试”面板中运行；流式用例用 ` + "`chunks`" + ` 给出一组上游字节块（字符串），断言输出事件。
`

const customProtocolSource = `import { definePlugin, type CanonicalEvent, type Ctx } from "@omnigate/plugin-sdk"
import { STOP, lineEvents, toAcmeRequest, toChatUsage } from "./acme"

async function listModels(ctx: Ctx) {
  const res = await og.fetch(ctx.channel.baseUrl + "/v1/models", { headers: { "X-Acme-Key": og.secret("apiKey") } })
  if (!res.ok) throw new Error("上游返回 " + res.status)
  return res.json()
}

export default definePlugin({
  // CanonicalRequest（Chat Completions 请求）→ 上游 HTTP 请求。url 以 / 开头时相对渠道 baseUrl。
  buildRequest(req, _ctx) {
    return {
      method: "POST",
      url: "/v1/generate",
      headers: { "X-Acme-Key": og.secret("apiKey"), Accept: req.stream ? "application/x-ndjson" : "application/json" },
      body: toAcmeRequest(req),
    }
  },

  // 非流式：上游响应 → Chat Completions 响应
  parseResponse(res, _ctx) {
    const j = JSON.parse(res.body)
    const calls = (j.output && j.output.calls) || []
    return {
      id: j.id,
      choices: [{
        index: 0,
        message: {
          role: "assistant",
          content: (j.output && j.output.text) || "",
          tool_calls: calls.length ? calls.map((c: any) => ({ id: c.id, type: "function", function: { name: c.name, arguments: c.args } })) : undefined,
        },
        finish_reason: STOP[j.stop] || "stop",
      }],
      usage: toChatUsage(j.usage),
    }
  },

  // 流式：每段上游字节调用一次；state 在整个请求期间保留（同一个运行时）
  parseStream(chunk, state, _ctx) {
    if (!state.decoder) {
      state.decoder = new TextDecoder()
      state.buf = ""
      state.calls = 0
    }
    state.buf += state.decoder.decode(chunk, { stream: true })
    const lines: string[] = state.buf.split("\n")
    state.buf = lines.pop() || ""
    const out: CanonicalEvent[] = []
    for (const line of lines) out.push(...lineEvents(line, state))
    return out
  },

  // 上游流结束：处理没有换行结尾的最后一行
  endStream(state, _ctx) {
    const rest = (state.buf || "") + (state.decoder ? state.decoder.decode() : "")
    state.buf = ""
    return lineEvents(rest, state)
  },

  // 错误响应 → 网关错误（状态码决定重试分类：429 / 5xx / 401 等）
  normalizeError(res, _ctx) {
    let code = ""
    let message = res.body
    try {
      const j = JSON.parse(res.body)
      code = j.code || ""
      message = j.message || message
    } catch (_e) {
      // 非 JSON 错误体：保留原文
    }
    const byCode: Record<string, number> = { invalid_key: 401, forbidden: 403, rate_limited: 429, overloaded: 503, bad_request: 400 }
    return { status: byCode[code] || res.status, message: code ? code + ": " + message : message }
  },

  capabilities: {
    async "models.list"(_input, ctx) {
      const j = await listModels(ctx)
      return { models: (j.models || []).map((m: { name: string }) => ({ id: m.name })) }
    },
    async "health.check"(_input, ctx) {
      const started = Date.now()
      await listModels(ctx)
      return { ok: true, latencyMs: Date.now() - started }
    },
  },
})
`

const customProtocolAcme = `import type { CanonicalEvent, CanonicalRequest, ChatUsage, FinishReason } from "@omnigate/plugin-sdk"

// Acme 的结束原因 → Chat Completions 的 finish_reason
export const STOP: Record<string, FinishReason> = { end: "stop", length: "length", call: "tool_calls", filtered: "content_filter" }

function textOf(content: unknown): string {
  if (typeof content === "string") return content
  if (Array.isArray(content)) return content.map((p: any) => (p && p.type === "text" ? p.text : "")).join("")
  return ""
}

export function toAcmeRequest(req: CanonicalRequest) {
  const input = req.messages.map((m) => {
    const msg: Record<string, unknown> = { role: m.role === "developer" ? "system" : m.role, text: textOf(m.content) }
    if (m.tool_calls && m.tool_calls.length) {
      msg.calls = m.tool_calls.map((c) => ({ id: c.id, name: c.function.name, args: c.function.arguments }))
    }
    if (m.role === "tool") msg.callId = m.tool_call_id
    return msg
  })
  const body: Record<string, unknown> = { model: req.model, input, stream: !!req.stream }
  const max = req.max_tokens != null ? req.max_tokens : req.max_completion_tokens
  if (max != null) body.maxTokens = max
  if (req.temperature != null) body.temperature = req.temperature
  if (req.tools && req.tools.length) {
    body.tools = req.tools.map((t) => ({ name: t.function.name, description: t.function.description || "", schema: t.function.parameters || {} }))
  }
  return body
}

export function toChatUsage(u: { in?: number; out?: number } | undefined): ChatUsage | undefined {
  if (!u) return undefined
  const prompt = u.in || 0
  const completion = u.out || 0
  return { prompt_tokens: prompt, completion_tokens: completion, total_tokens: prompt + completion }
}

// 一行 JSON → CanonicalEvent
export function lineEvents(line: string, state: any): CanonicalEvent[] {
  if (!line.trim()) return []
  const e = JSON.parse(line)
  switch (e.event) {
    case "text":
      return [{ type: "delta", content: e.text }]
    case "think":
      return [{ type: "delta", reasoning: e.text }]
    case "call":
      return [{ type: "delta", toolCalls: [{ index: state.calls++, id: e.id, type: "function", function: { name: e.name, arguments: e.args } }] }]
    case "done": {
      const out: CanonicalEvent[] = [{ type: "finish", reason: STOP[e.stop] || "stop" }]
      const usage = toChatUsage(e.usage)
      if (usage) out.push({ type: "usage", usage })
      return out
    }
    case "error":
      return [{ type: "error", message: e.message || "upstream error" }]
  }
  return []
}
`

const customTestBuild = `{
  "name": "构造上游请求（流式、带工具）",
  "hook": "buildRequest",
  "secrets": { "apiKey": "ak-test" },
  "request": {
    "model": "acme-1",
    "stream": true,
    "max_tokens": 256,
    "messages": [
      { "role": "system", "content": "你是助手" },
      { "role": "user", "content": [{ "type": "text", "text": "北京天气" }] }
    ],
    "tools": [{ "type": "function", "function": { "name": "get_weather", "parameters": { "type": "object" } } }]
  },
  "expect": {
    "output": {
      "method": "POST",
      "url": "/v1/generate",
      "headers": { "X-Acme-Key": "ak-test", "Accept": "application/x-ndjson" },
      "body": {
        "model": "acme-1",
        "stream": true,
        "maxTokens": 256,
        "input": [{ "role": "system", "text": "你是助手" }, { "role": "user", "text": "北京天气" }],
        "tools": [{ "name": "get_weather" }]
      }
    }
  }
}
`

const customTestResponse = `{
  "name": "非流式响应",
  "hook": "parseResponse",
  "response": {
    "status": 200,
    "body": { "id": "r-1", "output": { "text": "你好", "calls": [] }, "stop": "end", "usage": { "in": 5, "out": 2 } }
  },
  "expect": {
    "output": {
      "id": "r-1",
      "choices": [{ "message": { "role": "assistant", "content": "你好" }, "finish_reason": "stop" }],
      "usage": { "prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7 }
    }
  }
}
`

const customTestStream = `{
  "name": "流式：跨块的行、工具调用与用量",
  "hook": "parseStream",
  "chunks": [
    "{\"event\":\"think\",\"text\":\"查一下\"}\n{\"event\":\"text\",\"text\":\"好的\"}\n{\"event\":\"te",
    "xt\",\"text\":\"，稍等\"}\n",
    "{\"event\":\"call\",\"id\":\"call_1\",\"name\":\"get_weather\",\"args\":\"{\\\"city\\\":\\\"北京\\\"}\"}\n",
    "{\"event\":\"done\",\"stop\":\"call\",\"usage\":{\"in\":12,\"out\":9}}"
  ],
  "expect": {
    "output": [
      { "type": "delta", "reasoning": "查一下" },
      { "type": "delta", "content": "好的" },
      { "type": "delta", "content": "，稍等" },
      { "type": "delta", "toolCalls": [{ "index": 0, "id": "call_1", "function": { "name": "get_weather", "arguments": "{\"city\":\"北京\"}" } }] },
      { "type": "finish", "reason": "tool_calls" },
      { "type": "usage", "usage": { "prompt_tokens": 12, "completion_tokens": 9 } }
    ]
  }
}
`

const customTestError = `{
  "name": "错误映射",
  "hook": "normalizeError",
  "response": { "status": 400, "body": { "code": "invalid_key", "message": "key revoked" } },
  "expect": { "output": { "status": 401, "message": "invalid_key: key revoked" } }
}
`

const customTestModels = `{
  "name": "模型列表",
  "capability": "models.list",
  "secrets": { "apiKey": "ak-test" },
  "fetch": [
    { "match": { "url": "https://api.example.com/v1/models" }, "response": { "status": 200, "json": { "models": [{ "name": "acme-1" }] } } }
  ],
  "expect": { "output": { "models": [{ "id": "acme-1" }] } }
}
`
