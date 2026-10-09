# 契约：Round 6（续）—— 音频接口、插件自定义协议、计费插件

> 状态：**定稿，按此实现**（在 phase8 完成后开始）。通用约定同前；所有 SQL 同时支持 PostgreSQL 与 SQLite。

## 1. 音频接口（OpenAI 兼容）

| 方法 | 路径 | 请求体 | 响应 |
| --- | --- | --- | --- |
| POST | `/v1/audio/transcriptions` | multipart：`file`、`model`、`language`、`prompt`、`response_format`（json / text / srt / verbose_json / vtt）、`temperature`、`timestamp_granularities[]`、`stream`、`include[]`、`chunking_strategy` | 透传（JSON 或文本；`stream: true` 时 SSE：`transcript.text.delta` / `transcript.text.done`） |
| POST | `/v1/audio/translations` | multipart（同上子集） | 透传 |
| POST | `/v1/audio/speech` | JSON：`model`、`input`、`voice`、`instructions`、`response_format`、`speed`、`stream_format`（`audio` / `sse`） | 二进制音频流式透传（`Content-Type` 透传），或 `stream_format: sse` 时 SSE（`speech.audio.delta` / `speech.audio.done`） |

- 只路由到 `openai` 类型渠道；入口协议 `openai.audio.transcriptions` / `openai.audio.translations` / `openai.audio.speech`。
  multipart 处理复用图片接口的实现（请求体上限 `64 MiB`，可重放以便重试）。
- 二进制响应一旦开始写给客户端就不再重试（与流式相同）。
- 渠道路由、归属分档、重试、熔断、Key 策略、用户组倍率、分时价格、限额、套餐与计费规则与其他接口相同。
- 模型资料 `capabilities` 新增 `audioInput`、`audioOutput`；模型广场 `protocols` 新增 `openai.audio`。

### 1.1 计价

价格新增可选字段：

| 字段 | 含义 |
| --- | --- |
| `audioInputPerM` | 音频输入 token 单价；为空时按 `inputPerM` |
| `audioOutputPerM` | 音频输出 token 单价；为空时按 `outputPerM` |
| `perMinute` | 每分钟音频（转写 / 翻译按输入音频时长，按秒计费） |
| `perMCharacters` | 每百万输入字符（语音合成，按 `input` 的 Unicode 字符数） |

用量来源（依次尝试）：
1. 响应 `usage`：`{type: "tokens", input_tokens, output_tokens, input_token_details: {audio_tokens, text_tokens}}` → 按 token；`{type: "duration", seconds}` → 按 `perMinute`。
2. `verbose_json` 的 `duration` → 按 `perMinute`。
3. 语音合成：SSE 的 `speech.audio.done` 中的 `usage` → 按 token；否则按 `input` 字符数 × `perMCharacters`。
4. 都没有时只收 `perRequest`，并在请求日志中标记 `usageEstimated: true`。

请求日志新增 `audioSeconds`；用量新增 `audioInputTokens`、`audioOutputTokens`。套餐计量新增 `audio_seconds`。

## 2. 插件自定义协议

渠道插件可以实现完整的上游协议（上游既不是 OpenAI 兼容也不是 Anthropic 兼容时，例如百度文心、腾讯混元的私有协议）。
manifest 新增 `protocol: "custom"`（与已有的 `inherits: "openai" | "anthropic"` 互斥）；此时以下 Hook 必须实现：

```ts
// 把网关的中间格式（Chat Completions 请求）转换为上游 HTTP 请求
buildRequest(req: CanonicalRequest, ctx: Ctx): UpstreamRequest          // {method, url, headers, body}
// 非流式：上游响应 → Chat Completions 响应
parseResponse(res: UpstreamResponse, ctx: Ctx): CanonicalResponse
// 流式：每收到一段上游字节调用一次；state 由宿主在整个请求期间保存（可写的普通对象）
parseStream(chunk: Uint8Array, state: StreamState, ctx: Ctx): CanonicalEvent[]
// 可选：上游流结束时调用，用于输出剩余事件
endStream?(state: StreamState, ctx: Ctx): CanonicalEvent[]
// 可选：错误响应 → 网关错误（决定状态码与重试分类）
normalizeError?(res: UpstreamResponse, ctx: Ctx): { status: number; message: string }
```

