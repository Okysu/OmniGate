# 契约：Round 6 —— 通知（邮件 / Webhook / 站内）、告警与上游余额

> 状态：**定稿，按此实现**。通用约定同前。所有新增 SQL 必须同时支持 PostgreSQL 与 SQLite（ADR-0009）。

## 1. 邮件发送（SMTP）

配置来源：系统设置（数据库）→ 环境变量 → 未配置。未配置 SMTP 时邮件渠道不可用，站内通知照常记录。

| 系统设置字段 | 环境变量 | 说明 |
| --- | --- | --- |
| `notifications.smtp.host` | `OMNIGATE_SMTP_HOST` | 空 = 未配置 |
| `notifications.smtp.port` | `OMNIGATE_SMTP_PORT` | 默认 587 |
| `notifications.smtp.security` | `OMNIGATE_SMTP_SECURITY` | `starttls`（默认）、`tls`（隐式 TLS，465）、`none`（仅限开发环境；生产环境拒绝） |
| `notifications.smtp.username` | `OMNIGATE_SMTP_USERNAME` | |
| `notifications.smtp.password` | `OMNIGATE_SMTP_PASSWORD` | **只写**：读取时返回 `passwordSet: boolean`；数据库中用主密钥加密（ADR-0008） |
| `notifications.smtp.from` | `OMNIGATE_SMTP_FROM` | 如 `OmniGate <noreply@example.com>` |
| `notifications.enabled` | — | 总开关，默认 `true` |
| `notifications.emailRateLimitPerHour` | — | 每个用户每小时最多发送的邮件数，默认 20（超出的合并为一封摘要） |

`POST /api/admin/settings/smtp-test`（`settings.write`）`{to}` → 立即发一封测试邮件，返回 `{ok, error?}`（错误信息不包含密码）。

## 2. 通知事件

| 事件 `type` | 接收者 | 触发 | 默认（邮件 / 站内） |
| --- | --- | --- | --- |
| `wallet.balance_low` | 用户 | 可用余额从阈值以上降到阈值以下（阈值由用户设置，默认 1.00；恢复到阈值以上后才会再次触发） | 开 / 开 |
| `wallet.credited` | 用户 | 兑换码充值、管理员调整、新用户赠送 | 关 / 开 |
| `subscription.expiring` | 用户 | 订阅到期前 3 天（每份订阅一次） | 开 / 开 |
| `subscription.expired` | 用户 | 订阅到期或被取消 | 开 / 开 |
| `quota.near_limit` | 用户 | 某条配额规则在当前窗口用量达到 80%（每个窗口一次） | 关 / 开 |
| `quota.exhausted` | 用户 | 某条配额规则在当前窗口用完（每个窗口一次） | 开 / 开 |
| `model.price_changed` | 用户 | 用户近 30 天调用过的平台模型售价变化（含未来生效的价格，提前通知生效时间） | 开 / 开 |
| `model.removed` | 用户 | 用户近 30 天调用过的模型不再可用（平台下线，或共享被收回） | 开 / 开 |
| `model.added` | 用户 | 平台模型广场新增模型 | 关 / 关 |
| `key.expiring` | 用户 | API Key 到期前 7 天 | 开 / 开 |
| `channel.unhealthy` | 渠道所有者；平台渠道另通知 `channels.manage` 管理员 | 熔断打开或健康探测连续失败 | 开 / 开 |
| `channel.recovered` | 同上 | 从异常恢复 | 关 / 开 |
| `channel.auth_failed` | 同上 | 上游返回 401/403（凭据失效），每个渠道 6 小时内最多一次 | 开 / 开 |
| `upstream.balance_low` | 同上 | 渠道插件 `balance` 能力返回的余额低于该渠道设置的阈值 | 开 / 开 |
| `plugin.pending_approval` | `plugins.trust` 管理员 | 有插件版本等待审批 | 开 / 开 |

- 每个事件带去重键（如 `wallet.balance_low:<user>:<下降那次的时间>`），同一个键只通知一次。
- 事件只对**当时有权看到相关资源**的用户产生（例如共享渠道异常不会通知被共享的人，只通知所有者）。

