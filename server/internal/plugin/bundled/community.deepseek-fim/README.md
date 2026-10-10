# DeepSeek FIM 补全（community.deepseek-fim）

自定义协议插件：把 OmniGate 的 Chat Completions 请求转换为 DeepSeek 的 **FIM（Fill In the Middle）补全**（Beta）请求，
再把补全结果转换回 Chat Completions 响应。

FIM 补全：给出前缀（`prompt`）和可选的后缀（`suffix`），模型生成两者之间的内容，常用于代码补全（例如给出函数开头和结尾，补全函数体）。
没有后缀时就是普通的续写。

## 上游接口（DeepSeek 官方文档）

- 指南：<https://api-docs.deepseek.com/zh-cn/guides/fim_completion>（英文 <https://api-docs.deepseek.com/guides/fim_completion>）
- 接口文档：<https://api-docs.deepseek.com/api/create-completion>（FIM Completion API (Beta)）
- 模型与价格：<https://api-docs.deepseek.com/quick_start/pricing>；错误码：<https://api-docs.deepseek.com/quick_start/error_codes>

| 项目 | 内容 |
| --- | --- |
| 地址 | `POST https://api.deepseek.com/beta/completions`（Beta 功能需使用 `https://api.deepseek.com/beta` 作为 base_url） |
| 模型 | `deepseek-flash`、`deepseek-v4-pro`（价格页：FIM 仅支持非思考模式） |
| 请求体 | `model`、`prompt`（必填）、`suffix`、`max_tokens`、`temperature`（0–2）、`top_p`（0–1]、`stop`（最多 16 个）、`stream`、`stream_options.include_usage`（只能与 `stream: true` 一起使用，否则返回 400）、`echo`、`logprobs`（≤ 20）；`frequency_penalty` / `presence_penalty` 已废弃，传了也不生效 |
| 长度 | 指南写明“模型的最大补全长度为 4K” |
| 响应 | `object: "text_completion"`，`choices[].text`、`finish_reason`（`stop` / `length` / `content_filter` / `insufficient_system_resource` / `aborted`），`usage` 含 `prompt_tokens_details.cached_tokens`（与 `prompt_cache_hit_tokens` 相同）、`prompt_cache_hit_tokens`、`prompt_cache_miss_tokens` |
| 流式 | SSE：`data: {...}`，以 `data: [DONE]` 结束；用量在 `[DONE]` 之前的最后一个 chunk（`text` 为空、`finish_reason` 非空） |

DeepSeek 没有规定在一段文本中标记“空缺位置”的写法；下文的空缺标记是本插件的约定。

## 渠道设置

1. 插件页确认 “DeepSeek FIM 补全” 已启用（随 OmniGate 分发的版本自动批准；导入的版本需要发布并审批）。
2. 新建渠道，插件选 “DeepSeek FIM 补全”（渠道类型为 `custom`），Base URL 保持默认 `https://api.deepseek.com`
   （写成 `https://api.deepseek.com/v1` 也可以：FIM 地址按 Base URL 的**源**拼接 `fimPath`），填写 DeepSeek API Key。
3. 默认模型映射：`deepseek-fim` → `deepseek-flash`，`deepseek-fim-pro` → `deepseek-v4-pro`；也可以用“发现模型”同步上游模型。
   建议使用单独的逻辑模型名（如 `deepseek-fim`），不要和普通对话渠道共用模型名，否则对话请求可能被路由到 FIM 渠道。
4. “测试连接”执行 `health.check`（模型列表接口，不产生生成费用）；渠道详情页显示账户余额（每 10 分钟同步）。

配置项：

| 配置 | 默认 | 说明 |
| --- | --- | --- |
| `fimPath` | `/beta/completions` | 拼在 Base URL 的源（协议 + 主机 + 端口）之后，只能是路径（不能换主机） |
| `holeMarker` | `<\|fim_hole\|>` | 空缺标记 |
| `defaultMaxTokens` | `4096` | 客户端没有给 `max_tokens` / `max_completion_tokens` 时发送的值（文档的 4K 上限）；`0` 表示不发送 |
| `maxTokensLimit` | `0` | 大于该值的 `max_tokens` 截到该值；`0` 表示不限制、原样转发（实测 DeepSeek 已接受超过 4K 的值，需要按文档限制时设为 `4096`） |
| `includeSystem` | `false` | 从消息中取 prompt 时，是否把 system / developer 消息（换行拼接）放在 prompt 前面 |

## 请求映射（Chat Completions → FIM）

1. **prompt**：请求体顶层有字符串 `prompt` 时使用它（`messages` 被忽略）；否则取**最后一条 user 消息**的文本
   （内容为数组时拼接所有 `text` 片段，图片忽略）。更早的对话与 system 消息默认忽略——代码补全只需要当前的上下文，
   开启 `includeSystem` 时把 system 消息放在前面。
2. **suffix**：请求体顶层有字符串 `suffix` 时使用它；否则在上一步的文本中找**第一个**空缺标记：标记之前为 `prompt`，之后为 `suffix`
   （后面再出现的标记原样保留）；没有标记时只发送 `prompt`（续写）。
