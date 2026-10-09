# 契约：Round 5（续）—— 渠道归属优先与计费、模型资料、模型广场

> 状态：**定稿，按此实现**。通用约定同前。

## 1. 渠道归属：路由顺序与计费

相对于发起请求的用户，每个可用渠道属于以下三档之一：

| 档位 | 判定 | 是否计费 |
| --- | --- | --- |
| `own` 自有 | 渠道所有者就是该用户 | **不计费**：不预留、不扣钱包、不计入套餐额度 |
| `shared` 共享 | 所有者是其他**普通用户**（角色不是 `system_admin` / `channel_admin`），共享给了该用户 | **不计费**（上游费用由共享者承担） |
| `platform` 平台 | 所有者是平台管理员（`system_admin` / `channel_admin`），作用域为 `global` 或共享给该用户 | 按售价计费、计入套餐配额（与现在相同） |

> 安全修订（§5）：用户之间的共享需被共享者**接受**后才生效，按用户组共享只有渠道管理员可以设置；下表的 `shared` 档只包含这些生效的共享。

路由顺序：**own → shared → platform**。每一档内部仍按 优先级 → 协议匹配度 → 权重。
- 管理员的**路由规则**只作用于 `platform` 档（`targets`、`strategy`、`protocolPreference`、`retry`）；
  `own` / `shared` 档每个可用渠道最多尝试一次，按通用的重试分类决定是否继续。
- 规则的 `fallbackModels` 在请求模型的**所有档位**都失败后才生效；回退模型同样按 own → shared → platform 选择。
- 全局尝试上限 10 次不变。
- **计费检查延后**：套餐配额判断（`Decide`）和钱包预留，推迟到**第一次尝试 platform 档渠道之前**才执行。
  请求完全由 own/shared 渠道完成时，即使余额为 0、套餐用完也能成功。前面的 own/shared 尝试都失败、
  而 platform 档被配额或余额拦下时，返回 429 / 402（与现在相同）。
- 请求日志新增 `channelTier: "own" | "shared" | "platform"`（实际提供服务的渠道所属档位；失败请求取最后一次尝试的渠道）。
  own/shared 请求的 `charge` 与 `quotaCharge` 为 0，`cost` 为 null。
- 管理员自己的渠道对管理员本人是 `own`。
- `/v1/models` 不变（返回所有可用模型的并集）。

## 2. 模型资料（管理员）

管理员可以为逻辑模型补充展示信息，用于模型广场。没有资料的模型照常可用，广场中只显示模型名。

