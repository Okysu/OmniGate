# 契约：Round 11 —— 额度重置卡、保留刷新时间的额度重置

> 状态：**已实现**。通用约定同前；所有 SQL 同时支持 PostgreSQL 与 SQLite（迁移 `00018_reset_cards`）。
> 最新要求：管理员重置额度和用户使用重置卡只清零用量，保持原刷新时间。

## 1. 保留刷新时间的重置

管理员批量重置与重置卡共用同一个函数：将当前窗口计数行的 `used` 更新为 `0`，保留原 `window_start`，不创建新锚点。

| 窗口 | 重置效果 |
| --- | --- |
| `session` | 清零用量，保持原 `windowStart` 与 `resetsAt`；尚未开启或已结束的会话不被重置操作启动 |
| `rolling` | 清零活动分桶的用量，保留桶时间与原逐步释放时间 |
| `calendar` / `period` | 清零当前窗口用量，窗口边界不变 |
| `lifetime` | 仅管理员显式选择 `includeLifetime: true` 时清零；重置卡不匹配总量规则 |

操作在锁定订阅行的事务内执行，与请求结算互斥。定期清理继续按窗口保留期处理零用量行；会话内的空行必须保留到原窗口结束。重置不会改变订阅到期时间，也不会将原来的刷新时间推迟。

用量提醒仍按原窗口去重，同一窗口不会因人工重置而变成新的提醒周期。

## 2. 重置卡

### 2.1 卡类型与匹配

| `kind` | 名称 | 重置的规则 |
| --- | --- | --- |
| `5h` | 5小时重置卡 | `session` / `rolling` 窗口、`duration` 恰为 5 小时的规则 |
| `weekly` | 周重置卡 | `session` / `rolling` 窗口、`duration` 恰为 7 天的规则 |
| `both` | 双重置卡 | 以上两类规则，同一事务、同一时刻 A 一起重置 |

按**窗口时长**匹配（`5h`、`300m` 都是 5 小时；`7d`、`168h` 都是 7 天），与规则 id 无关；`calendar` 的“每周”、`period` 窗口不匹配。
订阅至少有一条匹配的规则时卡才可用于该订阅。

### 2.2 管理端（`billing.manage`，挂在 `/api/admin`）

**发放**：`POST /api/admin/billing/reset-cards/batches`，支持 `?dryRun=true`（或 `1`）。

```ts
{
  kind: '5h' | 'weekly' | 'both'
  quantity: number               // 每人张数，1–100
  expiresAt: string | null       // RFC 3339；null / 省略 = 永不过期；必须晚于当前时间
  planIds: string[] | null       // 限定套餐：卡只能用于这些套餐的订阅；null / [] = 不限
  note: string                   // ≤200，显示在用户的卡片与通知中，写入审计
  target:
    | { type: 'users'; userIds: string[] }   // 1–1000 个
    | { type: 'group'; groupId: string }      // 用户组成员
    | { type: 'plan'; planId: string }        // 持有该套餐有效订阅（active 且未到期）的用户
    | { type: 'all' }                         // 全部用户
}
→ 201 { recipients: number; cards: number; batch: ResetCardBatch }
→ 200 { recipients: number; cards: number; batch: null }      // dryRun
```

- 收件人只包括**状态正常**、角色拥有 `billing.own` 的用户（审计员、停用用户不计入；`userIds` 中不存在的 id 被忽略）。卡在发放时按收件人逐张生成，
  “全部用户”即发放时刻已存在的用户，之后注册的用户不会收到。
- `cards = recipients × quantity`，单批最多 200 000 张（超出 → `422 details.quantity`）。
- dry run 做与正式发放相同的校验（`422` / `404`），只返回人数与张数，不写数据、不写审计、不发通知。正式发放时没有收件人 → `422 details.target`。
- 错误：`422 validation_failed`（`details` 键 `kind`、`quantity`、`expiresAt`、`planIds`（含不存在的套餐）、`note`、`target`）；`target.groupId` / `target.planId` 不存在 → `404 not_found`。
- 审计 `reset_card.issue`（`resourceType = reset_card_batch`，metadata 含 `kind`、`quantity`、`target`、`planIds`、`expiresAt`、`note`、`recipients`、`cards` 与前 500 个用户 id）。
- 通知：每位收件人收到 `reset_card.issued`（§3）。

**批次列表**：`GET /api/admin/billing/reset-cards/batches?page=&pageSize=` → `Paginated<ResetCardBatch>`，最新在前。

