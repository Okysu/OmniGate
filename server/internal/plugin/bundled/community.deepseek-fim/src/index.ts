import { definePlugin, type CanonicalEvent, type Ctx } from "@omnigate/plugin-sdk"
import { fimConfig, fimUrl, lineEvents, origin, parseCompletion, toFimBody } from "./fim"

interface BalanceInfo {
  currency: string
  total_balance: string
  granted_balance?: string
  topped_up_balance?: string
}

/** 管理接口（/models、/user/balance）在 API 根路径下，与 FIM 一样按 Base URL 的源拼接。 */
async function get(ctx: Ctx, path: string): Promise<any> {
  const res = await og.fetch(origin(ctx.channel.baseUrl) + path, {
    headers: { Authorization: `Bearer ${og.secret("apiKey")}`, Accept: "application/json" },
  })
  if (res.status === 401) throw new Error("API Key 无效或已失效（401）")
  if (res.status === 402) throw new Error("账户余额不足（402）")
  if (res.status === 404) throw new Error("接口不存在（404）：Base URL 可能不是 DeepSeek 官方 API")
  if (!res.ok) throw new Error(`DeepSeek 返回 ${res.status}`)
  if (!(res.headers["content-type"] || "").includes("json")) {
    throw new Error("上游没有返回 JSON：Base URL 可能不是 DeepSeek 官方 API")
  }
  return res.json()
}

export default definePlugin({
  // Chat Completions 请求 → DeepSeek FIM 补全请求（规则见 README）
  buildRequest(req, ctx) {
    const cfg = fimConfig(ctx)
    return {
      method: "POST",
      url: fimUrl(ctx, cfg),
      headers: {
        Authorization: `Bearer ${og.secret("apiKey")}`,
        Accept: req.stream ? "text/event-stream" : "application/json",
      },
      body: toFimBody(req, cfg),
    }
  },

  // text_completion → Chat Completions 响应；HTTP 200 中的错误体返回 {error}
  parseResponse(res, _ctx) {
    return parseCompletion(res.body)
  },

  // SSE：data: {...choices[].text...} / data: [DONE]；行可能跨块，按行缓冲
  parseStream(chunk, state, _ctx) {
    if (!state.decoder) {
      state.decoder = new TextDecoder()
      state.buf = ""
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
    return rest ? lineEvents(rest, state) : []
  },

  // DeepSeek 错误体 {error: {message, type, code}} → 网关错误；状态码沿用上游（400/401/402/422/429/500/503）
  normalizeError(res, _ctx) {
    let message = res.body.slice(0, 500)
    try {
      const j = JSON.parse(res.body)
      const e = j && j.error
      if (e && typeof e.message === "string") message = e.message
      else if (j && typeof j.message === "string") message = j.message
    } catch (_e) {
      // 非 JSON 错误体：保留原文
    }
    const hint: Record<number, string> = {
      401: "API Key 无效或已失效",
      402: "DeepSeek 账户余额不足",
      404: "接口不存在：请检查 Base URL 与 fimPath",
    }
    return { status: res.status, message: hint[res.status] ? hint[res.status] + "：" + message : message }
  },

  capabilities: {
    async "balance.get"(_input, ctx) {
      const j = await get(ctx, "/user/balance")
      const infos: BalanceInfo[] = j.balance_infos || []
      const info = infos.find((b) => b.currency === "CNY") || infos[0]
      if (!info) return { unsupported: true, reason: "账户没有返回余额信息" }
      return {
        currency: info.currency,
        total: info.total_balance,
        granted: info.granted_balance,
        toppedUp: info.topped_up_balance,
        available: Boolean(j.is_available),
      }
    },

    async "models.list"(_input, ctx) {
      const j = await get(ctx, "/models")
      return { models: (j.data || []).map((m: { id: string }) => ({ id: m.id })) }
    },

    async "health.check"(_input, ctx) {
      const started = Date.now()
      await get(ctx, "/models")
      return { ok: true, latencyMs: Date.now() - started }
    },
  },
})
