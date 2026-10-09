# 契约：Round 4 —— 套餐与周期配额、嵌入接口、插件删除

> 状态：**定稿，按此实现**。概念与决策见 ADR-0006 与 [billing-and-quota.md](billing-and-quota.md)；本文件固定 Round 4 的实际范围与接口。
> 通用约定同前（camelCase、错误信封、CSRF 头、分页、`version` 乐观锁、金额为十进制字符串）。

## 0. 范围

| 本轮实现 | 推迟 |
| --- | --- |
| 套餐（Plan）CRUD、订阅（Subscription）发放/续期/取消、规则快照 | 计费插件 `computeUnits`（`custom:*` 计量） |
| 计量：`requests`、`tokens.input`、`tokens.output`、`tokens.total`、`charge`；按模型加权（`modelWeights`） | `user` / `key` 范围的配额（本轮只有 `subscription` 范围） |
| 窗口：`calendar`（日/周/月 + 时区）、`rolling`（5 分钟分桶）、`session`（首次使用起算）、`period`（与订阅起点对齐）、`lifetime` | Redis 计数器（本轮计数在 PostgreSQL，单实例与多实例语义一致，因为每次检查都读库） |
| 网关：请求前按订阅检查配额（429 + `retry-after`），结束后入账；超额 `block` 或 `overflow_to_wallet` | 在线支付 |
| 兑换码 `plan` 类型：开通或续期套餐 | |
| `POST /v1/embeddings`（OpenAI 兼容，直通） | 图像、音频接口 |
| 删除插件（上传/编辑器来源、且没有渠道使用） | 版本下架（yank） |

## 1. 套餐

```ts
interface QuotaRule {
  id: string                         // 套餐内唯一：^[a-z0-9_-]{1,32}$，如 "5h"、"weekly"
  label?: string                     // 展示名，如 "5 小时窗口"
  meter: 'requests' | 'tokens.input' | 'tokens.output' | 'tokens.total' | 'charge'
  window:
    | { kind: 'calendar'; unit: 'day' | 'week' | 'month'; timezone?: string }   // 默认 UTC；周从周一开始
    | { kind: 'rolling'; duration: string }    // 5m–31d，按 5 分钟分桶
    | { kind: 'session'; duration: string }    // 5m–31d，窗口过期后的首次请求开启新窗口
    | { kind: 'period'; every: string }        // 1h–366d，从订阅开始时间对齐
    | { kind: 'lifetime' }
  limit: string                      // 十进制；requests / tokens.* 必须为正整数；charge 为金额
  models?: string[]                  // 仅对这些逻辑模型计量；空 = 套餐覆盖的全部模型
  modelWeights?: Record<string, string>   // 计量倍率（十进制，0–1000），例 {"gpt-5.6-sol": "5"}；未列出的为 1
  onExceed: 'block' | 'overflow_to_wallet'
}

interface Plan {
  id: string
  name: string
  description: string
  listPrice: string | null           // 展示用标价（结算币种）
  duration: string                   // 每份（每个周期）的有效期，如 "30d"、"7d"；1h–366d
  models: string[]                   // 覆盖的逻辑模型；空 = 全部模型
  rules: QuotaRule[]                 // 1–10 条
  stackable: boolean                 // true：重复开通生成新订阅；false：延长现有订阅
  status: 'active' | 'archived'      // archived 不能再开通，不影响已有订阅
  subscribers: number                // 当前有效订阅数
  version: number
  createdAt: string
  updatedAt: string
}
```

时长格式：`<数字><单位>`，单位 `m`（分钟）、`h`、`d`。

## 2. 订阅

```ts
interface Subscription {
  id: string
  user: { id: string; displayName: string }
  plan: { id: string; name: string }   // 名称取自快照
  status: 'active' | 'expired' | 'cancelled'
  startsAt: string
  endsAt: string
  source: 'admin' | 'redeem'
  models: string[]                     // 快照
  rules: Array<QuotaRule & {           // 快照 + 当前用量
    used: string                       // 当前窗口已用（加权后）
    windowStart: string | null         // null：session 窗口尚未开始
    resetsAt: string | null            // null：lifetime
    remaining: string
    exceeded: boolean
  }>
  createdAt: string
}
```

- 开通时**快照**套餐的 `models` 与 `rules`；之后修改套餐不影响已有订阅。
- 续期（非 stackable 套餐、且用户已有该套餐的有效订阅）：`endsAt += duration × periods`，规则快照不变。
- 状态由时间推导：`now >= endsAt` 即 expired；取消立即生效。

## 3. 网关语义

请求前（在钱包预留之前）：
1. 找出用户所有有效订阅中**覆盖该逻辑模型**的订阅（`models` 为空或包含该模型）。
2. 对每份订阅检查其适用规则（规则 `models` 为空或包含该模型）：当前窗口 `used < limit` 才算可用。
3. 有可用订阅 → 选 `endsAt` 最早的一份，本次请求**不经过钱包**（不预留、不扣费）。
4. 都不可用：
   - 若每份订阅里被超出的规则都是 `overflow_to_wallet` → 走钱包（与 Phase 1 相同，受 `billing.enforce` 约束）；
   - 否则返回 **429** `quota_exceeded`，响应头 `retry-after`（秒）取所有阻断规则中最早的重置时间；`lifetime` 用尽时为 `quota_exhausted`（无 retry-after）。
