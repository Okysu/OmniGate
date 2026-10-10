# 契约：Round 14 —— 文本补全接口（`/v1/completions`，含 FIM）

> 状态：**已实现**。通用约定同前；所有 SQL 同时支持 PostgreSQL 与 SQLite（无新迁移：渠道 `config` 与模型资料 `capabilities` 都是 JSON 列）。
> 需求：编辑器的代码补全插件（如 Continue）使用 OpenAI 旧版文本补全接口，并用 `suffix` 做 FIM（Fill-In-the-Middle）补全；
> DeepSeek 等厂商在 `…/beta/completions` 提供 FIM。网关原生提供该入口，按与嵌入、图片、语音接口相同的“只直通”方式实现。

## 1. 接口

| 方法 | 路径 | 请求体 | 响应 |
| --- | --- | --- | --- |
| POST | `/v1/completions` | JSON：`model`（必填）、`prompt`、`suffix`、`max_tokens`、`temperature`、`top_p`、`n`、`stream`、`stream_options`、`logprobs`、`echo`、`stop`、`presence_penalty`、`frequency_penalty`、`best_of`、`logit_bias`、`user`、`seed` 以及任何其他字段 | 透传（`text_completion` 对象；`stream: true` 时 SSE 的 `text_completion` 块 + `data: [DONE]`） |

- 入口协议 `openai.completions`（请求日志 `inbound`、统计、Prometheus 标签、路由预览的 `inbound`）。
- 网关只读取 `model`、`stream`、`stream_options.include_usage` 与 `max_tokens`（余额预占估算），不解析 `prompt`：字符串、字符串数组、
  token 数组、token 数组的数组都原样转发。缺少 `model` → `400 invalid_request_error`。
- 错误信封为 OpenAI 格式，与 Chat 入口相同。

## 2. 只直通，渠道显式声明支持

文本补全**从不做协议转换**：`suffix`（FIM）、`echo`、`best_of`、`logprobs` 与 token 数组 prompt 在 Chat / Messages / Responses 中都没有等价物，
转换只会悄悄改变语义。因此：

- 只有 `type = openai` 且 `config.supportsCompletions = true` 的渠道是候选（扩展 `openai.chat` 的插件渠道类型也是 `openai`，同样适用）。
  `anthropic` 渠道与自定义协议（`custom`）渠道永远不提供该接口，即使配置里带了这个字段也被忽略。
- 与嵌入 / 图片不同，这里**需要显式开启**：很多 OpenAI 兼容上游（包括 OpenAI 官方的大部分模型）已经没有 `/completions`，
  默认把请求发过去只会得到 404 并触发重试与熔断计数。
- 上游请求：`POST {baseUrl}/completions`，请求体原样转发，只把 `model` 改成渠道的上游模型名；流式请求见 §4。
  例如 `baseUrl = https://api.deepseek.com/beta` 的渠道收到的是 `/beta/completions`。
- 没有任何渠道支持时（模型存在于其他渠道也一样）：`404`，`code: "model_not_found"`，消息
  `model "x" is not available for this API key (completions are served by OpenAI-compatible channels with Completions support enabled only)`，
  与嵌入 / 图片的“该接口不可用”一致。路由预览把不支持的渠道列为跳过（“该接口仅由开启了 Completions 的 OpenAI 兼容渠道提供”）。

```ts
interface ChannelConfig {
  // …（phase1-api.md）
  supportsResponses?: boolean
  supportsCompletions?: boolean   // 仅 openai：上游实现了旧版 /completions（含 suffix 的 FIM）；默认 false
}
```

渠道路由（自有 → 共享 → 平台）、路由规则（按模型匹配，目标 / 策略 / 回退模型 / 重试）、熔断、超时、Key 策略（允许的渠道、模型限制、IP、RPM）、
用户组 rpm / rpd 与消费限额、套餐配额、钱包预占与插件 Hook（`transformRequest` / `signRequest`，`dialect = "openai.completions"`、
`path = "/completions"`）都与其他接口相同。回退模型切换时同样只改写 `model`。

## 3. 用量与计费

用量从上游响应的 `usage` 读取（非流式取响应体，流式取 §4 的用量块），与 Chat 的规范化方式相同——缓存读取与输入分开计价：

| 上游字段 | 规范化 |
| --- | --- |
| `prompt_tokens_details.cached_tokens`（OpenAI） | `cacheRead`；`input = prompt_tokens − cached_tokens` |
| `prompt_cache_hit_tokens` / `prompt_cache_miss_tokens`（DeepSeek，未给 `cached_tokens` 时） | `cacheRead = hit`；`input = prompt_tokens − hit`（没有 `prompt_tokens` 时取 `miss`） |
| `completion_tokens` | `output` |