- `CanonicalEvent` 为 Chat Completions 流式 chunk 的子集：`{type: "delta", content?, reasoning?, toolCalls?}`、`{type: "finish", reason}`、`{type: "usage", usage}`、`{type: "error", message}`。宿主把它们转换为客户端协议（Chat / Messages / Responses）。
- 一个流式请求在整个生命周期内**固定使用同一个运行时**（ADR-0002），每次 `parseStream` 调用超时 50 ms，整个请求的 JS 总耗时上限 5 s（超出中断并返回 `plugin_error`）。
- `signRequest` 仍然可用（在 `buildRequest` 之后执行）。
- 编辑器模板新增“自定义协议”模板（以一个虚构的 JSON Lines 流式协议为例，带测试用例）；插件测试面板支持流式测试用例（输入一组上游字节块，断言输出事件）。
- 风险扫描：`protocol: "custom"` 的插件首次发布必须审批（权限变化规则不变）。

## 3. 计费插件

manifest `kind` 可以包含 `"billing"`。计费插件声明自定义计量：

```ts
billing: {
  meters: Record<string, { label: string; unit?: string; computeUnits(usage: Usage, ctx: BillingCtx): number | string }>
}
```

- 套餐规则的 `meter` 可以选择 `custom:<pluginKey>.<meterName>`（插件必须启用且版本已批准；规则保存时校验）。
- 结算时宿主调用 `computeUnits`：纯函数、无网络 / 存储 / 密钥，超时 5 ms；超时或抛错时该次计量按 0 记，并写入告警日志与指标 `omnigate_billing_plugin_errors_total`。
- `BillingCtx`：`{model, servedModel, channelId, channelTier, userGroup, inbound, imageCount, audioSeconds}`。
- 插件版本升级或停用时，已开通订阅的规则快照保持不变；插件不可用时该计量按 0 记并告警。
- 内置示例插件 `community.billing-examples`：`weighted_tokens`（输出 token × 4 + 输入 token）、`per_image`（图片张数）。

## 4. 实现差异

实现与上文的出入和补充细节如下，其余按契约实现。§1 由音频接口实现（`server/internal/gateway/audio.go`、`server/internal/protocol/audio.go`，迁移 `00013_audio`）；
§2 由 `server/internal/gateway/custom.go`、`server/internal/plugin/custom.go`、`server/internal/protocol/canonical.go`、
`server/internal/plugin/engine/session.go` 实现（迁移 `00014_custom_protocol`）；§3 由 `server/internal/plugin/billing.go`、
`server/internal/subscription/meters.go` 实现，示例插件在 `server/internal/plugin/bundled/community.billing-examples`。

