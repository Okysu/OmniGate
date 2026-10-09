# 契约：Phase 2 插件系统（Round 3 实现范围）

> 状态：**定稿，按此实现**。SDK 概念见 [plugin-sdk.md](plugin-sdk.md)；引擎与安全模型见 ADR-0002；UI 见 ADR-0003。
> 通用约定同 Phase 1（camelCase、错误信封、CSRF 头、分页、`version` 乐观锁、金额为十进制字符串）。

## 0. Round 3 范围

| 包含 | 推迟到 Round 4 |
| --- | --- |
| 插件包格式、manifest v1 校验、TS 编译（esbuild）、静态风险扫描 | 完整自定义协议（`parseResponse` / `parseStream` Hook） |
| Goja 运行时池、超时、内存看门狗、并发上限、违规熔断 | 订阅源（远程更新、签名校验） |
| 宿主 API：`og.fetch`（白名单 + 防 SSRF）、`og.secret` 句柄、`og.crypto`、`og.storage`、`og.log` | 灰度发布（按渠道比例） |
| 原子能力：`models.list`、`balance.get`、`usage.query`、`quota.get`、`health.check`、`custom.*`；手动触发、定时执行、结果缓存 | 插件市场 |
| 请求 Hook：`transformRequest`、`signRequest`（发送前改写路径/头/体） | |
| 草稿 → 发布（不可变版本）→ 权限审批 → 渠道固定版本 → 升级（`migrateConfig`）/ 回滚 | |
| 声明式 UI 贡献点与渲染 | |
| 在线编辑器（Monaco，多文件，SDK 类型提示）、编译检查、模拟测试、ZIP 导入导出 | |
| 内置插件：`builtin.openai`、`builtin.anthropic`；随二进制分发的示例：`community.deepseek` | |

## 1. 插件包

```
manifest.json      必需
src/index.ts       入口（由 manifest.entry 指定，.ts 或 .js）
src/**/*.ts|js     其他模块，只能用相对路径导入；另外可导入 "@omnigate/plugin-sdk"
README.md          可选
tests/*.json       可选：模拟测试用例（见 §6）
```

单个文件 ≤ 256 KiB，总计 ≤ 1 MiB，最多 64 个文件；路径只允许 `[A-Za-z0-9._/-]`，不允许 `..` 和绝对路径。

## 2. manifest v1

```jsonc
{
  "id": "community.deepseek",          // ^[a-z0-9]+(\.[a-z0-9-]+)+$；"builtin." 前缀保留
  "name": "DeepSeek",
  "version": "1.0.0",                  // semver；同一插件新版本必须大于已发布的最大版本
  "sdk": 1,
  "description": "…", "author": "…", "homepage": "https://…",
  "extends": "openai.chat",            // openai.chat | anthropic.messages（决定渠道 type）
  "entry": "src/index.ts",
  "defaults": {                        // 新建渠道时预填，可修改
    "baseUrl": "https://api.deepseek.com/v1",
    "models": [{ "model": "deepseek-chat", "upstreamModel": "deepseek-chat" }]
  },
  "configSchema": {                    // JSON Schema 子集，见 §2.1
    "type": "object",
    "properties": {
      "region": { "type": "string", "title": "区域", "enum": ["cn", "global"], "default": "cn" },
      "balanceAlert": { "type": "number", "title": "余额告警阈值", "minimum": 0 }
    }
  },
  "permissions": {
    "network": ["api.deepseek.com", "$baseUrl"],   // 主机名，或 "$baseUrl" 表示渠道 baseUrl 的主机
    "secrets": ["apiKey"],                          // 可通过 og.secret 取得句柄的字段；apiKey = 渠道主 API Key
    "storage": { "maxKeys": 100, "maxBytes": 65536 },
    "schedule": ["balance.get"],
    "dangerous": []                                 // 预留；Round 3 不接受任何危险权限
  },
  "capabilities": {
    "balance.get": {
      "output": "balance",             // balance | quota | models | usage | health | json
      "userTriggerable": true,
      "schedule": { "minInterval": "10m" },   // 需要 permissions.schedule 包含该能力
      "cacheTtl": "5m",              // 预留：Round 3 只校验不使用（结果总是来自最近一次执行）
      "timeout": "10s",                // 默认 10s，最大 60s
      "label": "余额"
    }
  },
  "hooks": ["transformRequest", "signRequest"],
  "uiContributions": [ /* §5 */ ]
}
```

