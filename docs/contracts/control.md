# 原生 Jev 管控

入口：后台「管理 → 管控系统」，需要 `settings.write`；保存配置还需 `users.write`。默认全局关闭，新规则默认关闭、动作「仅审计」，不会自动启用线上封禁。

行为参考 [AxonHub Jev Guard](https://github.com/looplj/axonhub-jev-guard/tree/5a55a891bb4e9cfdec5746d2022b5babcb22bba3)，用原生 Go 服务、数据库和后台页面实现，不依赖插件，也不迁移对方插件安装信息、Key 或历史 KV。保留六类默认分类、可定制的提问及正反条件、最短文本、8000 字符首尾截取、`noul`/`probability` 解析、阈值、异常策略、摘要和记录清理；原生版默认保留 1000 条，可设置 1—10000 条。

## 匹配和检查

- 规则按列表顺序采用第一条启用且匹配的规则，可在后台上移、下移。渠道列表和模型列表需同时满足；各列表内部取并集，空列表匹配全部。
- 模型可填写精确 ID、完整匹配的通配符（`gpt-*`、`glm-5.?-*` 等）或以 `re:` 开头的 Go/RE2 正则。正则默认部分匹配，需要完整匹配时写 `re:^gpt-6\.[12]-(sol|astra)$`。不支持回溯引用和前后查找；保存时校验，载入配置时编译。
- 在路由选定候选渠道后、配额/钱包检查和调用上游前审查。同一次请求内，同规则不会因渠道重试重复调用 Jev 或重复执行动作；回退到其他规则适用渠道时会检查该规则。命中拦截不会通过回退规则绕过。
- 默认 Base URL 为 `https://api.typesafe.ai/v1`，请求地址为 Base URL 加 `/systemone`。可使用兼容的自定义 API 根地址，禁止 URL 内嵌凭据、查询参数和片段；不跟随重定向。网络使用现有 SSRF 防护，私网兼容服务需要管理员配置 `OMNIGATE_CHANNELS_ALLOW_PRIVATE_NETWORK`。
- 模型默认为 `jev-latest`。Key 加密存储、只写不回显；省略 `apiKey` 保留原值，显式空字符串清除（启用时不允许无 Key）。配置使用 `version` 乐观锁。
- 分类每行格式「名称 | 问题 | 命中条件 | 非命中条件」，也可只写问题，最多 32 类。清空分类不执行检查。默认阈值 0.70，可设 0—1；任一分类达到阈值即命中，以最高命中概率作为主要原因。
- 抽取现有网关端点的对话、Responses 输入、Anthropic 消息/工具调用、补全、嵌入字符串、图像/语音提示等文本；multipart 只读取 prompt，不上传图片、音频等二进制文件。截取开头约四分之一和末尾，最多 8000 个 Unicode 字符。不会向原模型注入或改写提示词；不是输出内容审查。

## 动作和异常

| 动作 | 请求 | 后续行为 |
|---|---|---|
| audit | 放行 | 仅记录命中，可选通知 |
| block | 返回 403 | 不调用上游，可选通知 |
| notify | 放行 | 通知触发用户，可选通知管理员 |
| ban | 返回 403 | 无限期停用账号，撤销会话、失效 Key 鉴权缓存，可选通知 |

管理员通知开关开启时，通知所有启用的系统管理员。通知用户开关适用于其他动作；`notify` 动作始终通知用户。站内通知始终送达记录，SMTP 可用且邮箱已验证时强制邮件通知；账号停用不会跳过这些邮件。`sent` 只代表 SMTP 接受，不保证收件箱送达，可用邮件投递查询接口核查。

Key 不可用、网络/超时、非 2xx、无效 JSON、缺少分类概率等都属于检查异常。默认放行并记录；开启 `failClosed` 后返回 403。**异常绝不执行用户通知、违规管理员通知或自动封禁**。只有真正的分类命中执行规则动作，系统管理员不会被自动封禁。分类概率不等于人工认定违规，记录用于复核。

正常且未命中的请求不保存审查内容。命中及异常记录直接关联网关的请求、用户、Key、渠道、模型和协议。摘要可关闭，默认 1000 字，可设置为 180—10000 字。动作状态为 pending/completed/failed。异常退出留下的 pending 需人工核查。

## 请求快照与模型复核

- 快照可关闭。默认每个用户在 5 分钟内只保存第一份被记录的请求快照，窗口可配置为 60—86400 秒。后续记录保留各自摘要、共用该快照，并标注快照源请求。
- 快照保留消息、提示词和工具调用等文本，过滤图片、音视频、文件及对应的二进制字段。每份 JSON 最多 1 MiB，超限时保留首尾并标记截断。删除记录后，其他记录仍在引用的快照继续保留。最后一条引用删除后，快照会随清理删除。
- 每次在后台手动选择一个启用的全局渠道文本模型，可单条或批量复核。复核会将文本证据发送给所选模型。系统返回复核分数和理由，证据不足时可返回空分数。选用的模型及完整回复仅供当前页面预览，无平台用户账单或常规请求日志。
- 点击「应用复核」后，保存覆盖分数、有效阈值和应用时间。阈值可填写 0—100%，留空沿用该记录当前的阈值。等于阈值视为命中。原始 Jev 分数、阈值及历史执行动作均保留。覆盖判定后，如需解除既有封禁，从用户管理处理。

## 接口

- `GET /api/admin/control/settings`：配置、`version` 和 `apiKeySet`，没有明文 Key。
- `PUT /api/admin/control/settings`：完整配置（`enabled,baseUrl,model,threshold,categories,failClosed,storeExcerpt,excerptChars,storeSnapshots,snapshotWindowSeconds,keep,minChars,timeoutMs,rules,version`），可选 `apiKey`。每条规则包含 `id`（UUID）、`name,enabled,channelIds,models,action,notifyAdmins,notifyUser`。
- `GET /api/admin/control/events`：标准分页，可组合 `userId,channelId,ruleId,model,requestId` 筛选；返回用户、Key、渠道名称和 ID，以及分数、摘要、检查异常标记、动作与执行状态。
- `GET /api/admin/control/events/{id}/snapshot`：读取该记录关联的文本快照。
- `GET /api/admin/control/review-models`：列出可选的复核模型。
- `POST /api/admin/control/review`：提交 `eventId,model` 取得临时复核结果。
- `POST /api/admin/control/events/{id}/apply-review`：提交复核返回的 `token` 和可选 `threshold`（0—1），需要 `users.write`。
- `DELETE /api/admin/control/events/{id}`：删除一条记录。
- `DELETE /api/admin/control/events`：清空记录。删除写入平台审计；不会撤销已执行动作。

旧 Jev 插件/上游管控不会被自动停用；切换时由管理员确认启用范围，避免重复检查。线上启用需要管理员保存 Jev Key、规则和开关，本功能不会根据已有上游的审查结果追溯封禁历史用户。