| # | 条款 | 契约 | 实现 | 原因 |
| --- | --- | --- | --- | --- |
| 1 | §1.1 `perMinute` “按秒计费” | 按输入音频时长、按秒计费 | 上游报告的时长（`usage.seconds` 或 `verbose_json.duration`）向上取整到整秒（8.47 秒记 9 秒），费用 = 秒数 × `perMinute` / 60。请求日志 `audioSeconds` 与套餐计量 `audio_seconds` 都是这个整数秒数 | 计费单位为秒；整数便于配额计量与对账 |
| 2 | §1.1 用量 | 用量新增 `audioInputTokens`、`audioOutputTokens` | 另增 `usage.inputCharacters`（新列 `request_logs.input_characters`）：按字符计费的语音合成请求的 `input` 字符数，按 token 计量或其他请求为 0 | 否则按字符计费的请求在日志中看不出计费依据 |
| 3 | §1.1 `usageEstimated` | 请求日志标记 `usageEstimated: true` | 复用已有的 `usage_estimated` 列：日志新增顶层字段 `usageEstimated`（与 `usage.estimated` 相同）。音频请求只有在第 4 种情况（只收 `perRequest`）时为 true，音频接口从不估算 token | 避免同义的两列 |
| 4 | §1.1 token 用量 | `input_token_details.audio_tokens` | `audio_tokens` 记为音频输入（也接受 `input_tokens_details`）；有 `output_token_details.audio_tokens` 时记为音频输出。语音合成的 `usage` 没有输出明细时，**全部输出 token 记为音频输出**（按 `audioOutputPerM` 计）；转写的输出是文本 | 语音合成的输出只有音频（`gpt-4o-mini-tts` 的 `speech.audio.done` 不带明细） |
| 5 | §1 请求体 | 转写 / 翻译为 multipart | 只接受 `multipart/form-data`，其他 `Content-Type` 返回 `400 invalid_request`（图片接口另接受 JSON，音频不接受）；解析时读取 `model`、`stream`、`prompt` 三个文本字段（各限 1 MiB）。语音合成是 JSON，请求体上限为普通接口的 32 MiB，不是 64 MiB | 上传接口必须带文件；语音合成的 `input` 很短 |
| 6 | §1 临时文件 | 复用图片接口的实现 | multipart 缓存的临时文件名为 `omnigate-audio-*.part`（图片仍为 `omnigate-image-*.part`）；缓存失败的服务日志由 `image request spool failed` 改为 `multipart request spool failed`（两个接口共用） | 便于区分来源 |
| 7 | §1 响应分派 | JSON / 文本 / SSE / 二进制透传 | 按上游响应的 `Content-Type` 分派：`text/event-stream` → SSE 透传（无论请求是否要求流式）；语音合成的其他响应 → 二进制边收边发；转写 / 翻译的其他响应 → 读完后原样返回（上限 64 MiB）。上游没有给 `Content-Type` 时，语音合成用 `application/octet-stream`，转写按内容取 `application/json` 或 `text/plain; charset=utf-8` | 例如不支持流式的模型对 `stream: true` 返回普通 JSON |
| 8 | §1 二进制响应中断 | 开始写给客户端后不再重试 | 每次读到上游数据（最多 32 KiB）立即写出并 flush，网关不缓冲。上游返回 200 但正文为空时算失败（可重试）。已写出后上游断开：请求日志 `statusCode` 200、`errorClass = upstream_invalid_response`、计入熔断，按字符照常计费（与流式请求中途出错的规则相同），结算与写日志之后**中断客户端连接**（`http.ErrAbortHandler`），让客户端知道音频不完整 | 二进制正文无法携带错误事件；正常结束的分块响应会被客户端当成完整文件 |
| 9 | §1 请求日志 `stream` | — | 语音合成只有 `stream_format: "sse"` 时 `stream` 为 true；二进制音频虽然边收边发，`stream` 为 false。转写为请求中的 `stream` | `stream` 表示 SSE 响应 |
| 10 | §1 预留余额 | 计费规则与其他接口相同 | 强制计费下的预留额：语音合成 = `perRequest` + 字符数 × `perMCharacters`（再乘倍率）；转写 / 翻译只预留 `perRequest`（时长要等上游返回才知道）。结算时多退少补 | 请求前无法得知音频时长 |
| 11 | §1.1 用量来源顺序 | 依次尝试 | `usage` 优先于 `verbose_json.duration`；`usage` 既无 token 也无时长时视为没有。流式转写的 `transcript.text.done` 不带 `usage` 时按第 4 种情况处理。语音合成 SSE 没有 `speech.audio.done` 用量时按字符计费 | 与契约顺序一致的细化 |
| 12 | 路由预览 | — | `POST /api/admin/routes/preview` 的 `inbound` 新增 `openai.audio.transcriptions`、`openai.audio.speech`（翻译与转写路由相同，不单列） | 与图片接口只列 `generations` 一致 |
| 13 | §2 manifest | `protocol: "custom"` 与 `inherits: "openai" \| "anthropic"` 互斥 | 已有的继承字段是 `extends`（`openai.chat` / `anthropic.messages`，phase2），`protocol: "custom"` 与它互斥。自定义协议的 Hook 也要写进 manifest 的 `hooks`：`buildRequest`、`parseResponse`、`parseStream` 必填，`endStream`、`normalizeError`、`signRequest` 可选，不允许 `transformRequest`（由 `buildRequest` 构造请求）；发布与编译检查时校验声明的 Hook 都已导出。新增 `kind`（`channel` / `billing`，省略为 `["channel"]`） | 沿用现有 manifest 字段名 |
| 14 | §2 渠道类型 | — | 新增渠道类型 `custom`（迁移 `00014_custom_protocol`，SQLite 重建 `channels` 表以修改 CHECK），由插件决定，不能脱离插件创建。上游方言记为 `custom`（路由预览的 `upstreamDialect`，与任何客户端协议都算一步转换）。只服务 `openai.chat`、`anthropic.messages`、`openai.responses`（宿主经 Chat Completions 中转），不服务 embeddings、图片、音频；模型广场随之只列这三种协议。渠道“测试连接”与“发现模型”改为执行插件的 `health.check`（没有时 `models.list`）/ `models.list` 能力，没有这些能力时返回 `409 capability_not_found` | 私有协议没有统一的模型列表接口；独立类型让路由、广场与界面无需猜测插件 |
| 15 | §2 `UpstreamRequest` | `{method, url, headers, body}` | `method` 默认 POST（GET/POST/PUT/PATCH/DELETE）；`url` 以 `/` 开头时拼在渠道 baseUrl 之后，绝对地址必须是 https（与 baseUrl 同主机且 baseUrl 为 http 时可用 http）且主机为 baseUrl 主机或在 `permissions.network` 中；`body` 为字符串时原样发送，其他值 JSON 编码并默认 `Content-Type: application/json`。宿主**不添加认证头**（用 `og.secret()` 句柄），先应用渠道 `config.headers` 再应用插件头，丢弃 `Host`、`Content-Length` 等逐跳头。`signRequest` 在其后执行，收到同一对象（`dialect: "custom"`） | 协议自定义时认证方式也由插件决定 |
| 16 | §2 `UpstreamResponse` / `CanonicalResponse` / `CanonicalEvent` | 类型名 | `UpstreamResponse = {status, headers（小写名）, body: string（UTF-8 文本）}`。`CanonicalResponse` 是 Chat Completions 响应子集，宿主补齐 `id`、`object`、`created`、`model`（客户端模型名）、`index`、`role`、`finish_reason`（有工具调用时为 `tool_calls`）；`toolCalls` 与 `usage` 用 Chat Completions 格式（`prompt_tokens` 等）。补充：`error` 事件可带 `status`；`parseResponse` 可返回 `{error: {status, message}}`，与 `normalizeError` 的结果同样分类 | 一些私有协议用 HTTP 200 返回业务错误 |
| 17 | §2 超时 | 每次 `parseStream` 50 ms，请求 JS 总耗时 5 s | `buildRequest`、`signRequest`、`normalizeError`、`parseStream`、`endStream` 每次 50 ms；`parseResponse` 为 50 ms + 每 64 KiB 响应体 10 ms（最多 1 s）。5 s 是同一请求所有 Hook 调用耗时之和（不含等待并发名额）。流式请求固定一个运行时，但并发名额（每个插件版本 8 个）按调用占用，不在整个流期间占用。超时计为一次资源违规（5 分钟内 3 次自动停用，与其他 Hook 相同） | 长时间的流不能占满并发名额；大响应体解析更慢 |
| 18 | §2 错误与重试 | 超出中断并返回 `plugin_error` | Hook 抛错、超时、返回值格式错误都是 `plugin_error`（502）：尚未向客户端写出时按 `server_error` 重试类别换渠道并计入熔断，已写出后以流内错误事件结束。上游 `error` 事件：写出前按其 `status`（缺省 502）分类，写出后为流内错误。上游流结束而插件没有给出 `finish` 时按 `stop` 结束；没有 `usage` 事件时按输出估算（`usage.estimated`） | 与内置协议的流式规则一致 |
| 19 | §2 `normalizeError` | 决定状态码与重试分类 | 返回的 `status` 按状态码分类（429、5xx 重试，401/403 记为上游认证失败并触发渠道认证告警，其他 4xx 默认不重试）；`status` < 400 按 502；`normalizeError` 本身失败时退回按 HTTP 状态码分类。消息加前缀 `upstream: ` 并脱敏 | 与内置协议一致 |
| 20 | §2 字节块解码 | `chunk: Uint8Array` | 沙盒新增全局 `TextDecoder`（仅 UTF-8，`decode(chunk, {stream: true})` 保留不完整的尾部字节）与 `TextEncoder` | Goja 没有内置解码器，字节块可能截断多字节字符 |
| 21 | §2 测试面板 | 流式测试用例 | `POST /api/plugins/{id}/draft/test` 的 `hook` 新增 `buildRequest`（`request` 为 Chat Completions 请求，输出为替换密钥句柄后的上游请求）、`parseResponse` / `normalizeError`（新字段 `response: {status, headers, body}`，`body` 可为字符串或任意 JSON）、`parseStream`（新字段 `chunks` 字符串数组或 `chunksBase64`，按块依次调用后再调用 `endStream`，输出为全部事件拼成的数组，`expect.output` 按位置做子集匹配），结果另有 `calls`（每次 `parseStream` / `endStream` 调用的 `hook`、`chunk`、`durationMs`、`events`、`error`）与 `chatChunks`（宿主转换出的 Chat Completions chunk，即 Chat 客户端收到的内容；无效事件使用例失败）。编辑器测试的 Hook 超时放宽为 4 倍。模板 ID 为 `custom-protocol` | 字段名契约未定义 |
| 22 | §2 首次发布审批 | 首次发布必须审批 | 插件**第一个声明 `protocol: "custom"` 的版本**必须审批，即使此前已批准的继承协议版本权限相同；之后的自定义协议版本照常按权限变化判断 | “首次”指首次成为自定义协议插件 |
| 23 | §3 计量声明 | 计量写在 `definePlugin({billing})` 中 | 计量名、`label`、`unit` 在 **manifest.json 的 `billing.meters`** 中声明（`kind` 必须含 `billing`，计量名形如 `weighted_tokens`），代码只需实现 `billing.meters.<名>.computeUnits`，代码中的 `label` / `unit` 可选且被忽略；编译检查确认每个计量都已实现。仅计费插件（`kind` 不含 `channel`）不能声明 `extends`、`protocol`、`hooks`、`capabilities`、`uiContributions` | 与能力一样由 manifest 声明，规则校验与计量列表不需要执行代码 |
| 24 | §3 `computeUnits` | 纯函数，超时 5 ms | 执行时 `og` 只有 `og.log`、`og.encoding`、`og.crypto.sha256`。返回值须为非负有限数或十进制字符串，按最多 9 位小数四舍五入；不合法的返回值也按 0 记。指标 `omnigate_billing_plugin_errors_total{plugin, meter, reason}`，`reason` 为 `timeout`、`exception`、`invalid`、`unavailable`（插件停用、版本不存在或未声明该计量也计数），同时写 warn 日志。计量在结算事务**之前**计算，只对套餐覆盖的请求执行 | 计费要稳定、可解释 |
| 25 | §3 快照 | 升级或停用时已开通订阅的规则快照不变 | 规则新增只读字段 `pluginVersionId`、`meterLabel`、`meterUnit`（计量的 label / unit 随固定版本快照，目录与订阅页面无需插件权限即可显示）：保存套餐规则时解析为声明该计量的最新已批准版本并固定，订阅快照随之固定；之后升级插件不影响已开通订阅，也不影响已有套餐（直到重新保存其规则）。`PATCH` 不带 `rules` 时保留原有固定版本。插件停用或版本不可用时按 0 记。自定义计量的上限可以是小数；`modelWeights` 同样适用 | 计费口径在订阅期内不变 |
| 26 | §3 `BillingCtx` | 字段列表 | `model` 为请求的模型，`servedModel` 为实际服务的模型（回退后不同），`channelId` 为字符串，`userGroup` 为用户组**名称**（未知时为空），`inbound` 为入口协议名，`audioSeconds` 为计费的整数秒数 | 便于插件按组名写规则 |
| 27 | §3 接口补充 | — | 新增 `GET /api/admin/billing/meters`（`billing.manage`）：内置计量与已启用计费插件最新已批准版本的自定义计量（`meter`、`label`、`unit`、`builtin`、`integer`、`plugin`），供套餐编辑器选择。插件列表 / 详情新增 `protocol`、`kind`、`meters`（最新已批准版本） | 前端需要可选计量列表 |
| 28 | §3 示例插件 | `weighted_tokens`（输出 × 4 + 输入） | 输入 token 含缓存读取与写入（与 `tokens.input` 一致）；`per_image` 取 `ctx.imageCount`（没有时取 `usage.images`） | 与内置计量口径一致 |
