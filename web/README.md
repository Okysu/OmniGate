# OmniGate Web 控制台

OmniGate 管理控制台前端（Phase 0 脚手架）。构建产物 `dist/` 将由 Go 服务端嵌入并在同源下提供。

## 技术栈

- Vue 3 + TypeScript（`strict`、`noUncheckedIndexedAccess`）+ Vite
- Monaco Editor（`monaco-editor`，仅插件编辑器路由按需加载）
- Tailwind CSS v4 + shadcn-vue（reka-ui，`reka-vega` 风格，neutral 配色）
- vue-router、pinia、@vueuse/core、@lucide/vue 图标、vue-sonner 通知
- ESLint（flat config：typescript-eslint + eslint-plugin-vue）、vue-tsc、Vitest

## 常用命令

需要 Node 24+ 与 pnpm。

```bash
pnpm install      # 安装依赖
pnpm dev          # 启动开发服务器 http://localhost:5173
pnpm typecheck    # vue-tsc 类型检查
pnpm lint         # ESLint（pnpm lint:fix 自动修复）
pnpm test         # Vitest 单元测试（src/**/*.test.ts）
pnpm build        # 类型检查 + 生产构建，输出到 dist/
pnpm preview      # 本地预览生产构建
```

## 开发联调

- 开发服务器固定端口 **5173**，`/api` 与 `/v1` 代理到 `http://localhost:8080`（Go 后端）。
- 代理设置了 `changeOrigin: false`，后端看到的 Host / Origin 为 `localhost:5173`，开发环境下后端需允许该来源。Cookie 原样透传。
- 登录走整页跳转：`/api/auth/{providerId}/login?redirect=<path>`，回调结束后由后端重定向回原页面（未指定时默认 `/console`），失败时跳转 `/login?error=<code>`。
- 后端未配置任何登录方式时，登录页会提示设置 `OMNIGATE_AUTH_GITHUB_CLIENT_ID` / `OMNIGATE_AUTH_GITHUB_CLIENT_SECRET` 等环境变量。

## 目录结构

```
src/
  components/
    ui/          shadcn-vue 生成的基础组件（可按需修改）
    layout/      应用外壳：顶栏、侧边栏、主题切换、用户菜单、公告
    plaza/       模型广场：PlazaCatalog（搜索 / 过滤 / 排序 / 卡片 / 详情抽屉）及其子组件
    plugin-ui/   插件声明式 UI 渲染器（ADR-0003）：UiNode、UiContributionCard、ChannelBadges
    *.vue        通用组件：PlaceholderPage、PageHeader、ConfirmDialog、DataPagination 等
  composables/   useTheme（浅色 / 深色 / 跟随系统，localStorage 持久化）
  config/nav.ts  侧边栏导航、权限提示与占位页说明（唯一数据源）
  lib/
    api.ts       fetch 封装：CSRF 头、错误解析（ApiError）、401 跳转登录
    endpoints.ts 各接口的类型化调用
    types.ts     与后端契约对应的类型
    format.ts    时间 / 角色 / UA / 数字 / 耗时等格式化
    money.ts     金额（十进制字符串）格式化与精确运算（BigInt，不使用浮点）
    labels.ts    枚举的中文标签
    jsonPointer.ts  RFC 6901 JSON Pointer 与插件 UI 绑定（`<能力名>:<JSON Pointer>`）解析
    pluginFormat.ts 插件 UI 的值格式化（money / percent / boolean / datetime …）
    configSchema.ts 插件 configSchema 动态表单：字段、校验（与服务端一致）、请求体
    pluginPermissions.ts 插件权限的可读描述与相对上一已批准版本的差异
  router/        路由表与登录守卫（首次进入调用 /api/me，结果缓存在 pinia）
  stores/        auth（当前用户与权限）、system（/api/system/info）
  views/         页面（全部按路由懒加载）
```

## URL 结构

