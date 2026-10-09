# 契约：Phase 1 控制面 API 与网关行为（Round 2）

> 状态：**已实现（Round 2）**。机器可读版本见 `openapi.yaml`；与本文不一致处以 §7“实现差异”为准。
> 通用约定沿用 Round 1：JSON camelCase；错误 `{error:{code,message,requestId,details?}}`；非 GET 需要 `X-Requested-With: XMLHttpRequest`；
> 列表 `?page=&pageSize=` → `{items,total,page,pageSize}`；需要乐观锁的写操作带 `version`，冲突返回 409 `version_conflict`。
> **金额一律为十进制字符串**（结算币种，最多 9 位小数，ADR-0007）。时间为 ISO 8601 UTC。

## 1. 渠道（Channel）

Phase 1 内置两种渠道类型，用 Go 实现，接口与 Phase 2 的插件保持一致：

| type | 上游协议 | baseUrl 约定 | 健康探测 / 模型发现 |
| --- | --- | --- | --- |
| `openai` | OpenAI Chat Completions；`supportsResponses=true` 时也接受 Responses | 含版本路径，如 `https://api.openai.com/v1`（拼接 `/chat/completions`） | `GET {baseUrl}/models` |
| `anthropic` | Anthropic Messages | 不含版本，如 `https://api.anthropic.com`（拼接 `/v1/messages`） | `GET {baseUrl}/v1/models` |

```ts
interface Channel {
  id: string
  name: string
  type: 'openai' | 'anthropic'
  baseUrl: string
  scope: 'private' | 'shared' | 'global'
  sharedWith: string[]            // scope=shared 时的用户 ID；仅所有者和管理员可见
  status: 'enabled' | 'disabled'
  priority: number                // 越大越优先
  weight: number                  // ≥1，同优先级内加权随机
  models: Array<{ model: string; upstreamModel: string }>   // model = 对外逻辑名
  config: {
    headers?: Record<string, string>     // 额外请求头（不得包含 Authorization / x-api-key）
    supportsResponses?: boolean          // 仅 openai
    timeoutSeconds?: number              // 等待响应头的超时，默认 60，最大 600
    maxTokensField?: 'max_tokens' | 'max_completion_tokens' // 转换到 openai 渠道时输出上限字段，默认 max_tokens
  }                                      // config 各字段为可选，未设置时响应中省略
  secret: { set: boolean; hint: string | null }  // hint = 末 4 位，如 "…a1b2"；永不返回明文
  owner: { id: string; displayName: string }
  health: { state: 'healthy' | 'degraded' | 'open'; consecutiveFailures: number; lastError: string | null; lastCheckedAt: string | null }
  version: number
  createdAt: string
  updatedAt: string
}
```

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/channels?q=&type=&scope=&status=` | `channels.read` | 自己拥有的 + 共享给自己的 + global；持有 `channels.manage` 时为全部。**非所有者且无管理权限**的条目只返回 `id,name,type,scope,status,models,health`（`baseUrl`、`config`、`secret`、`sharedWith` 省略） |
| POST | `/api/channels` | `channels.write` | body：`Channel` 的可写字段 + `apiKey`（必填）；`scope=global` 需要 `channels.manage` |
| GET | `/api/channels/{id}` | 同列表可见性 | 不可见返回 404 |
| PATCH | `/api/channels/{id}` | 所有者或 `channels.manage` | 任意可写字段 + 可选 `apiKey`（提供时替换）；必须带 `version` |
| DELETE | `/api/channels/{id}` | 所有者或 `channels.manage` | 已有请求日志保留渠道名快照 |
| POST | `/api/channels/{id}/test` | 所有者或 `channels.manage` | 低成本探测（模型列表接口），返回 `{ok, latencyMs, statusCode, error?}`，并更新健康状态 |
| POST | `/api/channels/{id}/discover-models` | 所有者或 `channels.manage` | 返回 `{models: string[]}`（上游模型 ID），**不自动写入**，由前端展示差异后用户确认再 PATCH |

错误码：`validation_failed`、`forbidden`、`not_found`、`version_conflict`、`base_url_not_allowed`（见 §6 SSRF）、`secret_unavailable`（409，主密钥变更导致无法解密）、`upstream_error`（502，测试/模型发现时上游不可达或返回错误，消息为简短原因，不含内部错误细节）。

## 2. 模型目录与价格

```ts
interface ModelEntry {
  model: string                     // 逻辑模型名
  channels: number                  // 当前用户可用的渠道数
  price: Price | null               // 当前生效的售价；null = 免费（不扣费）
}
interface Price {
  id: string
  kind: 'sell' | 'cost'
  model: string
  channelId: string | null          // cost 价格对应的渠道；sell 为 null
  inputPerM: string                 // 每 100 万输入 token
  outputPerM: string
  cacheReadPerM: string
  cacheWritePerM: string
  perRequest: string                // 每次请求固定费用
  effectiveAt: string
  createdAt: string
  createdBy: string | null
}
```

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/models` | 登录用户 | 当前用户可用的逻辑模型；`?scope=all` 需要 `models.manage`，返回所有已启用渠道上的模型（用于定价） |
| GET | `/api/admin/prices?model=&kind=&channelId=` | `models.manage` | 价格版本历史（分页，按 effectiveAt 倒序） |
| POST | `/api/admin/prices` | `models.manage` | 新增价格版本（不可修改、不可删除；改价 = 新版本）；`effectiveAt` 缺省为现在，不得早于现在 |

