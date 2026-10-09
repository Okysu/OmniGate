# 契约草案：插件 SDK（Channel as Code）

> 状态：**概念文档**。Round 3 实际实现的 SDK v1 范围、字段与语义以 [phase2-api.md](phase2-api.md) 为准（例如 UI 绑定写作 `"bind": "balance.get:/total"`、
> 能力字段为 `cacheTtl`/`timeout`/`label`、宿主 API 为同步 `og.storage`）；类型声明见 `server/internal/plugin/sdk/omnigate-plugin-sdk.d.ts`。
> 自定义协议（`protocol: "custom"`）与计费插件（`kind: billing`）已在 Round 6 实现，规则见 [phase9-api.md](phase9-api.md) §2、§3 与 §4 实现差异，
> 本文 §5.1、§5.2 记录实际的 Hook 与类型。订阅源、灰度与 `estimate` / `planTemplates` / `beforeRoute` / `normalizeUsage` 尚未实现。
> SDK 版本号 `sdk: 1`。破坏性变更必须升主版本并暂停确认。

## 1. 插件包结构

```
my-plugin/
├── manifest.json        # 身份、版本、兼容范围、入口、权限、能力、UI 贡献
├── src/index.ts         # 入口：export default definePlugin({...})
├── src/*.ts             # 其他模块（打包时内联）
├── tests/*.test.ts      # 用宿主请求模拟器运行
└── README.md
```

## 2. manifest.json

```jsonc
{
  "$schema": "https://omnigate.dev/schema/plugin-manifest-v1.json",
  "id": "community.deepseek",          // 反向域名风格，全局唯一
  "name": "DeepSeek",
  "version": "1.2.0",                  // semver；已发布版本内容不可变（按哈希校验）
  "sdk": 1,
  "kind": ["channel"],                 // channel | billing，可同时声明；省略为 ["channel"]
  "author": "…",
  "homepage": "https://…",
  "engines": { "omnigate": ">=0.3.0 <1.0.0" },
  "entry": "src/index.ts",
  "extends": "openai.chat",            // 继承的内置上游协议（openai.chat | anthropic.messages）
  // "protocol": "custom",             // 或：插件实现完整上游协议（与 extends 互斥，见 §5.1）
  // "hooks": ["transformRequest", "signRequest"],  // 实现的 Hook；自定义协议见 §5.1
  // "billing": { "meters": { "weighted_tokens": { "label": "加权 token", "unit": "token" } } },  // kind 含 billing 时必填，见 §5.2
  "permissions": {
    "network": ["api.deepseek.com"],   // 精确主机名或 *.example.com；禁止 IP 字面量
    "secrets": ["apiKey"],             // 可以拿到哪些密钥字段的句柄
    "storage": { "maxKeys": 100, "maxBytes": 65536 },
    "schedule": ["balance.get"],       // 允许定时执行的能力
    "dangerous": []                    // 例如 "secrets:plaintext"，需单独审批
  },
  "configSchema": { "...": "JSON Schema，见 §3" },
  "capabilities": { "...": "见 §4" },
  "uiContributions": [ "...见 §6" ],
  "migrations": [ { "from": "1.x", "to": "1.2.0", "hook": "migrateConfig" } ]
}
```

## 3. 配置 Schema

在 JSON Schema 2020-12 的基础上增加以下扩展关键字：

| 关键字 | 作用 |
| --- | --- |
| `x-secret: true` | 敏感字段：加密存储，界面默认遮挡，读取接口永不返回 |
| `x-visible-if` | 条件显示，例如 `{ "field": "region", "equals": "cn" }` |
| `x-group` | 表单分组 |
| `x-help` | 帮助文本 / 文档链接（仅 https） |

## 4. 原子能力

```ts
interface CapabilityDecl {
  input?: JSONSchema
  output: JSONSchema
  userTriggerable: boolean          // 是否允许在界面上手动触发
  schedulable?: { minInterval: string }   // 例 "10m"；同时需要 permissions.schedule
  cache?: { ttl: string; scope: 'channel' | 'plugin' }
  timeout?: string                  // 默认 10s，上限 60s
  ui?: { label: string; icon?: string; confirm?: string }
}
```

