# 请求体改写（community.body-rewrite）

继承 OpenAI 兼容协议（`extends: openai.chat`），只实现 `transformRequest`：在网关把请求发往上游之前，按渠道配置改写 JSON 请求体。
适用于 Ollama、vLLM、SGLang、LM Studio、阿里云百炼兼容模式、智谱等 OpenAI 兼容上游。常见用途：

- 给视觉模型或本地模型强制开启 / 关闭思考；
- 做“小上下文专属渠道”，限制输出长度；
- 删除上游不支持、会报错的参数（如 `parallel_tool_calls`、`stream_options`、`service_tier`）；
- 注入厂商特有参数（如 `top_k`）。

所有配置项都可以不填，默认不做任何改动。插件不访问网络、不需要额外权限与密钥（渠道的 API Key 仍由网关自动添加）。

## 处理范围

只改写 **Chat Completions**（`/chat/completions`）与 **Responses**（`/responses`，渠道开启了 Responses 或模型的上游协议设为 responses 时）
两种 JSON 请求体。embeddings、图片、音频等请求原样转发；图片编辑等 multipart 请求不会进入 `transformRequest`。

改写按以下顺序进行，原地修改、不复制请求体，几 MiB 的长上下文或 base64 图片请求也远在 Hook 时限之内：

1. `modelRegex`：模型不匹配就什么都不做；
2. `removeFields`：删除字段；
3. `developerToSystem`：developer 角色改为 system；
4. `thinking`：开关思考（以及 `stripReasoningContent`）；
5. `maxTokensCap`：限制输出长度；
6. `mergeBody`：最后深度合并，因此它写入的值优先于前面的步骤。

不会删除或替换顶层的 `model`、`stream`、`messages`、`input`（删除字段、合并 JSON、自定义开关路径都会跳过它们），不会删除消息，
不改动工具定义与工具调用，流式请求照常流式。

## 配置项

| 配置项 | 类型 | 说明 |
| --- | --- | --- |
| `modelRegex` | 字符串 | 只改写上游模型名（请求体 `model`，即渠道映射后的上游模型）匹配的请求；不区分大小写；留空表示所有模型。例：`^qwen3`、`^(glm-4\.6\|glm-4\.7)` |
| `thinking` | `passthrough` / `on` / `off` | 默认 `passthrough`（不改动） |
| `thinkingStyle` | 枚举 | 思考开关的写法，见下表；默认 `openai` |
| `reasoningEffort` | `low` / `medium` / `high` | `thinking: on` 且写法为 `openai` / `ollama` 时写入的推理强度，默认 `medium` |
| `reasoningEffortOff` | `auto` / `none` / `minimal` / `low` | `openai` 写法关闭思考时的取值；`auto`（默认）按模型系列取 OpenAI 接受的最低值，见下文 |
| `thinkingPath` | 字符串 | `custom` 写法要设置的字段：点分路径 `extra.enable_reasoning` 或 JSON Pointer `/extra/enable_reasoning` |
| `thinkingOnValue` / `thinkingOffValue` | 字符串（JSON） | `custom` 写法开启 / 关闭时写入的 JSON 值，如 `true`、`"enabled"`、`{"type": "enabled"}`；留空表示该状态不写 |
| `stripReasoningContent` | 布尔 | `thinking: off` 时删除 assistant 消息中的 `reasoning_content` / `reasoning` 字段（部分本地服务拒绝它们）；只作用于 Chat 请求体 |
| `maxTokensCap` | 整数 ≥ 1 | 把 `max_tokens`、`max_completion_tokens`、`max_output_tokens` 中超过该值的压到该值 |
| `setMaxTokensWhenMissing` | 布尔 | 请求没有任何输出长度字段时写入上限：Chat 写 `max_tokens`，Responses 写 `max_output_tokens` |
| `removeFields` | 字符串 | 要删除的字段，逗号、空格或换行分隔，支持点分路径。例：`parallel_tool_calls, stream_options, service_tier, prompt_cache_key, metadata.user_id` |
| `developerToSystem` | 布尔 | 把 `role: "developer"` 的消息（Responses 为 `input` 中的项）改为 `role: "system"`，适用于不认识 developer 角色的上游 |
| `mergeBody` | 字符串（JSON 对象） | 最后深度合并进请求体：对象逐层合并，数组与标量（含 `null`）直接替换。例：`{"top_k": 20}`、`{"chat_template_kwargs": {"enable_thinking": false}}` |