计价规则：成本按 **渠道 + 上游模型** 的 cost 价格；售价按 **逻辑模型** 的 sell 价格；都取 `effectiveAt <= 请求开始时间` 的最新版本，
请求日志记录所用价格 ID。没有价格 = 0。

## 3. Gateway Key

格式：`og-` + 43 位 base64url（256 bit 随机）。数据库只存 SHA-256 摘要和前 12 位前缀。

```ts
interface GatewayKey {
  id: string
  name: string
  prefix: string                    // "og-AbCdEfGh…"
  status: 'enabled' | 'disabled'
  policy: {
    allowedModels: string[]         // 空 = 不限
    allowedChannels: string[]       // 渠道 ID；空 = 不限
    ipAllowlist: string[]           // CIDR；空 = 不限
    rpm: number | null              // 每分钟请求数上限
    compatMode: 'strict' | 'lenient' // 跨协议无法转换的字段：strict 返回 400；lenient 丢弃并在响应头列出
  }
  expiresAt: string | null
  lastUsedAt: string | null
  createdAt: string
  version: number
}
```

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/keys` | `keys.own` | 自己的 Key |
| POST | `/api/keys` | `keys.own` | `{name, policy?, expiresAt?}` → `{key: GatewayKey, secret: "og-..."}`，**secret 只返回这一次** |
| PATCH | `/api/keys/{id}` | 所有者 | `name/status/policy/expiresAt` + `version` |
| DELETE | `/api/keys/{id}` | 所有者 | 立即吊销（物理保留记录，status=revoked，列表中不再返回） |
| POST | `/api/keys/{id}/rotate` | 所有者 | 生成新 Key（继承名称与策略），旧 Key 立即失效；返回同 POST |

**用户被停用后，其所有 Key 立即无法调用网关。**

## 4. 请求日志与统计

```ts
interface RequestLog {
  id: string
  requestId: string
  startedAt: string
  user: { id: string; displayName: string } | null
  keyId: string | null
  keyName: string | null
  inbound: 'openai.chat' | 'openai.responses' | 'anthropic.messages' | 'openai.models'
  model: string
  channelId: string | null
  channelName: string | null
  upstreamModel: string | null
  stream: boolean
  statusCode: number
  errorClass: string | null         // 见 protocol-adapter.md §5
  errorMessage: string | null       // 脱敏摘要
  attempts: number
  fallbackPath: Array<{ channelId: string; channelName: string; statusCode: number; errorClass: string | null; durationMs: number }>
  ttftMs: number | null             // 首字节（流式为首个事件）延迟
  durationMs: number
  usage: { input: number; output: number; cacheRead: number; cacheWrite: number; reasoning: number; estimated: boolean }
  cost: string | null               // 上游成本：按最终渠道的 cost 价格计算（失败的尝试没有可计量用量）；仅 stats.all 可见，否则为 null
  charge: string                    // 向用户收取
}
```

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/logs?from=&to=&model=&channelId=&keyId=&userId=&status=success\|error` | `stats.own` | 自己的请求；`stats.all` 可查全部并按 `userId` 过滤 |
| GET | `/api/stats/summary?from=&to=&userId=` | `stats.own` | 见下 |

