# 契约：Round 6 —— 用户组、价格倍率、限额、分时价格、数据迁移工具

> 状态：**定稿，按此实现**。通用约定同前；所有 SQL 同时支持 PostgreSQL 与 SQLite（ADR-0009）。

## 1. 用户组

```ts
interface UserGroup {
  id: string
  name: string                 // 1–50，唯一
  description: string          // ≤200
  priceMultiplier: string      // 售价倍率，十进制 0–100，默认 "1"（"0" = 平台渠道免费；"0.8" = 八折）
  limits: {
    rpm: number | null         // 每个用户每分钟最多请求数（所有 Key 合计）
    rpd: number | null         // 每个用户每天最多请求数（按组的 timezone 计日）
    dailySpend: string | null  // 每个用户每天最多消费（平台渠道计费金额，结算币种）
    monthlySpend: string | null
  }
  timezone: string             // IANA，默认 "Asia/Shanghai"，用于日 / 月边界
  isDefault: boolean           // 新用户加入的组；有且只有一个
  members: number              // 只读
  version: number
  createdAt: string
  updatedAt: string
}
```

- 每个用户属于**恰好一个**组（`users.group_id`）。迁移时创建名为“默认”的组（`isDefault: true`、倍率 1、不限额）并把所有现有用户放入。
- 默认组不能删除；删除其他组时，其成员移到默认组。
- 系统设置 `auth.defaultGroup` 不新增，以组的 `isDefault` 为准。

### 1.1 价格倍率

- 平台渠道请求的售价 × 用户所在组的 `priceMultiplier`（在分时倍率之后再乘）。自有 / 共享渠道仍然免费。
- 套餐按售价折算的计量（`charge` meter、`quotaCharge`）同样使用乘过倍率的金额。
- 请求日志新增 `priceMultiplier`（本次生效的组倍率 × 分时倍率，十进制字符串）。
- 模型广场：`/api/plaza/models` 显示原价；`/api/plaza/mine` 的 `price` 为**乘过组倍率**的价格，另加 `basePrice`（原价）与 `priceMultiplier`。

### 1.2 按组共享渠道

渠道共享对象从“指定用户”扩展为“指定用户或用户组”：渠道 `sharedWith` 变为

```ts
sharedWith: { users: string[]; groups: string[] }    // 兼容：输入为 string[] 时视为 users
```

被共享的组的所有成员都可以使用该渠道（档位判定不变：取决于渠道所有者的角色）。

> 安全修订（phase5-api.md §5）：`groups` 只有持有 `channels.manage` 的调用者可以添加；`users` 是邀请，被共享者接受后才生效。

