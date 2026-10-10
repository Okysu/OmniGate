# 契约：Round 13 —— 客户端识别

> 状态：**已实现**。通用约定同前；所有 SQL 同时支持 PostgreSQL 与 SQLite（迁移 `00020_client_detect`）。
> 需求：知道请求来自哪个客户端（Claude Code、Codex、Cherry Studio、官方 SDK、curl …），按客户端查看日志、统计缓存命中率与会话亲和命中率，
> 并让会话亲和规则只作用于特定客户端。识别规则的思路参照 SukiRouter 的 `internal/clientdetect`，在 OmniGate 中重新实现并逐条核对。
> 只保存识别结果（客户端 id 与版本），**从不保存原始 User-Agent 或其他请求头**。

## 1. 识别规则（`server/internal/clientdetect`）

`clientdetect.Detect(header)` 在每个数据面请求开始时调用一次（`gateway.handle`，认证之前，所以认证失败的请求也有结果），
返回 `{ id, name, version }`。规则按下表顺序检查，**第一条匹配的规则胜出**；都不匹配时为 `unknown`。顺序的原则：
客户端独有的请求头 / User-Agent 产品名在前，它们所基于的 SDK 在后（Claude Code 基于 Anthropic SDK，很多工具基于 OpenAI SDK），
通用 HTTP 工具最后。User-Agent 比较不区分大小写（Claude Desktop 的 `Claude/` 除外）；“产品名”指出现在开头或空格之后、紧跟 `/` 的名称
（因此 `zed` 不会匹配 `optimized/1.0`）。

版本：能廉价解析时取对应产品名 `/` 之后的版本（以数字开头，只保留数字、字母、`.`、`-`、`+`、`_`，最多 32 个字符，按小写的 User-Agent 解析），
SDK 取 `X-Stainless-Package-Version`；解析不出时为 `null`（如 `opencode/local`、`Go-http-client/1.1` 中的 1.1 是 HTTP 版本，不作为版本）。