```ts
interface ResetCardBatch {
  id: string
  kind: '5h' | 'weekly' | 'both'
  quantity: number
  recipients: number
  target: { type: 'users'; userIds: string[] } | { type: 'group'; groupId: string; groupName: string }
        | { type: 'plan'; planId: string; planName: string } | { type: 'all' }
        | { type: 'filter'; groupIds?: string[]; planIds?: string[]; groupNames?: string[]; planNames?: string[] }  // 用户组与持有套餐取交集，空 = 不限；名称为发放时的快照
  plans: { id: string; name: string }[]     // 限定套餐（当前名称）；[] = 不限
  expiresAt: string | null
  note: string
  status: 'active' | 'revoked'
  counts: { issued: number; used: number; available: number; expired: number; revoked: number }
  createdBy: { id: string; displayName: string }
  createdAt: string
  revokedAt: string | null
}
```

**作废批次**：`POST /api/admin/billing/reset-cards/batches/{id}/revoke`（无请求体）→ 更新后的 `ResetCardBatch`。
该批次中所有未使用的卡（含已过期的）变为 `revoked`，已使用的卡不变；已作废的批次再次作废 → `409 card_batch_revoked`；批次不存在 → `404`。
与用户使用并发时，以先提交者为准。审计 `reset_card.revoke`（metadata 含被作废张数）。

**卡片列表**（用户管理）：`GET /api/admin/billing/reset-cards?userId=&batchId=&page=&pageSize=` → `Paginated<ResetCard>`，最新在前，两个过滤条件都可省略。

### 2.3 用户端（`billing.own`，挂在 `/api`）

```ts
interface ResetCard {
  id: string
  batchId: string
  user: { id: string; displayName: string }
  kind: '5h' | 'weekly' | 'both'
  status: 'available' | 'used' | 'expired' | 'revoked'
  expiresAt: string | null
  plans: { id: string; name: string }[]      // 限定套餐；[] = 不限
  note: string
  createdAt: string
  usedAt: string | null
  subscription: { id: string; planName: string } | null   // 使用在哪份订阅
  revokedAt: string | null
}
```

`expired` 是派生状态：存储为 `available` 且 `expiresAt` 已过（读取时计算，无后台任务）。

**我的卡**：`GET /api/billing/reset-cards` → `{ items: ResetCard[]; available: { '5h': n, weekly: n, both: n } }`。
`items` 中可用的卡在前（先过期的在前，永不过期的在后），其余按发放时间倒序，最多 500 张；`available` 统计全部可用的卡。

**预览**：`GET /api/billing/reset-cards/{id}/preview` →

```ts
{
  card: ResetCard
  now: string                       // 服务器时间
  subscriptions: Array<{            // 只含这张卡可用的有效订阅
    id: string
    plan: { id: string; name: string }
    endsAt: string
    rules: RuleUsage[]              // 会被重置的规则及当前 used / limit / resetsAt（与订阅视图相同）
    hasUsage: boolean               // 其中任一窗口有用量
  }>
}
```

卡不可用（已使用 / 过期 / 作废）时 `subscriptions` 为空；别人的卡或不存在 → `404`。

**使用**：`POST /api/billing/reset-cards/{id}/use`，`{ subscriptionId: string }` → `200 { card: ResetCard; subscription: SubscriptionView; rules: string[] }`
（`rules` 为被重置的规则 id，`subscription` 为重置后的视图）。在**一个事务**内：

1. 以条件更新认领卡：`status = 'available'`、属于调用者、未过期 → 置为 `used`、记录 `usedAt`。同一张卡并发使用只有一个成功，其余得到 `409 card_used`；
2. 锁定订阅并校验：属于调用者（否则 `404`）、有效且未到期（否则 `409 subscription_not_active`）、在限定套餐内（否则 `422 card_plan_not_allowed`）；
3. 找出匹配的规则；没有 → `422 card_not_applicable`；
4. 对这些规则执行 §1 的保留刷新时间重置；
5. 记录卡使用在哪份订阅，写审计 `reset_card.use`（metadata 含 `batchId`、`kind`、`subscriptionId`、`planName`、`rules`、`resetAt`）。

任何一步失败整个事务回滚，**卡不会被消耗**。卡的错误：不存在或属于别人 → `404 not_found`；`409 card_used` / `card_expired` / `card_revoked`。

## 3. 通知

新事件类型 `reset_card.issued`（钱包 / 套餐类，需要 `billing.own`；默认站内 + 邮件开启、Webhook 关闭；非告警，可合并进每日摘要；链接 `/console/billing`）。
标题如“你获得了 2 张5小时重置卡”，正文含可重置的额度、限定套餐、有效期与备注；`data` 为 `{batchId, kind, quantity, plans, note, expiresAt?}`。

