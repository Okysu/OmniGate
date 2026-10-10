# 契约：Round 12 —— 会话亲和、会话请求头透传、提示词缓存命中统计

> 状态：**已实现**。通用约定同前；所有 SQL 同时支持 PostgreSQL 与 SQLite（迁移 `00019_session_affinity`）。
> 需求原文：“提示词缓存命中率低”：上游网关 / 号池按客户端原生的会话标识把会话留在同一个账号上，而 OmniGate 丢弃了这些标识，
> 自己的路由又把一段对话分散到多个渠道。规则模型参照 new-api 的“渠道亲和（channel affinity）”，JSON 结构相同，模板可直接复制。
> 设计是透明的：透传客户端自己的标识并把会话固定到渠道，不针对任何具体上游；客户端没有标识时补全的也只是 OpenAI / Codex 的标准标识
> （`prompt_cache_key`、`Session_id`，§2.6）。

## 1. 设置 `gateway.affinity`

会话亲和是一个系统设置（[phase4-api.md §3](phase4-api.md)），键为 `gateway.affinity`，值是一个 JSON 对象，**字段名与 new-api 相同（snake_case）**：
读写都走 `GET` / `PATCH /api/admin/settings`（`settings.write`），PATCH 时整体替换，`null` 恢复默认值；修改写入审计 `settings.update`
（`metadata.before` / `after` 为整份设置）；`sources["gateway.affinity"]` 为 `default` 或 `db`（没有环境变量层）。

```ts
{
  enabled: boolean                  // 默认 true；false = 不匹配任何规则（不固定渠道、不透传请求头）
  session_mode: 'off' | 'prefer' | 'strict'  // 默认 'prefer'；规则 session_mode 为 inherit（或旧版空值）时使用
  switch_on_success: boolean        // 默认 true：会话由其他渠道成功处理时把绑定改到该渠道
  keep_on_channel_disabled: boolean // 默认 false：绑定的渠道不是候选时保留绑定（见 §2.3）
  max_entries: number               // 1–1 000 000，默认 100 000：内存中的绑定上限
  default_ttl_seconds: number       // 1–2 592 000（30 天），默认 3600
  rules: Rule[]                     // ≤50 条，按顺序匹配
}

Rule {
  name: string                      // 必填，≤64，唯一
  model_regex: string[]             // 客户端请求的逻辑模型，任一匹配；[] = 任意（≤32 个，每个 ≤512 字符，Go RE2 语法）
  path_regex: string[]              // 请求路径（/v1/v1/… 已折叠为 /v1/…），任一匹配；[] = 任意
  user_agent_include: string[]      // 可选，不区分大小写的子串，任一包含；[] = 不限
  key_sources: KeySource[]          // 1–16 个
  value_regex: string               // 可选：会话值必须匹配
  ttl_seconds: number               // 0–2 592 000；0 = default_ttl_seconds
  param_override_template: { operations: Operation[] } | null   // 只支持 pass_headers（≤8 个操作）
  skip_retry_on_failure: boolean    // new-api 旧字段：session_mode 为空时 true = strict
  session_mode: '' | 'inherit' | 'off' | 'prefer' | 'strict'
  include_using_group: boolean      // 绑定键包含用户的用户组 id
  include_model_name: boolean       // 绑定键包含请求的逻辑模型
  include_rule_name: boolean        // 绑定键包含规则名称
  inject_prompt_cache_key: boolean  // OmniGate 扩展，默认 false：OpenAI 格式上游请求体缺少时补全 prompt_cache_key（§2.6）
  inject_session_header: string     // OmniGate 扩展，默认 ''（关闭）：OpenAI 格式上游请求缺少该请求头时补全（§2.6）
}

KeySource = { type: 'gjson', path: string }          // 请求体 JSON 路径（gjson 语法，≤256）
          | { type: 'request_header', key: string }  // 请求头名称（字母、数字、- 和 _，≤64）
          | { type: 'anchor' }                       // OmniGate 扩展：对话锚点（§2.1.1），不接受 key / path

Operation = { mode: 'pass_headers', value: string[] | string, keep_origin: boolean }  // value 1–64 个请求头；字符串按逗号分隔
```