5. 没有任何覆盖该模型的订阅 → 走钱包。

请求结束后（成功或客户端中途断开但已产生用量）：按规则计量 `units = meter 值 × modelWeights[model]`，在同一事务中累加到各窗口；
失败请求不计入（与“失败不收费”一致）。配额为软上限：并发请求可能使用量略超上限，之后的请求会被阻断。

计量值：`requests`=1；`tokens.input`=输入（含缓存读写）；`tokens.output`=输出；`tokens.total`=两者之和；`charge`=按售价计算的金额（即使不扣钱包也照常计算，用于“按额度”套餐）。

请求日志新增 `subscriptionId`；订阅覆盖的请求 `charge` 为 0（不扣钱包），`quotaCharge` 记录按售价折算的值。

## 4. 兑换码

批次新增 `kind: "plan"`，payload `{planId, periods}`（1–120）。兑换时套餐必须为 `active`；按 §2 开通或续期，返回 `{kind: "plan", subscription}`。
钱包类兑换码的响应保持不变（`kind: "wallet_credit"`）。

## 5. 接口

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/plans` | 登录用户 | 所有 `active` 套餐（用户可见的套餐目录）。只返回 `id, name, description, listPrice, duration, models, rules, stackable`；订阅人数、状态、版本等管理信息只在管理接口中返回 |
| GET | `/api/admin/billing/plans` | `billing.manage` | 全部套餐（含 archived），`?status=` |
| POST | `/api/admin/billing/plans` | `billing.manage` | 创建 |
| PATCH | `/api/admin/billing/plans/{id}` | `billing.manage` | 修改（带 `version`）；不影响已有订阅 |
| GET | `/api/billing/subscriptions` | `billing.own` | 我的订阅（含已过期，最近 50 条），带用量 |
| GET | `/api/admin/billing/subscriptions` | `billing.manage` | 分页，`?userId=&planId=&status=` |
| POST | `/api/admin/billing/subscriptions` | `billing.manage` | `{userId, planId, periods}` 开通或续期（规则同兑换） |
| POST | `/api/admin/billing/subscriptions/{id}/cancel` | `billing.manage` | `{note}` 立即取消 |
| POST | `/api/admin/billing/redeem-batches` | `billing.manage` | 新增 `kind:"plan"`、`planId`、`periods` |
| DELETE | `/api/plugins/{id}` | `plugins.manage` | 删除插件（仅 upload/editor 来源，且没有渠道引用任何版本）；级联删除版本、草稿与插件存储 |
| POST | `/v1/embeddings` | Gateway Key | OpenAI 兼容嵌入接口，只路由到 openai 类型渠道（直通），计量输入 token |

错误码：`quota_exceeded`（429，网关）、`quota_exhausted`（429，网关）、`plan_archived`、`plan_in_use`（预留）、`plugin_in_use`、`subscription_not_active`。
审计：`plan.create`、`plan.update`、`subscription.grant`、`subscription.renew`、`subscription.cancel`、`plugin.delete`；兑换沿用 `billing.redeem`。

## 6. 实现差异（Round 4）

| 项目 | 契约原文 | 实现 |
| --- | --- | --- |
| `retry-after` | 阻断规则中最早的重置时间 | 一份订阅只有在**它所有超额规则都重置后**才会重新放行；`retry-after` 取各订阅中最早可放行的时间。重置时间晚于订阅到期时间的视为不可恢复；所有订阅都不可恢复时返回 `quota_exhausted`（不带 retry-after）。这样不会出现“提示可以重试、重试后又被阻断” |
| rolling 窗口展示 | — | `windowStart = now − duration`；`resetsAt` 为最早计入的分桶过期时间（无用量时为 null）；阻断时的 retry-after 是用量回落到上限以下的时刻 |
| 计量为 0 的请求 | — | 不记录，也不会开启 session 窗口（例如权重为 0 的模型） |
| 阻断消息 | — | 中文，包含套餐名、规则名与重置时间（按规则的时区，默认 UTC） |
| 开通接口状态码 | — | 新开通 201，续期 200，响应体都是订阅 |
| 兑换钱包码的响应 | 不变 | 额外带 `kind: "wallet_credit"` |
| 用量记录 | — | `Record` 按 request_id 幂等（`subscription_charges` 表），并锁定订阅行，避免并发请求开出两个重叠的 session |
| 嵌入接口 | — | 请求中的 `stream` 字段在转发前被去掉；预留余额时不计输出 token |
| 历史用量清理 | — | 每小时清理：订阅结束或取消满 30 天的全部计数；32 天前的 rolling/session 计数；400 天前的 calendar/period 计数；90 天前的幂等记录（对账以请求日志为准）。正在生效的计数不受影响 |