| # | id | 名称 | 类别 | 识别信号 | 版本来源 |
| --- | --- | --- | --- | --- | --- |
| 1 | `claude-code` | Claude Code | agent | UA 以 `claude-cli/` 开头；或有 `X-Claude-Code-Session-Id`；或 `X-App: cli` 且有 `anthropic-version` | `claude-cli/<ver>` |
| 2 | `codex` | Codex | agent | `Originator` 以 `codex` 开头（`codex_cli_rs`、`codex_vscode`、`codex_exec` …）；或 UA 以 `codex` 开头（`codex_cli_rs/…`、`Codex Desktop/…`）；或有任一 `X-Codex-*` 请求头；或有 `X-OpenAI-Subagent` | UA 第一个产品的版本 |
| 3 | `gemini-cli` | Gemini CLI | agent | UA 以 `GeminiCLI/` 开头 | `GeminiCLI/<ver>` |
| 4 | `opencode` | opencode | agent | UA 以 `opencode/` 开头 | `opencode/<ver>` |
| 5 | `github-copilot` | GitHub Copilot | agent | UA 含 `GitHubCopilot`（如 `GitHubCopilotChat/0.22.4`）；或有 `Copilot-Integration-Id`；或有 `X-Onbehalf-Extension-Id` | `GitHubCopilotChat/<ver>` |
| 6 | `kilo-code` | Kilo Code | agent | UA 以 `Kilo-Code/` 开头；或 `X-Title: Kilo Code` | `Kilo-Code/<ver>` |
| 7 | `roo-code` | Roo Code | agent | UA 以 `RooCode/` 开头；或 `X-Title: Roo Code` | `RooCode/<ver>` |
| 8 | `cline` | Cline | agent | UA 以 `Cline/` 开头；或 `X-Title: Cline` | `Cline/<ver>` |
| 9 | `continue` | Continue | agent | UA 以 `Continue/` 开头 | `Continue/<ver>` |
| 10 | `aider` | aider | agent | UA 以 `Aider/` 开头；或 `X-Title: Aider` | `Aider/<ver>` |
| 11 | `cursor` | Cursor | agent | UA 产品 `Cursor/`（Electron UA） | `Cursor/<ver>` |
| 12 | `zed` | Zed | agent | UA 产品 `Zed/` | `Zed/<ver>` |
| 13 | `cherry-studio` | Cherry Studio | chat | UA 含 `CherryStudio`（Electron UA）；或 `X-Title: Cherry Studio` | `CherryStudio/<ver>` |
| 14 | `chatbox` | Chatbox | chat | UA 含 `chatboxapp`（`xyz.chatboxapp.app/…`） | `…chatboxapp…/<ver>` |
| 15 | `lobechat` | LobeChat | chat | UA 含 `LobeChat`；或 `X-Title` 为 `LobeChat` / `Lobe Chat` / `LobeHub` | `LobeChat/<ver>` |
| 16 | `nextchat` | NextChat | chat | UA 产品 `NextChat/`；或 `X-Title: NextChat` | `NextChat/<ver>` |
| 17 | `rikkahub` | RikkaHub | chat | UA 含 `RikkaHub`（`RikkaHub-Android/…`） | `RikkaHub…/<ver>` |
| 18 | `kelivo` | Kelivo | chat | UA 含 `Kelivo` | `Kelivo/<ver>` |
| 19 | `claude-desktop` | Claude Desktop | chat | UA 含 ` Claude/`（区分大小写）且有产品 `Electron/`；其中运行的 Claude Code 发送 `claude-cli`，已在第 1 条识别 | `Claude/<ver>` |
| 20 | `anthropic-sdk-python` | Anthropic SDK (Python) | sdk | 见下方 SDK 规则，`X-Stainless-Lang: python` | `X-Stainless-Package-Version` |
| 21 | `anthropic-sdk-js` | Anthropic SDK (JS) | sdk | SDK 规则，`X-Stainless-Lang: js` | 同上 |
| 22 | `anthropic-sdk` | Anthropic SDK | sdk | SDK 规则，其他语言（go、java …） | 同上 |
| 23 | `openai-sdk-python` | OpenAI SDK (Python) | sdk | SDK 规则，`X-Stainless-Lang: python` | 同上 |
| 24 | `openai-sdk-js` | OpenAI SDK (JS) | sdk | SDK 规则，`X-Stainless-Lang: js` | 同上 |
| 25 | `openai-sdk` | OpenAI SDK | sdk | SDK 规则，其他语言 | 同上 |
| 26 | `ai-sdk` | Vercel AI SDK | sdk | UA 含 `ai-sdk/`（`ai-sdk/openai/2.0.1`、`ai-sdk/provider-utils/… runtime/node.js/22`） | — |
| 27 | `curl` | curl | tool | UA 以 `curl/` 开头 | `curl/<ver>` |
| 28 | `python-requests` | python-requests | tool | UA 以 `python-requests/` 开头 | `python-requests/<ver>` |
| 29 | `python-httpx` | python-httpx | tool | UA 以 `python-httpx/` 开头 | `python-httpx/<ver>` |
| 30 | `aiohttp` | aiohttp | tool | UA 产品 `aiohttp/`（`Python/3.12 aiohttp/3.10.5`） | `aiohttp/<ver>` |
| 31 | `postman` | Postman | tool | UA 以 `PostmanRuntime/` 开头 | `PostmanRuntime/<ver>` |
| 32 | `go-http` | Go net/http | tool | UA 以 `Go-http-client/` 开头 | — |
| 33 | `node` | Node.js fetch | tool | UA 恰为 `node` 或 `undici`（Node 内置 fetch；须精确匹配，`runtime/node.js/24` 不算） | — |
| — | `unknown` | 未知 | unknown | 以上都不匹配（含浏览器 UA、空请求头） | — |

**SDK 规则**（Stainless 生成的 OpenAI / Anthropic 官方 SDK）：有 `X-Stainless-Lang`，并按语言分到 python / js / 其他；
UA 以 `Anthropic/` 开头 → Anthropic，以 `OpenAI/` 开头 → OpenAI；UA 是其他厂商的 Stainless SDK（第一个产品的版本位置就是语言名，如
`Groq/Python 0.9.0`）→ 不认领；否则（浏览器构建保留浏览器 UA）有 `anthropic-version` → Anthropic，没有 → OpenAI。

`X-Title`（OpenRouter 的应用署名请求头，很多客户端对任何 OpenAI 兼容端点都发送）按去除首尾空白、不区分大小写的**整体相等**比较，
`My Cline Fork` 不算 Cline。

保守之处：只有信号明确、来源可查的规则才加入。Continue、aider、Cursor、LobeChat、NextChat 的请求头因版本 / 运行方式而异
（如 Cursor 的自定义端点请求可能由其服务器发出、aider 经 litellm 走 OpenAI SDK），识别不到时会落到 SDK 或 `unknown`，不会误判为其他客户端。
SukiRouter 中的 ZCode、CodeWhale、Ktor、Unify Chat Provider 规则来源无法核实，未移植。

性能：纯函数，一次 `ToLower` 加若干次字符串比较与请求头查找，约 1–2 µs、2 次内存分配（`BenchmarkDetect`）。

### 1.1 `GET /api/clients`（`stats.own`）

```ts
{ items: { id: string, name: string, kind: 'agent' | 'chat' | 'sdk' | 'tool' | 'unknown' }[] }
```

按上表顺序列出全部客户端，最后一项为 `unknown`。它是 `GET /api/logs?client=` 与会话亲和规则 `client_include` 的可选值；
控制台的过滤下拉、规则多选与类别图标都来自这里（`web/src/stores/clients.ts`），前端不硬编码客户端列表。

