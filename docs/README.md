# OmniGate 设计文档

| 文档 | 内容 |
| --- | --- |
| [architecture.md](architecture.md) | 系统架构、模块划分、请求流水线、降级行为、部署形态 |
| [threat-model.md](threat-model.md) | 资产、信任边界、威胁与缓解、残余风险 |
| [data-model.md](data-model.md) | 核心 ER 图（已实现 / 草案） |
| [adr/](adr/README.md) | 架构决策记录 |
| [contracts/openapi.yaml](contracts/openapi.yaml) | 已实现的 HTTP 接口（OpenAPI 3.1） |
| [contracts/protocol-adapter.md](contracts/protocol-adapter.md) | 协议适配层契约（Phase 1） |
| [contracts/plugin-sdk.md](contracts/plugin-sdk.md) | 插件 SDK 契约（Phase 2） |
| [contracts/billing-and-quota.md](contracts/billing-and-quota.md) | 计费、周期配额、钱包、兑换码契约 |
| [contracts/phase1-api.md](contracts/phase1-api.md) | Phase 1 控制面 API 与网关行为（含实现差异） |
| [contracts/phase2-api.md](contracts/phase2-api.md) | Phase 2 插件系统：包格式、manifest v1、运行时语义、宿主 API、UI 贡献点、控制面 API |
| [operations/deploy-production.md](operations/deploy-production.md) | **生产部署手册**（gate.example.com）：准备服务器、选择数据库、填写环境变量、GitHub OAuth、反向代理与 HTTPS、首次登录、备份恢复、升级回滚、监控与排障 |
| [operations/deployment.md](operations/deployment.md) | 单镜像部署：`docker run` / Compose、反向代理、健康检查、前端缓存与 CSP、备份、SQLite ⇄ PostgreSQL 数据迁移与升级 |
| [operations/configuration.md](operations/configuration.md) | 环境变量、登录配置、密钥轮换、部署、备份与升级 |

## 路线图

| 阶段 | 内容 | 状态 |
| --- | --- | --- |
| Phase 0 | 架构确认、ADR、可运行骨架、OIDC/GitHub 登录、最小 RBAC、审计 | ✅ Round 1 |
| Phase 1 | 三种协议入口、渠道、模型映射、Gateway Key、优先级/加权路由与回退、熔断、流式转发、请求日志与统计、钱包与兑换码 | ✅ Round 2 |
| Phase 2 | 插件系统闭环（Goja 运行时、SDK、Monaco 编辑器、声明式 UI、DeepSeek 示例、版本与回滚）；订阅源与完整自定义协议在 Round 4 | ✅ Round 3（主体） |
| Phase 3 | 共享与细粒度 Key 策略、高级路由、熔断回退、套餐与周期配额、价格版本、告警 | |
| Phase 4 | 压测、故障注入、备份恢复演练、OpenTelemetry、多节点、评估 iframe 插件 UI | |
