# 契约：Round 5 —— 路由规则、系统设置、超额偏好

> 状态：**定稿，按此实现**。通用约定同前（camelCase、错误信封、CSRF 头、分页、`version` 乐观锁、金额为十进制字符串）。

## 1. 超额偏好（已实现）

套餐额度用完后怎么处理，由**用户**决定，不再由套餐规则的 `onExceed` 决定（该字段已删除；迁移 00007 从已有规则中移除）。

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/billing/preferences` | `billing.own` | `{quotaOverflow: "block" \| "wallet"}`，默认 `block` |
| PUT | `/api/billing/preferences` | `billing.own` | 同上；变化时写审计 `billing.preferences` |

- API Key 策略新增 `quotaOverflow: "" | "block" | "wallet"`（空 = 跟随账户设置）。
- 网关：所有覆盖该模型的订阅都不可用时，按 Key 的设置、否则按账户设置：`wallet` 时改用钱包（受 `billing.enforce` 约束），
  `block` 时返回 429（消息末尾提示可以在“钱包与订阅”中修改设置）。

## 2. 路由规则

路由规则让管理员**改变某些模型的渠道选择方式**；没有规则命中时，行为与现在相同：优先级 → 协议匹配度 → 权重。

```ts
interface RouteRule {
  id: string
  name: string                       // 1–100
  description: string
  enabled: boolean
  position: number                   // 规则顺序，从 0 开始；按顺序匹配，第一个命中的规则生效
  match: {
    models: string[]                 // 1–100 个；精确的逻辑模型名，或带 * 的通配（"gpt-*"、"*"）
    roles: Role[]                    // 空 = 所有角色
  }
  // 限定并覆盖候选渠道。空 = 该模型的全部可用渠道（沿用渠道自身的优先级与权重）。
  // 规则永远不会扩大访问范围：最终候选 = 用户本来可用的渠道 ∩ targets。
  targets: Array<{ channelId: string; priority: number | null; weight: number | null }>  // ≤50；null = 沿用渠道设置
  strategy: 'priority'               // 默认：优先级分组，组内按权重随机
          | 'weighted'               // 忽略优先级，全部按权重随机
          | 'round_robin'            // 按顺序轮询（进程内计数）
          | 'least_latency'          // 按近期首字节延迟（进程内 EWMA）从低到高，没有数据的排最后
          | 'lowest_cost'            // 按该渠道该模型的成本价（输入 + 输出单价之和）从低到高，没有成本价的排最后
  protocolPreference: 'native_first' | 'ignore'   // native_first（默认）：同一档内优先选择不需要协议转换的渠道
  retry: {
    maxAttempts: number              // 1–5，含首次
    // 可以换渠道重试的失败类型（只按上游 HTTP 状态码判断，不看错误信息）：
    // rate_limit=429，server_error=5xx/响应异常，timeout=408/超时，network=网络错误，
    // auth_error=401/403，not_found=404，client_error=其他 4xx（400、402、409、413、422…）
    // 默认：除 client_error 外全部
    retryOn: Array<'rate_limit' | 'server_error' | 'timeout' | 'network' | 'auth_error' | 'not_found' | 'client_error'>
  }
  fallbackModels: string[]           // ≤5；本模型所有渠道都失败后，依次改用这些逻辑模型（同样经过 Key 策略与路由）
  version: number
  createdAt: string
  updatedAt: string
}
```

语义：
- 匹配：规则启用、模型匹配（精确或通配），并且 `roles` 为空或包含用户角色。
- **熔断**仍然生效：熔断中的渠道跳过（与现在相同）。
- **回退模型**：请求体中的 `model` 改写为回退模型；按**实际提供服务的模型**计价、计入套餐配额。
  请求日志的 `model` 记录请求的模型，新增 `servedModel` 记录实际模型（未回退时相同）。回退只在流式响应开始之前发生。
- 默认重试（没有规则命中）：`maxAttempts` 取系统设置 `gateway.maxAttempts`，`retryOn` 取系统设置 `gateway.retryOn`（默认除 `client_error` 外全部）。
- 预算：一次请求总共最多尝试 `maxAttempts × (1 + fallbackModels 数)` 次，硬上限 10。

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/admin/routes` | `routes.manage` | 全部规则（按 `position`），不分页 |
| POST | `/api/admin/routes` | `routes.manage` | 创建，追加到末尾 |
| PATCH | `/api/admin/routes/{id}` | `routes.manage` | 修改（带 `version`） |
| DELETE | `/api/admin/routes/{id}` | `routes.manage` | 删除，204 |
| PUT | `/api/admin/routes/order` | `routes.manage` | `{ids: string[]}`，必须恰好包含全部规则 |
| POST | `/api/admin/routes/preview` | `routes.manage` | 命中预览，见下 |