## 2. 请求日志存储

`request_logs` 新增两列（迁移 `00020_client_detect`，两种数据库都可为空）：

| 列 | 说明 |
| --- | --- |
| `client` | 客户端 id；新日志总是有值（识别不出为 `unknown`），迁移前的旧日志为 `NULL`（读取时视为 `unknown`） |
| `client_version` | 解析出的版本；解析不出为 `NULL` |

原始 User-Agent 与请求头从不写入日志、数据库或结算日志（journal）。旧日志没有保存请求头，因此**不回填**。

## 3. `GET /api/logs`

- 列表项新增 `client: { id: string, name: string, version: string | null }`（`NULL` 的旧日志为 `{ id: "unknown", name: "未知", version: null }`；
  `name` 是当前版本的显示名称，已不再识别的 id 原样作为名称）。
- 新增过滤参数 `client`：`GET /api/clients` 中的一个 id（区分大小写）；`unknown` 同时包含 `NULL` 的旧日志；其他值 → `422`
  （`client 不是已知的客户端标识（可选值见 GET /api/clients）`）。`GET /api/stats/summary` 接受同一参数。

## 4. 统计 `GET /api/stats/summary`

新增 `byClient[]`（时间范围、可见性与其他过滤同原接口），按请求数倒序（相同时按 id），`NULL` 的旧日志归入 `unknown`：

```ts
{
  client: string, name: string,
  requests: number, errors: number,           // errors = status_code >= 400
  inputTokens: number,                        // 提示 token：输入 + 缓存读 + 缓存写（与 totals.inputTokens 相同口径）
  outputTokens: number, cacheReadTokens: number, cacheWriteTokens: number,
  cacheHitRate: number | null,                // cacheReadTokens ÷ inputTokens；没有提示 token 时为 null（phase12-api.md §5）
  affinityHits: number,                       // affinity = hit
  affinityBound: number,                      // affinity ∈ hit、rebound、failover、broken、strict_failed
  affinityHitRate: number | null,             // affinityHits ÷ affinityBound；为 0 时 null（phase12-api.md §6 的公式）
  charge: Money                               // 收费合计（与 byModel 相同；按渠道 / 模型的明细没有成本，这里也不返回成本）
}
```

## 5. 会话亲和 `client_include`（OmniGate 扩展）

`gateway.affinity` 的规则新增可选字段 `client_include: string[]`（[phase12-api.md §1](phase12-api.md)）：

- 值为 `GET /api/clients` 中的 id（含 `unknown`，即只对识别不出的客户端生效）；去除首尾空白、去重、忽略空串；未知 id → `422`
  （`rules[0].client_include：未知的客户端标识 "…"`）。数量受已知 id 个数限制，不另设上限。
- `[]`（或缺失、`null`）= 任意客户端。new-api 的 JSON 不含该字段，照样可以粘贴；保存后的规范形式总是带 `client_include`（`[]`）。
- 匹配：与 `model_regex`、`path_regex`、`user_agent_include` 同时满足才继续取会话值；不满足时检查下一条规则（与其他条件相同）。
  识别结果在请求开始时计算一次，规则匹配与请求日志使用同一个结果。
- 内置预设（`gpt session`、`codex cli trace`、`claude cli trace`）的 `client_include` 为 `[]`，行为不变。

与 `user_agent_include` 的区别：后者是任意子串，`client_include` 使用 §1 的识别结果（也看 `Originator`、`X-Title`、Stainless 等请求头），
更不容易被 User-Agent 的细微变化绕过。

## 6. 控制台

- **请求日志**：新增“客户端”列（类别图标 + 名称，悬停显示名称、版本、类别与 id）；“客户端”过滤下拉（按类别分组，来自 `GET /api/clients`）；
  详情中显示客户端、版本与 id，识别不出时说明网关不记录原始 User-Agent。
- **统计**：新增“客户端”表：请求、失败率、输入 / 输出、缓存命中、亲和命中、收费。缓存命中率低于 30% 标红、低于 60% 标黄
  （提示 token 少于 1 万的客户端不标记，流量太少不足以判断）；表格在卡片内横向滚动。
- **系统设置 → 会话亲和**：规则编辑抽屉在“User-Agent 包含”下新增“客户端”多选（搜索、按类别提示，不选 = 任意客户端）；
  规则表的名称列在设置了 `client_include` 时显示“仅 Claude Code、Codex”。
- 类别图标为通用的 lucide 图标（编码代理 / 聊天客户端 / SDK / HTTP 工具 / 未识别），不使用品牌图标资源。

## 7. 实现差异

- 识别基于客户端自报的请求头，可以伪造；它用于统计与路由偏好，不是安全边界（规则的 `client_include` 不应用于权限控制）。
- 迁移前的日志没有客户端信息，统计中归入 `unknown`。
- 新增识别规则后，旧日志保持当时的识别结果（不重算）。
