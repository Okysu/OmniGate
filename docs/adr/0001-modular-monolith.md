# ADR-0001：模块化单体与控制面/数据面分离

- 状态：已接受
- 日期：2026-10-08

## 背景

需求要求单机开箱即用，同时为水平扩展保留边界；管理后台异常不能影响已建立的网关请求链路；
核心领域逻辑不能依赖 HTTP 框架、Vue 或具体数据库实现。

## 决策

1. **后端为 Go 模块化单体**（`server/`），一个二进制同时承载：
   - 数据面 `/v1/*`：协议适配、路由、转发、计量。
   - 控制面 `/api/*`：管理 API、登录、插件管理、统计查询。
   - 嵌入的管理前端（`web/dist` 构建时嵌入）。
2. **两棵路由树互不共享中间件**：各自的访问日志、错误格式（控制面 `{error:{code,message,requestId}}`，
   数据面按所选协议的错误格式）与恐慌恢复。控制面 handler 崩溃只影响该请求。
3. 依赖方向：`cmd` → `app`（装配）→ 适配层（`auth/http`、`adminapi`、`gateway`）→ 领域/服务
   （`identity`、`authz`、`audit`、`money`、后续 `billing`、`routing`、`plugin`）→ `platform`（db、httpx、secretbox）。
   领域包不导入 chi；HTTP 框架只出现在适配层。
4. 技术选型：
   - HTTP 路由：`chi`（仅 `net/http` 之上的薄层，handler 签名保持标准库）。
   - 数据库：PostgreSQL + `pgx/v5`；迁移使用 `goose`，SQL 文件嵌入二进制，启动时在 advisory lock 下自动执行。
   - 日志：标准库 `log/slog`（JSON）；指标：Prometheus（独立端口 `:9090`，不暴露公网）。
   - ID：应用侧生成 UUIDv7（时间有序，利于索引）。
5. **水平扩展边界**（Phase 4 评估）：数据面无状态化——限流/配额计数、熔断状态、短时缓存放 Redis；
   请求日志与用量事实走异步写入通道（先进程内批量，后续可换消息队列），与核心事务表解耦。
   届时可拆出“仅数据面”运行模式（同一二进制，`omnigate serve --plane=data`）。

## 影响与代价

- 单进程部署最简单；控制面的重查询（统计）必须走只读副本或聚合表，防止拖慢数据面（Phase 3 落实）。
- SQLite 兼容模式**暂不实现**：PostgreSQL 特性（advisory lock、jsonb、部分索引）已被使用，
  本地开发通过 `make dev-deps` 启动容器化 PostgreSQL 代替。如确有需要再单独评估。
