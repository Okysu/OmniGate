# ADR-0003：插件 UI——仅声明式贡献点

- 状态：已接受（2026-10-08 用户确认）
- 日期：2026-10-08

## 背景

插件需要扩展渠道列表、渠道详情、厂商能力页（如 DeepSeek 余额卡片）。允许插件注入任意前端代码会带来
XSS、越权读取页面数据、钓鱼界面等风险。

## 决策

- 首期**只支持声明式 UI**：插件在 manifest 的 `uiContributions` 中声明组件树（JSON Schema 约束），
  由宿主用 shadcn-vue 组件渲染。插件无法提供 HTML、CSS 或脚本。
- 组件白名单：`stat`（指标）、`statGroup`、`keyValue`、`table`、`chart`（折线/柱状，数据来自能力输出）、
  `badge`、`alert`、`progress`（额度/配额进度）、`actionButton`（调用插件声明的能力 ID）、`form`（基于 JSON Schema）、
  `link`（仅 https，且显示外链提示）、`markdown`（宿主渲染并净化，禁用原始 HTML）。
- 数据绑定：组件只能引用**本插件能力的输出**（如 `capability: "balance.get"` + JSON Pointer `/total`），
  不能读取页面上的其他数据。
- 动作：`actionButton` 只能触发本插件声明为“可由用户触发”的能力；宿主在调用前做权限校验，并对破坏性动作二次确认。
- 插槽（slot）：`channel.list.badge`、`channel.list.summary`、`channel.detail.overview`、`channel.detail.capabilities`（标签页）、
  `channel.settings.group`。新增插槽需要修改宿主，并在插件 SDK 中加版本号。
- Iframe 微前端推迟到 Phase 4 评估；数据模型中为 `uiContributions` 预留 `kind` 字段（当前只允许 `declarative`）。

## 影响与代价

- 厂商特性展示受组件白名单限制，但覆盖余额、用量、套餐、模型状态等首期场景。
- 前端需要实现一个 Schema 驱动的渲染器与“预览”模式（插件编辑器复用）。
