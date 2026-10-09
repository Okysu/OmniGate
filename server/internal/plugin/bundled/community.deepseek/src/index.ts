import { definePlugin, type Ctx } from "@omnigate/plugin-sdk"

interface BalanceInfo {
  currency: string
  total_balance: string
  granted_balance?: string
  topped_up_balance?: string
}

/** DeepSeek serves management endpoints at the API root, not under /v1. */
function root(ctx: Ctx): string {
  return ctx.channel.baseUrl.replace(/\/v1\/?$/, "")
}

async function get(url: string): Promise<any> {
  const res = await og.fetch(url, {
    headers: { Authorization: `Bearer ${og.secret("apiKey")}`, Accept: "application/json" },
  })
  if (res.status === 401) throw new Error("API Key 无效或已失效（401）")
  if (res.status === 402) throw new Error("账户余额不足（402）")
  if (res.status === 404) throw new Error("接口不存在（404）：Base URL 可能不是 DeepSeek 官方 API")
  if (!res.ok) throw new Error(`DeepSeek 返回 ${res.status}`)
  if (!(res.headers["content-type"] ?? "").includes("json")) {
    throw new Error("上游没有返回 JSON：Base URL 可能不是 DeepSeek 官方 API（余额接口只有官方 API 提供）")
  }
  return res.json()
}

export default definePlugin({
  // 1.0.3: the low-balance threshold moved to the platform (channel setting
  // "上游余额告警阈值", which sends real notifications); drop the old field.
  migrateConfig(old: unknown) {
    const { lowBalance: _unused, ...rest } = (old ?? {}) as Record<string, unknown>
    return rest
  },
  capabilities: {
    async "balance.get"(_input, ctx) {
      const j = await get(root(ctx) + "/user/balance")
      const infos: BalanceInfo[] = j.balance_infos ?? []
      const want = (ctx.config.currency as string) ?? "CNY"
      const info = infos.find((b) => b.currency === want) ?? infos[0]
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
      const j = await get(ctx.channel.baseUrl + "/models")
      return { models: (j.data ?? []).map((m: { id: string }) => ({ id: m.id })) }
    },

    async "health.check"(_input, ctx) {
      const started = Date.now()
      await get(ctx.channel.baseUrl + "/models")
      return { ok: true, latencyMs: Date.now() - started }
    },

    // DeepSeek has no public usage API; say so instead of scraping or guessing.
    async "usage.query"() {
      return { unsupported: true, reason: "DeepSeek 未提供公开的用量查询 API，请在 DeepSeek 控制台查看" }
    },
  },
})