```ts
interface StatsSummary {
  from: string; to: string
  totals: { requests: number; success: number; errors: number; successRate: number;
            inputTokens: number; outputTokens: number; cost: string | null; charge: string;
            latencyP50Ms: number | null; latencyP95Ms: number | null; latencyP99Ms: number | null; ttftP50Ms: number | null }
  daily: Array<{ date: string; requests: number; errors: number; inputTokens: number; outputTokens: number; charge: string }>
  byModel: Array<{ model: string; requests: number; errors: number; inputTokens: number; outputTokens: number; charge: string }>
  byChannel: Array<{ channelId: string | null; channelName: string | null; requests: number; errors: number; latencyP95Ms: number | null }>
}
```

`from/to` 缺省为最近 7 天；最大跨度 92 天。日期按 UTC 聚合（前端按用户时区显示）。

## 5. 钱包与兑换码（Phase 1 末，ADR-0006）

系统设置 `billing.enforce`（布尔，默认 `false`）：
- `false`：只记录费用，不拦截请求（个人自用场景）。
- `true`：调用前按预估金额**预留**余额，余额不足返回协议格式的 402/429（`insufficient_balance`）；结束后按实际金额结算。

```ts
interface Wallet { balance: string; reserved: string; available: string; currency: string; version: number }
interface LedgerEntry { id: string; kind: 'grant' | 'charge' | 'refund' | 'adjust'; amount: string; balanceAfter: string;
                        refType: 'request' | 'redeem' | 'admin'; refId: string; note: string | null; createdAt: string }
interface RedeemBatch { id: string; kind: 'wallet_credit'; amount: string; count: number; redeemed: number;
                        maxRedemptionsPerCode: number; perUserLimit: number; validFrom: string | null; expiresAt: string | null;
                        note: string | null; status: 'active' | 'disabled'; createdAt: string; createdBy: string }
```

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/billing/wallet` | `billing.own` | 不存在时视为余额 0 |
| GET | `/api/billing/ledger` | `billing.own` | 分页，倒序 |
| POST | `/api/billing/redeem` | `billing.own` | `{code}` → `{amount, wallet}`；限流：每用户每分钟 5 次 |
| GET | `/api/admin/billing/settings` | `billing.manage` | `{enforce: boolean, version}` |
| PUT | `/api/admin/billing/settings` | `billing.manage` | `{enforce, version}` |
| GET | `/api/admin/billing/redeem-batches` | `billing.manage` | 分页 |
| POST | `/api/admin/billing/redeem-batches` | `billing.manage` | `{amount, count(1..1000), maxRedemptionsPerCode?, perUserLimit?, validFrom?, expiresAt?, note?}` → `{batch, codes: string[]}`（**明文只返回这一次**） |
| PATCH | `/api/admin/billing/redeem-batches/{id}` | `billing.manage` | `{status}` |
| GET | `/api/admin/billing/wallets/{userId}` | `billing.manage` | 查看任一用户钱包 |
| POST | `/api/admin/billing/wallets/{userId}/adjust` | `billing.manage` | `{amount(可负), note(必填), version}` |

兑换码格式 `OG-XXXXX-XXXXX-XXXXX-XXXXX`（Crockford Base32，100 bit），输入时忽略大小写、空格与连字符。
错误码：`redeem_invalid`（不存在/格式错误）、`redeem_expired`、`redeem_not_started`、`redeem_used_up`、`redeem_user_limit`、
`redeem_batch_disabled`、`insufficient_balance`。

## 6. 网关（数据面）行为

| 端点 | 入口协议 | 认证 |
| --- | --- | --- |
| `POST /v1/chat/completions` | openai.chat | `Authorization: Bearer og-…` |
| `POST /v1/responses` | openai.responses | 同上 |
| `POST /v1/messages` | anthropic.messages | `x-api-key: og-…` 或 Bearer |
| `GET /v1/models` | 带 `anthropic-version` 头时返回 Anthropic 格式，否则 OpenAI 格式 | 同上 |
| `POST /v1/messages/count_tokens` | 有 Anthropic 渠道且上游支持时透传；否则返回估算值（约 4 字节/token）并带 `X-OmniGate-Estimated: true`；不计费 | 同上 |

- 路由：权限与可见性 → Key 策略 → 逻辑模型 → 渠道启用状态 → 熔断状态 → 按 `priority` 分组 → 组内按协议匹配度（直通 > 一步转换 > 两步转换，Round 4 起）→ 同档按 `weight` 加权随机；失败按顺序回退。
- 协议转换：`openai.chat ↔ anthropic.messages` 双向（含流式、工具调用、图片）；`openai.responses` **只能路由到** `supportsResponses=true` 的 openai 渠道（同协议直通），
  没有这样的渠道时返回 `model_not_found` 并说明原因。
- 同协议直通：只改写 `model` 字段，未知字段原样保留；流式逐事件转发。
- OpenAI 流式：网关总是向上游请求 `stream_options.include_usage=true` 以便计量；客户端没有请求时，网关会过滤掉那条只含 usage 的分片。
- 重试/回退：只在尚未向客户端写出任何字节时进行；可回退的情况：连接失败、响应头超时、上游 401/403（渠道凭据问题）、404（渠道模型映射问题，不计入熔断）、408、429、5xx、意外重定向、畸形响应或流在首个事件前中断。上游 400/413/422 等客户端错误不回退，直接返回。
- 熔断：同一渠道连续 3 次可回退的失败 → `open` 30 秒 → 放行 1 个探测请求（half-open）。
- **SSRF**：渠道 `baseUrl` 解析到环回、私有、链路本地、云元数据等地址时，连接会被拒绝（在 DNS 解析后的拨号阶段校验，防 DNS 重绑定）；
  只有当渠道所有者持有 `channels.manage` 且环境变量 `OMNIGATE_CHANNELS_ALLOW_PRIVATE_NETWORK=true` 时才允许（用于本地 Ollama 等）。
  上游请求不跟随重定向，也不读取进程的代理环境变量（需要代理时用 `OMNIGATE_UPSTREAM_PROXY`）。
- 响应头：`X-OmniGate-Request-Id`；跨协议丢弃字段时 `X-OmniGate-Compat-Warnings: field1,field2`。
- 默认不记录请求/响应正文。

## 7. 实现差异（Round 2 实测后补充）

| 项目 | 契约原文 | 实现 |
| --- | --- | --- |
| 私网渠道开关 | 系统设置 + 环境变量默认值 | 仅环境变量 `OMNIGATE_CHANNELS_ALLOW_PRIVATE_NETWORK`（系统设置页面在 Phase 3） |
| 渠道使用权 | — | 使用渠道需要所有者 / global / 显式共享；`channels.manage` **不授予**使用他人私有渠道的权限（只能管理） |
| Key 列表 | 分页 | `GET /api/keys` 返回 `{items}`，不分页（单用户 Key 数量有限） |
| Key 取消过期时间 | — | PATCH 增加 `clearExpiry: true`（JSON `null` 无法与“未提供”区分） |
| 钱包 | `Wallet` 字段 | 额外返回 `userId`；钱包 `version` 只在余额变化时递增（预留不影响管理员调整） |
| 余额调整 | 未定义响应 | 返回 `{wallet, entry}` |
| 兑换码批次 | — | 额外返回 `version`；PATCH 可选带 `version` |
| 兑换限流 | 每用户 | 每用户每分钟 5 次（进程内）；ADR 中提到的按 IP 限流未实现 |
| `billing.enforce` 无记录时 | — | GET 返回 `{enforce:false, version:0}`，PUT `version:0` 创建 |
| 余额不足 | 402/429 | 统一 402，OpenAI 类型 `insufficient_quota`，Anthropic 类型 `permission_error` |
| 价格未设置 | 0 | 不预留、不扣费；售价为 0 的请求即使在 `enforce=true` 下也不受余额限制 |
| 响应中的 `model` | — | 同协议直通时为上游返回的模型名；跨协议转换时为客户端请求的逻辑模型名 |
| 数据面错误体 | — | 不在 JSON 中携带 request id，统一通过响应头 `X-OmniGate-Request-Id` 提供 |
| `count_tokens` | — | 只尝试第一个可用的 Anthropic 渠道，失败即估算；消耗 RPM 配额，不写请求日志、不计费 |
| 代理模式下的 SSRF | 拨号时校验 | 配置 `OMNIGATE_UPSTREAM_PROXY` 后只能在请求前解析主机名校验（尽力而为，存在 DNS 重绑定窗口） |