- `/`：公开的产品首页（`views/landing/`），未登录也可访问，只调用 `/api/me`（401 视为未登录，不提示、不跳转）与公开的 `/api/system/info`。
- `/models`：公开的平台模型广场（与首页共用 `LandingHeader` / `LandingFooter`）。`site.publicModelPlaza=false` 时接口对访客返回 401，页面显示「登录后查看」（请求带 `skipAuthRedirect`，不跳转）。旧书签 `/models` 不再重定向到 `/console/models`（`legacyRedirects` 的 `PUBLIC_PATHS`）。
- `/login`：登录页；`redirect` 参数经 `safeRedirect()` 校验，默认 `/console`。
- `/console/**`：需要登录的控制台页面（`/console` 为概览）。前缀定义在 `lib/paths.ts`（`CONSOLE_BASE`），`config/nav.ts` 中的 `to` 均为完整路径。
- 旧的顶层地址（`/channels`、`/billing/plans`、`/plugins/:id/edit` …）由 `legacyRedirects()` 根据控制台路由表自动生成重定向，保留参数、query 与 hash。
- 未登录访问控制台、或任意接口返回 401 时，跳转 `/login?redirect=<当前控制台地址>`；公开页面上的 401 只清空登录态。
- 生产环境由 Go 服务同端口提供 SPA，`/api`、`/v1` 以外的路径需回退到 `index.html`。

## 约定

- **API 调用**：一律通过 `src/lib/api.ts`。所有对 `/api/*` 的非 GET/HEAD 请求自动带上 `X-Requested-With: XMLHttpRequest`（后端 CSRF 校验）；非 2xx 响应解析为 `ApiError { status, code, message, requestId }`；除 `GET /api/me` 外的任何 401 都会清空登录态并跳转 `/login?redirect=<当前路径>`（公开页面除外）。
- **权限**：侧边栏按 `/api/me` 返回的 `permissions` 隐藏无权限入口，仅为界面层面的提示，真正的鉴权由后端执行。权限字符串定义在 `src/config/nav.ts`，需与后端保持一致。
- **占位页面**：尚未实现的页面统一使用 `PlaceholderPage`，带有「占位 · Phase N」标记，**不展示任何假数据**。实现某个页面时，从 `config/nav.ts` 中删除该项的 `placeholder` 字段，并在 `router/index.ts` 中注册真实组件。
- **金额**：后端所有金额均为十进制字符串，一律用 `lib/money.ts` 格式化（`useCurrency()` 提供绑定了 `/api/system/info` 币种的 `money()`），不要转成 number 计算。
  - 显示精度固定为 3 位小数（`lib/money.ts` 的 `DISPLAY_DECIMALS`，与币种自身的 `decimals` 无关）：四舍五入（远离零）并补齐到 3 位，如 `$0.0025986` → `$0.003`、`$24` → `$24.000`；非零但会舍入为 0 的金额显示为 `<$0.001`（负数为 `-<$0.001`）。界面上不提供查看完整精度的方式。
  - 展示金额一律用 `money()`（包括操作结果提示、表单里的只读预览如「调整后余额」「计费预览」）；不要在 `title` / 提示中放完整精度的金额，提示只说明金额的含义。插件 UI 的 `money` 格式（`formatUiValue`）同样是 3 位小数。
  - 可编辑的金额输入框（价格、余额调整、兑换码面额、套餐标价 / 额度、分组限额、密钥消费上限、系统设置等）始终绑定后端返回的原始十进制字符串，不经过 `money()`，否则保存时会改变已存的值。