内置能力名：`models.list`、`balance.get`、`usage.query`、`quota.get`、`health.check`、`files.create`、`files.get`、
`batches.create`、`batches.get`、`fineTuning.list`；厂商特有能力使用 `custom.<name>`。

标准输出类型（让宿主能把结果用在统一的界面和告警里）：

```ts
interface BalanceOutput { currency: string; total: string; granted?: string; toppedUp?: string; available: boolean; asOf: string }
interface QuotaOutput  { windows: Array<{ id: string; label: string; window: Window; used: string; limit: string; resetsAt?: string }> }
interface ModelsOutput { models: Array<{ id: string; displayName?: string; contextWindow?: number; pricing?: PriceHint }> }
```

能力不可用时（例如官方没有余额接口），插件返回 `{ unsupported: true, reason }`，界面显示“不支持”。**不得抓取网页或编造数据。**

## 5. Hook（TypeScript 声明）

```ts
import type { Ctx, CanonicalRequest, UpstreamRequest, UpstreamResponse, CanonicalEvent, Usage, GatewayError } from '@omnigate/plugin-sdk'

export default definePlugin({
  // ---- 渠道 Hook（全部可选；不覆盖的 Hook 走内置协议的原生 Go 实现，不进入 JS） ----
  beforeRoute?(ctx: Ctx): RouteHints | void,
  transformRequest?(req: CanonicalRequest, ctx: Ctx): UpstreamRequest,
  signRequest?(req: UpstreamRequest, secrets: SecretHandles): UpstreamRequest,
  parseResponse?(res: UpstreamResponse, ctx: Ctx): CanonicalResponse,
  parseStream?(chunk: Uint8Array, state: StreamState): CanonicalEvent[],
  normalizeUsage?(raw: unknown): Usage,
  normalizeError?(res: UpstreamResponse): GatewayError,

  // ---- 能力实现：键与 manifest.capabilities 对应 ----
  capabilities: {
    'models.list'?: (input, ctx) => Promise<ModelsOutput>,
    'balance.get'?: (input, ctx) => Promise<BalanceOutput | Unsupported>,
    'quota.get'?:   (input, ctx) => Promise<QuotaOutput | Unsupported>,
    [name: `custom.${string}`]: (input, ctx) => Promise<unknown>,
  },

  // ---- 计费插件（kind 包含 "billing" 时） ----
  billing?: {
    meters?: Record<string, {
      label: string
      unit?: string
      // 请求结束后由结算器调用：纯函数、确定性、无网络；5ms 超时
      computeUnits(usage: Usage, ctx: BillingCtx): number | string
    }>,
    // 可选：请求前估算，严格 5ms 超时，超时则回退到宿主估算
    estimate?(req: CanonicalRequest, ctx: BillingCtx): Record<string, number | string>,
    // 套餐模板：管理员可以一键实例化，例如“类 Claude Pro：5h 会话窗口 + 每周上限”
    planTemplates?: Record<string, PlanTemplate>,
  },

  // ---- 配置迁移 ----
  migrateConfig?(old: unknown, fromVersion: string): unknown,
})
```

`BillingCtx` 的实际字段见 §5.2。计费插件**拿不到**密钥、网络、存储。

### 5.1 自定义协议（已实现，phase9-api.md §2）

manifest 声明 `"protocol": "custom"`（不写 `extends`），`hooks` 必须包含 `buildRequest`、`parseResponse`、`parseStream`，可选
`endStream`、`normalizeError`、`signRequest`（不支持 `transformRequest`）。这类插件的渠道类型为 `custom`，服务 Chat Completions、
Messages、Responses 三种客户端协议（宿主统一转换为 Chat Completions 交给插件，再把结果转换回客户端协议），不服务 embeddings、图片与音频。