校验（`422`，`details["gateway.affinity"]` 为中文消息，列出出错字段路径，如 `rules[0].key_sources[0].type：…`，最多 5 处）：

- new-api 内部的 Key 来源类型 `context_int`、`context_string` 被拒绝（OmniGate 没有对应的请求上下文），其他未知类型同样拒绝；
  `anchor` 带 `key` 或 `path` 被拒绝；
- `inject_session_header` 非空时必须是合法的请求头名称，且不能是下面列出的禁止透传的请求头；
- `param_override_template` 中 `pass_headers` 以外的操作（`set`、`delete`、`move` …）、`operations` 以外的顶层键（new-api 旧版的扁平参数覆盖）
  以及操作上的其他非空字段（如 `conditions`）被拒绝；
- 正则编译失败、名称重复、超出数量限制被拒绝；
- 透传请求头与渠道 `config.headers` 规则相同：名称格式校验，且禁止 `authorization`、`x-api-key`、`cookie`、`host`、`content-length`、
  `connection`、`transfer-encoding`、`te`、`upgrade`、`keep-alive`、`proxy-authorization`、`proxy-connection`、`content-type`、`accept-encoding`。

宽松之处（保证 new-api 的 JSON 可直接粘贴）：未知字段忽略；`null` 数组视为 `[]`；缺少的全局字段取默认值；缺少 `rules` 视为 `[]`；
缺少 OmniGate 扩展的两个规则选项（`inject_prompt_cache_key`、`inject_session_header`）时为关闭。
保存后的值是规范化的形式（数组从不为 `null`，正则去空白与重复）。

**生效的会话保持模式**：规则 `session_mode` 为 `off` / `prefer` / `strict` 时用规则自己的；为 `inherit` 时用全局；为空（new-api 旧版）时
`skip_retry_on_failure: true` 为 `strict`，否则用全局。

### 1.1 默认值（内置预设）

默认开启，按顺序带三条规则。第一条生效的规则胜出，所以 `gpt session` 排在最前：GPT 模型无论从哪个入口（`/v1/chat/completions`、
`/v1/responses`、`/v1/messages`，如 Claude Code 调用 gpt-* 模型）进来都由它处理；后两条（与 new-api 的两条规则相同，放宽了模型匹配，
这些 Key 来源只有这两个客户端会发送）实际上只处理其他模型。

`gpt session`：

| 字段 | 值 |
| --- | --- |
| `model_regex` | `["^gpt-"]` |
| `path_regex` | `[]`（任意路径） |
| `key_sources` | gjson `prompt_cache_key` → 请求头 `Session_id` → 请求头 `Session-Id` → gjson `metadata.user_id` → 请求头 `X-Claude-Code-Session-Id` → gjson `user` → `anchor` |
| `pass_headers`（`keep_origin: true`） | 与 `codex cli trace` 相同的 16 个 Codex 请求头 |
| `inject_prompt_cache_key` | `true` |
| `inject_session_header` | `"Session_id"` |
| `session_mode` / 作用域 | `prefer`；`include_using_group`、`include_rule_name`；不含模型 |

说明：Claude Code 的 `metadata.user_id` 含会话 id，原样使用（绑定键是哈希）；Chat 的 `user` 是终端用户 id 而不是会话，放在对话锚点之前作为兜底
（同一终端用户的流量留在同一渠道）；`previous_response_id` 每轮都变，**不**作为来源。

`codex cli trace` / `claude cli trace`：