### 2.1 configSchema 子集

根必须是 `{"type":"object","properties":{…}}`；属性类型 `string | number | integer | boolean`；支持 `title`、`description`、`default`、`enum`、
`minimum`、`maximum`、`minLength`、`maxLength`、`pattern`、`required`（根级数组），以及扩展：`x-secret: true`（加密存储、读取接口只返回是否已设置，
名称须同时出现在 `permissions.secrets`）、`x-group`、`x-help`。最多 32 个属性。

## 3. 数据模型（迁移 `00004_plugins.sql`）

| 表 | 说明 |
| --- | --- |
| `plugins` | `id uuid`、`plugin_key`（唯一）、`name`、`description`、`author`、`homepage`、`source`（builtin / bundled / upload / editor）、`status`（enabled / disabled）、`created_by`、时间、`version` |
| `plugin_versions` | 不可变：`plugin_id`、`version`（唯一）、`manifest jsonb`、`files jsonb`、`bundle text`（编译产物；builtin 为空）、`content_hash`、`risk jsonb`、`approval`（pending / approved / rejected）、`approved_by/at`、`published_by/at` |
| `plugin_drafts` | 每个插件一份可编辑草稿：`files jsonb`、`base_version_id`、`updated_by/at`、`version` |
| `plugin_storage` | `(plugin_id, channel_id, key) → value jsonb`，配额由 manifest 声明 |
| `capability_results` | 每个（渠道，能力）最新一次结果：`ok`、`output jsonb`、`error`、`plugin_version_id`、`duration_ms`、`fetched_at` |
| `channels` 新增列 | `plugin_version_id uuid NULL`（NULL = 按 type 使用内置协议）、`plugin_config jsonb` |

## 4. 运行时语义

### 4.1 能力调用

插件签名：`(input, ctx) => output | Promise<output>`。`ctx`：
`{ channel: { id, name, baseUrl, models: [{model, upstreamModel}] }, config: <非敏感插件配置>, capability: "balance.get", now: ISO 时间 }`。

标准输出（宿主校验，不符合即调用失败）：

| output | 结构 |
| --- | --- |
| `balance` | `{ currency: string, total: string, granted?: string, toppedUp?: string, available: boolean }` |
| `quota` | `{ windows: [{ id, label, used: string, limit: string, resetsAt?: string }] }` |
| `models` | `{ models: [{ id: string, displayName?: string }] }` |
| `usage` | `{ periods: [{ label, requests?: number, inputTokens?: number, outputTokens?: number, cost?: string, currency?: string }] }` |
| `health` | `{ ok: boolean, latencyMs?: number, message?: string }` |
| `json` | 任意 JSON（≤ 256 KiB） |

插件可以返回 `{ unsupported: true, reason: string }`，界面显示“不支持”而不是错误。

### 4.2 请求 Hook

在网关构造好上游请求、发送之前执行（同一次请求内共用一个运行时）。入参与返回值：

```ts
interface UpstreamRequest {
  dialect: "openai.chat" | "openai.responses" | "anthropic.messages"
  path: string                       // 相对渠道 baseUrl，如 "/chat/completions"
  headers: Record<string, string>    // 不含宿主设置的鉴权头
  body: any                          // 已解析的 JSON
}
transformRequest(req, ctx) => UpstreamRequest
signRequest(req, ctx) => UpstreamRequest   // 在 transformRequest 之后执行，通常用于自定义鉴权
```

约束：`path` 必须以 `/` 开头且不能改变主机；头名受与渠道自定义头相同的限制；头值可包含 `og.secret()` 句柄，由宿主在发送时替换。
声明了 `signRequest` 时，宿主**不再自动添加** `Authorization` / `x-api-key`。Hook 的超时是总预算：50ms × 声明的 Hook 数（两个都声明时 100ms）；返回值上限按请求体大小放宽（约 2 倍请求体 + 1MiB，最大 80MiB），不受能力输出 256KiB 的限制。Hook 失败按“可回退的上游失败”处理
（错误类别 `plugin_error`，计入熔断）。

### 4.3 宿主 API（全局 `og`）