两种字段同时出现时以 `cached_tokens` 为准。文本补全没有推理 token。上游没有返回用量时与 Chat 相同：按请求体与输出字节估算并标记 `usageEstimated`。
计价、分时倍率、用户组倍率、上下文长度阶梯（阈值按 `input + cacheRead + cacheWrite` 的提示词 token 判断，phase10-api.md §1）、成本价、
套餐计量与失败不计费规则与 Chat 完全相同；价格按逻辑模型设置，无需为 Completions 单独定价。

## 4. 流式

与 Chat 直通相同（`protocol.RewriteForPassthrough`）：

- `stream: true` 时网关强制 `stream_options.include_usage = true`（保留客户端的其他 `stream_options` 字段），使上游在末尾发送用量块
  （`choices: []` + `usage`）。
- 客户端自己没有要求 `include_usage` 时，这个只含用量的块不转发给客户端；带 `choices` 的块即使带用量也原样转发。
- 其余 SSE 事件（含注释行）字节不变地转发；`data: [DONE]` 为结束事件。开始输出后不再重试，中途出错以 OpenAI 格式的错误事件告知。

## 5. 会话亲和

- 规则可以用 `path_regex`（如 `^/v1/completions`）、模型、User-Agent、`client_include` 匹配文本补全请求，取值来源用请求头
  （编辑器插件的会话头）或请求体字段（如 `user`）。
- **`anchor` 对文本补全永远取不到值**：FIM 的 `prompt` / `suffix` 随每次按键变化，提示词指纹每次都不同，只会不断新建绑定而起不到亲和作用。
  只配了 `anchor` 来源的规则因此不会对文本补全生效（继续检查下一条规则）。
- `inject_prompt_cache_key` / `inject_session_header` **不作用于文本补全**（只作用于 Chat / Responses 上游请求，phase12-api.md §2.6）：
  FIM 上游不认识 `prompt_cache_key`，严格的上游可能拒绝未知字段。`pass_headers` 照常透传。

## 6. 模型资料与模型广场

- 模型资料 `capabilities` 新增 `completions: boolean`（控制台显示为「文本补全 / FIM」），旧数据缺省为 `false`。
- 模型广场 `protocols` 新增 `openai.completions`（标签「Completions」）：模型标记了 `completions` **且**至少有一个开启了
  `supportsCompletions` 的可用渠道提供该模型时才列出。该标记不影响 Chat / Responses / Messages 的列出规则（补全模型同时是对话模型）。
- 模型详情的调用示例在列出 `openai.completions` 时增加「FIM 补全 · curl / Python」；API Key 页的使用示例也包含 FIM 示例。

## 7. 控制台

- **渠道表单 → 高级**：`openai` 渠道新增开关「支持 Completions（/v1/completions，含 FIM）」，说明中给出 DeepSeek 的 Base URL 示例；
  渠道详情显示是否支持。模型发现不受影响。
- **请求日志**：入口协议显示「OpenAI Completions」。**路由预览**：客户端协议可选 `openai.completions`。
- **模型资料**：能力多了「文本补全 / FIM」；模型广场按能力筛选与协议筛选同样可用。

## 8. 示例：DeepSeek FIM

参照 DeepSeek 文档 [FIM Completion (Beta)](https://api-docs.deepseek.com/guides/fim_completion)：

1. 新建渠道：类型 `openai`，Base URL `https://api.deepseek.com/beta`（FIM 是 Beta 功能，必须带 `/beta`），填入 DeepSeek API Key；
   模型 `deepseek-flash`、`deepseek-v4-pro`（按实际可用的模型名填写或用“发现模型”）；在“高级”中打开「支持 Completions」。
2. （可选）在模型资料中为这些模型勾选「文本补全 / FIM」，模型广场即显示 Completions 标签与示例。
3. 客户端：

```bash
curl https://gw.example.com/v1/completions \
  -H "Authorization: Bearer og-你的密钥" \
  -H "Content-Type: application/json" \
  -d '{"model": "deepseek-flash", "prompt": "def fib(n):\n    a, b = 0, 1\n", "suffix": "\n    return a\n", "max_tokens": 128}'
```

注意：FIM 走非思考模式（不要在请求里开启思考 / 推理参数）；DeepSeek 的 FIM 输出上限为 4K token。同一个 `/beta` 渠道也可以照常服务
Chat 请求（`/beta/chat/completions`）；如需把对话流量留在正式地址，另建一个不开启 Completions 的渠道即可，二者服务同一个逻辑模型时
Chat 请求按正常的优先级 / 权重分配，文本补全请求只会到开启了 Completions 的渠道。

## 9. 实现差异

- 只支持 JSON 请求体；不做任何字段校验或改写（除 `model` 与流式的 `include_usage`），上游的参数错误以 4xx 返回（消息取自上游，默认不重试）。
- `max_tokens` 缺省时余额预占只按输入估算（与 Chat 相同；OpenAI 的缺省值 16 不计入预占）。
- 渠道连通性测试仍使用 Chat / 模型列表，不单独探测 `/completions`。