## 3. 通知渠道与用户偏好

```ts
interface NotificationPreferences {
  email: { enabled: boolean; address: string | null; verified: boolean }  // address 为空 = 使用账户邮箱（来自身份提供方）
  webhook: { enabled: boolean; url: string | null; secretSet: boolean; format: 'json' | 'feishu' | 'dingtalk' | 'wecom' | 'slack' }
  events: Record<EventType, { email: boolean; webhook: boolean; inApp: boolean }>
  thresholds: { walletBalanceLow: string }       // 金额，默认 "1"
  digest: 'off' | 'daily'                        // daily：除告警类事件（channel.* / upstream.* / quota.exhausted / wallet.balance_low）外，每天 09:00（用户时区）合并为一封
  timezone: string                               // IANA，默认 Asia/Shanghai
  version: number
}
```

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/notifications/preferences` | 登录 | 当前用户的偏好（不存在时返回默认值，`version: 0`） |
| PUT | `/api/notifications/preferences` | 登录 | 整体替换（带 `version`）；只能为自己有资格接收的事件开关（例如普通用户设置 `plugin.pending_approval` 返回 422） |
| POST | `/api/notifications/email/verify` | 登录 | `{address}` → 向该地址发送 6 位验证码（10 分钟有效，每小时最多 5 次） |
| POST | `/api/notifications/email/confirm` | 登录 | `{address, code}` → 验证通过后设为通知邮箱 |
| POST | `/api/notifications/webhook/test` | 登录 | 立即向配置的 Webhook 发送测试消息 |
| PUT | `/api/notifications/webhook/secret` | 登录 | `{secret}` 设置 HMAC 密钥（只写，加密存储） |

- 自定义邮箱必须验证；使用身份提供方的邮箱时，按其 `email_verified` 判断。
- Webhook：经过防 SSRF 拨号器（与渠道相同的私网规则），超时 10 秒，失败按 1、5、30 分钟重试 3 次。
  `json` 格式请求体 `{id, type, title, body, url, data, createdAt}`，带 `X-OmniGate-Signature: sha256=<HMAC(secret, body)>`；
  其他格式转换成对应机器人的消息格式。
- 每封邮件都带一键退订链接（签名令牌，关闭该事件的邮件通知）和设置页链接。

## 4. 站内通知

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/notifications?unread=&type=&page=&pageSize=` | 我的通知（分页，新的在前），每条 `{id, type, severity: info\|warning\|critical, title, body, link, data, readAt, createdAt}` |
| GET | `/api/notifications/unread-count` | `{count}` |
| POST | `/api/notifications/read` | `{ids}` 或 `{all: true}` 标为已读 |

站内通知保留 90 天。

## 5. 告警与上游余额（概览）

- 渠道新增设置 `alerts: { balanceBelow: string | null }`（上游余额低于此值时告警；按插件返回的币种比较，币种不同时不比较）。
- `GET /api/alerts/summary`（登录；只包含当前用户能管理的渠道）：

```ts
{
  channels: { total: number; healthy: number; degraded: number; down: number }
  balances: Array<{ channelId: string; channelName: string; currency: string; total: string; available: boolean
                    threshold: string | null; low: boolean; checkedAt: string }>   // 来自插件 balance 能力的最近结果
  recent: Notification[]            // 最近 10 条告警类通知（channel.* / upstream.*）
}
```

- 概览页的“告警与上游余额”卡片使用此接口（替换占位）。

## 6. 发送管道

- 事件写入 `notification_events`（含去重键，唯一约束）→ 按接收者展开写入 `notifications`（站内）与 `notification_deliveries`（邮件 / Webhook 发件箱：状态、尝试次数、下次重试时间、最后错误）。
- 后台工作协程领取到期的投递（PostgreSQL 用 `FOR UPDATE SKIP LOCKED`，SQLite 单实例直接领取），失败指数退避，最多 5 次。
- 邮件为 HTML + 纯文本两部分，中文模板，站点名取系统设置 `site.name`，链接基于 `OMNIGATE_PUBLIC_URL`。
- 审计：`notifications.smtp_update`（设置变化随 `settings.update` 记录）、`notifications.preferences_update`。
- 指标：`omnigate_notifications_sent_total{channel,type,result}`。

