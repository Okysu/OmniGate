# 套餐库存与福利活动

套餐新增 `stock`（剩余库存，默认 `-1` 不限量，`0` 售罄）和 `hidden`
（默认 `false`，隐藏于 `/api/plans` 和余额购买目录）。每次余额购买、续费、
升级或活动套餐领取消耗一份库存，付款或发放失败会回滚。管理员开通、兑换已发行
的兑换码不消耗此销售库存。调整库存使用套餐现有的 `version` 乐观锁。

隐藏套餐不能直接通过余额购买接口购买，也不能作为折价升级的原套餐；管理员
仍可在套餐管理中查看和编辑。活动关联的套餐必须有效、隐藏且可叠加，每次领取
创建一份独立订阅。活动编辑器复用套餐编辑器，默认创建 7 天有效期的套餐。

## 活动接口

| 接口 | 权限与用途 |
| --- | --- |
| `GET /api/public/benefits` | 公开的已发布活动列表 |
| `GET /api/public/benefits/{id}` | 分享页数据；草稿、下线活动返回 404 |
| `GET /api/benefits/claims` | `billing.own`，当前用户各活动领取次数 |
| `POST /api/benefits/campaigns/{id}/claim` | `billing.own`，领取或付费购买 |
| `GET /api/admin/benefits/campaigns` | `billing.manage`，完整活动配置 |
| `POST /api/admin/benefits/campaigns` | `billing.manage`，创建活动 |
| `PUT /api/admin/benefits/campaigns/{id}` | `billing.manage`，带 `version` 更新 |

活动字段：`title`、`description`（纯文本）、`status`（`draft/active/archived`）、
`rewardType`（`balance/subscription`）、`planId`、`amount`、`price`、`stock`、
`perUserLimit`（默认 1，1–1000）、`startsAt`、`endsAt`、`conditions`。
所有金额为站点结算币种的十进制字符串；日期为 RFC3339，可空。

余额活动要求 `amount > 0`、`price = 0`、`planId = null`；套餐活动要求
`amount = 0`，使用活动售价（`0` 免费），与隐藏套餐的目录标价无关。

条件同时满足：`groupIds`、`userIds`（列表内任一项即可）、`registeredAfter`
（含边界）、`registeredBefore`（不含边界）、`minSpend`（默认 `0`）、
`requirePurchase`（默认 `false`，有正金额的 `plan_purchases` 记录）。
消费统计复用 `lottery_days` 的持久化模型消费，包含钱包和套餐扣费；购买套餐
本身不计入。公开接口只返回条件说明，不返回指定用户或用户组 ID 名单。

领取请求：`{"requestId":"UUID","expectedPrice":"0"}`。同一用户同一请求编号
重试返回原结果；跨活动复用编号返回冲突。活动状态、时间、条件、每人次数、
活动库存和套餐库存均在事务内校验。钱包扣款/余额奖励、订阅、库存和领取记录
同事务提交。已产生领取的活动不能改变奖励类型、关联套餐、奖励金额或价格，
需要另建活动；支持调整说明、时间、条件、剩余库存和每人次数。

分享路径为 `/benefits/{id}`，未登录可查看，领取时跳转登录并回到原活动。
活动管理入口位于 `/console/benefits`，有 `billing.manage` 权限时显示新建和管理按钮，使用弹窗完成配置，不设置独立管理路由；福利首页继续保留每日消费抽奖。

新增迁移 27 同时覆盖 PostgreSQL 和 SQLite。新增 `benefit_campaigns`、
`benefit_claims`，余额发放和付费流水复用现有账本，审计为 `benefit.save`、
`benefit.claim`；付费活动套餐同时写入 `plan_purchases`。