```ts
interface ModelInfo {
  model: string                      // 逻辑模型名（主键）
  displayName: string                // ≤100，空 = 用模型名
  description: string                // ≤1000，纯文本
  vendor: string                     // ≤50，如 "DeepSeek"、"OpenAI"
  tags: string[]                     // ≤10 个，每个 ≤20
  contextWindow: number | null       // token
  maxOutput: number | null
  capabilities: { vision: boolean; tools: boolean; reasoning: boolean; embedding: boolean }
  hidden: boolean                    // true：不在任何模型广场中显示（仍可调用）
  sortOrder: number                  // 越小越靠前，默认 0
  version: number
  updatedAt: string
}
```

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/admin/model-info` | `models.manage` | 全部资料，`{items}` |
| PUT | `/api/admin/model-info/{model}` | `models.manage` | 新建或修改（修改时带 `version`），模型名需 URL 编码 |
| DELETE | `/api/admin/model-info/{model}` | `models.manage` | 删除资料，204 |

- `GET /api/admin/model-info` 只返回已有资料的条目；模型资料页的列表 = 所有渠道提供的逻辑模型（`GET /api/models?scope=all`）∪ 已有资料的模型（可能已没有渠道）。
- 422 `details` 的键：`displayName`、`description`、`vendor`、`tags`、`tags[0]`、`contextWindow`、`maxOutput`、`sortOrder`；`capabilities` 只接受四个布尔字段。

审计：`model_info.update`、`model_info.delete`。

## 3. 模型广场

### 3.1 平台模型广场

`GET /api/plaza/models` —— 平台提供的模型：由 **作用域为 `global` 的 platform 档渠道**提供、且未隐藏的逻辑模型。
- 系统设置新增 `site.publicModelPlaza: boolean`（默认 `true`）：为 `true` 时匿名可访问（落地页 `/models` 使用）；
  为 `false` 时需要登录，否则 401。
- 不暴露渠道名称、渠道数量与成本价。

```ts
interface PlazaModel {
  model: string
  displayName: string
  description: string
  vendor: string
  tags: string[]
  contextWindow: number | null
  maxOutput: number | null
  capabilities: { vision: boolean; tools: boolean; reasoning: boolean; embedding: boolean }
  protocols: string[]                // 可用的客户端协议，按模型资料的能力决定：对话模型（工具 / 视觉 / 推理，或未标记任何专用能力）为 openai.chat、openai.responses、anthropic.messages；标记了嵌入 / 图片生成 / 音频且有 openai 渠道时另有 openai.embeddings / openai.images / openai.audio；纯图片或纯嵌入模型只列出自己的接口
  price: {                           // 当前生效的售价（结算币种）；null = 未定价（不收费）
    inputPerM: string; outputPerM: string; cacheReadPerM: string | null; cacheWritePerM: string | null
  } | null
  plans: Array<{ id: string; name: string }>   // 覆盖该模型的 active 套餐（套餐 models 为空即覆盖全部模型）
}
```

响应 `{items: PlazaModel[], currency: {code, symbol, decimals}}`（`currency` 与 `/api/system/info` 中的结构相同），按 `sortOrder`、模型名排序。
`publicModelPlaza=false` 且未登录时返回标准错误信封 `401 unauthenticated`。

### 3.2 我的模型

`GET /api/plaza/mine`（登录用户）—— 当前用户可以调用的全部模型（平台 + 自有 + 共享，包括平台中非 global 但共享给他的渠道）。
每项为 `PlazaModel`（隐藏的模型也返回，因为用户可以调用）加上：

```ts
{
  sources: { own: number; shared: number; platform: number }   // 各档可用渠道数
  billing: 'free' | 'platform'       // own 或 shared 渠道数 > 0 → 'free'（优先使用，不计费；失败回退到平台时按 price 计费）
  subscription: { id: string; planName: string } | null      // 覆盖该模型的有效订阅（最早到期的一份）
}
```

### 3.3 前端页面

- 落地站点 `/models`：公开的平台模型广场（落地页顶栏增加“模型广场”入口；`publicModelPlaza=false` 时提示登录）。
- 控制台 `/console/plaza`“模型广场”（所有用户）与 `/console/my-models`“我的模型”（所有用户）。
- 现有 `/console/models` 改名为“模型管理”（需要 `models.manage`），在“模型列表 / 价格”之外增加“模型资料”标签页。
- 去掉控制台顶栏的“任务中心”和系统状态绿点。

## 4. 实现差异

后端实现（`server/internal/gateway`、`server/internal/channel`、`server/internal/plaza`、迁移 `00009_model_info_tiers`）与上文的出入及未明确之处的取舍：

| 主题 | 契约 | 实现 |
| --- | --- | --- |
| 档位判定 | own / shared / platform | 按渠道快照中所有者的**当前**角色：所有者 = 调用者 → `own`；所有者是 `system_admin` / `channel_admin` → `platform`；其余 → `shared`。因此普通用户（例如被降级的管理员）名下的 `global` 渠道对其他用户算 `shared`（免费）。快照每 15 秒刷新，管理员修改用户角色 / 状态后本实例立即刷新。 |
| own / shared 的重试 | 每个渠道最多一次，按通用重试分类 | 用系统设置 `gateway.retryOn` 判断（即使命中了规则，规则的 `retry` 只用于 platform 档）；own / shared 尝试不计入 `maxAttempts`（规则或 `gateway.maxAttempts`，均为每个模型的 platform 尝试上限），但计入全局 10 次上限。 |
| 计费检查时机 | 第一次尝试 platform 渠道之前 | 进入 platform 档时（检查熔断之前）执行一次 `Decide` + 预留；platform 渠道全部熔断时预留在请求结束时释放。请求模型的 platform 档被配额 / 余额拦下时直接返回 429 / 402，**不再尝试回退模型**（与之前相同）；回退模型被拦下时跳到下一个回退模型。 |
| 预留的释放 | — | 请求模型已在 platform 档预留、最终由回退模型的 own / shared 渠道完成时，释放预留、不扣费、不计配额（日志 `subscriptionId`、`sellPriceId` 清空）。 |
| 请求日志 | `channelTier` | 新列 `request_logs.channel_tier`（两种数据库），没有尝试任何渠道的请求与升级前的旧日志为 null。own / shared 请求存储的成本为 0，列表中 `cost` 恒为 null（即使有 `stats.all`）。`fallbackPath` 中的单次尝试不带档位。 |
| 预览 | 每个候选显示档位 | `RoutePreviewCandidate` 增加 `tier`，`RoutePreview` 增加 `tierOrder: ["own","shared","platform"]`；规则 targets 中该用户无权使用的渠道也标出其相对档位。 |
| `count_tokens` | — | `/v1/messages/count_tokens` 不计费，仍按原来的顺序（不分档）选择 anthropic 渠道。 |
| 模型资料 PUT | 新建或修改 | 新建返回 `201`、修改返回 `200`。整体替换：省略的字段取默认值。已存在却未带 `version`、`version` 不符、或带了 `version` 但资料不存在（已被删除），都返回 `409 version_conflict`。 |
| 模型资料校验 | 列出的 `details` 键 | 另有 `model`（路径中的模型名：1–128 个字符、不能有首尾空格）与 `hidden`（非布尔）。字符串去掉首尾空格；`tags` 丢弃空标签并不区分大小写去重（保留第一次的写法），去重后超过 10 个报 `tags`，单个超长报 `tags[i]`（i 为提交时的下标）；`contextWindow` / `maxOutput` 为 1 到 2^53−1 的整数或 null，且 `maxOutput` 不能超过 `contextWindow`（报 `maxOutput`）；`sortOrder` 为 −999999999 到 999999999 的整数（与前端一致）。长度按 Unicode 字符计。 |
| 模型资料审计 | `model_info.update`、`model_info.delete` | `model_info.update` 的 `metadata` 为 `{created, before, after}`（新建时 `before` 为 null）；`model_info.delete` 为 `{before}`。 |
| 广场 `price` | `cacheReadPerM` / `cacheWritePerM` 可为 null | 价格表不区分“未设置”与 0，缓存单价为 0 时返回 null；每次请求固定费用（`perRequest`）不在广场中显示。 |
| 广场 `protocols` | 三种协议，openai 渠道另有 embeddings | 平台广场看 global platform 渠道，“我的模型”看该用户可用的全部渠道：其中有 `openai` 渠道时追加 `openai.embeddings`。顺序固定为 `openai.chat`、`openai.responses`、`anthropic.messages`、`openai.embeddings`。 |
| 广场 `plans` | 覆盖该模型的 active 套餐 | 按套餐创建时间排序；已归档的套餐不显示。 |
| 广场 `displayName` | — | 没有资料或显示名为空时为 `""`（由前端回退为模型名）。 |
| 广场的渠道范围 | 已启用渠道 | 只计入已启用、且固定的插件处于启用状态的渠道（与网关一致）；熔断中的渠道照常计入。“我的模型”不考虑 API Key 策略。 |
| `/api/plaza/models` 认证 | 公开 / 需登录 | 带有效会话 Cookie 时识别登录状态（无效会话视同匿名）；登录用户看到的内容与匿名访问相同。 |
| 测试钩子 | — | `app.Options.UpstreamTransport`（仅测试）替换所有渠道的上游网络策略，使普通用户的渠道也能访问本机的假上游；生产代码不设置。 |

前端对照（`web/`，未修改）：`SystemInfo` 类型尚未声明 `publicModelPlaza`（后端已返回）；`RouteCandidate` / `RoutePreview` 类型尚未声明
`tier` / `tierOrder`，路由预览界面暂不显示档位。其余字段（模型资料、广场、我的模型、请求日志 `channelTier`、系统设置
`site.publicModelPlaza`）与 `web/src/lib/types.ts` 一致。

## 5. 安全修订（共享需接受）

> 状态：**定稿，按此实现**（2026-10-09，安全审计 HIGH 级问题的修复）。本节修订 §1 与 phase8-api.md §1.2 中共享的生效条件。

**问题**：修订前，任何普通用户都可以把自己的渠道单方面共享给任意用户或用户组（默认用户组的 id 固定、可被猜到），而路由顺序是
own → shared → platform。于是 alice 可以建一个指向自己服务器的渠道、共享给默认用户组，此后所有用户对该模型的请求都**先**发到 alice 的服务器，
她能读取并篡改提示词和响应。

**修订**：

1. **用户之间的共享需要对方接受**。共享给用户的渠道，只有被共享者**接受**后才对其生效；待接受、已拒绝的共享不授予任何使用权。
   这对所有渠道都一样（包括管理员名下的渠道、以及管理员修改他人渠道时添加的用户）。
2. **共享给用户组只有渠道管理员（`channels.manage`）可以设置**；按组共享不需要接受（管理员的决定）。
3. **路由顺序不变**：own → shared（已接受的用户共享、管理员设置的组共享）→ platform。被共享者是**主动选择**接受的，可以随时退出。
4. 渠道所有者被停用后，他的渠道的全部共享（用户与用户组）立即停止生效；重新启用后恢复（共享记录保留）。

### 5.1 数据

`channel_shares` 增加 `status`（`pending` | `accepted` | `declined`）与 `responded_at`（接受 / 拒绝 / 退出的时间，待接受时为 null）；
`created_at` 为（最近一次）邀请时间。迁移 `00015_share_acceptance`（PostgreSQL 与 SQLite）：已有的共享记录视为 `accepted`（升级前已经生效，
保持行为不变）。回滚（Down）删除所有未接受的记录后去掉这两列——旧版本把每条记录都当作已生效的共享。

### 5.2 所有者一侧（`/api/channels`）

- `sharedWith.users` 的含义变为“**邀请**的用户”：待接受 + 已接受（不含已拒绝的）。提交 `sharedWith` 时：
  - 新出现的用户 → 新的待接受邀请，并通知对方（`channel.share_invited`，见 §5.5）；
  - 已拒绝（或已退出）的用户再次出现在列表中 → **重新邀请**：状态回到 `pending`、邀请时间更新、`respondedAt` 清空，再次通知；
  - 已在列表中的待接受 / 已接受用户保持原状态；
  - 不在列表中的待接受 / 已接受用户被移除；已拒绝的记录保留（所有者可以看到对方拒绝了），只在渠道不再是 `shared` 作用域或被删除时一并删除。
- 完整视图（所有者与 `channels.manage`）增加 `shares`，逐个列出被邀请用户的状态（按邀请时间排序）：

```ts
interface ChannelShare {
  userId: string
  displayName: string
  status: 'pending' | 'accepted' | 'declined'
  createdAt: string           // 邀请时间
  respondedAt: string | null  // 接受 / 拒绝 / 退出的时间
}
```

- `sharedWith.groups`：只有持有 `channels.manage` 的调用者可以**添加**用户组；其他调用者提交了渠道当前没有的组时返回
  `422 validation_failed`，`details["sharedWith.groups"] = "只有渠道管理员可以共享给用户组"`。原样提交已有的组、或移除已有的组都允许
  （普通用户修改被管理员共享给组的自有渠道时不会被拒绝）。
- 精简视图（被共享者看到的 `Summary`）不变，不含 `shares`。

### 5.3 被共享者一侧（新接口）

```ts
interface IncomingShare {
  channelId: string
  name: string
  type: 'openai' | 'anthropic' | 'custom'
  channelStatus: 'enabled' | 'disabled'
  owner: { id: string; displayName: string }
  models: string[]            // 逻辑模型名（不含上游模型名、地址、配置与任何密钥）
  status: 'pending' | 'accepted'
  createdAt: string           // 邀请时间
  respondedAt: string | null
}
```

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/channel-shares` | `channels.read` | 共享给当前用户的渠道：`{items: IncomingShare[]}`，待接受的在前，各自按邀请时间倒序；不含已拒绝的 |
| POST | `/api/channel-shares/{channelId}/accept` | `channels.read` | 接受待接受的邀请，返回 `IncomingShare`（已接受时幂等返回 200） |
| POST | `/api/channel-shares/{channelId}/decline` | `channels.read` | 拒绝待接受的邀请，204；记录标记为 `declined`（所有者看到“已拒绝”） |
| POST | `/api/channel-shares/{channelId}/leave` | `channels.read` | 退出已接受的共享，204；记录标记为 `declined`，渠道立即对该用户不可用 |