| API | 说明 |
| --- | --- |
| `og.fetch(url, { method?, headers?, body?, timeoutMs? })` | 返回 `Promise<{ status, headers, text(), json() }>`；只允许 https（渠道允许内网时也允许 http）；主机必须在 `permissions.network`；不跟随重定向；响应 ≤ 4 MiB；单次调用最多 10 次 fetch；超时默认 10s |
| `og.secret(name)` | 返回不透明句柄（形如 `{{og-secret:apiKey:<nonce>}}`），只在 `og.fetch` 的 URL/头/体与 Hook 返回的头中被替换为明文 |
| `og.crypto.sha256(data, enc?)`、`og.crypto.hmacSha256(keyOrHandle, data, enc?)` | `enc` 为 `hex`（默认）或 `base64`；密钥可以是句柄，在 Go 侧解析 |
| `og.encoding.base64Encode(s)` / `base64Decode(s)` | |
| `og.storage.get(key)` / `set(key, value)` / `delete(key)` | 按（插件，渠道）隔离；受 manifest 配额限制 |
| `og.log.info/warn/error(...args)` | 写入调用日志（测试时返回给编辑器）与服务日志（句柄与疑似密钥会被脱敏） |

不提供 `require`、`process`、文件系统、计时器（`setTimeout`）。

### 4.4 资源限制

能力调用默认超时 10s（manifest 可设，最大 60s）；Hook 50ms；每个插件版本并发执行上限 8；单次调用输出 ≤ 256 KiB；
内存看门狗阈值 `OMNIGATE_PLUGIN_HEAP_LIMIT_MB`（默认 256，按进程堆增长量计）；同一插件（按插件计，不区分版本）5 分钟内 3 次资源违规（超时、内存）→ 自动停用该插件并写审计；计数在进程内存中，重启后清零。内置插件不能停用。插件启停状态随渠道快照每 15 秒刷新，多实例部署时各实例在 15 秒内同步。

## 5. 声明式 UI（ADR-0003）

```jsonc
{
  "slot": "channel.detail.capabilities",       // 或 channel.detail.overview / channel.list.badge
  "title": "账户余额",
  "component": {
    "type": "statGroup",
    "items": [
      { "type": "stat", "label": "余额", "bind": "balance.get:/total", "format": "money", "currencyBind": "balance.get:/currency" },
      { "type": "stat", "label": "可用", "bind": "balance.get:/available", "format": "boolean" }
    ]
  },
  "actions": [{ "label": "刷新", "capability": "balance.get" }]
}
```

组件白名单：`statGroup`、`stat`、`keyValue`、`table`（`columns: [{key,label,format?}]`、`rowsBind`）、`progress`（`valueBind`、`maxBind`）、`badge`、`alert`（`level`、`text`）、`markdown`（宿主净化，禁用 HTML）、`link`（仅 https）。
`format`：`text | number | money | percent | boolean | datetime | relativeTime`（`percent` 的值为比例，0.42 显示为 42%；`money` 的值为十进制字符串，币种取 `currencyBind`）。`bind` = `<能力名>:<JSON Pointer>`，只能引用本插件能力的最新结果。
`channel.list.badge` 只允许 `badge`。

## 6. 模拟测试用例（`tests/*.json`）

```jsonc
{
  "name": "余额正常",
  "capability": "balance.get",            // 或 "hook": "transformRequest"，并提供 "request"
  "input": null,
  "config": { "region": "cn" },
  "secrets": { "apiKey": "sk-test" },
  "fetch": [ { "match": { "method": "GET", "url": "https://api.deepseek.com/user/balance" },
               "response": { "status": 200, "json": { "is_available": true, "balance_infos": [ … ] } } } ],
  "expect": { "output": { "currency": "CNY", "total": "110.00" } }   // 子集匹配
}
```

模拟测试中 `og.fetch` 只命中 `fetch` 列表（未匹配即报错），绝不访问真实网络。