- **图表**：统计页使用手写 SVG 组件 `components/charts/ColumnChart.vue`（无第三方图表库，按需懒加载），颜色通过 `--viz-*` 变量区分浅色 / 深色。
- **上传与下载**：`api.post(path, formData)` 直接发送 FormData（不设置 JSON Content-Type，由浏览器生成 multipart 边界，CSRF 头照常添加）；二进制下载用 `requestBlob()`（如插件 ZIP 导出，再生成 Blob 链接保存）。
- **Monaco**：只在 `views/plugins/editor/monaco.ts` 中引入（由编辑器页面动态 `import()`），通过 Vite `?worker` 加载 editor / TypeScript / JSON worker，并注册 `/api/plugins/sdk.d.ts` 的类型声明。不要在其他模块静态引入 `monaco-editor`，否则数 MB 的编辑器代码会进入其他页面的包。monaco-editor ≥ 0.55 中 `monaco.languages.typescript` 已废弃，TypeScript 默认配置从 `monaco-editor/languages/features/typescript/register.js` 导入。
- **插件编辑器只读模式**：`/console/plugins/:id/edit?version=<vid>` 以只读方式在 Monaco 中查看已发布版本的文件（`plugins.read` 即可访问；不提供保存 / 编译 / 测试 / 发布），版本详情抽屉中的「在编辑器中查看」跳转到这里。路由 `meta.permission` 可以是按 query 计算权限的函数。
- **全屏布局**：路由 `meta.fullBleed: true` 时 AppShell 不加页面内边距与最大宽度（插件编辑器使用）。
- **插件 UI**：插件只能声明组件树，宿主渲染；所有文本按纯文本输出（`markdown` 组件也只按段落显示纯文本），链接仅允许 https 并带 `rel="noopener noreferrer"`。
- **套餐额度用完后**：由用户在「钱包与订阅」中设置（`GET/PUT /api/billing/preferences`，`block` 默认 / `wallet`），API Key 策略的 `quotaOverflow` 可覆盖（`''` 跟随账户）。套餐规则不再有 `onExceed`（服务端拒绝未知字段）。
- **站点设置**：站点名称、公告、首页开关与文档链接来自 `/api/system/info` 的 `siteName` / `announcement` / `landingEnabled` / `docsUrl`（`lib/site.ts`、`composables/useSite.ts`），旧后端缺少这些字段时回退到 "OmniGate"、无公告、显示首页、`VITE_DOCS_URL`。公告关闭状态按公告内容的哈希保存在 `localStorage['omnigate-announcement-dismissed']`；`landingEnabled=false` 时 `/` 由路由守卫跳转到 `/console`（未登录先登录）。保存系统设置后调用 `system.load()` 立即刷新。
- **模型广场**（phase5-api.md §3）：`components/plaza/PlazaCatalog.vue` 同时用于 `/models`、`/console/plaza`（模型广场）与 `/console/my-models`（我的模型，`mode="mine"` 增加来源过滤与 自有 / 共享 / 平台、计费、订阅徽标）。过滤、排序、K/M 格式化与费用估算（BigInt，不用浮点）在 `lib/plaza.ts`；价格同样显示 3 位小数。`/console/models` 现为「模型管理」（`models.manage`，无权限时跳转到模型广场），「模型资料」标签页的表单与合并逻辑在 `lib/modelInfoForm.ts`。
- **重试条件**：7 个按状态码划分的重试类别、标签与默认值（除 `client_error` 外全部）在 `lib/retry.ts`，路由规则表单与系统设置的 `gateway.retryOn` 共用 `components/RetryOnPicker.vue`。
- **路由规则**（`views/routes/`）：表单 ↔ 请求体、校验与模型通配（`*` 匹配任意字符，其余字面匹配，与后端 `routing.MatchModel` 一致）在 `lib/routeForm.ts`；系统设置的分组表单、脏检查与 PATCH 构造在 `lib/settingsForm.ts`（"恢复默认" 即 PATCH 该字段为 `null`）。
- **通知**（phase6-api.md）：事件目录、分类、接收资格（`channel.*` / `upstream.*` 需拥有渠道或 `channels.manage`，`plugin.pending_approval` 需 `plugins.trust`，钱包 / 套餐需 `billing.own`，API Key 需 `keys.own`）、偏好表单与 PUT 请求体在 `lib/notifications.ts`；PUT 只发送有资格的事件。侧边栏「通知中心」的未读数由 `stores/notifications.ts` + `composables/useUnreadPolling.ts` 每 60 秒轮询（标签页隐藏时暂停，后端返回 404/501 时停止）；控制台顶栏不放通知铃铛。渠道表单只在 `alerts.balanceBelow` 变化时发送 `alerts`（旧后端拒绝未知字段）。系统设置的 SMTP 字段嵌套在 `notifications.smtp.*`，密码只写（更换 = 新值，清除 = `""`，恢复默认 = `null`）。
- **用户管理**（phase7-api.md §2）：停用表单（原因必填 ≤200、可选到期时间）、PATCH / 批量请求体、批量结果汇总与登录页 `?error=account_disabled&reason=&until=` 提示在 `lib/userAdmin.ts`。用户列表支持多选批量 停用 / 启用 / 强制下线（`users/UserBatchDialog.vue`，排除自己，部分失败时逐个列出），点击行打开用户详情抽屉（`users/UserDetailSheet.vue`）；单个用户的角色 / 停用 / 启用确认统一由 `users/UserChangeDialogs.vue` 处理。没有 `users.write` 时只读；钱包调整与开通订阅另需 `billing.manage`（复用 `AdjustWalletDialog` / `GrantDialog` 的 `user` 固定用户参数）。
- **用户组与限额**（phase8-api.md §1–§3）：
  - 「用户组」页（`/console/admin/groups`，`users.read` 查看、`users.write` 编辑）与用户列表 / 详情 / 批量「设置分组」共用 `stores/groups.ts`（`GET /api/admin/groups` 缓存）；倍率文案（`×0.8 / 八折`）、精确乘法、表单校验与 PATCH（只发变化字段 + `version`）在 `lib/groups.ts`。成员数链接到 `/console/admin/users?groupId=`。`/api/me` 的 `group`（顶层或 `user.group`）保存在 `auth.group`。
  - 渠道共享对象为 `sharedWith: {users, groups}`（旧后端的 `string[]` 视为用户，`normalizeSharedWith()`），表单用 `components/SharePicker.vue`（用户 / 用户组两个标签页，合并显示带类型徽标）。
  - **共享需接受**（phase5-api.md §5）：共享给用户的渠道要对方接受后才生效；只有 `channels.manage` 能添加用户组（`SharePicker` 的 `canShareGroups=false` 时禁用「用户组」标签页并说明原因，已有的组仍可移除，前端校验与 422 `details["sharedWith.groups"]` 一致）。`SharePicker` 的 `shares`（渠道的 `shares`，新建时传 `[]`）为每个已选用户显示 待接受 / 已接受 / 保存后邀请 / 保存后重新邀请，已拒绝（含退出）的用户单独列出并可「重新邀请」（加回 `sharedWith.users`，保存即重新邀请）。状态标签、拆分与校验辅助在 `lib/shares.ts`。被共享者在 `/console/channels?tab=shared`「共享给我的」（`views/channels/IncomingSharesPanel.vue`，数据在 `stores/channelShares.ts`，标签页显示待接受数，后端 404 时隐藏）接受 / 拒绝邀请、退出共享；接受前的确认框必须说明：已接受的共享渠道先于平台渠道尝试且不计费，所有者看不到账户信息但能看到经由其渠道的请求与响应。通知 `channel.share_invited` 属于新分类「渠道共享」（`share`，所有用户都有资格，默认仅站内）。
  - API Key 的 `policy.spendLimit` 只在设置时或清除已有值时发送（旧后端拒绝未知字段）；`GET /api/billing/limits` 的解析（`group` 的限额可平铺或嵌套在 `limits`，`resetsAt` 的键名兼容 `day/daily/…`）在 `lib/limits.ts`，「钱包与订阅」的「用量限额」卡片在后端返回 404/501 时隐藏。
  - 分时价格（`schedule` / `scheduleTimezone`）的表单、与后端一致的重叠校验（周内分钟区间，跨零点的时段属于开始那一天）、时间轴、描述与广场「当前时段 ×0.5，08:30 恢复原价」提示在 `lib/priceSchedule.ts`；422 的 `schedule[1].start` 形式会归一为 `schedule.1.start` 显示在对应时段下。