投递在发放事务提交后异步进行：同一批次的内容对所有收件人相同，每 500 位收件人合并为一个事件（一次写入事务、邮件进入发件队列，由投递 worker 按每位用户的偏好与频率限制发送），
不会在发放请求中逐个同步发送。

## 4. 存储

迁移 `00018_reset_cards`（PostgreSQL 与 SQLite，都有 Down）：

- `reset_card_batches`：`kind`、`quantity`（1–100）、`recipients`、`target`（jsonb / JSON TEXT，含名称快照）、`plan_ids`（jsonb，NULL = 不限）、`expires_at`、`note`、
  `status`（`active` / `revoked`）、`created_by`、`created_at`、`revoked_at`、`revoked_by`。
- `reset_cards`：每张卡一行，`batch_id`、`user_id`、`kind` 与 `expires_at`（从批次复制，认领只需一条针对本行的条件 UPDATE）、`status`（`available` / `used` / `revoked`）、
  `used_at`、`subscription_id`（`ON DELETE SET NULL`）、`revoked_at`、`created_at`；索引 `(user_id, status)`、`(batch_id, status)`。

时间一律 UTC。

## 5. 控制台

- **管理端「重置卡」页**（计费 → 重置卡，`billing.manage`）：批次列表（卡类型、发放对象、人数 × 张数、已用 / 可用 / 过期 / 作废、有效期、备注、发放时间与发放人）、
  作废操作（确认对话框）；「发放重置卡」对话框：卡类型、每人数量、过期时间、发放对象（指定用户多选 / 用户组 / 套餐有效订阅用户 / 全部用户）、限定套餐、备注，
  填写过程中自动 dry run，显示“将发放给 N 位用户（共 M 张）”，人数为 0 时不能提交。
- **用户管理详情**：显示该用户的重置卡（可用张数与最近 50 张的状态）。
- **钱包与订阅页「我的重置卡」**（有卡时显示）：按卡类型分组，每组按批次列出张数、剩余有效期、限定套餐与备注；「使用」打开对话框：选择可用的订阅
  （只列出可用的），逐条显示“使用前 → 使用后”，如“5 小时：$18.200 / $30.000，14:35 刷新 → $0.000 / $30.000，下次刷新 19:35（5 小时后）”；
  所选窗口都没有用量时提示（不阻止）；成功后刷新订阅与卡片并提示。已使用 / 过期 / 作废的卡折叠在下方。
- 管理端「重置套餐额度」对话框的说明改为“用量清零，保持原刷新时间”。

## 6. 种子目录

内置目录（`omnigate seed builtin`）六个套餐的 `5h`、`weekly` 规则由 `rolling` 改为 `session`（时长不变：5h、7d），与线上套餐一致，重置卡只清零用量并保留其刷新时间。
已部署的实例需要手动执行 `omnigate seed builtin` 才会更新套餐（已有订阅保留各自的规则快照）。

## 7. 实现差异

实现位置：`server/internal/subscription/bulk.go`（`resetRules`）、`card.go`、`card_http.go`，`server/internal/notify/producers.go`（`ResetCardsIssued`）；前端见
`web/src/lib/resetCards.ts`、`web/src/views/billing/reset-cards/`。

| # | 条款 | 说明 | 原因 |
| --- | --- | --- | --- |
| 1 | §2.2 收件人 | 只发给状态正常且拥有 `billing.own` 的用户；`userIds` 中停用、审计员或不存在的用户静默忽略，dry run 的人数已排除他们 | 他们无法使用重置卡 |
| 2 | §2.3 校验顺序 | 先认领卡（得到 `card_*` 错误），再校验订阅与匹配规则；失败时整体回滚 | 并发使用同一张卡时只需一条条件更新决定胜负 |
| 3 | §2.3 未开始的会话 | 对没有进行中会话的 session 规则使用卡同样写入锚点：会话从使用时开始，`resetsAt = A + duration`（界面在“窗口没有用量”时提示） | 与“A + duration 刷新”的语义一致 |
| 4 | §2.2 作废 | 作废时已过期但未使用的卡也变为 `revoked`；`counts.expired` 只统计未作废的过期卡 | 状态互斥，计数之和等于 `issued` |
| 5 | §1 管理员重置 | 管理员重置不再“结束会话”，而是同样锚定在重置时刻（phase7 §3.1 已同步） | 一套重置语义 |