- 没有（待接受或已接受的）共享记录时返回 `404 not_found`；状态不符（拒绝已接受的共享、退出待接受的邀请）返回
  `409 share_state_conflict`（请改用 `leave` / `decline`）。
- 接受、拒绝、退出后立即重新加载本实例的渠道快照（其他实例最多 15 秒）。
- 审计：`channel.share_accept`、`channel.share_decline`、`channel.share_leave`（操作者为被共享者，资源为渠道，`metadata` 含 `name`、`ownerId`）。
  所有者一侧的 `channel.create` / `channel.update` 的 `metadata` 增加 `invited`（本次新发出邀请的用户 id 列表，没有时省略）。

### 5.4 “可用”的判定

渠道对用户可用 ⇔ 渠道已启用（且固定的插件已启用），并且满足其一：用户是所有者；作用域 `global`；作用域 `shared` 且
（用户的共享状态为 `accepted`，或用户所在的组在 `sharedWith.groups` 中）且**所有者未被停用**。

这一判定同时用于：网关候选渠道与 `/v1/models`、路由预览（`POST /api/admin/routes/preview`）、我的模型（`GET /api/plaza/mine`）、
渠道列表与详情对被共享者的可见性（待接受的邀请只出现在 `GET /api/channel-shares` 中）、以及按“可用用户”发送的通知。
API Key 策略的 `allowedChannels` 仍然先行收窄：不在其中的共享渠道永远不会被选中。

