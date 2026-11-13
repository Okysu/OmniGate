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
`rewards`（1–10 项奖励）、`price`（整组售价）、`stock`（整组库存）、
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

迁移 27 与 28 同时覆盖 PostgreSQL 和 SQLite。新增 `benefit_campaigns`、
`benefit_claims`，余额发放和付费流水复用现有账本，审计为 `benefit.save`、
`benefit.claim`；付费活动套餐同时写入 `plan_purchases`。

## 奖励组合与统一筛选（迁移 28）

新客户端写入 `rewards` 数组。支持单项或多项奖励，一次领取整组：

```json
{
  "rewards": [
    {"type":"balance","amount":"6.66"},
    {"type":"reset_card","kind":"weekly","quantity":1,"validDays":7,"planIds":[]},
    {"type":"lottery","quantity":5},
    {"type":"subscription","planId":"UUID"}
  ],
  "price":"0",
  "conditions": {
    "requireActiveSubscription":true,
    "quotaKind":"weekly",
    "excludePlanIds":["显式排除的套餐 UUID"]
  }
}
```

余额金额大于 0；重置卡及抽奖数量每项 1–100。重置卡有效天数 0–366，0 为长期有效，
从实际领取时计时；卡的 `planIds` 限制使用范围，与领取资格分别设置。
相同套餐不能重复出现在一组奖励中。包含套餐时允许收取一次整组售价，其他活动免费。
整组发放、扣款、库存与领取记录同事务，任意奖励失败全部回滚；请求重试返回相同整组结果。
多个套餐按 ID 排序锁定，付费金额均分归因到套餐购买记录，余数归首项，合计等于整组售价。
已领取后不能修改整组奖励或价格。旧版单项 `rewardType/amount/planId/reward` 请求继续兼容；
多项以 `rewardType=bundle` 保存，领取响应 `rewards` 含各项发放记录。
抽奖赠送来源记录在 `benefit_claims.lottery_chances`，可用次数为消费所得加活动赠送减去已抽次数，
可累积且支持现有连抽，不伪造每日消费记录。发卡不自动重置额度，使用卡保持原刷新时间。

条件共用 `subscription.AudienceFilter`，由福利、批量额度重置/调整/延期、重置卡发放和通知组合筛选复用。
接口 `POST /api/admin/billing/audience/preview`（`billing.manage`）返回：
`{users,subscriptions,sample:[{id,name}],truncated,asOf}`，用户去重，名单最多展示 50 位。
预估不写数据，反映当前条件的匹配人数，不扣除活动领取历史、库存和时间；实际领取重新校验全部限制。

可组合字段：`userIds/groupIds/planIds/excludePlanIds/sources`、`requireActiveSubscription`、
`excludeHiddenPlans`、`quotaKind`（空、`5h`、`weekly`、`both`，后者表示任一窗口）、
`registeredAfter/registeredBefore/minSpend/requirePurchase`。
列表内部取任一项，不同条件取交集；所有订阅条件须匹配同一份有效订阅（已开始、active、未到期），
窗口从订阅快照读取。正常且有 `billing.own` 的用户参与。
排除套餐完全由管理员显式选择，不依据可叠加属性或套餐名称推断加油包；某份订阅被排除，
用户仍可因另一份符合条件的订阅入选。兑换码来源不代表已付费；付费历史仍使用正金额购买记录。

批量操作使用 `target:{filter:AudienceFilter}`，发卡用 `target:{type:"filter",filter:AudienceFilter}`，
通知用 `target:{filter:AudienceFilter}`（需同时有 `users.write` 与 `billing.manage`）。
不能混用新旧字段。旧版通知用户列表与组列表仍取并集，空列表不会发送给所有人；新组合模式显式选择，空条件匹配所有正常且可使用账单的用户。
旧版显式订阅 ID 操作仍允许管理已停用用户；组合筛选与预估采用正常用户口径。