- **订阅批量操作**（phase7-api.md §3）：「重置额度」「延长有效期」对所选有效订阅或「按套餐（全部有效订阅）」生效，目标 / 规则选项 / 时长校验在 `lib/subscriptionBulk.ts`。确认前必须先 `?dryRun=true` 预览（`composables/useDryRun.ts`，请求变化后防抖重算，预览与当前请求一致且影响数 > 0 才能确认）。
- **图片接口**（phase7-api.md §1）：价格新增可选 `perImage` / `imageInputPerM`（留空不发送，`imageInputPerM` 为空按 `inputPerM` 计），模型能力 `imageGeneration`、广场协议 `openai.images`、套餐计量 `images`，请求日志显示 `imageCount` 与 `imageInputTokens`。
- **音频接口**（phase9-api.md §1）：价格新增可选 `audioInputPerM` / `audioOutputPerM`（留空按 `inputPerM` / `outputPerM` 计）与 `perMinute` / `perMCharacters`（留空 = 0，后端价格列表中未设置时为 `"0"`，显示时视为未设置），留空不发送；价格表的「音频」列与广场价格由 `lib/audio.ts` 的 `audioPriceParts()` / `lib/plaza.ts` 的 `hasAudioPrice()` 判断。模型能力 `audioInput`（语音识别）/ `audioOutput`（语音合成，旧后端缺失时为 false），广场协议 `openai.audio`，调用示例按能力显示转写（multipart `-F file=@audio.mp3`）/ 语音合成（`--output speech.mp3`）。请求日志显示 `audioSeconds`（`formatAudioSeconds()`：`1分23秒`）、音频 token、语音合成的 `usage.inputCharacters`（phase9 §4 #2），音频请求的 `usageEstimated` 显示为「估算」（只收每次请求费用）。路由预览支持 `openai.audio.transcriptions` / `openai.audio.speech`。
- **文本补全 / FIM**（phase14-api.md）：渠道 `config.supportsCompletions`（仅 openai，`lib/channelForm.ts` 只在开启时发送），渠道表单「高级」中的开关附 DeepSeek `https://api.deepseek.com/beta` 示例；模型能力 `completions`（「文本补全 / FIM」，旧后端缺失时为 false）、广场协议 `openai.completions`（标签「Completions」，`protocolsForCapabilities()` 的第三个参数表示是否有开启 Completions 的渠道），模型详情与 API Key 页的「FIM 补全 · curl / Python」示例（`prompt` + `suffix`）；请求日志入口协议「OpenAI Completions」，路由预览支持 `openai.completions`。
- **插件自定义协议**（phase9-api.md §2）：manifest `protocol: "custom"`（与 `extends` 互斥），渠道类型为 `custom`（`labels.ts` 的 `manifestChannelType()`，渠道表单锁定类型并提示接受 Chat / Messages / Responses 客户端、不支持 Embeddings / 图片 / 音频）。协议徽标（自定义协议 / 继承 OpenAI / 继承 Anthropic / 计费插件）在 `lib/pluginProtocol.ts` + `views/plugins/ProtocolBadge.vue`；首个自定义协议版本总是需要审批（版本抽屉、发布确认与发布结果中提示）。新建插件模板 `custom-protocol`。编辑器测试面板支持流式用例（`views/plugins/editor/StreamCaseEditor.vue`，`lib/pluginStream.ts`）：文本 / JSON 字节块 → `{hook: "parseStream", chunks, config?, expect?: {output: 事件数组}}`；后端返回 `output` = 事件数组与整个会话的 `durationMs`，界面显示事件、JS 总耗时与 50 ms / 5 s 上限（编辑器测试放宽 4 倍）；Chat chunks 由 `eventsToChatChunks()` 按 `protocol/canonical.go` 的规则在浏览器中生成（后端若返回 `chatChunks` / `calls` 则优先使用）。
- **计费插件**（phase9-api.md §3）：`Plugin.kind` / `meters`（`[{name, label, unit}]`）与 manifest `billing.meters` 由 `lib/customMeters.ts` 解析；套餐规则的计量可选 `custom:<插件 ID>.<计量名>`（「插件计量」分组，`stores/customMeters.ts` 优先 `GET /api/admin/billing/meters`，404 时回退到插件列表 + 最新已批准版本的 manifest），上限可为小数，规则摘要显示「标签 数值 单位」，未知计量显示计量名。`audio_seconds` 摘要附带分钟数（`3,600 秒音频（60 分钟）`）。仅计费插件不出现在渠道表单的插件列表中。
- **datalist 与 space-y**：隐藏元素（`<datalist>` 等）不要作为 `space-y-*` 容器的最后一个子元素，否则前一个控件会多出下边距、同一行的字段错位。
- **表单错误**：422 `validation_failed` 的 `error.details` 解析到 `ApiError.details`（`fieldErrors(err)`），在对应字段下方显示；409 `version_conflict` 用 `isVersionConflict(err)` 判断。
- **目录命名**：`.gitignore` 忽略名为 `logs` 的目录（Tailwind 也会因此跳过扫描），所以请求日志页面放在 `views/request-logs/`。
- **主题**：使用 `.dark` class 策略，偏好保存在 `localStorage['omnigate-theme']`（读写均有 try/catch）。`index.html` 中有一段内联脚本在首屏渲染前应用主题，避免闪烁。
- **UI 文案**：全部使用简体中文；代码标识符使用英文。
- **添加 shadcn-vue 组件**：`pnpm dlx shadcn-vue@latest add <component>`。如果处于 HTTP 代理环境下 CLI 报 “Failed to fetch from registry”，可改用 Node 内置代理支持：

  ```bash
  env -u https_proxy -u http_proxy NODE_USE_ENV_PROXY=1 pnpm dlx shadcn-vue@latest add <component>
  ```

  CLI 生成的样式依赖 `shadcn-vue/tailwind.css` 中的自定义变体（`data-open:` 等），本项目将其中用到的部分内置在 `src/styles/shadcn.css`，没有引入整个 CLI 包。