| | `codex cli trace` | `claude cli trace` |
| --- | --- | --- |
| `model_regex` | `[".*"]` | `[".*"]` |
| `path_regex` | `["^/v1/responses"]` | `["^/v1/messages"]` |
| `key_sources` | gjson `prompt_cache_key` → 请求头 `Session_id` → 请求头 `Session-Id` | gjson `metadata.user_id` → 请求头 `X-Claude-Code-Session-Id` |
| `pass_headers`（`keep_origin: true`） | `Originator`, `Session_id`, `Thread_id`, `Session-Id`, `Thread-Id`, `X-Client-Request-Id`, `User-Agent`, `X-Codex-Beta-Features`, `X-Codex-Turn-State`, `X-Codex-Turn-Metadata`, `X-Codex-Window-Id`, `X-Codex-Parent-Thread-Id`, `X-OpenAI-Subagent`, `X-OpenAI-Memgen-Request`, `X-ResponsesAPI-Include-Timing-Metrics`, `X-OpenAI-Internal-Codex-Responses-Lite` | `X-Stainless-Arch`, `X-Stainless-Lang`, `X-Stainless-Os`, `X-Stainless-Package-Version`, `X-Stainless-Retry-Count`, `X-Stainless-Runtime`, `X-Stainless-Runtime-Version`, `X-Stainless-Timeout`, `User-Agent`, `X-App`, `Anthropic-Beta`, `Anthropic-Dangerous-Direct-Browser-Access`, `Anthropic-Version`, `X-Claude-Code-Session-Id` |
| `session_mode` | `prefer`（不是 strict：可用性优先，管理员可改为 strict） | `prefer` |
| 作用域 | `include_using_group`、`include_rule_name`；不含模型 | 同左 |

网关实际提供的路径：`POST /v1/chat/completions`、`POST /v1/responses`（没有 WebSocket 或其他 Responses 入口）、`POST /v1/messages`；
`/v1/messages/count_tokens` 由单独的处理器提供，不参与会话亲和。

设置值保存在数据库中时（`sources["gateway.affinity"] = "db"`）不会自动获得新增的预设：在控制台点击“填充模板”加入 `gpt session`
（新规则插在它在预设中后面那条已有规则之前，原样保存过旧默认值时就是第一条），确认它排在会处理 GPT 请求的其他规则之前（可“上移”），再保存。

## 2. 行为

### 2.1 规则匹配

按顺序检查规则。一条规则**生效**的条件：`model_regex`、`path_regex`、`user_agent_include` 都匹配，**且**取到非空的会话值——
按 `key_sources` 的顺序取第一个非空值（去除首尾空白；gjson 结果为 JSON `null` 视为没有），设置了 `value_regex` 时必须匹配。
取不到值（或值不匹配）时继续检查下一条规则（与 new-api 相同）。第一条生效的规则胜出；没有规则生效时请求完全按原来的方式处理。
multipart / 二进制请求体（图片编辑、语音转写）的 gjson 与 anchor 来源取不到值。

#### 2.1.1 对话锚点（`anchor`，OmniGate 扩展）

客户端不发送任何会话标识时的兜底：从请求体计算对话指纹，值为 SHA-256 的十六进制串。只取对话开头的部分，因此同一段对话的后续轮次
（只在末尾追加消息）得到同一个值，兄弟对话（第一条用户消息不同）得到不同的值。按入口协议：

- OpenAI Chat：`messages` 开头连续的 `system` / `developer` 消息，加第一条 `user` 消息；
- OpenAI Responses：`instructions`，加 `input`——字符串即第一条用户消息；数组时取开头的 `system` / `developer` 项和第一条 `user` 项
  （没有 `role` 的项，如函数调用、reasoning，与 assistant 一样结束“开头”）；
- Anthropic Messages：`system`（字符串或文本块），加第一条 `user` 消息。

编码：依次哈希角色和内容单元，每个单元为 `标签=完整长度:前 4 KiB 字节\0`（长度前缀，编码无歧义，超大提示词与内联图片的开销有上限）；
字符串内容与 `text` / `input_text` / `output_text` 块哈希其文本（两种写法等价），其他块哈希类型及除 `type`、`cache_control`
以外的各字段原始 JSON（`cache_control` 会在轮次之间移动）。没有用户消息、其他协议（embeddings、图片、语音）或 multipart 请求时取不到值。
绑定键总是包含用户 id，锚点不会跨用户生效。

### 2.2 绑定键与存储

- 绑定键**总是**包含用户 id（会话永远不会影响其他用户的路由），再按规则的 `include_rule_name`、`include_using_group`（用户组 id）、
  `include_model_name`（请求的逻辑模型）加入对应部分，最后是会话值；内存中只保存这些内容的 SHA-256，原始值不进日志、不进数据库。