命中预览请求 `{model, userId?, inbound: 'openai.chat' | 'openai.responses' | 'anthropic.messages' | 'openai.embeddings'}`
（`userId` 缺省为当前用户），响应：

```ts
{
  rule: { id: string; name: string } | null
  strategy: string
  maxAttempts: number
  candidates: Array<{
    channelId: string; channelName: string; channelType: string
    priority: number; weight: number
    upstreamDialect: string          // 实际使用的上游协议
    conversionHops: 0 | 1 | 2
    breaker: 'closed' | 'open' | 'half_open'
    latencyMs: number | null         // least_latency 的依据
    costPerM: string | null          // lowest_cost 的依据（输入 + 输出单价）
    skipped: string | null           // 跳过原因（熔断、协议不支持……），否则 null
  }>                                 // 按尝试顺序排列；随机策略只是示例顺序
  fallbackModels: string[]
}
```

错误码：`route_target_invalid`（422，渠道不存在或不提供该模型）。审计：`route.create`、`route.update`、`route.delete`、`route.reorder`。

## 3. 系统设置

可以在运行时修改的全局设置，保存在 `system_settings` 表。读取顺序：数据库中的值 → 环境变量 → 默认值。
与安全边界相关的设置（主密钥、数据库、登录提供方、可信代理、渠道是否允许访问私网）**只能通过环境变量配置**，设置页只读显示。

