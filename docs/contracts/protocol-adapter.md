# 契约草案：协议适配层（Protocol Adapter）

> 状态：**Phase 1 已实现**（`server/internal/protocol`、`server/internal/gateway`）。§2–§3 的通用接口与标准化模型保留为 Phase 2 插件化时的目标形态；
> Phase 1 的实际实现与取舍见 §9。

## 1. 分层

```
客户端协议入口（OpenAI Chat / OpenAI Responses / Anthropic Messages）
        │  Decode：校验 + 转为标准请求 CanonicalRequest
        ▼
路由（Scope → Key 策略 → 模型映射 → 渠道可见性 → 健康 → 限额/并发 → 路由算法）
        │
        ▼
上游协议（渠道插件声明：继承内置协议，或完全自定义）
        │  Encode：CanonicalRequest → 上游 HTTP 请求（插件 Hook 可覆盖）
        ▼
上游响应 → 解析为标准事件流 CanonicalEvent → 按客户端协议重新编码输出
```

**同协议直通优化**：客户端协议与上游协议相同、且插件没有覆盖相关 Hook 时，请求体只做模型名替换和必要的头部处理，
响应按字节流透传，同时旁路解析 usage。这是性能最高、保真度最高的路径。

## 2. Go 接口

```go
package protocol

// Inbound：客户端协议入口。
type Inbound interface {
    Name() string                                         // "openai.chat" | "openai.responses" | "anthropic.messages"
    Decode(r *http.Request, body []byte) (*CanonicalRequest, error)
    // EncodeStream 把标准事件按本协议写给客户端，必须逐事件 Flush，不得缓冲整包。
    EncodeStream(w StreamWriter, ev CanonicalEvent) error
    EncodeResponse(resp *CanonicalResponse) ([]byte, error)
    EncodeError(e *GatewayError) (status int, body []byte)
}

// Upstream：上游协议出口（内置实现 + 插件扩展）。
type Upstream interface {
    Name() string
    BuildRequest(ctx context.Context, req *CanonicalRequest, ch ChannelRuntime) (*http.Request, error)
    ParseResponse(ctx context.Context, resp *http.Response) (*CanonicalResponse, error)
    // ParseStream 增量解析；state 由调用方持有，跨分片保留。
    ParseStream(ctx context.Context, chunk []byte, state *StreamState) ([]CanonicalEvent, error)
    NormalizeError(resp *http.Response, body []byte) *GatewayError
}
```

## 3. 标准化模型（节选）

```go
type CanonicalRequest struct {
    Model        string            // 客户端请求的逻辑模型名
    Messages     []Message         // system/user/assistant/tool；内容为多段 Part（text/image/audio/file/tool_use/tool_result/thinking）
    Tools        []Tool
    ToolChoice   *ToolChoice
    Stream       bool
    MaxTokens    *int
    Temperature  *float64          // 采样参数允许浮点；金额永不使用浮点
    TopP         *float64
    Stop         []string
    Reasoning    *ReasoningConfig  // effort / budget_tokens
    ResponseFormat *ResponseFormat
    Metadata     map[string]string
    // Extensions 保存无法映射到通用字段、但同协议直通时需要原样保留的字段。
    Extensions   map[string]json.RawMessage
    // Source 记录入口协议，用于判断能否同协议直通。
    Source       string
}

type CanonicalEvent struct {
    Type   EventType  // message_start | content_delta | tool_call_delta | thinking_delta | usage | message_stop | error
    Index  int
    Delta  string
    Tool   *ToolCallDelta
    Usage  *Usage
    Finish FinishReason // stop | length | tool_calls | content_filter | error
}

type Usage struct {
    InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens, ReasoningTokens int64
    AudioInputTokens, AudioOutputTokens, ImageTokens int64
    Estimated bool  // 上游未返回 usage、由宿主估算
}
```

## 4. 跨协议转换规则

- 可以无损映射的字段直接转换（消息、工具、流式事件、finish reason、usage）。
- **无法无损转换的字段**：
  - 请求方向：默认返回 `400 unsupported_parameter`，错误信息里写明字段名和目标协议；
    Gateway Key 策略可以设为 `warn`，此时丢弃该字段，并在响应头 `X-OmniGate-Compat-Warnings` 中列出被丢弃的字段。
  - 响应方向：上游独有的字段（例如某厂商的检索引用）放到 `Extensions`，同协议时原样输出，跨协议时丢弃并给出告警头。
