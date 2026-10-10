# 契约：Round 15 —— 余额购买套餐（购买 / 续费 / 补差价升级）与邀请返利

> 状态：**已实现**。通用约定同前；所有 SQL 同时支持 PostgreSQL 与 SQLite（迁移 `00021_purchase_referral`）。
> 需求：用户可以用钱包余额直接购买、续费套餐，或补差价升级到更高档位（已用额度保留，不清零）；套餐购买有独立页面，
> 「钱包与订阅」页不再显示套餐目录。新增邀请返利：邀请链接绑定新注册用户，被邀请人用兑换码充值后按比例返利给邀请人；
> 管理员可开关（默认关闭）、设置比例与起返金额。

## 1. 套餐售价

- `plans.listPrice` 从“仅展示”变为**余额购买的售价**（结算币种，十进制字符串）。`listPrice` 为 `null` 或 `"0"` 的套餐**不可购买**
  （仍可通过兑换码 / 管理员开通），购买选项里 `purchasable = false`。
- 归档（`archived`）套餐不可购买、不可作为升级目标；但已持有的归档套餐订阅**可以升级到在售套餐**（旧档位价格取该套餐当前的 `listPrice`）。
- 订阅新增来源 `source = "purchase"`（余额购买新建的订阅）。续费沿用原订阅行，来源不变；升级沿用原订阅行，来源不变。

## 2. 三种购买动作

对目标套餐 P（在售、`listPrice > 0`），用户 U，当前时间 now：

| 动作 | 条件 | 价格 | 效果 |
| --- | --- | --- | --- |
| `new` | U 没有 P 的生效中订阅（或 P 可叠加） | `P.listPrice` | 新建订阅，`startsAt = now`，`endsAt = now + P.duration`，`source = purchase` |
| `renew` | P 不可叠加，且 U 有 P 的生效中订阅 S | `P.listPrice` | `S.endsAt += P.duration`；快照与用量不变（与兑换码续期相同） |
| `upgrade` | 从 U 的生效中订阅 S（套餐 O ≠ P）升级，见下 | 补差价 | 原地把 S 换成 P：快照（名称 / 模型 / 规则）换成 P，从 now 起开始 P 的新周期（`endsAt = now + k × P.duration`），`startsAt` 不变，**已用额度保留** |

**升级条件**：S 状态 `active` 且未到期；P 不可叠加；U 没有 P 的其他生效中订阅（有的话应当续费 P）；P 的日均价格高于 O（`P.listPrice / P.duration > O.listPrice / O.duration`），不支持降级。

**补差价**（与 Claude 等订阅服务相同：升级即开始新周期，旧套餐未用完的时间按价值抵扣）：

```
remaining = S.endsAt − now                                   （秒）
credit    = O.listPrice × remaining / O.duration             （向下取整到 0.01）
k         = 使 k × P.listPrice > credit 的最小正整数（通常为 1）
price     = k × P.listPrice − credit                         （向上取整到 0.01）
endsAt    = now + k × P.duration
```

越早升级抵扣越多；临近到期时升级几乎等于新购，因此不能靠“到期前一天补一点差价”换取整月的高档额度。

- `O.listPrice` 取旧套餐**当前**的 `listPrice`（含已归档的旧套餐）；为 `null` 时按 0 计（兑换码 / 管理员开通的套餐，补的是新套餐剩余时间的全价）。
- 旧订阅续了多期、抵扣超过 P 一个周期的价格时，新周期按整数倍延长（`k > 1`），已付的钱不会浪费。
- 抵扣随时间减少，所以升级价格随时间**上升**（约每天 `O.listPrice / O.duration`）。

**用量保留**：`quota_usage` 以 `(subscription_id, rule_id, window_start)` 为键，升级不换订阅行，所以 P 中与 O **同 id** 的规则
（如 `5h` / `weekly` / `monthly`）沿用已用量，百分比按新上限重新计算（已用 $60：Go+ 75% → Pro 30%）；P 独有的规则从 0 开始，
O 独有的规则不再生效。`period` / `lifetime` 窗口锚定的 `startsAt` 不变。