```ts
interface SystemSettings {
  site: {
    name: string                     // 默认 "OmniGate"；1–50
    announcement: string             // 控制台顶部公告；≤500，空 = 不显示
    landingEnabled: boolean          // false：访问 "/" 直接进入控制台（未登录则去登录）
    docsUrl: string                  // 文档链接；空或 http(s) URL
  }
  auth: {
    registrationMode: 'open' | 'restricted' | 'closed'  // 新用户首次登录时是否自动创建；默认取环境变量
    allowedEmailDomains: string[]    // restricted 模式下允许的邮箱域名
  }
  billing: {
    enforce: boolean                 // 原 billing.enforce
    signupCredit: string             // 新用户首次登录时赠送的余额，默认 "0"
  }
  gateway: {
    maxAttempts: number              // 1–5，默认 3
    retryOn: string[]                // 未命中路由规则时的重试条件，取值同路由规则 retry.retryOn；默认除 client_error 外全部
    logRetentionDays: number         // 请求日志保留天数，0 = 永久；默认 90
  }
}
```

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/admin/settings` | `settings.write` | `{settings, sources, readonly, version}`。`sources` 为每个字段（如 `"site.name"`）的来源 `db \| env \| default`；`readonly` 为只读的环境变量配置（`currency`、`publicUrl`、`channelsAllowPrivateNetwork`、`loginProviders`、`env`） |
| PATCH | `/api/admin/settings` | `settings.write` | 部分更新：`{version, settings: Partial<SystemSettings>}`（按分组合并）；把字段设为 `null` 表示恢复为环境变量 / 默认值 |
| GET | `/api/system/info` | 公开 | 新增 `siteName`、`announcement`、`landingEnabled`、`docsUrl`（已有 `name`、`version`、`currency`、`registrationMode`） |

- `version` 为全部设置的整体版本号（乐观锁）。审计 `settings.update`，记录变更前后的值。
- 修改在 5 秒内对所有实例生效（读取时缓存 5 秒；本实例修改后立即刷新）。
- 原 `GET/PUT /api/admin/billing/settings` 保留，作为 `billing.enforce` 的别名。
- 预付费预留（`billing.enforce = true`）：由钱包支付的请求（平台渠道、有售价、无套餐覆盖）可用余额（余额 − 预留）> 0 即放行，否则
  `402 insufficient_balance`——**与估算额无关**，估算为 0 的请求同样要求可用余额 > 0（2026-10-09 安全修复）；估算时输出上限最多按
  1,000,000 token 计，无法计价（金额溢出）的请求返回 `400 invalid_request`；预留额 = 按售价估算的
  （输入 token 估算 + 请求中的 `max_tokens`，未提供时不计输出；embeddings 同样不计输出）费用，**不超过可用余额**，估算超过余额不会拒绝。
  结算按实际用量（余额最多因进行中的请求变为负数）。原计划的 `gateway.defaultOutputTokens` 设置已取消。

## 4. 实现差异

以下为实现时对契约中未写明或无法照做之处的取舍（代码为准）。

| # | 主题 | 实现 |
| --- | --- | --- |
| 1 | 规则列表响应 | `GET /api/admin/routes` 与 `PUT /api/admin/routes/order` 返回 `{items: RouteRule[]}`（与 `GET /api/keys` 相同的不分页形式）。 |
| 2 | 字段范围 | `description` ≤ 500 字符；`targets[].weight` 1–1000（与渠道权重相同），`targets[].priority` ±1 000 000；`match.models` 每项 1–128 字符，去首尾空格并去重；`fallbackModels` 不能含 `*`；`retry` 省略的子字段取默认值 / 保持不变（默认 `{maxAttempts: 3, retryOn: 全部四类}`）。创建时不能带 `version`。 |
| 3 | `route_target_invalid` | 渠道存在（不论启用与否）且 `channel_models` 中至少有一个模型被规则的 `match.models` 匹配即可；`details` 的键为 `targets[i].channelId`。 |
| 4 | 通配 | 只有 `*` 是通配符（匹配任意长度字符，含空串），其他字符（含 `/`、`?`、`[`）按字面匹配，区分大小写。 |
| 5 | 策略与 `native_first` | 每种策略先分“档”，`native_first` 在档内按协议转换步数排序，档内其余并列再按权重随机（`round_robin` 按轮转位置）：`priority` 档 = 相同优先级；`weighted`、`round_robin` 全部为一档；`least_latency` / `lowest_cost` 档 = 相同延迟 / 成本，无数据者排最后。 |
| 6 | `round_robin` | 轮询基准顺序为 `targets` 的顺序（`targets` 为空时为渠道列表顺序），每个请求把该规则的进程内计数加 1；预览只读取计数、不推进。 |
| 7 | `least_latency` | 延迟为每次尝试从发出请求到收到上游响应头的时间，只统计最终成功的尝试；按（渠道, 逻辑模型）维护 α = 0.3 的 EWMA，进程内、所有请求（有无规则）都会更新。 |
| 8 | `lowest_cost` | 成本价与结算相同：`kind = cost`、该渠道、**上游模型名**（`upstreamModel`）在请求时刻生效的版本，取 `inputPerM + outputPerM`。 |
| 9 | 重试类别 | `rate_limit` = 上游 429；`timeout` = 等待响应头超时、上游 408；`network` = 连接失败 / 被网络策略拒绝、读取非流式响应失败、流式在首个事件前读取中断；`server_error` = 上游 5xx、401/403、404、3xx、畸形或不完整响应、插件 Hook 失败。其他失败（上游其他 4xx、协议转换错误、客户端断开、响应过大）永不重试。失败类型不在 `retryOn` 中时**立即返回**该错误：不换渠道，也不使用回退模型。 |
| 10 | 重试与回退的规则来源 | 整个请求（含回退模型）使用请求模型所命中规则的 `retry`：每个模型最多 `maxAttempts` 次，总共 `min(maxAttempts × (1 + 回退数), 10)` 次。回退模型重新匹配规则，但只采用该规则的 `targets`、`strategy`、`protocolPreference`；回退模型自身规则的 `fallbackModels` 不再展开（不链式回退）。与请求模型相同的回退模型被跳过。 |
| 11 | 何时回退 | 请求模型的候选渠道全部失败（可重试的失败）或全部熔断，或请求模型没有任何候选渠道时，依次尝试回退模型。请求模型本身的 Key 策略拒绝（403）、配额阻断（429）、余额不足（402）直接返回，不回退。回退模型不被 Key 允许、没有候选渠道、被配额或余额阻断时静默跳过。全部失败时返回最后一个错误（没有发生任何尝试时为请求模型的 `404` 或“全部熔断”错误）。 |
| 12 | 回退时的请求体与计费 | 请求体的 `model` 改写为回退模型（JSON 重新序列化，字段顺序可能变化）；转换后的响应中的模型名为回退模型。每个回退模型重新做配额判定与钱包预留（同一请求 ID 的预留不会重复增加）；若请求模型走钱包、回退模型由订阅覆盖，结束时释放先前的预留。计价、配额入账按实际提供服务的模型。 |
| 13 | `servedModel` | 新列 `request_logs.served_model`（可空），为最后一次实际尝试的逻辑模型；接口返回 `COALESCE(served_model, model)`，因此旧数据与没有尝试的请求等于 `model`。`?model=` 仍按请求的模型过滤。 |
| 14 | 顺序与版本 | `position` 无唯一约束；删除后其后的规则依次前移；`PUT /routes/order` 不改变规则 `version`（只更新位置变化的规则的 `updatedAt`）。规则快照每 5 秒从数据库重新加载（多实例），本实例修改后立即重新加载。 |
| 15 | 角色 | 网关按 Key 认证缓存中的用户角色匹配 `roles`（缓存 10 秒，管理员修改用户时立即清空）。 |
| 16 | 预览 | 不考虑 API Key 策略（没有 Key）。`candidates` 依次为：可用渠道（尝试顺序；熔断 `open` 的标 `skipped`，`half_open` 不跳过），不支持该入口协议的渠道（标 `skipped`），规则 `targets` 中该用户无权使用但提供该模型的已启用渠道（标 `skipped`）。停用的渠道不出现。`userId` 不存在 → `404 not_found`。没有规则时 `strategy` 为 `priority`，`maxAttempts` 为 `gateway.maxAttempts`。 |
| 17 | 设置的存储 | 存为 `system_settings` 中 key = `system` 的一个 JSON 文档（只保存数据库层的值，扁平键如 `"site.name"`），该行的 `version` 即整体版本号。首次启动时创建（版本 1），并把旧的 `billing.enforce` 行的值带入；旧行保留但应用不再读取。数据库中不合法的值被忽略（记录警告）并回落到环境变量 / 默认值。 |
| 18 | 环境变量层 | 只有 `auth.registrationMode`（`OMNIGATE_REGISTRATION_MODE`）与 `auth.allowedEmailDomains`（`OMNIGATE_AUTH_ALLOWED_EMAIL_DOMAINS`）有环境变量；没有数据库值时这两项的来源总是 `env`（未设置环境变量时为配置默认值 `restricted` / 空）。其他字段为 `db \| default`。 |
| 19 | 设置校验 | 字符串去首尾空格；`docsUrl` ≤ 500；`allowedEmailDomains` ≤ 100 项，转小写、去重，必须形如域名（含 `.`，不含 `@`）；`signupCredit` 为非负金额，规范化保存（`"5.50"` → `"5.5"`）；`logRetentionDays` 0–3650。未知分组或字段 → `422`，`details` 的键为 `"<group>.<field>"`。`settings` 为空时不修改、版本不变。 |
| 20 | 设置审计 | `settings.update` 的 `metadata.before` / `metadata.after` 只包含本次请求中出现的字段（修改前后的**生效值**）。 |
| 21 | `GET /api/admin/settings` 的 `readonly` | `currency` 为 `{code, symbol, decimals}` 对象；`loginProviders` 为 `[{id, type, displayName}]`。读取同样需要 `settings.write`（与契约一致）。 |
| 22 | 计费设置别名 | `GET/PUT /api/admin/billing/settings` 仍需 `billing.manage`；`version` 为系统设置的整体版本号；`PUT` 写审计 `settings.update`（不再是 `billing.settings_update`）。 |
| 23 | 注册赠送 | 新账号创建后（在建号事务之外）写一条 `grant` 账本记录，`refType = signup`、`refId` = 用户 ID，部分唯一索引保证每个用户最多一次；审计 `billing.signup_credit`（操作者为新用户）。与 `billing.enforce` 无关。发放失败只记录日志，不影响登录。 |
| 24 | 注册模式 | `restricted` 模式下 `OMNIGATE_AUTH_ALLOWED_IDENTITIES` 仍然有效（只读环境变量）；邮箱域名比较不区分大小写；引导管理员总是可以登录。`/api/system/info` 的 `registrationMode` 为生效值。 |
| 25 | 日志保留 | 启动时执行一次，之后每 24 小时执行：截止时间 = 现在 − 天数（在 Go 中计算）；删除结束时间不晚于截止时间的整月分区（`request_logs_yYYYYmMM`），再删除剩余分区（含 DEFAULT 分区）中早于截止时间的行。 |
| 26 | `gateway.defaultOutputTokens` | 按用户决定取消，改为上文的预付费预留规则（`Options.DefaultOutput` 一并删除）。 |