- 存储是进程内的 LRU：容量 `max_entries`（修改后在下一次写入时生效），每条绑定有自己的 TTL（规则 `ttl_seconds` 或默认值），
  **滑动续期**——每次命中（查到绑定）和每次重新绑定都重新计时。单实例部署：不使用数据库或 Redis，重启后清空。

### 2.3 路由

网关先像原来一样计算候选渠道及其顺序（权限、key 允许的渠道、自有 → 共享 → 平台层级、模型、协议、路由规则的目标 / 策略 / 优先级等全部照旧），
然后对**请求的模型**（不含路由规则的回退模型）：

- 绑定的渠道在候选列表中、且熔断器不是 `open`：把它移到**所在层级**的最前面。层级顺序（自有 → 共享 → 平台）是计费边界，不会改变；
  所有候选在同一层级时就是整体最前面。
- 绑定的渠道不是候选（停用、删除、key 不允许、不再提供该模型）或熔断中：结果记为 `broken`，按正常顺序路由；
  `keep_on_channel_disabled = false` 时丢弃绑定，`true` 时保留（之后由其他渠道成功处理也不改绑，渠道恢复后会话回到原渠道）。
- 模式 `off`：不查找也不建立绑定（只透传请求头）。
- 模式 `prefer`：绑定渠道失败时按正常的重试 / 故障切换规则继续。
- 模式 `strict`：请求被路由到绑定渠道且这次尝试失败时，**不再尝试其他渠道**，直接返回这次的错误（与“最后一次尝试的错误”相同的格式），
  绑定保留。绑定渠道不是候选时 strict 也按正常路由处理。

请求成功（某次尝试提供了响应；流式响应在响应开始后即视为成功）时：没有绑定则建立绑定；由其他渠道处理且 `switch_on_success = true`
则改绑到该渠道，`false` 则保留原绑定。由回退模型处理的请求不建立、不修改绑定。

路由预览（`POST /api/admin/routes/preview`）不考虑会话亲和（它是逐请求的运行时状态）。

### 2.4 请求头透传（pass_headers）

规则生效时（任何模式，包括 `off`），把列出的客户端请求头复制到上游请求，每次尝试、每个渠道都复制：

- `keep_origin: true`：渠道配置**显式设置**的同名请求头（渠道 `config.headers`、插件 hook 返回的请求头、自定义协议插件 `buildRequest`
  的请求头，大小写不敏感）保持渠道的值；其他情况使用客户端的值。网关默认的 `User-Agent`（`OmniGate/<版本>`）和默认的
  `anthropic-version` 不是渠道配置，会被客户端的值替换。
- `keep_origin: false`：一律使用客户端的值。
- 禁止的请求头（§1）在运行时也永远不复制；上游凭据（`Authorization` / `x-api-key`）始终是渠道自己的。
- 请求头名称按 Go 的规范形式发送（`Session_id`、`X-Codex-Turn-Metadata`、`X-Openai-Subagent`）：HTTP 请求头不区分大小写，下划线保留。
  OmniGate 前面的 nginx 默认丢弃带下划线的请求头，需要 `underscores_in_headers on;`。
- 适用于所有渠道类型：OpenAI、Anthropic、带 hook 的插件渠道（在 hook 之后加入，hook 看不到这些请求头）、自定义协议插件（在 `buildRequest`
  的请求头之后、默认 `User-Agent` 之前加入）。插件在 hook 内自己发起的 HTTP 请求不受影响。
- 一个规则有多个 `pass_headers` 操作时合并名单，任一操作 `keep_origin: true` 即按 true 处理。

### 2.5 协议转换

`prompt_cache_key` 与 `safety_identifier` 在 Chat Completions 与 Responses 中含义相同，**Responses ⇄ Chat 转换时保留**（以前作为提示类字段丢弃）。
`service_tier` 仍是提示类字段（它选择上游的处理 / 计费档位，OpenAI 兼容的 Chat 上游很少实现，只在同协议直通时保留）。
Chat → Anthropic、Anthropic → Chat 不变（Responses → Anthropic 经 Chat 转换时仍丢弃并告警 `prompt_cache_key`）。

