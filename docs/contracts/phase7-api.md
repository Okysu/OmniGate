# 契约：Round 6（续）—— 图片接口、用户管理增强、套餐额度重置与批量延期

> 状态：**定稿，按此实现**。通用约定同前；所有 SQL 同时支持 PostgreSQL 与 SQLite（ADR-0009）。

## 1. 图片接口（OpenAI 兼容）

| 方法 | 路径 | 请求体 | 说明 |
| --- | --- | --- | --- |
| POST | `/v1/images/generations` | JSON | 文生图（`gpt-image-*`、`dall-e-*` 等） |
| POST | `/v1/images/edits` | `multipart/form-data` | 图片编辑：`image`（可多张，`image[]`）、可选 `mask`、`prompt`、`model`、`n`、`size`、`quality`、`background`、`output_format`、`response_format`、`stream` 等 |
| POST | `/v1/images/variations` | `multipart/form-data` | 图片变体（`dall-e-2`） |

- 只路由到 `openai` 类型渠道（与嵌入相同）；入口协议 `openai.images`（日志 `inbound` 分别为 `openai.images.generations` / `openai.images.edits` / `openai.images.variations`）。不做协议转换，Anthropic 渠道不提供图片接口。
- 网关只解析并改写 `model`：JSON 改写字段；multipart **流式重新编码**（其他字段与文件原样转发，不落盘），图片接口请求体上限 `64 MiB`（超出返回 413）。
- `stream: true`（`gpt-image-*` 支持）按 SSE 透传（`image_generation.partial_image` / `image_generation.completed` / `image_edit.*` 事件），用量取完成事件中的 `usage`。
- 渠道路由、归属优先（own → shared → platform）、重试、熔断、Key 策略、套餐配额与计费规则与其他接口相同。渠道插件的 `transformRequest` Hook 只作用于 JSON 请求；multipart 请求只执行 `signRequest`。
- `/v1/models` 不变；模型资料的 `capabilities` 新增 `imageGeneration: boolean`；模型广场的 `protocols` 新增 `openai.images`（openai 渠道提供该模型且资料标记了 `imageGeneration` 时）。

### 1.1 计价

售价 / 成本价新增两个可选字段（结算币种，十进制）：

| 字段 | 含义 |
| --- | --- |
| `perImage` | 每张输出图片的价格（按响应 `data` 数组长度或流式完成事件数计） |
| `imageInputPerM` | 图片输入 token 单价（每百万）；为空时按 `inputPerM` 计 |

费用 = `perRequest` + 文本输入 token × `inputPerM` + 图片输入 token × `imageInputPerM` + 输出 token × `outputPerM` + 图片张数 × `perImage`。
上游返回 `usage`（`input_tokens`、`output_tokens`、`input_tokens_details.{text_tokens,image_tokens}`）时按 token 计；没有 `usage` 时（如 `dall-e-3`）只按 `perImage` 与 `perRequest` 计。
预留余额：`perRequest` + `n`（默认 1）× `perImage` + 估算的文本输入，按可用余额封顶（与 Round 5 规则一致）。

- 请求日志新增 `imageCount`（输出图片张数），用量新增 `imageInputTokens`。
- 套餐配额新增计量 `images`（按输出图片张数计，可配合 `modelWeights`）。

## 2. 用户管理增强

现有能力（保留）：`GET /api/admin/users`、`PATCH /api/admin/users/{id}`（`role`、`status`），停用后会话立即失效、API Key 被网关拒绝。

### 2.1 停用原因与期限

`PATCH /api/admin/users/{id}` 新增字段：`disabledReason: string`（≤200，停用时必填）、`disabledUntil: string | null`（到期自动启用；null = 永久）。
- 被停用用户登录时跳转 `/login?error=account_disabled`，登录页显示原因与到期时间（如有）。
- 网关对停用用户的 Key 返回 `403 account_disabled`（消息含原因），不再返回 401。
- 后台任务每分钟启用到期的用户，审计 `user.auto_enable`。

### 2.2 用户详情

`GET /api/admin/users/{id}`（`users.read`）返回：