### 1.3 接口

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/admin/groups` | `users.read` | 全部用户组，`{items}` |
| POST | `/api/admin/groups` | `users.write` | 创建 |
| PATCH | `/api/admin/groups/{id}` | `users.write` | 修改（带 `version`）；设 `isDefault: true` 会取消原默认组 |
| DELETE | `/api/admin/groups/{id}` | `users.write` | 删除（默认组 409 `group_is_default`），成员移到默认组 |
| PUT | `/api/admin/users/{id}/group` | `users.write` | `{groupId}` 修改用户所在组 |
| POST | `/api/admin/users/batch` | `users.write` | 新增 `action: 'set_group'`（需要 `groupId`） |

- 用户列表与详情返回 `group: {id, name}`；`GET /api/admin/users?groupId=` 过滤。`/api/me` 返回自己的 `group: {id, name, priceMultiplier, limits}`。
- 审计：`group.create`、`group.update`、`group.delete`、`user.group_change`。用户被移组时收到通知 `account.group_changed`（站内，默认开启）。

## 2. 限额（用户组与 API Key）

### 2.1 API Key 消费上限

Key 策略新增：

```ts
spendLimit: { amount: string; window: 'day' | 'week' | 'month' | 'total' } | null   // 只统计平台渠道计费金额
```

窗口按用户所在组的 `timezone` 计算（周从周一开始）。

### 2.2 判定与计数

- 计数表 `usage_counters (scope: 'user' | 'key', scope_id, window: 'day' | 'week' | 'month' | 'total', window_start, requests, charge_nano)`，在结算事务中累加（与钱包扣费同一事务）。
- **请求前**检查（在归属分档之后、平台档首次尝试之前，与套餐 / 钱包检查同一位置；自有 / 共享渠道不受消费限额约束，但受 `rpm` / `rpd` 约束）：
  - 组 `rpm`：每个实例进程内令牌桶（多实例时为每实例上限，文档说明）；超出 → `429 rate_limited`，带 `Retry-After`。
  - 组 `rpd`：读计数表，超出 → `429 user_request_limit`，`Retry-After` 为到次日零点的秒数。
  - 组 `dailySpend` / `monthlySpend`、Key `spendLimit`：已用金额 ≥ 上限 → `429 spend_limit_exceeded`（消息说明是哪个限额、何时重置）；预留金额额外不超过剩余额度（与钱包预留同样按剩余额度封顶）。
- 限额为软上限：并发请求可能略微超出。
- 新增通知事件：`limit.spend_near`（达到 80%，每窗口一次，默认站内开）、`limit.spend_reached`（达到上限，默认邮件 + 站内开）。

### 2.3 查询

- `GET /api/billing/limits`（登录用户）：`{group: {...limits}, usage: {rpdUsed, dailySpent, monthlySpent, resetsAt: {...}}, keys: Array<{id, name, spendLimit, spent, resetsAt}>}`。

## 3. 分时价格

价格版本（售价与成本价）新增可选 `schedule`：

```ts
schedule: Array<{
  days: number[]          // 0=周日 … 6=周六；空 = 每天
  start: string           // "HH:MM"（含）
  end: string             // "HH:MM"（不含）；可以跨零点（start > end）
  multiplier: string      // 十进制 0–10，作用于该价格的全部单价
}> | null
scheduleTimezone: string  // IANA，默认 "Asia/Shanghai"
```

- 按**请求开始时间**匹配第一个命中的时段；未命中时倍率为 1。时段之间不允许重叠（422）。
- 例：DeepSeek 低谷价 `[{days: [], start: "00:30", end: "08:30", multiplier: "0.5"}]`。
- 模型广场的 `price` 增加 `schedule`、`scheduleTimezone` 与 `currentMultiplier`。

## 4. 数据迁移工具

`omnigate migrate-db --from <url> --to <url> [--batch 1000] [--dry-run]`

- 支持 SQLite → PostgreSQL 与 PostgreSQL → SQLite。目标库必须是**空库**（迁移到最新版本后没有业务数据；否则拒绝，除非 `--force-empty-check=false`）。
- 先对源库与目标库执行迁移到相同的最新版本，然后按外键依赖顺序逐表复制（批量插入；PostgreSQL 目标使用 COPY），最后校验每张表的行数与金额合计（钱包余额、流水、请求日志 charge），任何不一致都报错并返回非零退出码。
- 复制过程中源库只读打开；建议停机迁移（文档说明步骤：停服务 → 备份 → 迁移 → 用新 `OMNIGATE_DATABASE_URL` 启动）。
- 加密数据（渠道密钥、SMTP 密码等）原样复制，迁移前后必须使用同一主密钥。
- 输出进度（每张表行数）与最终校验摘要；`--dry-run` 只做检查与计数。

## 5. 实现差异

§1–§3 已按本契约实现（迁移 `00012_groups_limits`，PostgreSQL 与 SQLite）。下表列出契约未写明、或与字面描述不完全一致的地方。

| 位置 | 契约 | 实现 | 原因 |
| --- | --- | --- | --- |
| §1 迁移 | 创建“默认”组并放入现有用户 | 默认组使用固定 ID `01920000-0000-7000-8000-000000000001`；`users.group_id` 外键指向 `user_groups`。插入用户时未给出组（其他工具、数据导入）由触发器放入**当前**默认组：PostgreSQL 为 `BEFORE INSERT`（列 `NOT NULL`），SQLite 为 `AFTER INSERT`（列可空：SQLite 不能给已有表加带外键的 `NOT NULL` 列，触发器也不能改写 `NEW`）。应用写入时总是显式给出组 | 两种数据库都能保证“恰好一个组”；`omnigate migrate-db` 复制时 `user_groups` 已有迁移种下的默认组行 |
| §1 | `UserGroup` | `PATCH` 中 `limits` 提供时整体替换（省略的限额 = 不限）；`priceMultiplier` 与金额既接受字符串也接受数字，`null` / `""` = 不限；回传只读字段（`id`、`members`、`createdAt`、`updatedAt`）被忽略。名称唯一不区分 ASCII 大小写，冲突为 `409 group_name_exists`；对默认组设 `isDefault: false` 返回 `422 details.isDefault`。列表默认组在前，其余按名称排序 | 契约未定 |
| §1.3 | `PUT /api/admin/users/{id}/group` | 返回修改后的 `User`；用户已在该组时不做修改（不审计、不通知）；不改变用户的 `version`（避免与进行中的角色 / 状态编辑冲突）；`groupId` 缺失 / 非法 / 不存在为 `422 details.groupId`。批量 `set_group` 同样规则，审计另有一条 `user.batch`（metadata 含 `groupId`） | 契约未定 |
| §1.3 | 被移组时通知 `account.group_changed` | 删除组导致的移组也通知（`data.deleted: true`）；事件类别为“账户”，`info`，默认仅站内开启，可关闭（不锁定） | 用户同样被移组 |
| §1.3 | `/api/me` 的 `group: {id, name, priceMultiplier, limits}` | 另含 `timezone`；`user` 对象本身也带 `group: {id, name}`（与用户列表一致） | 前端需要按组时区显示重置时间 |
| §1.1 | 售价 × 组倍率（在分时倍率之后） | 金额 = 版本金额 × 分时倍率 × 组倍率，**整体一次舍入**到 9 位小数（不是逐个单价舍入）；成本价只乘自己的分时倍率。`priceMultiplier` 只在成功、有售价的平台档请求上记录（含套餐覆盖的请求），其他为 null。`lowest_cost` 路由比较成本价时也乘当前分时倍率 | 精度；“组倍率不作用于成本” |
| §1.1 | 广场 `mine` 的 `price` 乘组倍率 | `price` 的各单价分别乘组倍率后舍入；`basePrice` 为原价（未定价时为 null）；`priceMultiplier` 总是返回（默认 `"1"`）。`price` / `basePrice` 中的单价都是未乘分时倍率的金额，此刻的分时倍率见 `currentMultiplier` | 契约未定 |
| §1.2 | `sharedWith: {users, groups}` | 响应总是对象（两个数组，`scope` 不为 `shared` 时都为空）；最多 200 个用户、50 个组；不存在的组 `422 details["sharedWith.groups"]`。组被删除时其共享随之删除。按组共享在渠道快照中展开为成员，移组 / 删组后重新加载快照（本实例立即，其他实例 ≤ 15 秒） | 契约未定 |
| §2.2 | 计数表列 `window` | 列名为 `window_kind` | `window` 是 PostgreSQL 保留字 |
| §2.2 | 计数在结算事务中累加 | 需要钱包结算（扣费或释放预留）的请求与钱包同一事务；不经过钱包的请求（自有 / 共享渠道且无预留、套餐覆盖、未定价）单独一个事务。Key 的计费金额在 `day` / `week` / `month` / `total` 四个窗口都累加（更换 Key 限额窗口不丢失历史），用户计数只维护 `day` / `month`。旧窗口随每日保留任务清理（日 / 周 60 天、月 400 天） | 自有渠道也要计入 `rpd` |
| §2.2 | `rpd` | 只统计通过请求限额检查、且至少尝试了一个渠道的请求（被消费限额、余额、配额拦下的请求不计）。`rpm` / `rpd` 检查在第一次尝试任何渠道之前，且在平台档的套餐 / 钱包 / 消费检查之前 | 被拒绝的请求不应占用每日次数 |
| §2.2 | 消费限额统计“平台渠道计费金额” | 只统计**由钱包支付**的金额（`charge`）：套餐覆盖的请求（`quotaCharge`）不计入、也不受消费限额拦截；没有售价的模型不检查。与 `billing.enforce` 无关（关闭时同样统计与拦截）。上限 `"0"` 表示禁止使用平台渠道 | 套餐额度已有自己的配额 |
| §2.2 | `429 spend_limit_exceeded` | `Retry-After` 为到窗口结束的秒数（`total` 窗口不带）；消息为英文（数据面惯例），如 `daily spend limit of your user group reached (5 / 5); resets at 2026-10-10 00:00 CST`。`user_request_limit` 与 `spend_limit_exceeded` 是新的数据面错误类别（OpenAI 格式 `type` 分别为 `rate_limit_exceeded` / `insufficient_quota`，Anthropic 为 `rate_limit_error`）；组 `rpm` 复用 `rate_limited` | 契约未定 |
| §2.1 | Key 窗口按组的 timezone | 用户换到时区不同的组后，日 / 周 / 月窗口按新时区计算（等于开始新的窗口） | 窗口起点是按时区计算的时刻 |
| §2.2 | 通知 80% / 达到上限，每窗口一次 | 结算后按新的已用金额判断：≥ 上限发 `limit.spend_reached`，否则 ≥ 80% 发 `limit.spend_near`（一次请求直接越过上限时只发 `reached`）；按“事件类型 + 限额 + 窗口起点”去重。`reached` 为告警类（不进入摘要）。`data` 为 `{kind: daily \| monthly \| key, window, spent, limit, windowStart, resetsAt?, keyId?, keyName?}` | 契约未定 |
| §2.3 | `GET /api/billing/limits` | `group` 为 `{id, name, priceMultiplier, timezone, rpm, rpd, dailySpend, monthlySpend}`（限额平铺）；`usage.resetsAt` 为 `{day, month}`；`keys` 为当前用户未吊销的全部 Key，没有上限的 Key `spendLimit`、`spent`、`resetsAt` 都为 null，`total` 窗口的 `resetsAt` 为 null。任何登录用户可调用 | 契约未定 |
| §2.1 | Key `spendLimit` | `amount` 保存时规范化（如 `"20.50"` → `"20.5"`）；非法时 `422 details["policy.spendLimit.amount"]` / `details["policy.spendLimit.window"]` | 契约未定 |
| §3 | `schedule` | `days` 指时段**开始**的那一天（跨零点时段零点后的部分属于第二天）；`end` 可以是 `24:00`（不能用于跨零点）；`start == end` 非法；最多 48 个时段；`days` 去重排序、`multiplier` 规范化后保存；空数组等于 null。分时倍率作用于全部单价（含 `perRequest`、`perImage`）。时段修改被视为价格变化（`model.price_changed`） | 契约未定 |
| §3 | 422 | 校验失败 `details.schedule`（消息指出第几个时段及重叠的时段）、`details.scheduleTimezone` | 契约未定 |