- 不做“静默丢弃”。

## 5. 错误分类（GatewayError）

| 分类 | 含义 | 可重试/回退 | OpenAI 映射 | Anthropic 映射 |
| --- | --- | --- | --- | --- |
| `invalid_request` | 客户端参数错误 | 否 | 400 invalid_request_error | 400 invalid_request_error |
| `unsupported_parameter` | 跨协议无法转换 | 否 | 400 | 400 |
| `authentication` | Gateway Key 无效 | 否 | 401 | 401 authentication_error |
| `permission` | Key 无权使用该模型/渠道 | 否 | 403 | 403 permission_error |
| `account_disabled` | Key 有效但所属用户已停用（消息含原因与到期时间，phase7-api.md §2.1） | 否 | 403（`code: account_disabled`） | 403 permission_error |
| `model_not_found` | 无可用映射 | 否 | 404 | 404 not_found_error |
| `quota_exceeded` | 本地配额/预算不足（Phase 3 套餐配额启用后产生） | 否 | 429 insufficient_quota | 429 rate_limit_error |
| `insufficient_balance` | 预付费模式余额不足 | 否 | 402 insufficient_quota | 402 permission_error |
| `rate_limited` | 本地限流 | 否 | 429 rate_limit_exceeded | 429 rate_limit_error |
| `user_request_limit` | 用户组每日请求数用完（phase8 §2.2） | 否 | 429 rate_limit_exceeded | 429 rate_limit_error |
| `spend_limit_exceeded` | 用户组每日 / 每月消费或 Key 消费上限已达到（phase8 §2.2） | 否 | 429 insufficient_quota | 429 rate_limit_error |
| `upstream_rate_limited` | 上游 429 | 是（换渠道） | 429 | 429 |
| `upstream_unavailable` | 上游 5xx/连接失败 | 是 | 502 | 502 overloaded_error |
| `upstream_authentication` | 上游拒绝渠道凭据（401/403，正文不回显） | 是（换渠道） | 502 | 502 api_error |
| `upstream_bad_request` | 上游判定请求无效（400/413/422…） | 否 | 原状态（422→400） | 同左，invalid_request_error |
| `upstream_timeout` | 上游超时 | 是（未输出前） | 504 | 504 |
| `upstream_invalid_response` | 畸形响应/SSE | 视情况 | 502 | 502 |
| `client_closed` | 客户端断开（仅用于日志，状态 499） | 否 | — | — |
| `internal` | 网关内部错误 | 否 | 500 | 500 api_error |

上游原始错误只保留**脱敏摘要**（状态码、上游错误码、前 256 字节消息，经过密钥模式过滤）。

## 6. 重试与回退

- 只在**尚未向客户端写出任何字节**时才允许重试或回退。
- 流式响应一旦开始输出，上游中断会以协议格式的错误事件结束流，**不切换渠道，也不拼接两份响应**。
- 每次尝试都记录成本事实与健康事件；向下游只收一次（ADR-0006）。

## 7. 取消传播

客户端断开 → 请求 `context` 取消 → 上游 HTTP 请求取消 → 插件运行时 `Interrupt`。已经产生的上游用量照常记账。

## 8. 验收测试清单（Phase 1）

流式/非流式 × 三种入口协议 × 同协议/跨协议；工具调用；多模态；畸形 SSE；上游超时；客户端断开；
首包延迟（不等完整响应）；不支持的字段返回明确错误。

## 9. Phase 1 实际实现（Round 2）

**结构**：以 Chat Completions 为中间格式，三种协议两两互转（Round 4 起）：Chat ↔ Messages、Chat ↔ Responses 为直接转换器，
Messages ↔ Responses 由两步组合（请求、响应、流式都是如此；流式通过串联两个流处理器实现）。同协议请求走直通（只改写 `model`）。

上游协议的选择（`channel.Dialect(client, model)`）：

| 渠道 | 上游协议 |
| --- | --- |
| anthropic | 总是 Messages |
| openai，且该模型的 `upstreamProtocol = "responses"` | 总是 Responses（适用于只支持 Responses 的模型） |
| openai，Responses 客户端，渠道 `supportsResponses = true` | Responses 直通 |
| openai，其他情况 | Chat Completions |