## 可选构建变量

| 变量            | 说明                                           |
| --------------- | ---------------------------------------------- |
| `VITE_DOCS_URL` | 顶栏「文档」按钮与首页次要按钮的链接（系统设置中的「文档链接」优先）；未设置时控制台按钮显示为未配置，首页改为「查看特性」锚点 |

## 文件监听报错 EMFILE: too many open files

现象：`pnpm dev` 刷屏 `file watcher error: EMFILE: too many open files, watch '...'`，并且**修改文件后页面不会热更新**。

原因：Linux 上 Vite 用 inotify 监听文件，而当前用户的 inotify **实例数**上限 `fs.inotify.max_user_instances`（很多发行版默认 128）
已被 IDE、编辑器插件、其他开发工具占满，新的监听全部失败。与本项目文件数量无关，提高 `ulimit -n` 也没用。

处理：

- `make dev-web` 会先运行 `scripts/check-inotify.sh`，检测到耗尽时自动改用轮询模式（不依赖 inotify，CPU 占用略高）。
- 直接用 pnpm 时可手动开启：`VITE_USE_POLLING=1 pnpm dev`。
- 根治（需要 sudo，永久生效）：

  ```bash
  printf 'fs.inotify.max_user_instances=1024\nfs.inotify.max_user_watches=524288\n' | sudo tee /etc/sysctl.d/60-inotify.conf
  sudo sysctl --system
  ```