```ts
// CanonicalRequest：Chat Completions 请求（model 为渠道的上游模型，stream 表示客户端是否流式）
buildRequest(req: CanonicalRequest, ctx: Ctx): CustomUpstreamRequest   // 50 ms
//   CustomUpstreamRequest = { method?: "GET"|"POST"|"PUT"|"PATCH"|"DELETE"（默认 POST）, url: string, headers?: Record<string,string>, body?: unknown }
//   url 以 "/" 开头时拼在渠道 baseUrl 后；绝对地址须为 https，且主机为 baseUrl 主机或在 permissions.network 中
//   body 为字符串时原样发送，其他值 JSON 编码（默认 Content-Type: application/json）；宿主不添加认证头，用 og.secret() 句柄
signRequest?(req: CustomUpstreamRequest & { dialect: "custom" }, ctx: Ctx): CustomUpstreamRequest   // 在 buildRequest 之后，50 ms
parseResponse(res: UpstreamResponse, ctx: Ctx): CanonicalResponse   // 50 ms + 每 64 KiB 10 ms，最多 1 s
//   UpstreamResponse = { status: number, headers: Record<string,string>（小写名）, body: string（UTF-8 文本） }
//   CanonicalResponse = { id?, choices: [{ index?, message: { role?, content, reasoning_content?, tool_calls? }, finish_reason? }], usage?: ChatUsage }
//                     | { error: { status?: number, message: string } }   // HTTP 200 中的业务错误
parseStream(chunk: Uint8Array, state: StreamState, ctx: Ctx): CanonicalEvent[]   // 每块 50 ms
endStream?(state: StreamState, ctx: Ctx): CanonicalEvent[]                        // 上游流结束时，50 ms
normalizeError?(res: UpstreamResponse, ctx: Ctx): { status: number; message: string }   // 非 2xx 响应，50 ms

type CanonicalEvent =
  | { type: "delta"; content?: string; reasoning?: string; toolCalls?: ChatToolCall[] }   // ChatToolCall 为 Chat Completions 格式（index、id、type、function{name, arguments}）
  | { type: "finish"; reason: "stop" | "length" | "tool_calls" | "content_filter" }
  | { type: "usage"; usage: ChatUsage }   // { prompt_tokens, completion_tokens, prompt_tokens_details?: {cached_tokens}, completion_tokens_details?: {reasoning_tokens} }
  | { type: "error"; message: string; status?: number }
```

- 一个请求的所有 Hook 在**同一个运行时**中执行；`state` 是宿主为该请求创建的普通对象，在 `parseStream` / `endStream` 调用之间保留。
- 同一请求所有 Hook 的 JS 执行时间合计不超过 **5 s**；任一 Hook 抛错、超时或返回值格式错误都返回 `plugin_error`：尚未向客户端写出时按
  `server_error` 换渠道重试，已写出后以流内错误结束。超时计为资源违规（5 分钟内 3 次自动停用）。
- 上游流结束时没有 `finish` 事件按 `stop` 结束；没有 `usage` 事件时按输出估算用量。
- `normalizeError` 返回的 `status` 决定分类（429、5xx 重试，401/403 为上游认证失败，其他 4xx 默认不重试）；没有实现或执行失败时按 HTTP 状态码分类。
- 沙盒提供 `TextDecoder`（仅 UTF-8，`decode(chunk, {stream: true})` 处理跨块的多字节字符）与 `TextEncoder`。
- 插件**第一个**自定义协议版本发布后必须审批（即使权限没有变化）。
- 渠道的“测试连接”与“发现模型”执行插件的 `health.check` / `models.list` 能力。
- 编辑器模板 `custom-protocol`（“自定义协议”）以虚构的 JSON Lines 流式协议为例，附带全部 Hook 的测试用例。测试用例字段：
  `hook: "buildRequest"` + `request`（Chat Completions 请求）；`hook: "parseResponse" | "normalizeError"` + `response: {status, headers, body}`；
  `hook: "parseStream"` + `chunks`（字符串数组）或 `chunksBase64`，按块调用后再调用 `endStream`，输出为全部事件数组；结果中的
  `calls` 列出每次调用（`hook`、`chunk`、`durationMs`、`events`、`error`），`chatChunks` 是宿主转换出的 Chat Completions chunk。
- `finish_reason` / `finish` 事件的 `reason` 类型为 `FinishReason`（`"stop" | "length" | "tool_calls" | "content_filter"`）；
  映射上游结束原因时把映射表声明为 `Record<string, FinishReason>`（模板即如此）。

### 5.2 计费插件（已实现，phase9-api.md §3）

manifest `kind` 包含 `"billing"`，并在 `billing.meters` 中声明计量（名称形如 `weighted_tokens`，`label` 必填，`unit` 可选）；代码实现
`billing.meters.<名称>.computeUnits`：