### 2.6 上游会话标识补全（OmniGate 扩展）

上游号池（如在多个 Codex 账号前面的 OpenAI 格式网关）按会话标识把对话留在同一个上游账号上。Anthropic → OpenAI 转换把
`metadata.user_id` 变成 OpenAI 的 `user`（哈希值），号池不把它当作会话；普通 Chat 客户端根本不发送会话标识。规则生效（取到会话值）时，
两个选项给上游请求补上稳定的、按对话区分的标识：

- 只作用于 **OpenAI 格式**的上游请求：Chat Completions 或 Responses（协议转换之后的上游协议）；Anthropic 上游、embeddings / 图片 / 语音、
  自定义协议插件不补全。每次尝试、每个渠道都补全。
- 值由与绑定键相同的内容派生（用户 id + 规则的 `include_*` 部分 + 会话值）：`d = SHA-256("omnigate-affinity/upstream\0" ‖ 绑定键)`，
  所以按用户、按对话区分，从不发送原始会话值，也不发送内存中的绑定键本身。
- `inject_prompt_cache_key`：最终的上游请求体没有非空的 `prompt_cache_key` 时设为 `"og-" + hex(d[0:16])`（32 个十六进制字符）。
  客户端的值（任何非空值）永远不会被覆盖。字段缺失时插入为第一个成员，其余字节不变；为空串或 `null` 时替换。在插件 hook 之前加入
  （hook 看得到、签名覆盖它）。
- `inject_session_header`：上游请求没有该请求头（客户端经 `pass_headers` 透传的、渠道 `config.headers` 或插件 hook 设置的都算有）时，
  设为由 `d[0:16]` 构成的 UUID 字符串（小写 8-4-4-4-12，版本位 8、RFC 4122 变体位，即合法的 UUIDv8），在其他请求头都设置完之后加入。
  内置预设用 `Session_id`：Codex 原生的会话请求头，OpenAI 格式号池（如默认配置的 AxonHub，从任意路径的 Codex `Session_id` 提取 trace）
  与 Codex 后端都以它为会话标识，于是普通 Chat 客户端、Claude Code → GPT 的转换请求无需任何上游配置也能在上游保持会话。
- 请求日志只记录规则名称与结果，不记录补全的值。

## 3. 管理端绑定缓存（挂在 `/api/admin`）

| 方法与路径 | 权限 | 说明 |
| --- | --- | --- |
| `GET /api/admin/affinity/stats` | `settings.read` | `{ entries: number, maxEntries: number, rules: { [ruleName]: number } }`：当前有效绑定数（先清除过期的）、容量、按规则名称的条目数 |
| `POST /api/admin/affinity/clear` | `settings.write` | 请求体 `{ rule?: string }`（可省略请求体）：省略或空 = 清空全部，否则只清空该规则建立的绑定。返回 `{ cleared: number, stats }`。写审计 `affinity.clear`（`resourceType: "system_settings"`、`resourceId: "gateway.affinity"`、`metadata: { cleared, rule? }`） |

设置本身没有单独的接口，见 §1。

## 4. 请求日志

`request_logs` 新增两列（迁移 `00019_session_affinity`，两种数据库都可为空）：

| 列 / 字段 | 说明 |
| --- | --- |
| `affinity` / `affinity` | 结果；没有规则生效、或请求在发往任何上游之前就被拒绝（余额不足、配额、限额等；`off` / `broken` 除外）时为 `null` |
| `affinity_rule` / `affinityRule` | 生效规则的名称；从不记录会话值 |

| `affinity` | 含义 |
| --- | --- |
| `hit` | 由绑定的渠道处理 |
| `new` | 还没有绑定：处理请求的渠道成为绑定渠道 |
| `rebound` | 有绑定，由其他渠道处理，绑定改到该渠道（`switch_on_success`） |
| `failover` | 有绑定，但未由绑定渠道处理（失败、回退模型、或 `switch_on_success = false`），绑定保持不变 |
| `broken` | 绑定的渠道不是候选（§2.3） |
| `strict_failed` | strict：绑定渠道失败，没有尝试其他渠道 |
| `miss` | 还没有绑定，且请求失败（没有建立绑定） |
| `off` | 规则生效但模式为 off（只透传请求头） |

