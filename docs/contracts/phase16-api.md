# 契约：Round 16 —— Responses WebSocket 模式、按时间窗口熔断、上下文超长归为客户端错误

> 状态：**已实现**。通用约定同前；无新迁移。
> 需求：支持 OpenAI Responses API 的 WebSocket 模式（Codex 等客户端使用）；渠道不能因为个别请求失败就熔断；
> 「输入超出上下文窗口」是用户输入的问题，不能让渠道熔断或被换渠道重试。

## 1. Responses WebSocket 模式

`GET /v1/responses`（WebSocket 升级）。鉴权与 HTTP 相同：握手请求头 `Authorization: Bearer og-…`（或 `x-api-key`）；
Key 无效时握手直接返回 HTTP 401 错误（不升级）。不带升级头的 `GET /v1/responses` 返回 `400`。

客户端事件只有 `response.create`，载荷与 `POST /v1/responses` 的请求体相同，另有：

| 字段 | 说明 |
| --- | --- |
| `stream_id` | 可选，1–256 个字母 / 数字 / `_` `-` `.`；同一 `stream_id`（lane）内的请求按顺序执行，不同 lane 并发 |
| `generate` | `false` 时不调用上游，只把本轮输入缓存在新的响应 id 下（预热），后续可用 `previous_response_id` 接续 |
| `stream`、`background` | 忽略（WebSocket 模式总是流式） |

服务端事件：与 HTTP 流式相同的 `response.*` 事件，每个事件一帧（JSON 文本）；属于命名 lane 的事件带 `stream_id`。
错误为 `{"type":"error","status":<HTTP 状态>,"error":{"type","code","message","param?"},"stream_id?"}`，连接保持打开，其他 lane 不受影响。

实现：每个 `response.create` 作为一次流式 `POST /v1/responses` 走完整的网关流程——路由、协议转换（Chat / Messages 上游同样可用）、
计费、套餐配额、请求日志（`inbound = openai.responses`，每轮一条）与熔断都与 HTTP 相同。

**`previous_response_id`**：连接内缓存每个 lane 最近 4 个响应（整个连接最多 64 个）的「完整输入 + 输出」。引用缓存中的 id 时，
网关把历史与本轮新输入拼成完整 `input` 发给上游，并去掉 `previous_response_id`——因此 `store=false`、不保存状态的上游、
以及经协议转换的渠道都能接续多轮对话。不在缓存中的 id 原样转发给上游（保存了响应的上游仍可解析）。

| 限制 | 值 |
| --- | --- |
| 同时进行的响应 | 每连接 16 个（超出的排队） |
| 命名 lane | 每连接 32 个，超出返回 `websocket_stream_limit_reached` |
| 连接时长 | 60 分钟，到期发送 `websocket_connection_limit_reached` 后关闭；客户端重连后需发送完整输入 |
| 单条消息大小 | 与 HTTP 请求体上限相同（`OMNIGATE_MAX_BODY_BYTES`） |

反向代理需要转发 WebSocket 升级（`deploy/nginx/omnigate.conf` 已包含；Caddy 自动支持）。

## 2. 熔断：按时间窗口判断

渠道熔断从「连续 3 次失败」改为：**60 秒内至少 5 次健康相关失败，且失败数不少于该窗口内全部请求的一半**。
熔断 30 秒后由后台主动探测（或放行一个请求）恢复，失败的探测会再熔断 30 秒。
健康状态：窗口内没有失败 = `healthy`；有失败但未达阈值 = `degraded`；熔断中 = `open`。
健康相关失败仍只包括连接失败、超时、401 / 403、408、429、5xx 与无效的上游响应。

## 3. 上下文超长是客户端错误

上游以任意状态码（包括 5xx，以及部分代理在流式请求时用 `200` + JSON 错误体）返回「输入超出上下文窗口」时：

- 识别依据：`error.code` 为 `context_length_exceeded` / `model_context_window_exceeded`，或消息匹配常见表述
  （context window / context length / maximum context / prompt is too long / input is too long / reduce the length of the messages 等）；
  429、401、403 不参与识别。
- 返回客户端 `400`，OpenAI 格式 `{"error":{"type":"invalid_request_error","code":"context_length_exceeded",…}}`，
  Anthropic 格式 `invalid_request_error`；流式中途出现时以流内错误事件结束。
- **不换渠道重试，不计入渠道健康**（不会触发熔断）。

另外，流式请求收到 `200` 且正文是 `{"error": …}` 的 JSON（而不是 SSE）时，按其中的错误分类，而不是笼统的“上游响应无效”；
非流式请求收到只含 `error` 的 2xx 正文同样按错误处理。