## 3. 接口（用户，`billing.own`）

### 3.1 `GET /api/billing/purchase/options`

一次返回购买页需要的全部信息（价格由服务端计算）：

```ts
interface PurchaseOptions {
  available: string                 // 钱包可用余额（balance − reserved）
  currency: string                  // 结算币种代码
  plans: PurchaseOption[]           // 在售套餐，顺序同 /api/plans
  subscriptions: ActiveSubscription[] // 用户的生效中订阅（购买页据此提示：新购会与覆盖相同模型的订阅额度叠加）
}

interface ActiveSubscription {
  id: string
  planId: string
  planName: string                  // 订阅快照中的名称
  models: string[]                  // 覆盖的模型（[] = 全部）
  endsAt: string
}

interface PurchaseOption {
  plan: CatalogPlan                 // 同 GET /api/plans 的条目
  purchasable: boolean              // listPrice > 0
  action: 'new' | 'renew'           // 直接购买时的动作
  price: string | null              // 直接购买价格（= listPrice；不可购买时 null）
  renewSubscriptionId: string | null  // action = renew 时被续期的订阅
  currentEndsAt: string | null      // action = renew 时当前到期时间
  newEndsAt: string | null          // 直接购买后的到期时间（new：now + duration；renew：currentEndsAt + duration）
  upgrades: UpgradeOption[]         // 可从哪些生效中订阅升级到该套餐（按价格升序）
}

interface UpgradeOption {
  fromSubscriptionId: string
  fromPlanId: string
  fromPlanName: string              // 订阅快照中的名称
  fromPrice: string | null          // 旧套餐当前 listPrice
  price: string                     // 此刻的补差价（= k × P.listPrice − credit）
  credit: string                    // 旧套餐剩余时间的抵扣
  remainingSeconds: number
  endsAt: string                    // 升级后到期时间（now + k × P.duration）
}
```

### 3.2 `POST /api/billing/purchase`

```ts
// 请求
{ planId: string, fromSubscriptionId?: string, expectedPrice: string }
// 200 响应
interface PurchaseResult {
  id: string                        // 购买记录 id
  action: 'new' | 'renew' | 'upgrade'
  price: string                     // 实际扣款
  wallet: Wallet                    // 扣款后的钱包（同 GET /api/billing/wallet）
  subscription: Subscription        // 购买后的订阅（同 GET /api/billing/subscriptions 的条目，含用量）
}
```

- 不带 `fromSubscriptionId`：按 §2 决定 `new` / `renew`。带上：`upgrade`。
- `expectedPrice` 是界面上展示给用户的价格。实际价格 **> expectedPrice** 时拒绝（`409 price_changed`，`details.price` 为当前价格）；
  实际价格 ≤ 展示价格时按实际价格扣款。升级价格随时间缓慢上升，界面停留较久后确认会得到 `price_changed`，刷新后重新确认即可。
- 一次事务内完成：锁用户 → 读套餐 → 锁订阅 → 锁钱包 → 校验余额（`available ≥ price`）→ 扣款（`ledger kind = charge`，
  `refType = purchase`，`refId = 购买记录 id`，`note = "购买套餐 <名称>" / "续费套餐 …" / "升级套餐 <旧> → <新>"`）→ 写订阅 → 写购买记录 → 审计。
- 余额购买**不受** `billing.enforce` 影响（未开启计费时同样扣余额）。
- 审计动作 `subscription.purchase`（`details`: `action, planId, planName, price, subscriptionId, fromPlanId?, fromPlanName?, endsAt`）。

| 错误 | HTTP | code |
| --- | --- | --- |
| 套餐不存在 | 404 | `not_found` |
| 套餐已归档 | 409 | `plan_archived` |
| 套餐不可购买（无售价） | 409 | `plan_not_for_sale` |
| 余额不足 | 403 | `insufficient_balance`（`details.price`、`details.available`） |
| 升级来源订阅不存在 / 不属于本人 | 404 | `not_found` |
| 来源订阅已到期 / 已取消 | 409 | `subscription_not_active` |
| 不是升级（同套餐、差价 ≤ 0、目标可叠加、已持有目标套餐） | 409 | `not_an_upgrade` |
| 价格变化 | 409 | `price_changed`（`details.price`） |