3. `max_tokens`（没有时取 `max_completion_tokens`，都没有时取 `defaultMaxTokens`，再按 `maxTokensLimit` 截断）、`temperature`、`top_p`、`stop` 原样转发。
4. 流式请求发送 `stream: true` 与 `stream_options: {include_usage: true}`（保证拿到用量）；非流式发送 `stream: false`。
5. 不转发：`frequency_penalty` / `presence_penalty`（DeepSeek 已废弃）、`logprobs` / `echo`（Chat 响应里没有位置，且 `echo` 不能与 `suffix` 同用）、
   `tools` / `tool_choice` / 图片（FIM 用不上，**静默忽略**而不是报错：插件抛错会被网关当成渠道故障重试并计入熔断）、`n`、`user` 等其他字段。
6. 请求头 `Authorization: Bearer <API Key>`（密钥句柄由宿主替换，明文不进入插件）。

顶层 `prompt` / `suffix` 只能通过 **Chat Completions**（`/v1/chat/completions`）传入：网关对 Chat 请求原样保留未知字段交给插件。
Messages（`/v1/messages`）与 Responses（`/v1/responses`）客户端会先被转换为 Chat Completions，转换时未知字段在 strict 兼容模式下被拒绝
（`unsupported_parameter`）、lenient 模式下被丢弃——这两种协议请使用空缺标记写法。

## 响应映射（FIM → Chat Completions）

- `choices[0].text` → `message.content`；`finish_reason`：`stop` / `length` / `content_filter` 原样映射，其他值按 `stop`。
- `insufficient_system_resource` 按上游 503、`aborted` 按上游 502 处理（生成被中断，输出不完整）：尚未向客户端写出时换渠道重试，
  流式已写出后以流内错误结束。
- `usage`：`prompt_tokens`、`completion_tokens`、`total_tokens`，缓存命中 `prompt_tokens_details.cached_tokens`
  （取 `prompt_tokens_details.cached_tokens`，没有时取 `prompt_cache_hit_tokens`），按缓存价计费。
- HTTP 200 中的错误体 `{error: {...}}` 按上游 502 处理；非 2xx 由 `normalizeError` 取 `error.message`，状态码沿用上游
  （400 / 401 / 402 / 422 / 429 / 500 / 503；429 与 5xx 换渠道重试，401 记为渠道认证失败，其他 4xx 直接返回给客户端）。
- 流式：按行缓冲（行可以跨块，`TextDecoder` 流式解码多字节字符），忽略 `: keep-alive` 注释行；用量既可以在带 `finish_reason` 的末块上，
  也可以是单独的 chunk。

## 调用示例

以下 `$OMNIGATE` 为网关地址，`$KEY` 为网关 Key（不是 DeepSeek 的 Key）。

顶层 `prompt` / `suffix`：

```bash
curl $OMNIGATE/v1/chat/completions -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" -d '{
  "model": "deepseek-fim",
  "prompt": "def fib(a):\n",
  "suffix": "    return fib(a-1) + fib(a-2)\n",
  "max_tokens": 128,
  "messages": []
}'
```

（`messages` 可以省略；部分 SDK 要求必须有 `messages`，传空数组即可。OpenAI Python SDK 用 `extra_body={"prompt": ..., "suffix": ...}`。）

空缺标记写在 user 消息中：

```bash
curl $OMNIGATE/v1/chat/completions -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" -d '{
  "model": "deepseek-fim",
  "messages": [{"role": "user", "content": "def fib(a):\n<|fim_hole|>\n    return fib(a-1) + fib(a-2)\n"}],
  "max_tokens": 128
}'
```

流式：

```bash
curl -N $OMNIGATE/v1/chat/completions -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" -d '{
  "model": "deepseek-fim",
  "stream": true,
  "prompt": "// 冒泡排序\nfunc bubbleSort(a []int) {\n",
  "suffix": "\n}\n"
}'
```

返回标准 Chat Completions 响应（流式为 `chat.completion.chunk`），补全内容在 `choices[0].message.content` / `delta.content` 中，
请求日志照常记录用量（含缓存命中）与费用。

## 限制

- OmniGate **没有** `/v1/completions` 入口：只会调用旧版 Completions 接口的客户端（例如直接配置为 OpenAI Completions 的补全插件）不能使用本渠道，
  需要客户端能调用 Chat Completions（或经网关转换的 Messages / Responses）。
- 只取 `choices[0]`（不支持 `n > 1`）；不返回 `logprobs`。
- FIM 是 DeepSeek 的 Beta 功能，接口与模型可能变化；地址不同时修改 `fimPath`，模型变化时修改渠道的模型映射。
- 余额查询与模型发现调用 `GET /user/balance`、`GET /models`（同样按 Base URL 的源拼接），只有 DeepSeek 官方 API 提供。

权限：只能访问 `api.deepseek.com` 与渠道 Base URL 所在主机；只能取得 `apiKey` 的句柄。`tests/` 下是模拟测试用例，在插件编辑器中运行时不会访问真实网络。