```ts
{
  user: User & { disabledReason: string | null; disabledUntil: string | null; lastLoginAt: string | null }
  identities: Array<{ provider: string; subject: string; email: string | null; createdAt: string }>
  wallet: { balance: string; reserved: string } | null
  subscriptions: Subscription[]          // 最近 20 条
  keys: Array<{ id; name; prefix; status; lastUsedAt; createdAt }>
  sessions: { active: number }
  usage30d: { requests: number; charge: string; tokens: number }
  channels: { own: number }
}
```

### 2.3 操作

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| POST | `/api/admin/users/{id}/logout` | `users.write` | 撤销该用户全部会话，返回 `{revoked}` |
| POST | `/api/admin/users/{id}/keys/disable` | `users.write` | 禁用该用户全部 API Key（可再由用户或管理员启用），返回 `{disabled}` |
| POST | `/api/admin/users/batch` | `users.write` | `{ids: string[] (1–200), action: 'disable' \| 'enable' \| 'logout', reason?: string, until?: string \| null}` → `{succeeded: string[], failed: Array<{id, code, message}>}`；逐个应用与单个操作相同的保护规则（不能停用自己、不能停用最后一位系统管理员） |

审计：`user.update`（已有）、`user.logout`、`user.keys_disable`、`user.batch`（另为每个用户写一条对应审计）。
停用 / 启用 / 强制下线会给用户发送通知事件 `account.status_changed`（站内 + 邮件，默认开启，不可关闭邮件之外的站内通知）。

## 3. 套餐额度重置与批量延期（发福利）

### 3.1 重置额度

`POST /api/admin/billing/subscriptions/reset-quota`（`billing.manage`）

```ts
{
  target: { ids: string[] } | { planId: string | null; status: 'active' }   // 指定订阅，或按套餐（null = 全部套餐）的全部有效订阅
  rules: string[] | null           // 只重置这些规则 id；null = 全部规则
  includeLifetime: boolean         // 默认 false：lifetime（总量）规则不重置，避免误把一次性额度清零
  note: string                     // ≤200，写入审计与用户通知
}
→ { affected: number; subscriptions: string[] }   // 最多返回前 500 个 id
```