## 6.1 补充约定（前后端对齐，Round 6）

1. `GET /api/notifications/preferences` 额外返回只读字段 `smtpConfigured: boolean`（SMTP 未配置或通知总开关关闭时为 false）。
   SMTP 未配置时 `POST /api/notifications/email/verify` 与 `smtp-test` 返回 `409 smtp_not_configured`。
2. `GET /api/notifications?type=` 接受逗号分隔的多个精确类型（`type=wallet.balance_low,wallet.credited`）。
3. `PUT /api/notifications/preferences`：`events` 中**省略**的事件保持默认值；对无资格的事件显式设为开启才返回 422；
   请求中的只读字段（`email.verified`、`webhook.secretSet`、`smtpConfigured`）被忽略。
4. `PUT /api/notifications/webhook/secret` 传 `{secret: ""}` 表示清除。系统设置 `notifications.smtp.password`：`""` 表示清除数据库中的密码（不回退到环境变量），`null` 表示恢复为环境变量 / 未设置。
5. `POST /api/notifications/webhook/test` 响应 `{ok: boolean, error?: string, statusCode?: number, latencyMs?: number}`；`smtp-test` 响应 `{ok, error?}`。
6. Webhook 对所有事件默认关闭（用户需要逐项开启）。
7. 通知的 `link` 只会是站内路径（如 `/console/billing`），不会是外部链接。

## 7. 实现差异

实现（`server/internal/notify` 等）与上文的出入或补充约定：

