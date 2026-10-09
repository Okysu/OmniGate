# DeepSeek 插件（示例）

继承 OpenAI 兼容协议（对话请求由网关原生转发），额外提供：

| 能力 | 说明 |
| --- | --- |
| `balance.get` | 调用 `GET /user/balance`，每 10 分钟自动同步一次，也可在渠道详情页手动刷新 |
| `models.list` | 调用 `GET /models`，用于发现上游模型 |
| `health.check` | 低成本探测（模型列表接口），不会产生生成费用 |
| `usage.query` | DeepSeek 没有公开的用量 API，明确返回“不支持” |

配置项：`currency`（多币种账户显示哪个币种）。余额告警由平台统一处理：在渠道设置中填写“上游余额告警阈值”（1.0.3 起移除了插件自身的 `lowBalance`，升级时自动清理）。
权限：只能访问 `api.deepseek.com` 与渠道 Base URL 所在主机；只能取得 `apiKey` 的句柄（明文不会进入插件）。

`tests/` 下是模拟测试用例，在插件编辑器中运行时不会访问真实网络。