> 插件配置表单只支持字符串、数字、整数与布尔值，所以 `removeFields` 写成逗号分隔的字符串，`mergeBody` 写成 JSON 文本
> （保存时检查是否以 `{` 开头、`}` 结尾）。`modelRegex` 无效时插件不做任何改写；`mergeBody` 或自定义开关值不是合法 JSON 时跳过这一步，
> 其余步骤照常执行。这些情况会在插件编辑器的测试日志中给出提示。

## 思考开关写法

| `thinkingStyle` | 开启（`on`） | 关闭（`off`） | 依据 |
| --- | --- | --- | --- |
| `openai` | Chat：`"reasoning_effort": "<reasoningEffort>"`；Responses：`"reasoning": {"effort": …}`（合并进已有的 `reasoning` 对象） | 同一字段写入最低强度：`auto` 时 gpt-5.1 及以后、gpt-6-sol / luna 与其他模型为 `none`；`gpt-5` / `gpt-5-mini` / `gpt-5-nano` 为 `minimal`；o 系列、`gpt-6-astra`、`gpt-6.1-sol` 为 `low`；`gpt-5-pro` 只接受 `high`，不改动 | [Reasoning 指南](https://developers.openai.com/api/docs/guides/reasoning)、[gpt-5](https://developers.openai.com/api/docs/models/gpt-5)、[gpt-5.1](https://developers.openai.com/api/docs/models/gpt-5.1)、[gpt-5-pro](https://developers.openai.com/api/docs/models/gpt-5-pro)、[gpt-6-astra](https://developers.openai.com/api/docs/models/gpt-6-astra)、[gpt-6.1-sol](https://developers.openai.com/api/docs/models/gpt-6.1-sol)、o 系列见 [openai-python 中的参数说明](https://github.com/openai/openai-python/blob/v1.99.0/src/openai/types/chat/completion_create_params.py) |
| `qwen_dashscope` | `"enable_thinking": true` | `"enable_thinking": false` | [百炼：深度思考](https://help.aliyun.com/zh/model-studio/deep-thinking)（OpenAI 兼容模式下为请求体顶层字段） |
| `chat_template_kwargs` | `"chat_template_kwargs": {"enable_thinking": true, "thinking": true}` | 同上，值为 `false` | [vLLM：Reasoning Outputs](https://docs.vllm.ai/en/latest/features/reasoning_outputs.html)、[SGLang：Reasoning Parser](https://docs.sglang.io/advanced_features/separate_reasoning.html) |
| `zhipu` | `"thinking": {"type": "enabled"}` | `"thinking": {"type": "disabled"}` | [智谱：思考模式](https://docs.bigmodel.cn/cn/guide/capabilities/thinking-mode)、[Z.ai: Thinking Mode](https://docs.z.ai/guides/capabilities/thinking-mode) |
| `ollama` | Chat：`"reasoning_effort": "<reasoningEffort>"`；Responses：`"reasoning": {"effort": …}` | 同一字段写入 `"none"` | [Ollama：OpenAI 兼容](https://docs.ollama.com/api/openai-compatibility)、[源码 openai/openai.go](https://github.com/ollama/ollama/blob/main/openai/openai.go)（`ThinkingFromReasoningEffort`）、[openai/responses.go](https://github.com/ollama/ollama/blob/main/openai/responses.go) |
| `qwen_soft_switch` | 最后一条用户消息末尾追加 ` /think` | 追加 ` /no_think` | [Qwen3 模型卡：软开关](https://huggingface.co/Qwen/Qwen3-8B#advanced-usage-switching-between-thinking-and-non-thinking-modes-via-user-input) |
| `custom` | 在 `thinkingPath` 写入 `thinkingOnValue` | 写入 `thinkingOffValue` | 由你按上游文档填写 |

各写法的细节：

- **openai**：OpenAI 不同模型接受的取值不同（例如 gpt-5 不支持 `none`，gpt-5.1 不支持 `minimal`，gpt-6.1-sol 两者都不支持），
  `auto` 按上述规则选择；模型名带前缀（如 `openai/gpt-5`）时按最后一段判断。新模型或其他厂商的模型可能有不同取值，
  遇到 400 时用 `reasoningEffortOff` 固定取值。开启时写入的 `reasoningEffort` 同样需要模型支持（gpt-5-pro 只接受 `high`）。
- **ollama**：Ollama 的 `/v1/chat/completions` **不读取** `think` 字段，它把 `reasoning_effort`（或优先级更高的 `reasoning.effort`）
  转换为原生的 think 值：`"none"` 关闭思考，`low` / `medium` / `high` 开启（只支持开关的模型按 `true` 处理）。
  因此 Chat 请求中已有 `reasoning` 对象时插件同步改写其 `effort`；Responses 请求中扩展字段 `think` 优先于 `reasoning.effort`，插件会删除 `think`。
- **chat_template_kwargs**：Qwen3 的模板读取 `enable_thinking`，DeepSeek-V3.1 及以后的模板读取 `thinking`，插件两者都写；
  vLLM 会丢弃模板未使用的参数，Jinja 模板会忽略未使用的变量，所以对两类模型都安全。已有的 `chat_template_kwargs` 其他键保留。
- **zhipu**：合并进已有的 `thinking` 对象（保留 `clear_thinking` 等字段）。GLM-5.3 等强制思考的模型无法关闭。
- **qwen_soft_switch**：只改最后一条 `role: "user"` 的消息：字符串内容直接追加；多段内容改最后一个文本段（Chat 为 `text`、Responses 为 `input_text`），
  没有文本段时追加一个；Responses 的字符串 `input` 直接追加。末尾已经有 `/think` 或 `/no_think` 时先去掉再追加，因此不会重复。
  按 Qwen3 模型卡，软开关只在模板的 `enable_thinking` 为 true（默认）时生效，Qwen3-2507 等纯思考 / 纯指令版本不支持软开关。
- 对 Responses 请求体，`qwen_dashscope`、`chat_template_kwargs`、`zhipu` 写入与 Chat 相同的顶层字段；上游的 `/responses` 是否识别取决于厂商实现。

## 配方

### Ollama 关闭思考

```text
thinking            = off
thinkingStyle       = ollama
stripReasoningContent = 开（可选：不把历史推理内容发回去）
```

请求体变化：`{"model": "qwen3:8b", "messages": [...]}` → `{"model": "qwen3:8b", "messages": [...], "reasoning_effort": "none"}`。

### vLLM Qwen3 强制思考

```text
modelRegex    = ^Qwen/Qwen3
thinking      = on
thinkingStyle = chat_template_kwargs
```

请求体增加 `"chat_template_kwargs": {"enable_thinking": true, "thinking": true}`。SGLang 相同。

### 小上下文专属渠道（maxTokensCap + num_ctx）

为显存有限的本地模型单独建一个渠道，限制输出长度，并让上游用较小的上下文窗口：

```text
maxTokensCap            = 1024
setMaxTokensWhenMissing = 开
```

上下文窗口要在上游设置，OpenAI 兼容接口没有通用的请求级参数：

- **Ollama**：`/v1/chat/completions` 不读取 `options`（[官方说明](https://docs.ollama.com/api/openai-compatibility#setting-the-local-context-size)），
  因此 `mergeBody` 写 `{"options": {"num_ctx": 8192}}` 对它**不起作用**。请用 Modelfile 建一个小上下文模型
  （`FROM qwen3:8b` + `PARAMETER num_ctx 8192`，`ollama create qwen3-8k`），渠道的上游模型映射到 `qwen3-8k`。
- **vLLM / SGLang**：上下文长度是服务启动参数（`--max-model-len` / `--context-length`）。
- 读取 `options` 的上游（例如在 Ollama 前面做了转换的代理）可以用 `mergeBody`：`{"options": {"num_ctx": 8192}}`。

### 删除上游不支持的参数

```text
removeFields = parallel_tool_calls, stream_options, service_tier, prompt_cache_key
```

### developer 角色改为 system

部分 OpenAI 兼容服务（旧版本地推理服务、一些厂商接口）不认识 OpenAI 新增的 `developer` 角色：打开 `developerToSystem`，
`{"role": "developer", "content": "…"}` 会以 `{"role": "system", "content": "…"}` 发出。

### 注入厂商参数

```text
mergeBody = {"top_k": 20, "repetition_penalty": 1.05}
```

## 限制

- `mergeBody` 不能覆盖顶层的 `model`、`stream`、`messages`、`input`（这些键被忽略）；`removeFields` 与 `thinkingPath` 也不能改写它们。
- 键名形如 `__x__` 的字段会被跳过（防止原型链污染）。
- `removeFields` 只支持对象路径，不支持数组下标。
- `maxTokensCap` 只压低数值型的长度字段；请求没有长度字段时只写 `max_tokens` / `max_output_tokens`
  （Ollama 只读取 `max_tokens`，不读取 `max_completion_tokens`）。
- `stripReasoningContent` 只作用于 Chat 请求体的 assistant 消息；Responses 的 `reasoning` 输入项保持不变。
- 插件在网关完成协议转换之后执行，看到的是发往上游的最终请求体（例如 Anthropic 客户端的请求已转换为 Chat Completions）。
- `modelRegex` 使用 JavaScript 正则语法。

`tests/` 下是模拟测试用例，可在插件编辑器中运行。