语义：清空每份订阅所选规则的**当前窗口**用量——
- `calendar` / `period`：删除当前窗口的计数；
- `rolling`：删除窗口内的全部分桶；
- `session`：从重置时刻 A 重新开始一个空的会话窗口（锚定式重置，[phase11-api.md §1](phase11-api.md#1-锚定式重置anchored-reset)）：用量为 0，下一次刷新为 A + 窗口时长（如 5 小时窗口在 A + 5h 刷新），与原来的刷新时间无关；之后的用量计入这一窗口；
- `lifetime`：仅在 `includeLifetime` 时清零。
被阻断的用户立即恢复可用。审计 `subscription.quota_reset`（记录目标、规则与数量），每位受影响用户收到通知 `subscription.quota_reset`（站内 + 邮件，默认开启）。

### 3.2 批量延期

`POST /api/admin/billing/subscriptions/extend`（`billing.manage`）

```ts
{ target: 同上; duration: string /* 1h–366d */; note: string } → { affected: number; subscriptions: string[] }
```

把所选有效订阅的 `endsAt` 延后 `duration`。审计 `subscription.extend`，通知 `subscription.extended`。

### 3.3 预览

两个接口都支持 `?dryRun=true`：只返回 `affected` 与 id 列表，不做修改（界面在确认前显示“将影响 N 份订阅”）。

## 4. 补充约定（前后端对齐）

1. `GET /api/admin/users` 列表的每个用户同样包含 `disabledReason`、`disabledUntil`。
2. 被停用用户登录时跳转 `/login?error=account_disabled&reason=<URL 编码的原因>&until=<RFC 3339，永久时省略>`。
3. 批量与单个操作的错误码：`cannot_disable_self`（不能停用 / 强制下线自己）、`last_admin`（不能停用最后一位系统管理员）、`not_found`。
4. 重置与延期的 `note` 可为空字符串。`dryRun` 的结果取决于全部参数（包括 `rules`、`includeLifetime`：没有可重置规则的订阅不计入 `affected`）。
5. 启用用户（`status: "active"`）时服务端清空 `disabledReason` 与 `disabledUntil`。
6. 价格的 `perImage` 省略或为 `null` 表示 0；`imageInputPerM` 省略或为 `null` 表示按 `inputPerM` 计。响应中二者分别为十进制字符串与 `string | null`。
7. 模型广场 `price` 对象额外包含 `perImage: string | null`、`imageInputPerM: string | null`（未设置时为 null）。
8. 用量中 `input` 为**全部**输入 token（含图片），`imageInputTokens` 是其中的图片部分；计费时文本输入 = `input − imageInputTokens`。
9. `account.status_changed` 归入新类别“账户”，属于告警类（不进入每日摘要）；站内通知始终开启，邮件 / Webhook 可关闭。
   `subscription.quota_reset`、`subscription.extended` 默认邮件 + 站内开启、Webhook 关闭。

## 5. 实现差异

实现（`server/`，迁移 `00011_images_users`）与上文的出入和补充细节如下，其余按契约实现。

| # | 条款 | 契约 | 实现 | 原因 |
| --- | --- | --- | --- | --- |
| 1 | §1 multipart “不落盘” | 流式重新编码，不落盘 | 请求体在解析时缓存一份：8 MiB 以内在内存，超过部分写入 `os.TempDir()`（`$TMPDIR`）下权限 0600 的临时文件 `omnigate-image-*.part`，请求结束（成功、失败或客户端断开）即删除。每次尝试（含换渠道重试、路由规则的回退模型）都从缓存经 `io.Pipe` + `multipart.Writer` 流式重新编码，各部分的头与字节原样复制，只替换 `model`；编码是确定的，先计算长度后带 `Content-Length` 发送 | 路由前必须读完请求体才能拿到 `model`（字段可能在文件之后），且重试要能重放请求体；全部放内存时 64 MiB × 并发数风险过高 |
| 2 | §1 请求体格式 | generations 为 JSON，edits / variations 为 multipart | 三个接口都按 `Content-Type` 处理：`multipart/form-data` 走 multipart，其余按 JSON 透传（OpenAI 的 edits 也接受 JSON） | 兼容上游新形态，不影响契约内的用法 |
| 3 | §1 multipart 字段 | — | `model`、`prompt`、`n`、`stream` 四个文本字段在解析时读取，单个上限 1 MiB（超出 400）；缺少边界、格式错误返回 400 `invalid_request`，缺少 `model` 返回 400 | 防止异常大的文本字段占用内存 |
| 4 | §1 Hook | multipart 只执行 `signRequest` | `signRequest` 收到的 `body` 为 `{}`（不是原始数据），可修改 `path` 与请求头，返回的 `body` 被忽略；JSON 图片请求照常执行 `transformRequest` + `signRequest`，`dialect` 为入口名（`openai.images.*`） | 让按 JSON 编写的签名 Hook（读取 `req.body.xxx`）不因 `null` 报错 |
| 5 | §1 `stream: true` | 用量取完成事件中的 `usage` | 图片张数 = `*.completed` 事件数；多个完成事件都带 `usage` 时每个字段取最大值（上游未说明是逐张还是累计，取最大值不会多算）；`error` 事件视为结束。上游对流式请求返回 JSON（如不支持流式的 `dall-e-*`）时按非流式响应处理 | 规避重复计费；兼容不支持流式的模型 |
| 6 | §1.1 无 `usage` | 只按 `perImage` 与 `perRequest` 计 | 同契约，token 数记 0，且**不做估算**（其他接口无用量时按字节估算） | — |
| 7 | §1.1 预留 | `perRequest + n × perImage + 估算的文本输入` | `n` 缺省或非法按 1，上限按 100 计；文本输入按 `prompt` 字节数 / 4 估算（multipart 不把文件字节计入）；非流式响应体上限沿用 64 MiB | 防止异常 `n` 造成过大的预留 |
| 8 | §2.1 停用 | 停用后会话立即失效 | 停用时同时在库中吊销该用户全部会话（`revoked_reason = admin_disabled`），重新启用后旧会话也不会恢复；`disabledReason` 去掉首尾空白后 1–200 字符，`disabledUntil` 必须晚于当前时间（422，`details` 键为 `disabledReason` / `disabledUntil`）；未停用时提交这两个字段返回 422；已停用的用户再次提交 `status: disabled` 会更新原因与期限 | 防止会话在启用后“复活”；表单校验 |
| 9 | §2.1 到期 | 后台任务每分钟启用 | 任务每分钟（及启动时）执行，审计 `user.auto_enable`（无操作人）并发送 `account.status_changed`。到期但任务尚未执行的这段时间里，登录、会话与 API Key 已按启用处理 | 避免到期后最多一分钟的误拒绝 |
| 10 | §2.1 网关 | 停用用户的 Key 返回 `403 account_disabled` | 只有 Key 本身有效（启用、未过期）时才返回 403（错误类 `account_disabled`，OpenAI 格式 `type: invalid_request_error`、`code: account_disabled`，Anthropic 格式 `permission_error`；消息含原因与到期时间）；无效、吊销、停用或过期的 Key 仍返回 401。`/v1/models` 与 `count_tokens` 同样适用 | 不向持有无效 Key 的人泄露账号状态 |
| 11 | §2.2 详情 | — | `wallet` 在用户没有钱包记录时为 `null`；`keys` 不含已吊销的 Key，按创建时间倒序；`sessions.active` 为未吊销、未过期且未空闲超时的会话数；`usage30d.requests` 含失败请求，`charge` 为钱包扣费合计（不含套餐抵扣），`tokens` = 输入 + 输出 + 缓存读 + 缓存写 | 契约未细化 |
| 12 | §2.3 单个操作 | — | `logout` 对自己返回 409 `cannot_disable_self`，用户不存在返回 404；`keys/disable` 只统计原本启用的 Key（审计 `user.keys_disable`，不发通知） | 与批量规则一致 |
| 13 | §2.3 批量 | 逐个应用保护规则 | 批量操作不做乐观锁；`ids` 中重复的只处理一次，非法 UUID 记为 `not_found`；启用已启用的用户算成功（不写审计、不发通知）；`failed[].message` 为中文说明。缺少 `reason`、`until` 已过、`ids` 数量不在 1–200、`action` 非法时整体返回 422（`details` 键 `reason` / `until` / `ids` / `action`）。`last_admin` 只在被操作的系统管理员是最后一位“启用状态”的系统管理员时出现（操作人本身也是启用的系统管理员，正常情况下不会触发） | — |
| 14 | §2 通知 | `account.status_changed` | 停用用户也会收到（站内 + 邮件 + Webhook，是唯一会投递给停用用户的事件）；严重程度：停用 / 强制下线为 `warning`，启用为 `info`；`data` 为 `{action: disabled \| enabled \| logout, auto, reason?, until?}` | 让用户知道被停用的原因 |
| 15 | §3.1 / §3.2 目标 | `{ids}` 或 `{planId, status: 'active'}` | 只作用于状态为 active 且 `endsAt` 晚于当前时间的订阅（`ids` 中已取消、已过期的订阅被忽略，不报错）；`ids` 1–1000 个；`planId` 不存在返回 404；返回的 `subscriptions` 按订阅创建时间升序 | 防止误操作已结束的订阅 |
| 16 | §3.1 重置 | 清空当前窗口用量 | 统一实现为：删除所选规则中 `window_start` 不早于“当前窗口起点”的计数行（calendar / period：当前窗口；rolling：窗口内全部分桶；session：当前会话行，随后写入 `window_start` = 重置时刻（截断到秒）、`used = 0` 的锚点行作为新的当前会话（Round 11 起，见 phase11-api.md §1；此前为“下一个请求开启新窗口”）；lifetime：全部）。`rules` 为 `null` 或 1–50 个规则 ID（空数组返回 422）。审计 `subscription.quota_reset` 一条（`resourceId` 为空，metadata 含 `target`、`rules`、`includeLifetime`、`note`、`affected` 与前 500 个 ID） | — |
| 17 | §3 通知 | 每位受影响用户收到通知 | 每次操作每位用户一条（多份订阅合并在一条里），`data` 为 `{subscriptions: [{subscriptionId, planName, endsAt, rules?}], note, duration?}` | 避免批量操作刷屏 |
| 18 | §3.2 时长 | `1h–366d` | 与套餐时长同一解析器：`<正整数><m\|h\|d>`，范围 1h–366d（因此也接受不少于 60 分钟的 `m`，如 `90m`），422 `details.duration` | 复用现有校验 |
| 19 | §3.3 预览 | `?dryRun=true` | 也接受 `?dryRun=1`；预览同样做完整校验（422 / 404），不写审计、不发通知 | — |
| 20 | §1 `/v1/models` | 不变 | 不变；模型广场只有在 openai 渠道提供该模型**且**模型资料勾选了 `imageGeneration` 时才列出 `openai.images` | 同契约 |