## 7. 控制面 API

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/plugins` | `plugins.read` | 列表：`{items:[Plugin]}`，含最新已批准版本、待审批版本、使用中的渠道数 |
| POST | `/api/plugins` | `plugins.manage` | `{id, name, template: "openai-compatible" \| "anthropic-compatible" \| "blank"}` → 新建插件与草稿 |
| POST | `/api/plugins/import` | `plugins.manage` | `multipart/form-data` 的 `file`（ZIP）→ 新插件或已有插件的新草稿；返回 `{plugin, draft, build}` |
| GET | `/api/plugins/{id}` | `plugins.read` | 详情 + `versions`（不含文件内容） |
| PATCH | `/api/plugins/{id}` | `plugins.manage` | `{status, version}`；停用后使用它的渠道在数据面上被跳过 |
| GET / PUT | `/api/plugins/{id}/draft` | `plugins.manage` | `{files: {path: content}, version}` |
| POST | `/api/plugins/{id}/draft/build` | `plugins.manage` | 编译 + manifest 校验 + 风险扫描：`{ok, manifest?, diagnostics:[{file,line,column,severity,message}], risk:[{rule,file,line,message}], bundleBytes}` |
| POST | `/api/plugins/{id}/draft/test` | `plugins.manage` | body 为 §6 的用例（或 `{case: "tests/x.json"}`）→ `{ok, output, error, logs, fetches, durationMs, expectation}` |
| POST | `/api/plugins/{id}/draft/publish` | `plugins.manage` | 编译通过才可发布 → `PluginVersion`；权限与上一已批准版本相同则自动批准，否则 `approval=pending` |
| GET | `/api/plugins/{id}/versions/{vid}` | `plugins.read` | 含文件、manifest、风险、`permissionDiff`（相对上一已批准版本） |
| POST | `/api/plugins/{id}/versions/{vid}/approve` | `plugins.trust` | `{decision: "approve" \| "reject", note?}` |
| GET | `/api/plugins/{id}/versions/{vid}/export` | `plugins.read` | ZIP |
| GET | `/api/plugins/sdk.d.ts` | 登录用户 | Monaco 使用的 SDK 类型声明 |

渠道相关（扩展 Phase 1）：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST / PATCH | `/api/channels[/{id}]` | 新增字段：`pluginVersionId`（必须是已批准且插件已启用的版本；`type` 由其 `extends` 决定；不提供时绑定对应类型的内置插件；创建时缺省的 `baseUrl`/`models` 取自 manifest.defaults）、`pluginConfig`（整体替换，按 configSchema 校验并补默认值）、`secrets`（`{字段名: 值}`，只写；空字符串表示删除）。只能在同一插件的版本之间切换；切换时若新版本导出 `migrateConfig`，先迁移配置 |
| GET | `/api/channels[/{id}]` | 新增 `plugin: {id, key, name, version, versionId} \| null`（精简视图也包含）、`pluginConfig`、`secretFields: {名称: {set, hint}}`；列表中管理者可见的渠道另有 `badges: [{value, format, currency}]`（由 `channel.list.badge` 贡献点从最新能力结果解析） |
| GET | `/api/channels/{id}/capabilities` | 渠道所有者或 `channels.manage`（余额等属于所有者账户信息，不向仅能使用渠道的人公开）：`{plugin, capabilities: [{name, label, output, userTriggerable, schedule}], results: {名称: CapabilityResult}, ui: [贡献点]}` |
| POST | `/api/channels/{id}/capabilities/{name}` | 渠道所有者或 `channels.manage`，且能力声明了 `userTriggerable`：立即执行并返回 `CapabilityResult`（body 可选 `{input}`） |

`CapabilityResult = { ok, output, error, unsupported, durationMs, fetchedAt, pluginVersion }`。

错误码新增：`plugin_exists`、`plugin_builtin`（内置插件不能编辑/导出/停用）、`plugin_build_failed`（422，`details.diagnostics`）、`plugin_version_exists`、`plugin_not_approved`、`plugin_disabled`、`not_pending`、`capability_not_found`、`capability_forbidden`。
能力执行失败**不使用 HTTP 错误**：返回 200 与 `CapabilityResult{ok:false, error}`（结果同样被持久化，界面据此显示）；手动执行的响应额外包含 `logs` 与 `fetches` 便于排查。

## 8. 审计

`plugin.create`、`plugin.import`、`plugin.draft_update`、`plugin.publish`、`plugin.approve`、`plugin.reject`、`plugin.update`（启停）、
`plugin.auto_disabled`、`channel.plugin_upgrade`（含 from/to 版本），以及能力的手动执行 `capability.invoke`。