### 5.5 通知 `channel.share_invited`

| 事件 | 类别 | 默认开关 | 说明 |
| --- | --- | --- | --- |
| `channel.share_invited` | 渠道共享（新类别 `share`，所有用户都有资格接收） | 仅站内开（邮件、Webhook 关） | info 级别、非告警类；接收者为被邀请的用户（已停用的用户不发送）；链接 `/console/channels?tab=shared`；`data` 含 `channelId`、`channelName`、`owner {id, displayName}`、`models` |

每次邀请（含重新邀请）一个去重键；同一渠道对同一用户 1 小时内最多通知一次（反复重新邀请仍会产生待接受的邀请，但不重复通知）。

### 5.6 路由与隐私说明（面向被共享者的提示）

- 已接受的共享渠道排在平台渠道**之前**尝试，并且**不计费**（上游费用由所有者承担）。
- 所有者看不到被共享者账户的任何信息（钱包、Key、请求日志都看不到），但**请求内容会经过所有者配置的上游**：
  所有者能看到、也可能修改经由他的渠道发送的提示词与响应。只接受你信任的人的共享；可以随时“退出共享”。
- 控制台“渠道”页的“共享给我的”标签页（`/console/channels?tab=shared`）列出待接受的邀请（接受 / 拒绝，接受前显示上述隐私提示）与已接受的共享（退出共享）。

### 5.7 升级注意

升级前由普通用户设置的**用户组共享**仍然有效（无法区分是否由管理员设置）。升级后请管理员检查：

```sql
SELECT c.id, c.name, u.display_name AS owner, gs.group_id
FROM channel_group_shares gs JOIN channels c ON c.id = gs.channel_id JOIN users u ON u.id = c.owner_id
WHERE u.role NOT IN ('system_admin', 'channel_admin');
```

对不认可的记录，在渠道表单中移除该用户组（或把渠道改为私有）。