```ts
billing: {
  meters: {
    weighted_tokens: {
      computeUnits(usage: Usage, ctx: BillingCtx): number | string   // 非负数或十进制字符串（最多 9 位小数）
    },
  },
}
// Usage = { input, output, cacheRead, cacheWrite, reasoning, estimated, imageInputTokens?, images?, audioSeconds?, ... }（input 不含缓存）
// BillingCtx = { model, servedModel, channelId, channelTier, userGroup（组名）, inbound, imageCount, audioSeconds }
```

- 套餐规则的 `meter` 写 `custom:<插件 ID>.<计量名>`；保存规则时校验插件已启用且有已批准版本声明该计量，并把该版本固定在规则的
  `pluginVersionId` 中，同时快照计量的 `meterLabel` / `meterUnit`（订阅快照随之固定，插件升级不影响已开通订阅）。可选计量列表：`GET /api/admin/billing/meters`。
- `computeUnits` 在请求结算时（套餐覆盖的请求）执行：纯函数，`og` 只有 `log`、`encoding`、`crypto.sha256`，没有网络、存储与密钥；
  超时 5 ms。超时、抛错、返回值不合法或插件不可用（停用、版本不存在）时该次计量按 0 记，写 warn 日志并计入指标
  `omnigate_billing_plugin_errors_total{plugin, meter, reason}`。
- 随附示例 `community.billing-examples`：`weighted_tokens`（输出 token × 4 + 输入 token，输入含缓存）、`per_image`（图片张数）。
- 测试用例：`{ "meter": "weighted_tokens", "usage": {...}, "billingCtx": {...}, "expect": { "output": "325" } }`，输出为十进制字符串。

## 6. 宿主 API（全局 `og`）

| API | 说明 | 所需权限 |
| --- | --- | --- |
| `og.fetch(url, init)` | 受控 HTTP；只允许白名单主机；防 SSRF；响应体上限 4 MiB | `network` |
| `og.secret(name)` | 返回密钥句柄（不透明字符串），用在请求头/查询参数中由宿主替换 | `secrets` |
| `og.crypto.hmac(alg, handle, data)` / `og.crypto.sha256(data)` | 在 Go 侧完成签名，明文不进入 JS | `secrets` |
| `og.storage.get/set/delete` | 按（插件，渠道）隔离的键值存储，有配额 | `storage` |
| `og.log.info/warn/error` | 结构化日志；宿主会过滤疑似密钥 | 无 |
| `og.config` | 当前渠道的非敏感配置（只读） | 无 |

宿主**不提供** `require`、`process`、文件系统、子进程、任意计时器。

## 7. UI 贡献（声明式，ADR-0003）

```jsonc
{
  "slot": "channel.detail.overview",
  "kind": "declarative",
  "component": {
    "type": "statGroup",
    "items": [
      { "type": "stat", "label": "余额", "bind": { "capability": "balance.get", "pointer": "/total" }, "format": "currency" },
      { "type": "stat", "label": "同步时间", "bind": { "capability": "balance.get", "pointer": "/asOf" }, "format": "relativeTime" }
    ],
    "actions": [ { "type": "actionButton", "label": "刷新", "capability": "balance.get" } ]
  }
}
```

## 8. 生命周期与版本

- 草稿 → 发布（不可变版本，记录内容哈希、签名状态、来源、发布人、时间）→ 渠道**固定**某个版本。
- 升级：展示差异与权限变化 → 新增权限需要重新授权 → 运行 `migrateConfig` → 可以先灰度到部分渠道 → 一键回滚。
- 订阅源：下载 → 校验哈希/签名 → 展示权限变化 → 管理员确认后才安装。
- 静态风险扫描（例如 `eval`、超大正则、可疑字符串）只用于提示，**不作为安全保证**。

## 9. 示例：DeepSeek（Phase 2 验收）

继承 `openai.chat`；只覆盖 `capabilities['balance.get']`（调用官方 `GET /user/balance`）与 `models.list`；
贡献余额卡片和“刷新”按钮；`permissions.network = ["api.deepseek.com"]`；`schedule = ["balance.get"]`，最小间隔 10 分钟。
核心代码中不出现任何 DeepSeek 专用分支。