| 客户端 \\ 上游 | Chat | Responses | Messages |
| --- | --- | --- | --- |
| `/v1/chat/completions` | 直通 | 转换 | 转换 |
| `/v1/responses` | 转换 | 直通 | 转换（经 Chat） |
| `/v1/messages` | 转换 | 转换（经 Chat） | 直通 |

**同一模型有多个渠道时的选择顺序**（Round 4）：① 渠道优先级（管理员显式设置，最高优先）；② 协议匹配度——同优先级内先选
无需转换的渠道（直通），再选一步转换（Chat ↔ Messages / Chat ↔ Responses），最后选两步转换（Messages ↔ Responses）；
③ 同一档内按权重随机。回退也按这个顺序进行。这样例如 Responses 客户端访问同时存在于 Responses 渠道与 Anthropic 渠道的模型时，
会优先直通 Responses 渠道，保留推理摘要等原生特性；只有该渠道失败或熔断时才回退到需要转换的渠道。

Responses 转换的取舍：`previous_response_id`（依赖上游会话状态）在任何模式下都无法转换，会返回 400；`input` 中的 `reasoning` 项、
`reasoning.summary` 属于提示类字段（丢弃并告警）；Chat 的 `stop` 在 Responses 中没有对应参数（严格模式报错）；转换到 Responses 时
总是设置 `store:false`，避免上游保存对话。

**字段分类**（`protocol/request.go`）：

| 类别 | 例子 | 严格模式 | 宽松模式 |
| --- | --- | --- | --- |
| 可转换 | messages、tools、tool_choice、stop、temperature、图片、tool_result、reasoning/thinking | 转换 | 转换 |
| 中性值（等同默认值，不影响语义） | `n:1`、`presence_penalty:0`、`logprobs:false`、`thinking:{type:disabled}` | 忽略 | 忽略 |
| 提示类（只影响缓存/延迟，或历史中无法还原） | `cache_control`、历史 `thinking` 块、`service_tier`、`tools[].function.strict` | 丢弃 + 告警头 | 丢弃 + 告警头 |
| 无法转换 | `top_k`、`seed`、`logprobs:true`、`n>1`、服务端工具、文档块 | **400 unsupported_parameter** | 丢弃 + 告警头 |

**思考（reasoning）映射**：

| 方向 | 规则 |
| --- | --- |
| Anthropic `thinking` → OpenAI `reasoning_effort` | `enabled`：budget < 4096 → `low`，< 16384 → `medium`，否则 `high`；`adaptive` → `medium`；`disabled` → 不设置 |
| OpenAI `reasoning_effort` → Anthropic `thinking` | `low/medium/high` → budget 2048/8192/24576；`minimal/none` → 不开启；`max_tokens` 不大于 budget 时自动提高并告警；开启后去掉 `temperature`/`top_p` 并告警（Anthropic 的要求） |
| 响应中的推理内容 | OpenAI `reasoning_content`（或 `reasoning`，两者同时出现时只取一份）↔ Anthropic `thinking` 块 / `thinking_delta` |

**usage 归一化**：`input` 不含缓存（OpenAI 的 `prompt_tokens` 会减去 `cached_tokens`），`cacheRead`/`cacheWrite` 单独计价；
`output` 含推理 token；上游未返回 usage 时按约 4 字节/token 估算并标记 `estimated`。OpenAI 流式请求一律向上游请求 `include_usage`，
客户端未请求时过滤掉只含 usage 的分片。

**实测**（Round 2–4，真实上游，官方 openai 3.26 / anthropic 1.12 SDK）：Round 4 增加 Chat 客户端（流式 + 工具）→ Responses 上游、Anthropic 客户端与 Claude Code → Responses 上游、Responses 客户端 → Chat / Anthropic 渠道（含流式），全部通过；直通（Chat、Messages、Responses，流式与非流式）、
双向跨协议流式 + 工具调用多轮、thinking 双向映射、Claude Code 风格请求（system/tools 带 `cache_control`、回传带签名的 thinking）、
严格/宽松模式、两种格式的模型列表与错误格式，全部通过。
