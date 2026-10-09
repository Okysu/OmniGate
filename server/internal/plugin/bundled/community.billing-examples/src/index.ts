import { definePlugin, type Usage } from "@omnigate/plugin-sdk"

// 输入 token 与 tokens.input 计量一致：未命中缓存的输入 + 缓存读取 + 缓存写入。
function inputTokens(u: Usage): number {
  return (u.input || 0) + (u.cacheRead || 0) + (u.cacheWrite || 0)
}

export default definePlugin({
  billing: {
    meters: {
      // 输出 token × 4 + 输入 token
      weighted_tokens: {
        label: "加权 token（输出 ×4）",
        unit: "token",
        computeUnits(usage) {
          return (usage.output || 0) * 4 + inputTokens(usage)
        },
      },
      // 图片张数（图片接口的输出张数）
      per_image: {
        label: "图片张数",
        unit: "张",
        computeUnits(usage, ctx) {
          return ctx.imageCount || usage.images || 0
        },
      },
    },
  },
})