`GET /api/logs` 新增过滤参数 `affinity`：上表中的一个值，或 `any`（有规则生效的请求）；其他值 → `422`。

## 5. 统计

`GET /api/stats/summary` 新增（时间范围与可见性同原接口）：

- `totals.cacheReadTokens`、`totals.cacheWriteTokens`；`totals.cacheHitRate` = 缓存读取 token ÷ 提示 token，其中提示 token
  = 输入 + 缓存读取 + 缓存写入（即 `totals.inputTokens`，与用量规范化一致：`input` 不含缓存）；没有提示 token 时为 `null`。
- `byChannel[]` 每项新增 `inputTokens`（提示 token）、`cacheReadTokens`、`cacheHitRate`（同上）。
- `affinity`：`{ [outcome]: number }`，各结果的请求数（只含出现过的结果）。

## 6. 控制台

- **系统设置 → 会话亲和**（`settings.write`）：放在“网关”分组之后，单独保存（带设置的整体 `version`）。会话亲和是全局设置，作用于所有层级
  （自有、共享、平台渠道），而路由规则只作用于平台层级，所以放在系统设置而不是路由页。
  - 全局选项：启用、默认会话保持、成功后切换绑定、渠道不可用时保留绑定、默认 TTL、最大缓存条目。
  - 规则表：名称与模型 / 路径正则；Key 来源（`gjson` / `header` / `anchor` 徽标 + 路径 / 请求头，最多显示 3 个，排在后面的 `anchor` 也显示）；会话保持（图标 + 模式，“规则单独设置 / 继承全局”）；
    TTL（“全局默认” / `Ns`）；作用域徽标（分组 / 模型 / 规则）、透传请求头数与补全标记（“补全 prompt_cache_key · Session_id”）；缓存（该规则的条目数）；操作（编辑；更多：上移、下移、复制、
    清空该规则缓存、删除）。表格在卡片内横向滚动。
  - “可视化 / JSON”切换：JSON 编辑器读写 §1 的完整 JSON，实时显示解析与校验错误；可粘贴 new-api 的设置。
  - 规则编辑抽屉：Key 来源类型“gjson（请求体）/ 请求头 / 对话锚点”（锚点无输入框）；“上游会话标识”分组中的“补全 prompt_cache_key”开关与
    “补全会话请求头”输入框（占位 `Session_id`）。
  - “填充模板”加入内置预设（§1.1；同名规则被替换，其他规则保留；新规则插在它在预设中后面那条已有规则之前，否则追加到末尾）；“添加规则”打开规则编辑抽屉；“恢复默认”（值来自数据库时）删除数据库中的值。
  - 底部：缓存条目 N / 上限、刷新缓存、清空全部缓存（确认对话框）。
- **请求日志**：渠道列显示“亲和·命中”等徽标，详情中显示结果、规则与说明；新增“会话亲和”过滤。
- **统计**：新增“提示词缓存命中率”（缓存读 / 输入）与“会话亲和命中”（已绑定会话中由绑定渠道处理的比例：`hit ÷ (hit + rebound + failover + broken + strict_failed)`）
  两个指标，按渠道表新增“缓存命中”列。

## 7. 实现差异

- new-api 的 `context_int` / `context_string` Key 来源与 `pass_headers` 以外的参数覆盖操作不支持（校验时拒绝，见 §1）。
- OmniGate 扩展：`anchor` Key 来源（§2.1.1）、`inject_prompt_cache_key` 与 `inject_session_header`（§2.6）。new-api 的 JSON 不含它们，
  照样可以粘贴；含扩展的设置粘贴回 new-api 时这些字段 / 来源不被支持。
- 绑定只在本实例内存中（单实例部署），重启后清空；修改或删除规则不会自动清除它已有的绑定（TTL 到期或手动清空）。
- 绑定只调整层级内的顺序（§2.3），不会让平台渠道越过用户自己的或共享的渠道。
- 绑定的渠道熔断器处于 `half_open` 且探测名额已被占用时，这次请求跳过它，由其他渠道处理（之后按 `switch_on_success` 改绑）。