### 3.3 `GET /api/billing/purchases?page&pageSize`

本人的购买记录（新到旧）：

```ts
interface PurchaseRecord {
  id: string
  action: 'new' | 'renew' | 'upgrade'
  planId: string
  planName: string
  fromPlanName: string | null
  price: string
  subscriptionId: string
  createdAt: string
}
```

## 4. 邀请返利

### 4.1 设置（`/api/admin/settings`，`billing` 组）

| 键 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `billing.referralEnabled` | bool | `false` | 是否发放邀请返利 |
| `billing.referralRate` | 十进制字符串 | `"10"` | 返利比例（百分比，0–100，最多 2 位小数） |
| `billing.referralMinRecharge` | 金额字符串 | `"0"` | 单次兑换码充值金额 ≥ 该值才返利 |

`GET /api/system/info` 增加 `referralEnabled: boolean`（未登录也可见，用于决定是否显示入口）。

### 4.2 邀请码与绑定

- 每个用户有一个邀请码（8 位，大写字母与数字，首次查询时生成，永久不变）。邀请链接：`{publicUrl}/login?invite=<code>`。
- 网页在**任意页面**读到 `?invite=` 时存入 `localStorage`（`og_invite`，30 天），登录页发起登录时带上：
  `GET /api/auth/{provider}/login?redirect=…&invite=<code>`。服务端把它封装进 OAuth state（不经过 IdP），回调时如果**本次登录创建了新用户**，
  且邀请码有效、不是自己，就绑定 `被邀请人 → 邀请人`。已有用户登录不会绑定；绑定永久不变。
- 绑定与开关无关（关闭时也绑定，开启后的充值才返利）。

### 4.3 返利

被邀请人兑换**余额兑换码**（`wallet_credit`）成功时，同一事务内：

- 返利开启、被邀请人有邀请人、充值金额 `amount ≥ referralMinRecharge` → `rebate = amount × referralRate / 100`（四舍五入到 nano），
  `rebate > 0` 时给邀请人钱包记 `kind = grant`、`refType = referral`、`refId = 返利记录 id`、`note = "邀请返利 <被邀请人名称>"`。
- 套餐兑换码、余额购买、管理员调整、注册赠送都**不**返利。
- 邀请人收到 `wallet.credited` 通知（原因“邀请返利”）。
- 锁顺序：兑换码 → 批次 → 被邀请人钱包 → 邀请人钱包。邀请关系只能指向更早注册的用户，不会成环，因此不会死锁。

### 4.4 接口（用户，`billing.own`）

`GET /api/billing/referral`：

```ts
interface ReferralInfo {
  enabled: boolean
  rate: string                      // 当前比例（百分比）
  minRecharge: string
  code: string
  link: string                      // {publicUrl}/login?invite=code
  invitedCount: number
  rebateTotal: string               // 累计返利
  invitees: Invitee[]               // 最近 50 位被邀请人（新到旧）
}

interface Invitee {
  displayName: string               // 脱敏：首尾各 1 个字符，中间 *（≤ 2 个字符时只保留首字符）
  joinedAt: string
  rebateTotal: string               // 该被邀请人累计为你带来的返利
}
```

`GET /api/billing/referral/rebates?page&pageSize`：返利记录（新到旧）

```ts
interface ReferralRebate {
  id: string
  inviteeName: string               // 同上脱敏
  recharge: string                  // 被邀请人这次充值的金额
  rate: string                      // 当时的比例
  rebate: string
  createdAt: string
}
```

## 5. 其他

- 账本 `refType` 增加 `purchase`（购买扣款）与 `referral`（邀请返利）。
- 订阅 `source` 增加 `purchase`。
- 迁移 `00021_purchase_referral`：`plan_purchases`、`referral_codes`、`referrals`、`referral_rebates` 四张表；
  放宽 `subscriptions.source` 与 `ledger_entries.ref_type` 的 CHECK（SQLite 重建表）。