| # | 条目 | 实现 |
| --- | --- | --- |
| 1 | `subscription.expired`（取消） | 到期与取消都由后台扫描（每 10 分钟）产生，取消后最长约 10 分钟才通知；去重键 `subscription.expired:<订阅>`。`subscription.expiring` 的键含到期时间，续期后会再次提醒。 |
| 2 | 重试次数 | 邮件按 1、2、4、8 分钟指数退避，最多 5 次；Webhook 按 §3 为 1 + 3 次（1、5、30 分钟）。领取后 5 分钟内未完成的投递（进程崩溃）会被重新领取。 |
| 3 | 每小时邮件上限 | 只统计逐条通知与合并摘要；每日摘要不计入。超限的通知暂存，在最早一封发出满 1 小时后合并为一封“N 条通知摘要”（不受上限约束）。 |
| 4 | 退订链接 | 逐条通知邮件的链接关闭该事件的邮件开关；每日摘要与合并摘要的链接关闭全部邮件通知（`email.enabled = false`）。`GET /api/notifications/unsubscribe` 默认返回 HTML 页面，`Accept: application/json` 时返回 `{ok, type}`；无效令牌 400（HTML）/ 422 `invalid_token`（JSON）。令牌为主密钥派生密钥的认证加密，不过期。 |
| 5 | 滚动窗口配额 | “每个窗口一次”对 calendar / period / session / lifetime 按窗口起点；rolling 窗口没有固定起点，按“窗口时长大小的时间桶”去重。同一次请求同时跨过 80% 与 100% 时只发 `quota.exhausted`。 |
| 6 | 上游余额币种 | 渠道阈值没有独立的币种字段，始终按插件返回的币种解释并比较（“币种不同时不比较”无从判断）；低于阈值时设置标记，回到阈值以上（或删除阈值）后重新武装。`alerts.balanceBelow` 规范化为十进制字符串，`""` 等同 `null`。 |
| 7 | 告警概览 | `channels` 只统计已启用的可管理渠道，`down` = 熔断打开；`balances` 为每个渠道第一个（按名称）`output: balance` 能力的最近一次成功结果（含已停用渠道）；`recent` 为当前用户自己的最近 10 条 `channel.*` / `upstream.*` 通知。 |
| 8 | 熔断事件 | 熔断状态在各实例内存中，多实例部署时同一故障可能各通知一次（去重键为 `channel.unhealthy:<渠道>:<打开时刻秒>`）。健康测试（`POST /api/channels/{id}/test`）的失败 / 成功同样驱动熔断，因此也会产生异常 / 恢复事件。 |
| 9 | `channel.auth_failed` 限流 | 本实例内存 6 小时窗口 + 数据库中同渠道 6 小时内已有事件则跳过 + 6 小时时间桶去重键。渠道测试返回 401/403 也会触发。 |
| 10 | 模型增删 | 首次扫描（升级后第一次启动）只记录现状不通知。`model.removed` 的“调用过”= 近 30 天状态码 < 400 的请求日志；模型恢复可用后重新武装。`model.added` 针对平台广场（管理员的全局渠道、未隐藏）新增的模型，接收者为全部活跃用户中开启了该事件的人（默认全关）。扫描在渠道快照变化时与每 10 分钟执行。 |
| 11 | `model.price_changed` | 只在新售价版本与其生效时刻原本生效的版本金额不同时产生；接收者为近 30 天经平台渠道（`channel_tier = platform` 或旧日志为空）成功调用过该模型的用户。 |
| 12 | `wallet.balance_low` | 在余额变动（账本）提交后判断：变动前余额 ≥ 阈值且变动后可用余额（余额 − 预留）< 阈值，且没有未解除的标记；正向入账使可用余额回到阈值以上时解除标记。只有预留（未结算）不会触发。阈值每用户缓存 1 分钟（修改偏好时立即失效）。`wallet.credited` 包括兑换码、管理员正向调整、新用户赠送。 |
| 13 | 通知总开关 | `notifications.enabled = false` 时不再产生邮件与 Webhook 投递（已排队的跳过），站内通知照常；偏好中的 `smtpConfigured` 为 false，邮箱验证返回 `409 smtp_not_configured`。管理员的 `smtp-test` 只要求 SMTP 已配置。 |
| 14 | 接口细节 | `email/verify` → `200 {ok, expiresAt}`（发送失败 `502 email_send_failed`，超过每小时 5 次 `429 rate_limited`）；`email/confirm` → 新的偏好（错误码 `verification_invalid` / `verification_expired`，每个验证码最多尝试 5 次）；`webhook/secret` → `{secretSet}`，不改变偏好版本；`notifications/read` → 204；未保存 Webhook 地址时 `webhook/test` → `422`（`details.webhook.url`）。 |
| 15 | 偏好细节 | GET 的 `events` 只包含有资格接收的事件；PUT 中未知事件类型 → 422；`email.address` 只能是 `null` 或当前已验证的自定义地址（大小写不敏感），新地址必须走 verify / confirm；`webhook.url` 在保存时按渠道的私网规则校验，发送时再由拨号器校验。`email.enabled` 默认 true。 |
| 16 | 验证码存储 | 存 SHA-256（用户、小写地址、验证码）摘要；验证码只是 6 位数，靠 10 分钟有效期与每码 5 次尝试防暴力。验证码记录 1 天后清理。 |
| 17 | 审计 | SMTP 字段变化除 `settings.update`（密码只记录是否设置）外另写 `notifications.smtp_update`（变更字段、host、security）；`notifications.preferences_update` 也用于邮箱确认、Webhook 密钥设置与一键退订（`metadata.via = unsubscribe`）。 |
| 18 | 设置页 | `GET /api/admin/settings` 的 `readonly` 增加 `smtpConfigured`（当前生效的 host 与 from 是否都非空）。SMTP 身份验证只支持 AUTH PLAIN（`net/smtp`），在未加密连接上只允许连接本机地址时发送凭据。 |
| 19 | 保留 | 站内通知与事件（含去重键）保留 90 天，已完成的投递 30 天，随请求日志保留任务每天执行；各扫描器的回看窗口都短于 90 天，删除旧去重键不会导致重复通知。 |
| 20 | 严重级别 | critical：`channel.unhealthy`、`channel.auth_failed`；warning：`wallet.balance_low`、`subscription.expiring`、`quota.exhausted`、`model.removed`、`key.expiring`、`upstream.balance_low`；其余 info。 |
