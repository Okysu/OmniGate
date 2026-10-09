# 架构决策记录（ADR）

每个不可逆或影响面大的决策写一份 ADR。状态：`提议` → `已接受` / `已否决` / `已被取代`。
修改已接受的 ADR 时新增一份 ADR 取代它，不直接改写历史结论。

| 编号 | 标题 | 状态 | 日期 |
| --- | --- | --- | --- |
| [0001](0001-modular-monolith.md) | 模块化单体与控制面/数据面分离 | 已接受 | 2026-10-08 |
| [0002](0002-plugin-engine-goja.md) | 插件执行引擎：Goja 嵌入式运行时 | 已接受（用户确认） | 2026-10-08 |
| [0003](0003-plugin-ui-declarative.md) | 插件 UI：仅声明式贡献点 | 已接受（用户确认） | 2026-10-08 |
| [0004](0004-tenancy-rbac.md) | 租户层级：三级作用域 + 预留 workspace | 已接受（用户确认） | 2026-10-08 |
| [0005](0005-auth-oidc-github.md) | 身份认证：仅外部身份（通用 OIDC + GitHub 适配器） | 已接受（用户确认） | 2026-10-08 |
| [0006](0006-billing-quota-currency.md) | 计费、周期配额、兑换码与结算币种 | 已接受（用户确认） | 2026-10-08 |
| [0007](0007-money-fixed-point.md) | 金额定点表示（nano 单位 int64） | 已接受 | 2026-10-08 |
| [0008](0008-secrets-encryption.md) | 凭据加密与密钥轮换 | 已接受 | 2026-10-08 |
| [0009](0009-sqlite-lightweight-mode.md) | SQLite 轻量模式（与 PostgreSQL 并存） | 已接受并实施 | 2026-10-08 |
| [0010](0010-durable-settlement.md) | 结算、配额与请求日志的持久化（本地 journal） | 已接受并实施 | 2026-10-09 |

模板见 [0000-template.md](0000-template.md)。
