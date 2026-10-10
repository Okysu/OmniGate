# OmniGate 配置与运维参考

本文依据 `server/internal/config/config.go`、`server/internal/app/app.go`、`server/cmd/omnigate/main.go`、
`server/internal/channel`、`server/internal/gateway`、`server/internal/protocol`、`server/internal/billing`、`server/internal/subscription`、
`server/internal/plugin`、`server/internal/platform/netguard`、
`docker-compose.yml`、`Makefile` 等代码编写。HTTP 接口的完整契约见 `docs/contracts/openapi.yaml`。代码为唯一事实来源；若本文与代码不一致，以代码为准。

## 1. 环境变量

### 1.1 通用规则

- 所有运行时配置都来自 `OMNIGATE_` 前缀的环境变量。
- 读取时会去除首尾空白。**值为空等同于未设置**，会使用默认值（因此无法通过设为空字符串来“关闭”一个有默认值的选项）。
- 列表型变量以英文逗号分隔，每项去除空白，空项忽略。
- 时长使用 Go duration 语法：`720h`、`90m`、`1h30m`（不支持 `d`）。
- 启动时一次性校验全部配置，所有错误合并输出后进程以非零状态退出。

### 1.2 变量一览

“生产必填”列：**是** 表示在 `OMNIGATE_ENV=production` 下缺失或不合规会拒绝启动；“建议”表示不会阻止启动，但生产环境应当设置。

| 变量 | 默认值 | 生产必填 | 说明 |
|---|---|---|---|
| `OMNIGATE_ENV` | `development` | 是（设为 `production`） | 运行环境，仅接受 `development` / `production`。`production` 会强制：`PUBLIC_URL` 为 https、必须配置 `MASTER_KEY`、`BOOTSTRAP_ADMINS` 与至少一种登录方式、身份提供方地址为 https，并关闭“首个用户自动成为管理员”（见 [1.4](#14-启动时的安全检查)）。 |
| `OMNIGATE_HTTP_ADDR` | `:8080` | 否 | 主 HTTP 监听地址（控制面、数据面、前端页面、探针）。 |
| `OMNIGATE_METRICS_ADDR` | `:9090` | 否 | Prometheus 指标监听地址（该端口任意路径均返回指标，通常抓取 `/metrics`）。设为 `off` 关闭；**设为空字符串不会关闭**，而是回到默认值 `:9090`。不要暴露到公网。 |
| `OMNIGATE_PUBLIC_URL` | `http://localhost:8080` | 是（必须 https） | 用户浏览器访问的对外地址（只能是源，即 scheme://host[:port]，不能带路径；末尾 `/` 会被去掉）。用于拼接 OAuth/OIDC 回调地址，其源（scheme://host[:port]）也是 CSRF `Origin` 校验的默认允许源。仅支持部署在域名根路径。 |
| `OMNIGATE_ALLOWED_ORIGINS` | 空 | 否 | 额外允许的浏览器源（逗号分隔，如 `http://localhost:5173`）。`PUBLIC_URL` 的源总是被允许。 |
| `OMNIGATE_TRUSTED_PROXIES` | 空 | 建议 | 受信任反向代理的地址（逗号分隔，CIDR 或单个 IP；单个 IP 视为 /32 或 /128）。仅当直连对端在此列表内时才解析 `X-Forwarded-For`。只写代理的精确地址，`deploy/` 的 Compose 部署为 `172.30.88.1`（宿主机代理）或 `172.30.88.10`（容器版 Caddy）。详见[反向代理](#7-反向代理)。 |
| `OMNIGATE_DATABASE_URL` | 无（Docker 镜像中为 `sqlite:///data/omnigate.db`） | 是 | 数据库地址，以 `sqlite:` 开头时使用 SQLite，否则为 PostgreSQL 连接串（pgx 格式，如 `postgres://user:pass@host:5432/db?sslmode=disable`）。SQLite：`sqlite:///绝对路径.db`、`sqlite://相对路径.db`（相对工作目录）、`sqlite::memory:`（仅测试）；文件所在目录不存在时自动创建。两者区别见 [1.5](#15-数据库postgresql-与-sqlite)。Compose 部署中由 `docker-compose.yml` 设置为 PostgreSQL。 |
| `OMNIGATE_REDIS_URL` | 空 | 否 | Redis 连接串。Phase 1 仍然只读取、**未使用**：Key 的 RPM 限流、兑换码限流、登录限流、Key 认证缓存、渠道熔断状态都保存在**各进程内存中**，多实例部署时各实例独立计数（套餐配额的用量计数保存在 PostgreSQL，多实例共享，见 [11](#11-套餐与周期配额)）。 |
| `OMNIGATE_MASTER_KEY` | 空 | 是 | 主密钥，格式 `<kid>:<base64 32 字节>`，多个以逗号分隔，第一个为当前加密密钥。开发环境未设置时使用进程内临时密钥并打印警告（`make dev-server` 会从自动生成的 `.env.dev.local` 读取固定的本地密钥，见[第 5 节](#5-本地开发流程)）。详见[主密钥与轮换](#3-主密钥与轮换)。 |
| `OMNIGATE_CHANNELS_ALLOW_PRIVATE_NETWORK` | `false` | 否 | 是否允许渠道连接环回、私有、链路本地等内网地址（Go 布尔写法，非法值导致启动失败）。为 `true` 时，**只有所有者持有 `channels.manage`（`system_admin` / `channel_admin`）的渠道**可以访问内网（本地 Ollama、内网模型网关）；普通用户的渠道无论如何都不能访问。默认关闭是出于 SSRF 防护：渠道地址由用户自行填写，如果允许访问内网，任何能创建渠道的用户都可以借网关探测内网服务或云元数据地址（`169.254.169.254`）。校验在 DNS 解析后的拨号阶段进行（防 DNS 重绑定），上游重定向一律不跟随。详见 [8.6](#86-网络安全ssrf)。 |
| `OMNIGATE_UPSTREAM_PROXY` | 空 | 否 | 所有上游（渠道）请求经由的代理，`http://`、`https://` 或 `socks5://` URL（可带 `user:pass@`），其他格式导致启动失败。上游请求**刻意忽略** `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` 等环境变量，需要代理时只能用此变量。设置后拨号阶段只能看到代理地址，网关改为在发请求前解析目标主机名并拒绝内网地址（尽力而为，存在 DNS 重绑定窗口）；允许内网的渠道同样经过代理。只影响渠道调用，不影响 GitHub / OIDC 登录请求（后者使用 Go 默认 HTTP 客户端，会读取标准代理环境变量）。 |
| `OMNIGATE_PLUGIN_HEAP_LIMIT_MB` | `256` | 否 | 插件内存看门狗阈值（MiB，整数 16–8192，超出范围或非整数导致启动失败）。Goja 无法限制单个插件的内存（ADR-0002），因此只要有插件在执行，看门狗每 10 ms 采样一次**进程堆**，比这些执行开始时记录的最低堆大小增长超过该值，就中断运行时间最长的那次插件执行（计一次资源违规，见 [9.5](#95-停用与自动停用)）。多个插件同时运行时可能误伤正常插件；调大会降低误伤，但单次失控执行能占用的内存也更多。建议同时为容器设置内存上限，并用 Go 运行时读取的 `GOMEMLIMIT` 环境变量设置软上限（OmniGate 自身不设置）。 |
| `OMNIGATE_LOG_LEVEL` | `info` | 否 | `debug` / `info` / `warn` / `error`（不区分大小写），无法识别时按 `info`。 |
| `OMNIGATE_LOG_FORMAT` | `json` | 否 | `text` 为人类可读格式，其他任何值均为 JSON。日志输出到 stdout；访问日志不记录请求头、查询串与请求体。 |
| `OMNIGATE_CURRENCY` | `USD` | 建议（首次启动前确定） | 结算币种，ISO 4217 三字母代码（自动转大写）。**首次启动写入数据库后锁定**，详见[结算币种](#4-结算币种)。 |
| `OMNIGATE_REGISTRATION_MODE` | `restricted` | 否 | 注册模式：`open` / `restricted` / `closed`，详见 [2.5](#25-注册模式)。 |
| `OMNIGATE_BOOTSTRAP_ADMINS` | 空 | 是 | 引导管理员身份匹配器列表，首次登录即成为 `system_admin`。语法见 [2.4](#24-身份匹配器语法)。生产环境为空、或没有一项能匹配已配置的登录方式时拒绝启动；使用 `github:<login>` 时警告。 |
| `OMNIGATE_AUTH_ALLOWED_IDENTITIES` | 空 | 否 | `restricted` 模式下允许注册的身份匹配器列表。 |
| `OMNIGATE_AUTH_ALLOWED_EMAIL_DOMAINS` | 空 | 否 | `restricted` 模式下允许注册的邮箱域名（逗号分隔，自动转小写）。仅匹配**已验证**邮箱，且为精确匹配（`example.com` 不匹配 `sub.example.com`）。 |
| `OMNIGATE_SESSION_TTL` | `720h` | 否 | 会话绝对有效期（30 天），同时也是 `og_session` Cookie 的过期时间。 |
| `OMNIGATE_SESSION_IDLE_TTL` | `168h` | 否 | 会话空闲超时（7 天），按最近活动时间计算（每分钟最多刷新一次）。 |
| `OMNIGATE_COOKIE_SECURE` | 自动 | 否 | 会话与 OAuth state Cookie 是否带 `Secure`。未设置时：`PUBLIC_URL` 为 https 则为 `true`，否则 `false`。接受 `true/false/1/0` 等 Go 布尔写法。生产环境设为 `false` 会拒绝启动。 |
| `OMNIGATE_AUTH_GITHUB_CLIENT_ID` | 空 | 否 | GitHub OAuth App 的 Client ID。设置后启用 GitHub 登录（provider id 固定为 `github`）。 |
| `OMNIGATE_AUTH_GITHUB_CLIENT_SECRET` | 空 | 启用 GitHub 时必填 | GitHub OAuth App 的 Client Secret。设置了 Client ID 而缺少该值时拒绝启动（任何环境）。 |
| `OMNIGATE_AUTH_GITHUB_AUTH_URL` | `https://github.com/login/oauth/authorize` | 否 | 授权端点覆盖（GitHub Enterprise Server 或测试用）。三个端点都必须是绝对 http(s) URL，生产环境必须 https。 |
| `OMNIGATE_AUTH_GITHUB_TOKEN_URL` | `https://github.com/login/oauth/access_token` | 否 | 令牌端点覆盖。 |
| `OMNIGATE_AUTH_GITHUB_API_URL` | `https://api.github.com` | 否 | REST API 基地址覆盖（GHES 一般为 `https://<host>/api/v3`），末尾 `/` 会被去掉。 |
| `OMNIGATE_AUTH_OIDC` | 空 | 否 | 启用的通用 OIDC 提供方 ID 列表（逗号分隔，自动转小写）。ID 只能包含小写字母、数字和 `-`，长度 1–32，且不能是 `github`。 |
| `OMNIGATE_AUTH_OIDC_<ID>_ISSUER` | 无 | 启用该提供方时必填 | Issuer URL，须与 IdP 发现文档中的 `issuer` 完全一致；生产环境必须 https（http 会明文传输客户端密钥与授权码、明文获取签名公钥，拒绝启动）。`<ID>` 为提供方 ID 转大写并把 `-` 换成 `_`（如 `corp-sso` → `CORP_SSO`）。 |
| `OMNIGATE_AUTH_OIDC_<ID>_CLIENT_ID` | 无 | 启用该提供方时必填 | 客户端 ID，同时用作 id_token 的 audience 校验。 |
| `OMNIGATE_AUTH_OIDC_<ID>_CLIENT_SECRET` | 空 | 视 IdP 而定 | 客户端密钥。代码不强制要求（公共客户端 + PKCE 时可为空），机密客户端必须填写。 |
| `OMNIGATE_AUTH_OIDC_<ID>_NAME` | 提供方 ID | 否 | 登录页显示名称。 |
| `OMNIGATE_AUTH_OIDC_<ID>_SCOPES` | `openid,profile,email` | 否 | 请求的 scope（逗号分隔）。自定义时务必包含 `openid`。 |
| `OMNIGATE_SMTP_HOST` | 空 | 否 | 邮件通知的 SMTP 服务器（系统设置 `notifications.smtp.host` 的环境变量层；数据库中的值优先）。与 `OMNIGATE_SMTP_FROM` 都为空时邮件渠道不可用，站内通知照常记录。详见 [16. 通知](#16-通知邮件--webhook--站内)。 |
| `OMNIGATE_SMTP_PORT` | `587` | 否 | SMTP 端口（1–65535，非法值导致启动失败）。隐式 TLS 一般为 465。 |
| `OMNIGATE_SMTP_SECURITY` | `starttls` | 否 | `starttls`（要求服务器支持 STARTTLS）、`tls`（隐式 TLS）或 `none`（不加密，**仅限开发环境**，生产环境拒绝启动）。 |
| `OMNIGATE_SMTP_USERNAME` | 空 | 否 | SMTP 用户名；为空时不做身份验证（AUTH PLAIN，仅在 TLS 连接或本机地址上发送）。 |
| `OMNIGATE_SMTP_PASSWORD` | 空 | 否 | SMTP 密码。只写：设置页只显示 `passwordSet`；在设置页修改时以主密钥加密存入数据库。错误信息与日志中从不出现密码。 |
| `OMNIGATE_SMTP_FROM` | 空 | 否 | 发件人，如 `OmniGate <noreply@example.com>`。 |
| `OMNIGATE_DATA_DIR` | 生产 `/data`，开发 `./.data` | 否 | 本地状态目录（不存在时以 0700 创建）：结算日志 `settlement-journal.jsonl`——数据库暂时不可用时尚未写入的结算、用量与请求日志，恢复后按顺序重放。容器中即 `/data` 卷，使用 PostgreSQL 时也必须持久化；两个进程不能共用一个目录。 |
| `OMNIGATE_WEB_ENABLED` | `true` | 否 | 是否在主端口提供前端页面（Go 布尔写法，非法值导致启动失败）。为 `false` 时只提供 `/api`、`/v1` 与探针，其他路径返回 JSON 404，适合前端另行托管或纯 API 部署。 |
| `OMNIGATE_WEB_DIR` | 空 | 否 | 从该目录（须包含 `index.html`，通常是 `web/dist` 的构建结果）提供前端，替代编译进二进制的版本；目录不存在或缺少 `index.html` 时拒绝启动。文件每次请求时读取，替换目录内容无需重启。容器中可放在 `/data/web` 下。详见 [deployment.md](deployment.md#7-前端页面)。 |
| `OMNIGATE_DATA_DIR` | `production` 为 `/data`（镜像的卷与工作目录），`development` 为 `./.data`（相对工作目录；`make dev-server` 时即 `server/.data`） | 否 | 本地持久化目录，目前存放**结算日志（journal）**：数据库不可用时没能写入的结算与请求日志（见 [10.2](#102-billingenforce-开关) 与 [ADR-0010](../adr/0010-durable-settlement.md)）。目录不存在时自动创建（权限 0700，文件 0600）；无法创建或打开时拒绝启动。必须在持久卷上，且**只能由一个实例使用**（不要让两个进程共享同一目录）。 |
| `OMNIGATE_AUTO_MIGRATE` | `true` | 否 | 为 false（Go 布尔写法：`false`/`0`/`FALSE` 等）时，`serve` 跳过启动时的自动迁移；非法值会导致启动失败。 |
| `OMNIGATE_SEED_ON_START` | 空（不导入） | 否 | 启动时（迁移与结算币种初始化之后）导入价格 / 模型资料 / 套餐目录，**每个数据库只导入一次**：`builtin` 为二进制内置的 GPT 目录（USD），也可以是容器内的 JSON 文件路径；`off` / `false` 等同于空。成功后写入 `system_settings` 的 `seed.applied` 标记，之后的启动跳过（目录有变化时只在日志提示）。币种不一致、目录无效或写入失败时记录 ERROR 并继续启动。见 [11.8](#118-用目录种子批量导入价格模型资料与套餐) 与 [deploy/seed/README.md](../../deploy/seed/README.md)。 |
| `OMNIGATE_TEST_DATABASE_URL` | 空 | —（仅测试） | 集成测试使用的数据库。PostgreSQL 连接串：用户须有 `CREATE DATABASE` 权限，测试会为每个用例创建并删除 `omnigate_test_<时间戳>` 数据库（`make test-integration`，默认 `postgres://omnigate:omnigate-dev@localhost:5432/postgres?sslmode=disable`）；`sqlite`：每个用例在临时目录中使用一个新的数据库文件，无需外部服务（`make test-sqlite`）。未设置时集成测试被跳过。`make test` 两种都跑。 |

### 1.3 不由 OmniGate 进程读取的变量

以下变量只被 Docker Compose 用于插值，OmniGate 二进制并不读取（其中两个恰好也以 `OMNIGATE_` 开头）：

| 变量 | 默认值 | 用途 |
|---|---|---|
| `POSTGRES_PASSWORD` | `omnigate-dev` | PostgreSQL 密码，同时拼进 Compose 中 `omnigate` 服务的 `OMNIGATE_DATABASE_URL`。只在数据卷**首次初始化**时生效，之后修改不会改变数据库中的密码。 |
| `POSTGRES_PORT` | `5432` | PostgreSQL 映射到宿主机 `127.0.0.1` 的端口。 |
| `OMNIGATE_PORT` | `8080` | OmniGate 映射到宿主机的端口（**监听在所有网卡**，见 [6](#6-compose-部署流程)）。 |
| `OMNIGATE_VERSION` | `dev` | 构建参数与镜像 tag（`omnigate:<version>`），也就是 `/api/system/info` 返回的 `version`。 |

生产 Compose 文件 `deploy/docker-compose.yml` 从 `deploy/.env` 读取 `OMNIGATE_TAG`（镜像 `ghcr.io/okysu/omnigate` 的标签，默认 `latest`）或 `OMNIGATE_IMAGE`（完整镜像名，优先）、
`OMNIGATE_PORT`（宿主机端口，只绑定 `127.0.0.1`，默认 8080）、`OMNIGATE_MEM_LIMIT` / `OMNIGATE_GOMEMLIMIT`（容器内存上限与 Go 软上限，默认 `1g` / `800MiB`）、
`COMPOSE_PROFILES`（可选捆绑服务：`postgres`、`caddy`）、`OMNIGATE_DOMAIN`（caddy 站点域名）、`OMNIGATE_SUBNET_PREFIX`（固定网段前三段，默认 `172.30.88`）、
`POSTGRES_PASSWORD`（`postgres` profile）；
OmniGate 自己的变量写在 `deploy/omnigate.env`。见 [deploy-production.md](deploy-production.md)。

> 根目录 Compose 中 `omnigate` 服务的 `environment:` 显式设置了 `OMNIGATE_DATABASE_URL`、`OMNIGATE_HTTP_ADDR`、`OMNIGATE_METRICS_ADDR`，它们的优先级高于 `.env`（`env_file`），因此在 `.env` 中修改这三个变量**不会生效**；如需修改请改 `docker-compose.yml` 或使用 override 文件。

### 1.4 启动时的安全检查

配置校验在 `config.Load` 中一次完成，所有错误合并输出；生产模式下的检查发生在**打开数据库、执行迁移之前**，配置错误不会留下半初始化的数据库。

| 条件 | development | production |
|---|---|---|
| `OMNIGATE_PUBLIC_URL` 不是 https | 允许 | 拒绝启动 |
| 未设置 `OMNIGATE_MASTER_KEY` | 使用临时密钥并警告 | 拒绝启动 |
| 未设置 `OMNIGATE_BOOTSTRAP_ADMINS` | 允许；若 `PUBLIC_URL` 是 localhost/环回地址且数据库中尚无用户，第一个登录的账号成为 `system_admin`；非环回地址时不启用这条规则 | 拒绝启动 |
| 未配置任何登录方式（GitHub 或 OIDC） | 警告 | 拒绝启动（否则没有人能登录） |
| `OMNIGATE_BOOTSTRAP_ADMINS` 中没有一项能匹配已配置的登录方式（如只配置了 GitHub 却写 `corp:…`） | 警告 | 拒绝启动；部分条目不匹配时警告 |
| `OMNIGATE_BOOTSTRAP_ADMINS` 使用 `github:<login>` | 允许 | 允许，启动日志警告（建议改用不可变的 `github-id:`） |
| `github-id:` 后不是数字（两个匹配器变量） | 拒绝启动 | 拒绝启动 |
| OIDC Issuer 或 GitHub 端点覆盖不是 https | 允许（本地模拟 IdP） | 拒绝启动 |
| `OMNIGATE_CURRENCY` 与数据库中已锁定的币种不同 | 警告并沿用数据库中的值 | 同左 |
| `OMNIGATE_SMTP_SECURITY=none`（或系统设置中 `notifications.smtp.security = none`） | 允许 | 拒绝启动（设置页保存返回 422，发送时也拒绝） |

### 1.5 数据库：PostgreSQL 与 SQLite

OmniGate 支持两种数据库（[ADR-0009](../adr/0009-sqlite-lightweight-mode.md)），由 `OMNIGATE_DATABASE_URL` 的前缀选择，功能完全相同：

- **SQLite**（`sqlite:` 开头；Docker 镜像的默认值 `sqlite:///data/omnigate.db`）：嵌入式、零依赖，适合**单实例**、中低写入量。
  驱动为纯 Go 的 `modernc.org/sqlite`（无需 cgo）。运行参数固定为：WAL 日志、`busy_timeout=5000`、`foreign_keys=ON`、
  `synchronous=NORMAL`、所有事务 `BEGIN IMMEDIATE`；写操作在进程内排队（同一时刻只有一个写事务），读操作并发。
  时间以 UTC 文本（RFC 3339，固定 9 位小数）保存，JSON 以文本保存并做 `json_valid` 校验，配额用量以十进制文本保存、在写事务内精确相加；
  请求日志是一张表（无分区），保留期由定期 `DELETE` 实现。
- **PostgreSQL**（其余连接串）：支持多实例、并发写入，请求日志按月分区。

迁移文件分别在 `server/migrations/postgres/` 与 `server/migrations/sqlite/`，版本号一一对应（`omnigate migrate status` 输出中的
`dialect` 字段说明当前使用哪一种）。差异与容量参考见
[deployment.md「SQLite 与 PostgreSQL 差异」](deployment.md#sqlite-与-postgresql-差异)。

两种数据库之间迁移数据（SQLite → PostgreSQL 或反向）用 `omnigate migrate-db --from <源库地址> --to <目标库地址>`：
停机执行，目标必须是空库（`--force-empty-check=false` 则替换目标库已有的数据），`--dry-run` 只做检查与计数；
复制后逐表校验行数与金额合计，不一致时以非零退出码结束。加密数据原样复制，切换后必须继续使用**同一个 `OMNIGATE_MASTER_KEY`**。
完整步骤（停服务 → 备份 → 迁移 → 切换 `OMNIGATE_DATABASE_URL` → 启动 → 验证）见
[deployment.md 第 9 节](deployment.md#9-sqlite-迁移到-postgresql以及反向)。

SQLite 的注意事项：

- **只能单实例**：不要让多个进程或容器同时打开同一个数据库文件运行 OmniGate；数据库文件须在本地文件系统上（不要用 NFS/SMB）。
- 备份：`omnigate backup <文件>` 用 `VACUUM INTO` 生成一致的副本，服务运行中也可执行；不要直接复制正在使用的 `.db` 文件。
- 用 `sqlite3` 命令行查看数据时请只读打开（`sqlite3 -readonly`），长时间持有写锁会让网关写入超时。

## 2. 登录配置

OmniGate 只接受外部身份登录，不存储任何密码（ADR-0005）。支持两类提供方：GitHub（OAuth2 适配器）与任意标准 OIDC 提供方，可同时启用多个。登录方式列表的顺序是：GitHub 在前，其余 OIDC 提供方按 `OMNIGATE_AUTH_OIDC` 中的顺序排列。

所有提供方都使用授权码流程 + PKCE（S256）+ 加密的一次性 state Cookie（10 分钟有效）。登录相关接口按客户端 IP 前缀（IPv4 /24、IPv6 /48）限流：突发 30 次，每 2 秒恢复 1 次，限流状态只保存在当前进程内。

### 2.1 GitHub OAuth App

GitHub 不为用户登录提供 OIDC（它的 OIDC 只面向 Actions 工作负载身份），所以 OmniGate 使用一个 OAuth2 适配器：先用授权码换取 access token，再调用 `GET /user` 获取不可变的数字 ID、login、昵称和头像，然后调用 `GET /user/emails`，只采用**已验证的主邮箱**。

1. 打开 GitHub → Settings → Developer settings → **OAuth Apps** → New OAuth App（组织级应用在组织设置的同名位置创建）。
2. 填写：
   - **Homepage URL**：`${OMNIGATE_PUBLIC_URL}`，例如 `https://gateway.example.com`
   - **Authorization callback URL**：`${OMNIGATE_PUBLIC_URL}/api/auth/github/callback`，例如 `https://gateway.example.com/api/auth/github/callback`
3. 创建后生成 Client Secret，写入：
   ```dotenv
   OMNIGATE_AUTH_GITHUB_CLIENT_ID=Ov23liXXXXXXXXXXXXXX
   OMNIGATE_AUTH_GITHUB_CLIENT_SECRET=xxxxxxxxxxxxxxxx
   ```
4. OmniGate 请求的 scope 为 `read:user user:email`，无需在 GitHub 端额外配置。
5. 查询某个用户的数字 ID（用于 `github-id:` 匹配器）：
   ```bash
   curl -s https://api.github.com/users/octocat | grep '"id"'
   ```

GitHub Enterprise Server：同样创建 OAuth App，并设置 `OMNIGATE_AUTH_GITHUB_AUTH_URL=https://<ghes>/login/oauth/authorize`、`OMNIGATE_AUTH_GITHUB_TOKEN_URL=https://<ghes>/login/oauth/access_token`、`OMNIGATE_AUTH_GITHUB_API_URL=https://<ghes>/api/v3`。provider id 仍然是 `github`，因此 github.com 与 GHES 不能同时启用。

本地开发如需测试 GitHub 登录，回调地址填 `http://localhost:5173/api/auth/github/callback`（与 `.env.dev` 中的 `PUBLIC_URL` 一致）。

### 2.2 通用 OIDC 提供方

适用于 Keycloak、Authentik、Authelia、Zitadel、Okta、Entra ID、Google 等任何支持 Discovery（`/.well-known/openid-configuration`）的提供方。

1. 为提供方选一个 ID，例如 `corp`（小写字母、数字、`-`，最长 32，不能为 `github`）。**ID 会作为身份的一部分存入数据库，上线后不要修改**，否则已有用户将无法匹配到原账号。也请避免使用 `email` 或以 `-id` 结尾的 ID，以免与匹配器语法产生歧义。
2. 在 IdP 上注册客户端：
   - 授权类型：Authorization Code（支持 PKCE S256）
   - 重定向 URI：`${OMNIGATE_PUBLIC_URL}/api/auth/corp/callback`
   - Scope：`openid profile email`
3. 配置：
   ```dotenv
   OMNIGATE_AUTH_OIDC=corp
   OMNIGATE_AUTH_OIDC_CORP_NAME=公司 SSO
   OMNIGATE_AUTH_OIDC_CORP_ISSUER=https://id.example.com/realms/main
   OMNIGATE_AUTH_OIDC_CORP_CLIENT_ID=omnigate
   OMNIGATE_AUTH_OIDC_CORP_CLIENT_SECRET=xxxxxxxx
   # OMNIGATE_AUTH_OIDC_CORP_SCOPES=openid,profile,email
   ```
   多个提供方：`OMNIGATE_AUTH_OIDC=corp,partner`，再分别配置 `OMNIGATE_AUTH_OIDC_CORP_*` 与 `OMNIGATE_AUTH_OIDC_PARTNER_*`。

行为说明：

- Discovery 在第一次登录时才进行（成功后缓存），IdP 暂时不可用不会阻止 OmniGate 启动；不可用期间登录会跳转到 `/login?error=oauth_failed`。
- 校验 id_token 的签名、issuer、audience（= Client ID）、有效期与 nonce。
- 身份以 `sub` 为准；`preferred_username` 只用于展示，**不会**用于匹配器。
- 只有 `email_verified=true` 的邮箱才参与 `email:` 匹配和邮箱域名白名单。

### 2.3 账号与身份的关系

- 每个外部身份以 `(provider, subject)` 唯一标识（GitHub 为数字 ID，OIDC 为 `sub`），首次登录时自动创建账号并关联。
- 不会按邮箱自动合并账号：同一个人分别用 GitHub 和 OIDC 登录，会得到两个独立账号。
- 账号被停用后，其所有会话立即失效，再次登录会跳转到 `/login?error=account_disabled`（登录页显示停用原因与到期时间），见 [2.7](#27-用户管理停用强制下线与批量操作)。

### 2.4 身份匹配器语法

`OMNIGATE_BOOTSTRAP_ADMINS` 与 `OMNIGATE_AUTH_ALLOWED_IDENTITIES` 使用相同的语法，每项形如 `<provider>:<value>`，多项以逗号分隔：

| 写法 | 匹配对象 | 说明 |
|---|---|---|
| `github:<login>` | GitHub 用户名 | 不区分大小写。GitHub 用户名**可以修改**，原用户名释放后可能被他人注册，存在冒用风险。 |
| `github-id:<id>` | GitHub 数字用户 ID | 不可变，**推荐**。必须是数字，否则拒绝启动。 |
| `<oidc-id>:<sub>` | 指定 OIDC 提供方的 `sub` | 精确匹配，区分大小写，如 `corp:248289761001`。 |
| `email:<address>` | 任意提供方的邮箱 | 不区分大小写；仅匹配已验证邮箱（GitHub 为已验证主邮箱，OIDC 需要 `email_verified=true`）。 |

推荐 `github-id`：它是 GitHub 侧的不可变主键，与 OmniGate 内部用来关联身份的字段相同；而 `github:<login>` 依赖可变、可被重新注册的用户名，用作引导管理员时风险最大。

示例：

```dotenv
OMNIGATE_BOOTSTRAP_ADMINS=github-id:583231,corp:7f1c2a90-3c1e-4c55-9b0e-2a5d0c8f1e11
OMNIGATE_AUTH_ALLOWED_IDENTITIES=github-id:1003,email:alice@partner.example
OMNIGATE_AUTH_ALLOWED_EMAIL_DOMAINS=example.com
```

> 匹配器**只在身份首次登录、创建账号时**生效。从 `BOOTSTRAP_ADMINS` 中移除某人不会降级其账号，把已有账号的身份加进去也不会提升权限；角色变更请在管理界面（或 `PATCH /api/admin/users/{id}`）中完成。反过来，如果所有管理员都无法登录，可以把一个**尚未在 OmniGate 中登录过**的身份加入 `BOOTSTRAP_ADMINS`，用它登录来恢复管理权限。

### 2.5 注册模式

对**尚未关联账号**的身份，准入判断按以下顺序进行：

1. 命中 `OMNIGATE_BOOTSTRAP_ADMINS` → 创建为 `system_admin`（任何注册模式下都生效，包括 `closed`）。
2. 开发模式规则（见 2.6）。
3. 按 `OMNIGATE_REGISTRATION_MODE`：

| 模式 | 新身份的结果 |
|---|---|
| `open` | 任何通过认证的身份都创建为 `user`。 |
| `restricted`（默认） | 命中 `AUTH_ALLOWED_IDENTITIES`，或已验证邮箱的域名在 `AUTH_ALLOWED_EMAIL_DOMAINS` 中 → 创建为 `user`；否则拒绝，跳转 `/login?error=not_allowed`。 |
| `closed` | 拒绝，跳转 `/login?error=registration_closed`。已有账号照常登录。 |

所有拒绝都会写入审计日志 `auth.login_denied`（metadata 中包含 `reason`）。当前注册模式可通过 `GET /api/system/info` 的 `registrationMode` 查看。

### 2.6 开发模式与生产模式的管理员引导

- **开发模式**（`OMNIGATE_ENV=development`）下，如果 `OMNIGATE_BOOTSTRAP_ADMINS` 为空、`OMNIGATE_PUBLIC_URL` 的主机是 localhost 或环回地址、且数据库中还没有任何用户，第一个成功登录的账号自动成为 `system_admin`（用非环回域名运行开发模式时不会启用，防止被陌生人抢占），不受注册模式限制，同时打印警告日志。只要配置了任意引导管理员，这条规则就不生效。
- **生产模式**下未配置 `OMNIGATE_BOOTSTRAP_ADMINS` 时**拒绝启动**，上述规则永远不会生效。
- 系统始终保证至少有一个启用状态的 `system_admin`：降级或停用最后一位系统管理员会返回 `409 last_admin`，管理员也不能停用自己（`409 cannot_disable_self`）。

> `.env.example` 中 `OMNIGATE_ENV` 的默认值是 `development`（便于本机试用）。部署到服务器时运行 `./scripts/init-env.sh https://你的域名`，脚本会自动设为 `production` 并写入 `PUBLIC_URL`；也可以手动修改。

### 2.7 用户管理：停用、强制下线与批量操作

在控制台“用户”页面操作（接口契约见 `docs/contracts/phase7-api.md` §2）。查看用户与详情需要 `users.read`（`system_admin`、`auditor`），
修改需要 `users.write`（仅 `system_admin`）。

**停用原因与期限**：`PATCH /api/admin/users/{id}` 把 `status` 设为 `disabled` 时必须填写 `disabledReason`（去除首尾空白后 1–200 字），
可选 `disabledUntil`（RFC 3339，必须晚于当前时间；省略或 `null` 表示永久停用）。不带 `status: "disabled"` 单独设置这两个字段返回 `422`；
改回 `active` 时服务端清空两者。用户对象（列表、详情、`/api/me`）中的 `disabledReason` / `disabledUntil` 未停用时为 `null`。

停用后：

| 方面 | 行为 |
|---|---|
| 会话 | 该用户的全部会话在数据库中撤销（`revoked_reason = admin_disabled`），立即失效 |
| 登录 | 跳转 `/login?error=account_disabled&reason=<URL 编码的原因>&until=<RFC 3339 UTC>`（永久停用时省略 `until`），登录页显示原因与到期时间 |
| API Key | Key 本身仍有效（启用、未过期）时网关返回 **`403 account_disabled`**（OpenAI 格式 `type=invalid_request_error`，Anthropic 格式 `type=permission_error`），消息形如 `account disabled: <原因> (until <时间>)`；未知、已撤销、已停用、已过期的 Key 仍返回 `401`。Key 认证结果在各进程缓存 10 秒：本实例立即生效，多实例部署时其他实例最多 10 秒后生效 |
| 通知 | 用户收到 `account.status_changed`（停用用户只会收到这一类通知，见 [16.2](#162-事件与接收者)） |

**到期自动启用**：后台任务在启动时及之后每分钟执行一次，把 `disabledUntil` 已到的停用用户改回 `active` 并清空原因与期限，写审计
`user.auto_enable`（无操作者，metadata 含原停用原因与期限），并通知用户。在任务执行之前，期限已过的停用对登录、会话与 API Key 就已视为启用。
每个实例都运行该任务（操作幂等）。

**用户详情**：`GET /api/admin/users/{id}` 汇总身份列表、钱包（余额 / 预留，没有钱包时为 `null`）、最近 20 份订阅（任意状态，含规则用量）、
未撤销的 API Key、活跃会话数、近 30 天用量（请求数含失败请求、钱包扣费合计、token 合计）与其拥有的渠道数。

**单个操作**（`users.write`）：

| 接口 | 作用 | 审计 |
|---|---|---|
| `POST /api/admin/users/{id}/logout` | 强制下线：撤销全部会话，返回 `{revoked}`；不能对自己执行（`409 cannot_disable_self`）。同时通知用户（`account.status_changed`，action `logout`） | `user.logout` |
| `POST /api/admin/users/{id}/keys/disable` | 禁用该用户全部已启用的 API Key，返回 `{disabled}`（数量）；Key 不会被撤销，所有者之后可以在“API Keys”页面重新启用 | `user.keys_disable` |

**用户组**：每个用户属于恰好一个用户组，用户列表与详情显示 `group`，`GET /api/admin/users?groupId=` 按组过滤；`PUT /api/admin/users/{id}/group`
（`{"groupId": "..."}`）或批量操作 `set_group` 修改，见[第 17 节](#17-用户组价格倍率限额与分时价格)。

**批量操作**：`POST /api/admin/users/batch`，`{"ids": [...], "action": "disable" | "enable" | "logout" | "set_group", "reason": "...", "until": "..." | null, "groupId": "..."}`，
`ids` 1–200 个（重复的只处理一次）；`disable` 必须带 `reason`。返回 `200 {"succeeded": [...], "failed": [{"id", "code", "message"}]}`，
每个用户逐个应用与单个操作相同的保护规则，单个失败不影响其他用户：

| 失败 `code` | 原因 |
|---|---|
| `not_found` | ID 不是合法 UUID，或用户不存在 |
| `cannot_disable_self` | 停用或强制下线自己 |
| `last_admin` | 停用最后一位启用状态的系统管理员 |

- `enable` 已启用的用户视为成功（无变化）；`disable` 已停用的用户会更新其原因与期限。批量操作不做乐观锁校验。
- 参数本身不合法（`ids` 数量越界、未知 `action`、`disable` 缺少 `reason`、`until` 不合法）时整个请求返回 `422`。
- 审计：一条 `user.batch`（action、ids、reason、until、成功与失败列表），另为每个成功的用户写一条 `user.update`（停用 / 启用）或 `user.logout`。

单个接口的错误码与之相同：`409 cannot_disable_self`、`409 last_admin`、`404 not_found`。`user.update` 审计的 before / after 中包含停用原因与期限。

## 3. 主密钥与轮换

### 3.1 格式与用途

```dotenv
OMNIGATE_MASTER_KEY=<kid>:<base64(32 字节)>[,<kid>:<base64(32 字节)>...]
```

- `kid` 为密钥标识，不能包含 `:` 和 `,`，且不能重复；密钥部分为标准 Base64（带 `=` 填充），解码后必须正好 32 字节。
- **第一个密钥是当前加密密钥**，其余只用于解密旧数据。
- 每个子系统用 HKDF 从主密钥派生独立的子密钥（AES-GCM），密文格式为 `v1:<kid>:<...>`，因此可以根据 kid 找到对应的解密密钥。
- 主密钥用于加密 OAuth 登录 state Cookie（10 分钟有效）和**渠道凭据（上游 API Key）**。渠道凭据的密文与渠道 ID 绑定，控制台只显示末 4 位。
- Gateway Key 与兑换码不加密存储，而是只保存 SHA-256 摘要（明文只在创建时显示一次），与主密钥无关。

生成密钥（任选其一）：

```bash
make keygen                                  # 源码目录
docker compose run --rm omnigate keygen      # 使用镜像
openssl rand -base64 32                      # 自行加上前缀，如 k2:<输出>
```

`omnigate keygen` 输出形如 `k20261008-a1b2c3:3q2+7w...=`，kid 由日期和随机后缀组成，不会重复。

开发环境不设置主密钥时，每次启动都会生成一个临时密钥：重启后，正在进行中的登录会失败（`state_mismatch`），已保存的渠道凭据**永久无法解密**——数据面会跳过这些渠道，测试渠道返回 `409 secret_unavailable`，需要在渠道编辑页重新填写 API Key。`make dev-server` 通过自动生成的 `.env.dev.local` 提供固定的本地主密钥，避免了这个问题（见[第 5 节](#5-本地开发流程)）。

### 3.2 轮换步骤

1. 生成新密钥。
2. 把新密钥放在**最前面**，保留旧密钥：
   ```dotenv
   OMNIGATE_MASTER_KEY=k20261101:NEW_BASE64...,k20261008:OLD_BASE64...
   ```
3. 重启 OmniGate（Compose：`docker compose up -d`）。此后新写入的数据都用新密钥加密，旧数据依然可以用旧密钥解密。
4. **不要移除旧密钥。** 当前版本**没有**批量重新加密工具：轮换后只有新写入或重新填写的渠道凭据使用新密钥，已有渠道凭据仍需旧密钥解密。
   只有在每个渠道都重新填写过 API Key（`PATCH /api/channels/{id}` 带 `apiKey`）之后，才能安全移除旧密钥。
   提前移除旧密钥会导致相应渠道从数据面消失（静默跳过，其他渠道不受影响），测试 / 发现模型返回 `409 secret_unavailable`。

### 3.3 备份警告

- 主密钥**不在数据库里**。仅有数据库备份而丢失主密钥，所有加密数据（包括全部渠道凭据）都无法恢复，只能逐个重新填写上游 API Key。
- 主密钥与数据库备份同时泄露，就等于所有加密的密钥都已泄露。请把主密钥与数据库备份**分开保存**（密码管理器 / 密钥管理服务），并限制 `.env` 的权限（`init-env.sh` 会设为 `600`）。
- 每次轮换后都要同步更新主密钥的备份。

## 4. 结算币种

- OmniGate 整个部署只使用一种结算币种（ADR-0006），金额以定点数存储（9 位小数），从不使用浮点数。
- 首次启动时，`OMNIGATE_CURRENCY` 被写入 `system_settings` 表（key `billing.currency`，值如 `{"code":"CNY","symbol":"¥","decimals":2}`）。写入使用 `ON CONFLICT DO NOTHING`，此后**以数据库中的值为准并锁定**。
- 之后再修改 `OMNIGATE_CURRENCY` 不会生效，只会在启动日志中打印警告 `OMNIGATE_CURRENCY differs from the stored settlement currency; keeping stored value`。`GET /api/system/info` 返回的始终是锁定的币种。
- 更换币种必须通过显式的数据迁移（同时换算所有账本数据），目前没有提供相应工具。不要直接修改 `system_settings`。
- 内置符号与小数位：USD `$`、CNY `¥`、EUR `€`、GBP `£`、HKD `HK$`、SGD `S$`（2 位），JPY `¥`（0 位）。其他三字母代码也会被接受，符号为“代码 + 空格”、小数位为 2。代码只校验长度是否为 3，所以**请在首次启动前确认币种代码正确**。

## 5. 本地开发流程

前置要求：Docker（含 Compose v2）、Go、Node.js 与 pnpm。

```bash
make dev-deps     # 启动 PostgreSQL(5432)、mock-oidc(9000)，仅监听 127.0.0.1
make dev-server   # 读取 .env.dev 与 .env.dev.local（不存在时先自动生成），在宿主机运行后端 :8080（启动时自动迁移）
make dev-web      # 前端开发服务器 http://localhost:5173（另开终端）
```

打开 <http://localhost:5173>，选择“本地模拟 OIDC”，在 mock-oauth2-server 的交互式登录页中**输入任意用户名**即可登录（用户名即 `sub`）：

- 用户名 `admin` 命中 `.env.dev` 中的 `OMNIGATE_BOOTSTRAP_ADMINS=dev:admin`，成为 `system_admin`。
- 其他用户名在 `open` 注册模式下成为普通 `user`。
- 由于 `.env.dev` 配置了引导管理员，“第一个用户自动成为管理员”的规则在这里不会生效。

说明：

- `.env.dev` 中 `OMNIGATE_PUBLIC_URL=http://localhost:5173`：浏览器始终访问 Vite，Vite 把 `/api` 与 `/v1` 代理到 `:8080`（保留原始 Host/Origin），所以 OIDC 回调地址是 `http://localhost:5173/api/auth/dev/callback`，CSRF 的允许源也是 5173。直接访问 `:8080` 只能看到占位页（除非已执行 `make build`），并且写操作会因 Origin 不匹配而被拒绝。
- **本地主密钥**：随仓库提供的 `.env.dev` 不含主密钥。`make dev-env` 会在仓库根目录生成 `.env.dev.local`（权限 600，已被 `.gitignore` 的
  `*.local` 规则忽略，**不要提交**），其中只有一行固定的本地主密钥 `OMNIGATE_MASTER_KEY=kdev-<随机后缀>:<随机 32 字节 base64>`；
  文件已存在时不会覆盖。`make dev-server`、`make migrate`、`make migrate-status` 都依赖 `dev-env`，并在 `.env.dev` 之后加载 `.env.dev.local`
  （其中的变量优先），所以首次运行时会自动生成。这样重启后端后，已保存的渠道 API Key 仍能解密，进行中的登录也不会因 state Cookie
  无法解密而失败。删除 `.env.dev.local` 等于更换主密钥：之前保存的渠道凭据会变得无法解密（见 [3.1](#31-格式与用途)），需要重新填写。
  需要其他本地覆盖（如 GitHub 登录的 Client ID）时也可以写在这个文件里。
- 直接 `cd server && go run ./cmd/omnigate serve` 而不加载 `.env.dev.local` 时，开发环境会退回到进程内临时密钥（日志中有警告），重启后渠道凭据无法解密。
- **前端文件监听报 `EMFILE` / 页面不热更新**：Linux 上 Vite 依赖 inotify，当前用户的 inotify 实例数（`fs.inotify.max_user_instances`，
  很多发行版默认 128）被 IDE 等工具占满时，所有新的监听都会失败。`make dev-web` 启动前会运行 `scripts/check-inotify.sh`，检测到剩余实例不足 8 个时
  自动设置 `VITE_USE_POLLING=1` 改用轮询（CPU 占用略高）；直接用 pnpm 时可手动 `VITE_USE_POLLING=1 pnpm dev`。根治方法（提高 sysctl 上限）
  见 `web/README.md`。
- 本地调试渠道时，如果上游是本机服务（如 Ollama `http://127.0.0.1:11434/v1`），需要在 `.env.dev` 中设置 `OMNIGATE_CHANNELS_ALLOW_PRIVATE_NETWORK=true`，
  并用管理员账号（如 `admin`）创建该渠道，见 [8.6](#86-网络安全ssrf)。
- 其他常用命令：`make migrate`、`make migrate-status`（加 `DATABASE_URL=sqlite:///tmp/omnigate.db` 可对 SQLite 文件操作）、`make keygen`、`make test-unit`、`make test-integration`（集成测试跑在 `dev-deps` 的 PostgreSQL 上）、`make test-sqlite`（集成测试跑在临时 SQLite 文件上，无需外部服务）、`make lint`、`make build`（生成嵌入前端的 `bin/omnigate`）。
- 不启动 PostgreSQL 也可以在本地用 SQLite 运行后端（`.env.dev` 指向 PostgreSQL，需在其后覆盖）：
  `cd server && set -a && source ../.env.dev && source ../.env.dev.local && set +a && OMNIGATE_DATABASE_URL=sqlite:///tmp/omnigate-dev.db go run ./cmd/omnigate serve`。

## 6. Compose 部署流程

> 生产环境请使用 `deploy/docker-compose.yml`（只有 omnigate 一个服务，数据库由 `OMNIGATE_DATABASE_URL` 决定，配置在 `deploy/omnigate.env`），步骤见 [deploy-production.md](deploy-production.md)。本节描述仓库根目录的 `docker-compose.yml`（从源码构建试用，同时包含本地开发依赖）。只支持单实例部署。

```bash
./scripts/init-env.sh          # 由 .env.example 生成 .env：随机主密钥 + 随机数据库密码，权限 600；已有 .env 时不会覆盖
vi .env                        # 至少修改下列配置
docker compose up -d --build   # 或 make up
docker compose logs -f omnigate
curl -fsS http://127.0.0.1:8080/readyz
```

`.env` 中至少需要设置：

```dotenv
OMNIGATE_ENV=production
OMNIGATE_PUBLIC_URL=https://gateway.example.com
OMNIGATE_TRUSTED_PROXIES=<网关地址>          # 代理到达容器时的精确地址（宿主机代理即该 Compose 网络的网关），见第 7 节
OMNIGATE_BOOTSTRAP_ADMINS=github-id:583231
OMNIGATE_AUTH_GITHUB_CLIENT_ID=...
OMNIGATE_AUTH_GITHUB_CLIENT_SECRET=...
OMNIGATE_CURRENCY=USD                       # 首次启动后锁定
```

说明：

- Compose 会启动 `postgres`（17）和 `omnigate`（当前版本不使用 Redis，已移除）。数据保存在 `pgdata` 卷中；`make down` / `docker compose down` 会保留数据卷（不要加 `-v`）。
- PostgreSQL 只监听宿主机 `127.0.0.1`；OmniGate 映射为 `${OMNIGATE_PORT:-8080}:8080`，**监听宿主机所有网卡**。如果反向代理和 OmniGate 在同一台机器上，建议用 override 文件改为 `127.0.0.1:8080:8080`，或在防火墙上屏蔽该端口。
- 指标端口 9090 只在 Compose 内部网络中暴露，供 Prometheus 抓取。
- 镜像基于 distroless static，以非 root 用户（uid 65532）运行，内置 `HEALTHCHECK` 通过 `omnigate healthcheck` 子命令访问 `/healthz`（镜像中没有 shell、curl、wget）。收到 SIGTERM 后进程会优雅退出，最多等待 30 秒让进行中的请求（包括流式响应）完成。
- 健康检查：`/healthz` 只检查进程存活；`/readyz` 检查数据库连通性和迁移状态（失败返回 503），适合作为负载均衡的就绪检查。

## 7. 反向代理

生产环境应在 OmniGate 前放置终止 TLS 的反向代理。

- **https**：`OMNIGATE_PUBLIC_URL` 必须是用户实际访问的 https 地址（生产强制要求）。Cookie 会自动带 `Secure`。
- **不要改写 `Origin`**：CSRF 校验比较请求的 `Origin` 头与 `PUBLIC_URL` 的源。
- **根路径部署**：路由、`/login` 重定向和 Cookie 路径都假定 OmniGate 部署在域名根路径，请使用独立的（子）域名，不要挂在 `/omnigate/` 之类的子路径下。
- **真实客户端 IP**：把代理到达 OmniGate 时的来源地址加入 `OMNIGATE_TRUSTED_PROXIES`（CIDR）。OmniGate 只读取 `X-Forwarded-For`（不读 `X-Real-IP` / `Forwarded`），从右向左跳过受信任地址，取第一个不受信任的地址作为客户端 IP。客户端 IP 会以 /24（IPv4）或 /48（IPv6）前缀的形式写入会话与审计日志，并用作登录限流的键。如果没有正确配置，所有用户都会显示为代理的 IP，并且共用同一个登录限流桶。
  - 代理与 OmniGate 在同一主机、通过映射端口访问容器时，容器看到的来源是该 Compose 网络的网关地址。`deploy/docker-compose.yml` 固定了网段
    `172.30.88.0/24`，因此填 `172.30.88.1`；容器版 Caddy（`docker compose --profile caddy`）固定为 `172.30.88.10`。其他部署可用
    `docker network inspect <网络> --format '{{(index .IPAM.Config 0).Gateway}}'` 查出网关地址。
  - 只信任确实是代理的地址，**不要**写 `172.16.0.0/12` 这类大网段：同网段的任何容器（以及端口对公网发布时经端口映射进入的外部连接）都能伪造 `X-Forwarded-For`。
    OmniGate 端口应只绑定 `127.0.0.1`。
- **SSE / 流式响应**：`/v1` 的流式响应（`stream: true`，`Content-Type: text/event-stream`）必须**关闭代理缓冲**，否则客户端要等整个回答生成完才收到内容
  （首字延迟变成总耗时，Claude Code 等工具会表现为“卡住”）。nginx 需要 `proxy_buffering off; proxy_read_timeout 600s;`（OmniGate 会在 SSE 响应上
  附带 `X-Accel-Buffering: no`，但仍建议显式配置；如启用了 gzip，也不要对 `text/event-stream` 压缩）。读超时应大于两次事件之间的最长间隔：
  OmniGate 在上游 5 分钟无数据时中止流，等待首个响应头的超时由渠道 `timeoutSeconds` 决定（默认 60 秒，最大 600 秒）。OmniGate 本身没有设置写超时。
- **请求体大小**：数据面请求体上限 32 MiB（超出返回 413），图片接口（`/v1/images/*`）与语音转写 / 翻译（`/v1/audio/transcriptions`、
  `/v1/audio/translations`）为 64 MiB，控制面 1 MiB。代理的 `client_max_body_size` 建议设为 `32m`，提供图片编辑或语音转写接口时设为 `64m`，
  否则大图片 / 大音频 / 长上下文请求会先被代理拒绝（见 [8.9](#89-图片接口)、[8.10](#810-音频接口)）。语音合成（`/v1/audio/speech`）的音频边收边发，
  代理同样需要关闭缓冲（`proxy_buffering off`），否则客户端要等整段音频生成完才开始播放。
- **带下划线的请求头**：Codex CLI 用 `Session_id`、`Thread_id` 等带下划线的请求头标识会话。nginx 默认丢弃这类请求头，需要
  `underscores_in_headers on;`（放在 `server` 块中），否则会话亲和与上游号池只能依靠请求体中的 `prompt_cache_key`（见 [18](#18-会话亲和与上游号池缓存命中)）。
  Caddy 默认原样转发。
- **指标端口**：不要把 9090 端口暴露到公网。

完整、可直接使用的配置见 `deploy/nginx/omnigate.conf`（含 HTTP 跳转、certbot、HSTS）与 `deploy/Caddyfile`（自动 HTTPS），
步骤见 [deploy-production.md 第 6 节](deploy-production.md#6-反向代理与-https)。nginx 的关键部分：

```nginx
server {
    listen 443 ssl http2;
    server_name gateway.example.com;
    ssl_certificate     /etc/letsencrypt/live/gateway.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/gateway.example.com/privkey.pem;
    add_header Strict-Transport-Security "max-age=31536000" always;

    client_max_body_size 64m;   # 图片编辑、语音转写的上限；其余数据面接口 32 MiB 由 OmniGate 自己返回 413
    underscores_in_headers on;  # 转发 Codex CLI 的 Session_id 等会话请求头

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_set_header Host              $host;
        proxy_set_header X-Forwarded-For   $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
        # 流式响应（SSE、语音合成）——必须关闭缓冲
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 900s;
        proxy_send_timeout 900s;
    }
}
```

Caddy 的关键部分：

```caddyfile
gateway.example.com {
    request_body {
        max_size 64MiB
    }
    reverse_proxy 127.0.0.1:8080 {
        flush_interval -1   # 立即转发 SSE 事件，不缓冲
    }
}
```

## 8. 渠道接入

渠道（Channel）是 OmniGate 转发请求的上游。在控制台“渠道”页面或 `POST /api/channels` 创建，任何持有 `channels.write` 的用户（默认包括普通 `user`）都可以创建自己的渠道。

### 8.1 渠道类型与 Base URL 约定

| type | 上游协议 | `baseUrl` 约定 | 网关拼接的路径 | 测试 / 发现模型 |
|---|---|---|---|---|
| `openai` | OpenAI Chat Completions（默认）或 OpenAI Responses（按模型的 `upstreamProtocol` 或渠道的 `supportsResponses`）；嵌入；文本补全（`supportsCompletions`） | **含版本路径**，如 `https://api.openai.com/v1` | `/chat/completions`、`/responses`、`/embeddings`、`/images/generations`、`/images/edits`、`/images/variations`、`/audio/transcriptions`、`/audio/translations`、`/audio/speech`、`/completions` | `GET {baseUrl}/models` |
| `anthropic` | Anthropic Messages | **不含版本路径**，如 `https://api.anthropic.com` | `/v1/messages`、`/v1/messages/count_tokens` | `GET {baseUrl}/v1/models` |
| `custom` | 插件实现的私有协议（见 [9.9](#99-自定义协议插件)）；只能通过选择 `protocol: "custom"` 的插件版本创建 | 由插件约定 | 由插件的 `buildRequest` 决定 | 插件的 `health.check` / `models.list` 能力 |

- 任何 OpenAI 兼容服务都可以作为 `openai` 渠道，`baseUrl` 填到 `/chat/completions` 之前的部分，例如自建 vLLM `http://10.0.0.5:8000/v1`、本地 Ollama `http://127.0.0.1:11434/v1`（内网地址需要开启私网开关，见 [8.6](#86-网络安全ssrf)）。
- 最常见的错误：`openai` 渠道漏掉 `/v1`，或 `anthropic` 渠道多写了 `/v1`（变成 `/v1/v1/messages`），两者都会得到上游 404。创建后先点“测试”。
- `baseUrl` 必须是 http(s) 绝对地址，不能带用户信息、查询参数或片段，末尾的 `/` 会被去掉。`type` 创建后不能修改。
- 网关自动设置认证头：`openai` 渠道发送 `Authorization: Bearer <apiKey>`；`anthropic` 渠道发送 `x-api-key: <apiKey>`，并转发客户端的 `anthropic-version` / `anthropic-beta` 头（客户端未提供时使用 `anthropic-version: 2023-06-01`）。渠道所用插件声明了 `signRequest` Hook 时，认证头改由插件生成（见[插件](#9-插件)）。`custom` 渠道不添加任何认证头，由插件的 `buildRequest` / `signRequest` 生成。
- 上游 API Key 用主密钥加密保存，控制台只显示末 4 位（`…a1b2`）；修改渠道时不填 API Key 表示保持不变。

### 8.2 模型映射

- 每个渠道至少配置一个模型映射 `{model, upstreamModel}`：`model` 是客户端请求中使用的**逻辑模型名**，`upstreamModel` 是发给上游的真实名称（留空则相同）。
  例如把 `claude-sonnet` 映射到 `claude-sonnet-4-5`，以后升级上游模型时客户端不用改配置。
- 多个渠道配置同一个逻辑模型时，它们互为备份：按 `priority`（-1000–1000，越大越优先）分组；同一优先级内优先选择与客户端协议相同、无需转换的渠道
  （其次是一步转换，最后是 Messages ↔ Responses 这类经 Chat 两步转换的渠道），协议匹配度相同的再按 `weight`（1–1000）加权随机；
  前一个渠道连接失败、超时、上游 401/403/404/408/429/5xx 时自动回退到下一个（每个模型最多 3 个平台渠道，可在系统设置 `gateway.maxAttempts` 修改，
  且只在尚未向客户端输出任何内容时回退）。用户自己的渠道和别人共享给他的渠道总是排在平台渠道之前，见 [8.8](#88-渠道归属与计费)。
- 同一渠道连续 3 次失败后熔断 30 秒。冷却结束后后台每 15 秒主动探测一次（OpenAI / Anthropic 渠道请求模型列表，自定义协议渠道调用插件的 `health.check`，没有则 `models.list`）：成功即恢复为 healthy，401 / 403 / 408 / 429 / 5xx 或连接失败则再熔断 30 秒；探测结果不确定（如上游没有模型列表接口返回 404）时改为放行一个真实请求作为探测，5 分钟后再主动探测。熔断状态在各进程内存中，重启后归零。
- “发现模型”按钮调用上游模型列表，只用于展示差异，不会自动写入映射。
- **按模型指定上游协议**（Round 4，仅 `openai` 渠道）：映射中的 `upstreamProtocol` 为 `chat`（默认，保存为空、响应中省略）或 `responses`。
  设为 `responses` 时，该模型的所有请求（不论客户端用 Chat、Messages 还是 Responses）都以 OpenAI Responses 协议发送到 `{baseUrl}/responses`，
  适用于只支持 Responses API 的模型（例如 `{"model":"gpt-6-pro","upstreamProtocol":"responses"}`）。只有 `openai` 渠道可以设置，`anthropic` 渠道设置会返回 `422`。
  其他取值返回 `422`。同一渠道中不同模型可以使用不同的上游协议。
- 嵌入模型（如 `text-embedding-3-small`）同样作为普通映射配置在 `openai` 渠道上，客户端通过 `/v1/embeddings` 调用（见 [8.7](#87-协议转换与嵌入接口)）；
  图片模型（如 `gpt-image-1`、`dall-e-3`）也一样，客户端通过 `/v1/images/*` 调用（见 [8.9](#89-图片接口)）。

### 8.3 可见性与共享

| scope | 谁能使用（通过网关调用） | 谁能看到 |
|---|---|---|
| `private`（默认） | 仅所有者 | 所有者；持有 `channels.manage` 的管理员 |
| `shared` | 所有者 + `sharedWith.users` 中**已接受**共享的用户（最多邀请 200 个）+ `sharedWith.groups` 中用户组的全部成员（最多 50 个组，只有渠道管理员可以设置） | 同左（待接受的邀请只出现在被邀请者的“共享给我的”中），被共享者只看到名称、类型、模型与健康状态 |
| `global` | 所有用户 | 所有用户（同样只看到精简信息） |

- 发布 `global` 渠道需要 `channels.manage`。
- `sharedWith` 为 `{"users": [...], "groups": [...]}`（Round 6 起可以共享给用户组，见[第 17 节](#17-用户组价格倍率限额与分时价格)）；
  旧客户端提交的字符串数组仍然接受，视为 `users`。按组共享时，成员加入 / 离开该组后立即获得 / 失去使用权；组被删除时其共享随之删除。
  档位不变：管理员的渠道共享给组后对成员仍是平台档（计费），普通用户的渠道是共享档（免费）。
- **共享给用户需要对方接受**（安全修订，`docs/contracts/phase5-api.md` §5）：把渠道共享给某个用户只是发出邀请，对方在控制台
  “渠道 → 共享给我的”（`/console/channels?tab=shared`，接口 `GET /api/channel-shares`）中**接受**后才能使用；也可以拒绝，或在接受后随时
  “退出共享”。被邀请者会收到站内通知 `channel.share_invited`（见 [16.2](#162-事件与接收者)）。所有者在渠道表单的共享区域看到每个人的状态
  （待接受 / 已接受 / 已拒绝）；把已拒绝的用户重新加入列表并保存即重新邀请。这一规则对所有渠道都适用（包括管理员的渠道，以及管理员修改他人渠道时添加的用户）。
  修订的原因：路由顺序是 own → shared → platform，未经同意的共享会让共享者的上游**先于**平台渠道收到被共享者的请求，从而读取或篡改提示词与响应。
- **共享给用户组只有持有 `channels.manage` 的管理员可以设置**（普通用户提交新的用户组返回 `422`，`details["sharedWith.groups"]`），
  按组共享不需要成员接受。普通用户修改自己的渠道时可以保留或移除管理员设置的用户组。
- 渠道所有者被停用后，他的渠道的全部共享（用户与用户组）立即停止生效；重新启用后恢复。
- **升级到迁移 `00015_share_acceptance` 时**：已有的用户共享视为已接受（行为不变）；回滚该迁移会删除所有未接受的邀请。
  升级前由普通用户自行设置的**用户组共享**仍然有效，请管理员用 `docs/contracts/phase5-api.md` §5.7 中的 SQL 检查并移除不认可的记录。
- API Key 策略的 `allowedChannels` 仍然先行收窄：不在其中的共享渠道永远不会被选中。
- 被共享 / global 的用户永远看不到 `baseUrl`、`config` 和密钥提示，也不能修改或测试。
- `channels.manage` 允许管理（查看完整配置、修改、删除）他人的渠道，但**不**允许通过网关使用他人的 `private` 渠道。
- 渠道修改后会立即触发本实例重新加载渠道快照；多实例部署时其他实例最多 15 秒后生效（周期性重新加载）。

### 8.4 高级配置（`config`）

| 字段 | 默认 | 说明 |
|---|---|---|
| `supportsResponses` | `false` | 仅 `openai` 渠道：上游实现了 `/responses`（如 OpenAI 官方）时开启，`/v1/responses` 请求会**直通**到该渠道（保真度最高，支持 `previous_response_id` 等依赖上游状态的功能）。未开启时 `/v1/responses` 请求被转换为 Chat Completions 发送（Round 4 起不再返回 404）。只影响 Responses 客户端；要让某个模型的**所有**请求都走 Responses，请在模型映射中设置 `upstreamProtocol: "responses"`（见 [8.2](#82-模型映射)）。 |
| `supportsCompletions` | `false` | 仅 `openai` 渠道：上游实现了旧版 `/completions`（含 `suffix` 的 FIM 代码补全）时开启。只有开启了它的渠道会收到 `/v1/completions` 请求（直通，见 [8.11](#811-文本补全接口completions--fim)）；未开启的渠道永远不会被选中，所以不支持该接口的上游不会因此收到 404。例如 DeepSeek FIM 需要 Base URL `https://api.deepseek.com/beta`。 |
| `maxTokensField` | `max_tokens` | 仅 `openai` 渠道：Anthropic 请求（如 Claude Code）或 Responses 请求被转换为 Chat Completions 时，输出上限写入哪个字段。o 系列等较新的 OpenAI 模型拒绝 `max_tokens`，此时设为 `max_completion_tokens`。同协议直通、以及发往 Responses 上游（使用 `max_output_tokens`）的请求不受影响。 |
| `timeoutSeconds` | `0`（= 60 秒） | 等待上游**响应头**的超时，范围 0–600 秒。不限制流式响应的总时长（流式在上游 5 分钟无数据时中止）。推理模型首包较慢时可适当调大。 |
| `headers` | 无 | 附加到每个上游请求的自定义请求头，最多 20 个。名称只能包含字母、数字、`-`、`_`（最长 64），值不能含换行。**禁止**设置 `Authorization`、`x-api-key`（认证头由网关根据 API Key 生成）以及 `Host`、`Content-Length`、`Content-Type`、`Accept-Encoding`、`Connection`、`Transfer-Encoding`、`Cookie`、`TE`、`Upgrade`、`Keep-Alive`、`Proxy-Authorization`、`Proxy-Connection`（不区分大小写）。典型用途：OpenRouter 的 `HTTP-Referer` / `X-Title`、某些代理服务要求的组织 / 项目头。 |

`config` 在修改时整体替换（未提交的字段回到默认值）。

### 8.5 测试与健康

- “测试”请求上游的模型列表接口（20 秒超时），显示状态码与延迟；失败也会计入熔断计数，成功则恢复为 healthy。停用的渠道也可以测试。
- 测试 / 发现模型返回 `secret_unavailable` 表示 API Key 无法用当前主密钥解密（见 [3.2](#32-轮换步骤)），重新填写 API Key 即可。
- 上游错误信息在展示和记录前会做脱敏（去掉形似密钥的字符串）。

### 8.6 网络安全（SSRF）

- 默认情况下，渠道不能连接环回（`127.0.0.0/8`、`::1`）、私有（`10/8`、`172.16/12`、`192.168/16`、`fc00::/7`）、链路本地（`169.254/16`，含云元数据地址）、CGNAT、组播、文档保留等非公网地址。
  校验在 DNS 解析后的拨号阶段进行，域名解析到内网地址同样被拒绝，DNS 重绑定也无法绕过；上游返回的 3xx 重定向一律不跟随。
- 创建或修改渠道时，字面量内网 IP 或 `localhost` 会被直接拒绝（`422 base_url_not_allowed`）；解析到内网的域名在测试 / 调用时失败，提示“目标地址被安全策略拒绝”。
- 设置 `OMNIGATE_CHANNELS_ALLOW_PRIVATE_NETWORK=true` 后，**所有者持有 `channels.manage`** 的渠道可以访问内网；判断依据是渠道所有者的当前角色，而不是操作者的角色。
  因此要接入本地 Ollama 或内网网关，请由管理员创建该渠道，再按需设为 `shared` / `global` 供其他人使用。
- 需要经代理访问上游时使用 `OMNIGATE_UPSTREAM_PROXY`；进程的 `HTTP_PROXY` 等环境变量对上游请求无效。

### 8.7 协议转换与嵌入接口

网关有四个生成类入口：`/v1/chat/completions`（OpenAI Chat）、`/v1/responses`（OpenAI Responses）、`/v1/messages`（Anthropic Messages）
和 `/v1/embeddings`（OpenAI Embeddings）。Round 4 起前三种协议**两两互转**（含流式、工具调用、图片、推理内容），任何一种客户端都可以使用任何
渠道上的模型；转换以 Chat 为中间格式，Messages ↔ Responses 经 Chat 两步完成。每个渠道使用的上游协议：

| 渠道 | 上游协议 |
|---|---|
| `anthropic` | 总是 Messages |
| `openai`，该模型 `upstreamProtocol = "responses"` | 总是 Responses |
| `openai`，`/v1/responses` 请求且 `supportsResponses = true` | Responses（直通） |
| `openai`，其他情况 | Chat Completions |

客户端协议与上游协议相同时直通（只改写 `model`），否则转换。转换无法表达的字段按 Key 的兼容模式处理（`strict` 返回 400，`lenient` 丢弃并在
`X-OmniGate-Compat-Warnings` 中列出，见第 12 节）。与 Responses 相关的几点：

- `previous_response_id` 依赖上游保存的会话状态，转换时**无论兼容模式**都返回 `400 unsupported_parameter`；需要它的客户端请让请求直通：渠道开启 `supportsResponses`，或该模型设为 `upstreamProtocol: "responses"`。
- Chat 的 `stop` 在 Responses 中没有对应参数：`strict` 下返回 400，`lenient` 下丢弃。
- 转换到 Responses 时网关总是发送 `store: false`，上游不会保存对话。
- Responses 输入中的 `reasoning` 项与 `reasoning.summary` 只是提示，转换到其他协议时丢弃并告警；推理摘要在响应方向会映射为 Chat 的
  `reasoning_content` / Anthropic 的 `thinking`。

**嵌入接口** `POST /v1/embeddings`：OpenAI 兼容，只路由到 `openai` 渠道并直通到 `{baseUrl}/embeddings`（只改写 `model`）。
逻辑模型只由 `anthropic` 渠道提供时返回 `404 model_not_found`（消息注明嵌入只由 OpenAI 兼容渠道提供）。请求中的 `stream` 字段会被去掉后再转发（嵌入不支持流式）。
用量取上游的 `prompt_tokens`，按售价的**输入**单价计费；路由回退、熔断、Key 策略、套餐配额与请求日志（`inbound = openai.embeddings`）
与其他接口相同。强制计费下只按估算的输入 token 预留余额（嵌入不产生输出 token），结算时按实际用量多退少补。

**图片接口** `/v1/images/*` 与**音频接口** `/v1/audio/*` 同样只路由到 `openai` 渠道并直通，见 [8.9](#89-图片接口)、[8.10](#810-音频接口)；
**文本补全** `/v1/completions` 只路由到开启了 `supportsCompletions` 的 `openai` 渠道，见 [8.11](#811-文本补全接口completions--fim)。

### 8.8 渠道归属与计费

同一个逻辑模型可能同时由用户自己的渠道、别人共享的渠道和平台渠道提供。网关按渠道**相对于调用者**的归属分三档（`docs/contracts/phase5-api.md` §1）：

| 档位 | 判定 | 计费 |
|---|---|---|
| `own` 自有 | 渠道所有者就是调用者（管理员自己的渠道对管理员本人也是 `own`） | **不计费**：不预留、不扣钱包、不计入套餐额度 |
| `shared` 共享 | 所有者是其他**普通用户**（角色不是 `system_admin` / `channel_admin`），共享给了调用者且调用者**已接受**（或由管理员共享给调用者所在的用户组） | **不计费**（上游费用由共享者自己承担） |
| `platform` 平台 | 所有者是 `system_admin` 或 `channel_admin`，作用域为 `global` 或共享给了调用者 | 按售价计费、计入套餐配额（[第 10 节](#10-计费模式)、[第 11 节](#11-套餐与周期配额)） |

- **路由顺序** own → shared → platform，每档内部仍按优先级 → 协议匹配度 → 权重。shared 档排在平台之前是因为被共享者**主动接受**了共享
  （见 [8.3](#83-可见性与共享)），可以随时退出；接受前界面会提示：共享者看不到被共享者的账户信息，但能看到（也可能修改）经由他的渠道发送的请求与响应。own / shared 档的每个可用渠道最多尝试一次
  （不受 `gateway.maxAttempts` 限制），失败后是否换下一个渠道按系统设置 `gateway.retryOn` 判断；一次请求总共最多尝试 10 次。
- **路由规则只作用于 platform 档**：`targets`、`strategy`、`protocolPreference`、`retry` 不影响用户自己的和共享的渠道；
  `fallbackModels` 在请求模型的所有档位都失败后才生效，回退模型同样按 own → shared → platform 选择。“路由规则”页的命中预览为每个候选标出档位。
- **计费检查延后**：套餐配额判断和钱包预留推迟到**第一次尝试平台渠道之前**。请求完全由 own / shared 渠道完成时，即使开启了
  `billing.enforce`、余额为 0 或套餐已用完也能成功；自己的渠道失败（例如上游 5xx）而回退到平台渠道时照常检查，余额不足返回 `402`，
  配额用完返回 `429`。
- **请求日志**的 `channelTier` 记录实际提供服务的渠道档位（失败的请求取最后一次尝试的渠道）。own / shared 请求的 `charge` 与 `quotaCharge`
  为 0，`cost` 为空（上游费用不属于平台）；统计中的费用汇总因此只包含平台渠道的请求。
- 档位按渠道所有者**当前**的角色判断：把普通用户提升为 `channel_admin` 后，他的渠道（对其他用户）立即变为平台渠道并开始计费；
  反之降级后变为 `shared`（他已发布的 `global` 渠道对所有人免费，请同时收回作用域）。修改角色后本实例立即重新加载渠道快照，其他实例最多 15 秒。
- `/v1/models` 不变，返回所有可用模型的并集。

**模型资料与模型广场**（§2–§3）：

- 持有 `models.manage` 的管理员可在“模型管理 → 模型资料”（`PUT /api/admin/model-info/{model}`）为逻辑模型补充显示名、简介、厂商、标签、
  上下文长度、最大输出、能力标记与排序；`hidden` 的模型不在任何广场中显示，但仍可调用。没有资料的模型照常可用，广场中只显示模型名。
- **平台模型广场**（落地页 `/models`、控制台“模型广场”，`GET /api/plaza/models`）只列出由作用域为 `global` 的平台渠道提供、且未隐藏的模型，
  附当前售价（含 `perImage` / `imageInputPerM` 与音频价格 `audioInputPerM` / `audioOutputPerM` / `perMinute` / `perMCharacters`，未设置时为 `null`）、覆盖该模型的有效套餐与可用的客户端协议；不显示渠道名称、渠道数量与成本价。
  可用协议中的 `openai.images` 只在有 openai 渠道提供该模型、**且**模型资料的能力标记中勾选了 `imageGeneration` 时显示（见 [8.9](#89-图片接口)）；
  `openai.audio` 同理，需要勾选 `audioInput`（语音识别）或 `audioOutput`（语音合成）（见 [8.10](#810-音频接口)）。系统设置 `site.publicModelPlaza`（默认开启）
  控制是否允许未登录访问，关闭后匿名请求返回 `401`。
- **我的模型**（`GET /api/plaza/mine`）列出当前用户能调用的全部模型（平台 + 自有 + 共享，含隐藏的模型），带各档可用渠道数、
  是否免费（有 own / shared 渠道即为 `free`，回退到平台时按售价计费）与覆盖该模型的订阅。

### 8.9 图片接口

OpenAI 兼容的图片接口（Round 6 续，契约见 `docs/contracts/phase7-api.md` §1），使用 Gateway Key 认证，与 `/v1/embeddings` 相同：

| 接口 | 请求体 | 上游路径（相对渠道 `baseUrl`） | 请求日志 `inbound` |
|---|---|---|---|
| `POST /v1/images/generations` | JSON | `/images/generations` | `openai.images.generations` |
| `POST /v1/images/edits` | `multipart/form-data` | `/images/edits` | `openai.images.edits` |
| `POST /v1/images/variations` | `multipart/form-data` | `/images/variations` | `openai.images.variations` |

每个接口也接受 JSON 请求体（`Content-Type` 不是 multipart 时按 JSON 处理并原样转发）。

**路由**：只路由到 `openai` 类型渠道，不做协议转换（Anthropic 渠道不提供图片接口）。逻辑模型没有 openai 渠道提供时返回 `404 model_not_found`
（消息注明图片接口只由 OpenAI 兼容渠道提供）。图片模型按普通模型映射配置在 openai 渠道上，`baseUrl` 同样含 `/v1`。渠道归属分档
（own → shared → platform）、重试与回退、熔断、Key 策略（可用模型、可用渠道、IP 白名单、RPM）、路由规则（含 `fallbackModels`）、套餐配额与计费都与其他接口相同。

**改写与转发**：网关只改写 `model`（改为渠道的上游模型名），其余内容原样转发。

- JSON：只替换 `model` 字段。
- multipart：**流式重新编码**——每个部分（头与原始字节，不做传输解码）原样复制，只替换 `model` 文本字段的值；使用新的随机 boundary，
  并发送精确的 `Content-Length`。解析时 `model`、`prompt`、`n`、`stream` 文本字段各自最多 1 MiB；缺少 boundary 或格式错误返回
  `400 invalid_request`，缺少 `model` 返回 400（“缺少 model 字段”）。

**请求体上限 64 MiB**（其他 `/v1` 接口仍为 32 MiB），超出时在联系任何上游之前返回 `413`（OpenAI 错误格式，`code: invalid_request`，消息
`request body exceeds 67108864 bytes`）。反向代理的 `client_max_body_size` 需相应调大（见[第 7 节](#7-反向代理)）。非流式响应同样最多 64 MiB。

**临时文件**：为了在重试（包括回退到其他渠道或路由规则的回退模型）时能重新发送，multipart 请求体在解析时**缓存一次**——不超过 8 MiB 的部分
保存在内存中，超过后整个请求体转存到临时目录（Go 的 `os.TempDir()`，即 `$TMPDIR`，未设置时为 `/tmp`）中的私有文件 `omnigate-image-*.part`，
请求结束时（成功、失败或客户端断开）删除。每次尝试都从缓存流式生成新的编码，不会把整个文件读进内存。运维注意：

- 临时目录必须**可写且空间充足**：最坏情况下占用约“并发的大图片请求数 × 64 MiB”。
- Docker 镜像（distroless，nonroot）中临时目录默认为 `/tmp`。以**只读根文件系统**运行容器（`read_only: true` 等）时，需要为 `/tmp` 挂载一个可写卷
  （如 tmpfs，注意 tmpfs 占用内存）。
- 临时文件无法创建或写入时请求返回 `500 internal error`，服务日志中有 `multipart request spool failed`。

**流式**：`stream: true`（`gpt-image-*` 支持）按 SSE 逐字节透传（`image_generation.partial_image` / `image_generation.completed` /
`image_edit.partial_image` / `image_edit.completed`）。上游对流式请求返回 JSON 时（例如不支持流式的 `dall-e-*`），按普通 JSON 响应返回。
反向代理同样需要对 `/v1` 关闭缓冲。

**用量**：非流式响应的图片张数 = `data` 数组长度，流式 = `*.completed` 事件数；token 用量取上游的 `usage`（`input_tokens`、`output_tokens`、
`input_tokens_details.image_tokens`，流式取完成事件中的 `usage`，多个事件带用量时每项取最大值）。上游没有返回 `usage` 时（如 `dall-e-3`）
token 记为 0、**不做估算**，只按 `perImage` 与 `perRequest` 计费。请求日志新增 `imageCount`（输出图片张数，非图片请求为 0）与
`usage.imageInputTokens`（`input` 是全部输入 token，其中图片部分为 `imageInputTokens`）。计价见 [10.1](#101-价格)。

**插件 Hook**：JSON 图片请求照常执行 `transformRequest` 与 `signRequest`（dialect 为上表的 `inbound` 名称）；multipart 请求**只执行
`signRequest`**，Hook 收到的 `body` 是 `{}`，可以修改路径与请求头，返回的请求体被忽略。

**模型广场**：图片模型不会自动出现图片协议。请在“模型管理 → 模型资料”（`PUT /api/admin/model-info/{model}`）中勾选能力 `imageGeneration`，
模型由 openai 渠道提供时，广场的可用协议才会显示 `openai.images`。`/v1/models` 不变。

### 8.10 音频接口

OpenAI 兼容的音频接口（Round 6 续，契约见 `docs/contracts/phase9-api.md` §1），使用 Gateway Key 认证：

| 接口 | 请求体 | 上游路径（相对渠道 `baseUrl`） | 请求日志 `inbound` |
|---|---|---|---|
| `POST /v1/audio/transcriptions` | `multipart/form-data`（`file`、`model`、`language`、`prompt`、`response_format`、`temperature`、`timestamp_granularities[]`、`stream`、`include[]`、`chunking_strategy`） | `/audio/transcriptions` | `openai.audio.transcriptions` |
| `POST /v1/audio/translations` | `multipart/form-data`（同上的子集） | `/audio/translations` | `openai.audio.translations` |
| `POST /v1/audio/speech` | JSON（`model`、`input`、`voice`、`instructions`、`response_format`、`speed`、`stream_format`） | `/audio/speech` | `openai.audio.speech` |

**路由**：与图片接口相同，只路由到 `openai` 类型渠道、不做协议转换；只由 anthropic 渠道提供的模型返回 `404 model_not_found`
（消息注明音频接口只由 OpenAI 兼容渠道提供）。渠道归属分档、重试与回退、熔断、Key 策略、路由规则、用户组倍率与限额、分时价格、套餐与计费都与其他接口相同。
音频模型（如 `whisper-1`、`gpt-4o-transcribe`、`tts-1`、`gpt-4o-mini-tts`）按普通模型映射配置在 openai 渠道上。

**上传**：转写 / 翻译只接受 `multipart/form-data`（JSON 请求体返回 `400 invalid_request`），处理方式与图片编辑完全相同：只替换 `model`，
其余部分（含音频文件）原样转发；请求体上限 **64 MiB**；请求体缓存一次以便重试，超过 8 MiB 时写入临时文件 `omnigate-audio-*.part`
（与 `omnigate-image-*.part` 同一目录，运维注意事项见 [8.9](#89-图片接口)“临时文件”）。语音合成是普通 JSON 请求（上限 32 MiB）。

**响应**：

- 转写 / 翻译：`json`、`text`、`srt`、`verbose_json`、`vtt` 响应原样返回，`Content-Type` 透传；`stream: true` 时按 SSE 透传
  `transcript.text.delta` / `transcript.text.done`（上游对流式请求返回普通响应时按普通响应返回）。
- 语音合成：二进制音频**边收边发**——每收到一段上游数据立即写给客户端并 flush，网关不缓冲整段音频，`Content-Type`（如 `audio/mpeg`、`audio/wav`）透传；
  `stream_format: "sse"` 时按 SSE 透传 `speech.audio.delta` / `speech.audio.done`。
- 第一个字节写给客户端之前（上游错误、连接失败、空响应）可以按重试策略换渠道；之后不再重试。二进制音频传输中途上游断开时，请求日志记为
  `statusCode` 200、`errorClass = upstream_invalid_response`，网关随后**中断与客户端的连接**，客户端能发现音频不完整（不会把截断的文件当成完整的）。

**用量与计费**（依次尝试，第一个可用的为准）：

1. 响应的 `usage`：`{"type": "tokens", "input_tokens", "output_tokens", "input_token_details": {"audio_tokens", "text_tokens"}}` 按 token 计费
   （`audio_tokens` 记为音频输入 token）；`{"type": "duration", "seconds"}` 按 `perMinute` 计费。
2. `response_format=verbose_json` 响应的 `duration` → 按 `perMinute` 计费。
3. 语音合成：SSE `speech.audio.done` 中的 `usage` 按 token 计费（语音合成的输出 token 都是音频输出 token）；否则按 `input` 的 **Unicode 字符数**
   （码点数，一个汉字或一个 emoji 都算 1 个字符）× `perMCharacters` 计费。
4. 都没有时（如 `text` / `srt` / `vtt` 响应不带用量）只收 `perRequest`，请求日志的 `usageEstimated` 为 `true`。需要按时长计费的转写模型，
   请让客户端使用 `json` / `verbose_json`，或设置合适的 `perRequest`。

音频时长按秒计费，不足一秒按一秒（8.47 秒记 9 秒）。计价字段见 [10.1](#101-价格)。请求日志新增 `audioSeconds`（计费秒数）、`usageEstimated`、
`usage.audioInputTokens`、`usage.audioOutputTokens` 与 `usage.inputCharacters`（按字符计费时的字符数）；套餐规则可以使用计量 `audio_seconds`
（见[第 11 节](#11-套餐与周期配额)）。强制计费下的预留额：语音合成 = `perRequest` + 字符数 × `perMCharacters`；转写 / 翻译 = `perRequest`
（时长要等上游返回后才知道），结算时按实际用量多退少补。

**插件 Hook**：语音合成（JSON）照常执行 `transformRequest` 与 `signRequest`；转写 / 翻译（multipart）只执行 `signRequest`，规则同图片接口。

**模型广场**：在模型资料中勾选能力 `audioInput`（语音识别）或 `audioOutput`（语音合成），模型由 openai 渠道提供时，广场的可用协议才会显示 `openai.audio`。

### 8.11 文本补全接口（Completions / FIM）

OpenAI 旧版文本补全 `POST /v1/completions`（Round 14，契约见 `docs/contracts/phase14-api.md`），编辑器代码补全插件常用，`suffix` 字段用于 FIM：

- **只直通、从不转换**：只路由到 `type = openai` 且 `config.supportsCompletions = true` 的渠道，转发到 `{baseUrl}/completions`，
  只改写 `model`（`prompt` 的字符串 / 数组 / token 数组、`suffix`、`echo`、`best_of`、`logit_bias` 及未知字段原样转发）。
  没有这样的渠道时返回 `404 model_not_found`（消息注明需要开启了 Completions 的 OpenAI 兼容渠道）。`anthropic` 与自定义协议渠道不提供该接口。
- **流式**：与 Chat 相同，网关强制 `stream_options.include_usage = true` 以便计量，客户端没有要求时过滤掉只含用量的分片。
- **计费**：用量取上游 `usage`；`prompt_tokens_details.cached_tokens` 或 DeepSeek 的 `prompt_cache_hit_tokens` 计为缓存读取（按 `cacheReadPerM`），
  其余提示词 token 为输入。价格、阶梯、分时倍率、套餐、限额、路由规则、Key 策略与请求日志（`inbound = openai.completions`）都与 Chat 相同。
- **会话亲和**：规则可用 `path_regex: ["^/v1/completions"]` 加请求头或请求体字段作为会话值；`anchor` 来源对文本补全不生效
  （FIM 的提示词每次按键都变），`inject_prompt_cache_key` / `inject_session_header` 也不作用于文本补全。
- **模型广场**：在模型资料中勾选能力「文本补全 / FIM」（`completions`），且有开启 `supportsCompletions` 的渠道提供该模型时，
  广场显示 `Completions` 协议标签与 FIM 调用示例。

**DeepSeek FIM**（[官方文档](https://api-docs.deepseek.com/guides/fim_completion)）：新建 `openai` 渠道，Base URL 填
`https://api.deepseek.com/beta`，模型如 `deepseek-flash`、`deepseek-v4-pro`，在“高级”中打开「支持 Completions」。FIM 走非思考模式，
输出上限 4K token。Continue 等插件把 OmniGate 的 `/v1` 地址配置为 OpenAI 兼容的补全端点即可。

## 9. 插件

插件为渠道补充协议之外的能力（余额、模型发现、健康检查等）与发送前的请求改写（Hook）。插件代码是 TypeScript / JavaScript，运行在网关进程内的
Goja 沙盒中（ADR-0002），界面通过声明式 UI 贡献点渲染（ADR-0003）。设计契约见 `docs/contracts/phase2-api.md`，HTTP 接口见
`docs/contracts/openapi.yaml` 的 `plugins` 标签与 `/api/channels/{id}/capabilities`。

### 9.1 插件来源

| `source` | 来源 | 说明 |
|---|---|---|
| `builtin` | 内置 | `builtin.openai`、`builtin.anthropic`：用 Go 原生实现 OpenAI / Anthropic 协议，没有 JS 代码、能力或 Hook，不能编辑或导出。新建渠道时不指定插件版本，就按 `type` 绑定对应的内置插件。 |
| `bundled` | 随二进制分发 | 示例 `community.deepseek`（见 [9.7](#97-示例插件deepseek)）与计费示例 `community.billing-examples`（见 [9.10](#910-计费插件)）。**每次启动时自动安装 / 更新**：二进制中的版本号在数据库中还不存在时，新增该版本并**自动批准**（审批备注“随 OmniGate 分发，自动批准”）。**已有渠道不会被自动升级**，升级 OmniGate 后需要在渠道上手动切换到新版本。如果同 ID 的插件已经由用户导入 / 新建（`source` 不同），启动时不会改动它。随附插件也可以导入 / 编辑草稿并发布新版本，照常走审批。 |
| `upload` | ZIP 导入 | `POST /api/plugins/import` 上传的插件包（≤ 4 MiB；解压后单文件 ≤ 256 KiB、总计 ≤ 1 MiB、最多 64 个文件）。导入同 ID 的已有插件时会**直接覆盖其草稿**。 |
| `editor` | 在线新建 | 控制台在线编辑器从模板新建：`openai-compatible`、`anthropic-compatible`（带模型列表、健康检查能力与测试用例的骨架）、`custom-protocol`（“自定义协议”，以虚构的 JSON Lines 流式协议为例实现全部协议 Hook，附测试用例，见 [9.9](#99-自定义协议插件)）或 `blank`。 |

- Phase 2 之前创建的渠道没有绑定插件（`plugin` 为 null），继续按 `type` 使用内置协议；对这类渠道做任意一次修改时，会自动绑定对应的内置插件。
- 插件 ID（manifest 的 `id`）形如 `vendor.name`，全局唯一，`builtin.` 前缀保留。

### 9.2 权限：谁能做什么

| 操作 | 所需权限 | 默认拥有者 |
|---|---|---|
| 查看插件、版本详情（含源码）、导出 ZIP | `plugins.read` | 所有角色 |
| 新建、导入、编辑草稿、编译检查、模拟测试、发布、启用 / 停用插件 | `plugins.manage` | `system_admin`、`channel_admin` |
| 审批 / 拒绝待审批版本 | `plugins.trust` | 仅 `system_admin` |
| 为渠道选择插件版本、填写插件配置与敏感字段 | 渠道所有者（`channels.write`）或 `channels.manage` | 所有能创建渠道的用户 |
| 查看渠道能力结果、手动执行能力 | 渠道所有者或 `channels.manage`（仅能使用渠道的人看不到） | — |
| 读取 SDK 类型声明（`/api/plugins/sdk.d.ts`） | 已登录 | 所有角色 |

普通用户不能上传代码，只能在**已批准**的版本中选择。插件代码在网关进程内执行且没有硬性内存上限，`plugins.manage` 实际上就是
“在网关进程中运行代码”的权限，只应授予受信任的管理员；`plugins.trust` 负责把关新权限（可访问的主机、可读取的密钥等）。

### 9.3 发布与审批

1. **草稿**：每个插件一份可编辑草稿（多文件），保存使用乐观锁。
2. **编译检查**：manifest v1 校验、esbuild 编译（ES2017，只允许相对导入与 `@omnigate/plugin-sdk`）、检查声明的能力与 Hook 是否都已实现、
   静态风险扫描（`eval`、原型链修改、死循环、混淆、未声明的主机等；**只供审批参考**，不阻止发布）。
3. **模拟测试**：运行 `tests/*.json` 或临时用例，`og.fetch` 只命中用例中的预置响应，绝不访问真实网络。
4. **发布**：编译检查通过后生成**不可变**版本，版本号必须大于该插件已发布的最大版本；草稿随之删除。
5. **审批**：新版本的权限（`network`、`secrets`、`schedule`、`storage`、`dangerous`）与该插件**最新已批准版本**完全相同时自动批准；
   否则进入 `pending`，由持有 `plugins.trust` 的管理员在版本详情中查看 `permissionDiff`、风险扫描与源码后批准或拒绝。
   插件的第一个版本总是需要审批；插件**第一个 `protocol: "custom"` 的版本**也总是需要审批（即使权限没有变化）。被拒绝的版本不能再批准，修正后发布新版本即可。

只有已批准的版本可以被渠道选用。发布、审批、拒绝都会写审计（`plugin.publish`、`plugin.approve`、`plugin.reject`）。

### 9.4 渠道固定版本、升级与回滚

- 每个渠道固定到一个插件版本（渠道的 `pluginVersionId` / `plugin.versionId`）。发布或批准新版本、随附插件随 OmniGate 升级，都**不会**移动任何渠道。
- **升级**：在渠道上选择同一插件更新的已批准版本。若新版本导出 `migrateConfig(oldConfig, fromVersion)`，先用它迁移现有配置
  （2 秒超时，失败则拒绝切换），再按新版本的 `configSchema` 校验并补默认值；新版本不再声明的敏感字段会被删除。写审计 `channel.plugin_upgrade`（含 from / to 版本）。
- **回滚**：同样的操作，选择旧版本即可（同样会调用目标版本的 `migrateConfig`，如有）。
- 只能在**同一插件**的版本之间切换；换用其他插件需要新建渠道。渠道 `type` 始终由插件的 `extends`（或 `protocol: "custom"` → `custom`）决定；继承协议与自定义协议的版本之间切换时，渠道类型随之改变。
- 插件配置（`pluginConfig`）提交时整体替换；`x-secret` 敏感字段通过 `secrets` 写入，和 API Key 一样用主密钥加密，界面只显示是否已设置与末 4 位。

### 9.5 停用与自动停用

- **停用插件**（`plugins.manage`）后：固定到该插件任一版本的渠道**立即不再参与路由**（请求改走其他渠道，没有其他渠道时客户端收到
  `404 model_not_found`）；手动与定时能力执行停止；新渠道不能绑定、已有渠道不能切换到它的版本。渠道配置保持不变，重新启用即恢复。
- **自动停用**：同一插件 **5 分钟内 3 次资源违规**——能力或 Hook 执行超时（包括 `og.fetch` 等待慢上游拖垮整次执行），或被内存看门狗中断——
  时系统自动停用该插件，写审计 `plugin.auto_disabled`（无操作者），服务日志中有 `plugin resource violation` 警告。违规计数在各进程内存中，
  重启后清零。自动停用后需要管理员排查原因并手动重新启用。
- 当前实现的限制：插件启停状态缓存在各进程内存中，只在本实例修改或自动停用时刷新；**多实例部署时其他实例要重启后才会感知**。
  内置插件不能停用（返回 `409 plugin_builtin`）；需要停止某个渠道时请停用该渠道。插件启停状态随渠道快照每 15 秒刷新，多实例部署时各实例在 15 秒内同步。

### 9.6 资源限制

| 项目 | 限制 |
|---|---|
| 能力执行超时 | 默认 10 s，manifest 中 `timeout` 可调，最大 60 s |
| 请求 Hook 超时 | 每个 Hook 50 ms（同时声明 `transformRequest` 与 `signRequest` 时共 100 ms）；失败按可回退的上游失败处理，返回 `502 plugin_error` 并计入该渠道的熔断 |
| 自定义协议 Hook | `buildRequest`、`signRequest`、`normalizeError`、每次 `parseStream` / `endStream` 50 ms；`parseResponse` 50 ms + 每 64 KiB 响应体 10 ms（最多 1 s）；**同一请求所有 Hook 合计 5 s**。见 [9.9](#99-自定义协议插件) |
| 计费计量 `computeUnits` | 每次 5 ms；失败按 0 计（见 [9.10](#910-计费插件)） |
| `migrateConfig` / 模块顶层代码 | 2 s |
| 并发 | 每个插件版本同时最多 8 个执行；超出的调用最多排队 1 秒（报“插件并发执行已达上限”，不计违规），排队时间不计入调用超时。自定义协议的流式请求固定使用一个运行时，但只在每次 Hook 调用期间占用并发名额 |
| 输出 | 能力调用的返回值序列化后 ≤ 256 KiB；Hook 的返回值包含整个请求体，上限按请求体放宽为约 2 倍请求体 + 1 MiB（最大 80 MiB） |
| `og.fetch` | 每次执行最多 10 次；只允许 https（渠道允许访问内网时也允许 http，见 [8.6](#86-网络安全ssrf)）；主机必须在 `permissions.network` 中；**不跟随重定向**（3xx 原样返回给插件）；响应 ≤ 4 MiB；默认超时 10 s（`timeoutMs` 最大 30 s，且不超过整次执行的剩余时间）。请求经过与渠道相同的防 SSRF 客户端与 `OMNIGATE_UPSTREAM_PROXY` |
| 内存 | 没有单插件硬上限；由 `OMNIGATE_PLUGIN_HEAP_LIMIT_MB` 看门狗兜底（见 [1.2](#12-变量一览)） |
| `og.storage` | 按（插件，渠道）隔离，配额在 manifest 中声明（`maxKeys` ≤ 1000，`maxBytes` ≤ 1 MiB） |
| 定时执行 | 调度器每 30 秒扫描一次已启用的渠道，最多 4 个并发；能力结果早于 `schedule.minInterval`（至少 1 分钟）时重新执行。每个实例都运行调度器 |

沙盒中没有 `require`、`process`、文件系统和定时器（`setTimeout`）；密钥只以句柄形式交给插件，由宿主在 `og.fetch` / Hook 返回的请求头中替换为明文，
日志中的句柄与疑似密钥会被脱敏。

### 9.7 示例插件：DeepSeek

`community.deepseek`（随二进制分发，源码在 `server/internal/plugin/bundled/community.deepseek`）演示了“继承 OpenAI 兼容协议 + 账户能力”的典型写法：

- `extends: openai.chat`，没有 Hook，请求热路径上不执行任何 JS；新建渠道时预填 `baseUrl` `https://api.deepseek.com/v1` 与
  `deepseek-chat`、`deepseek-reasoner` 两个模型映射，填写 API Key 即可使用。
- 权限：`network` 为 `api.deepseek.com` 与 `$baseUrl`，`secrets` 为 `apiKey`，`schedule` 为 `balance.get`。
- 配置：`currency`（`CNY` / `USD`，默认 `CNY`，账户有多个币种余额时显示哪一个）；`lowBalance`（默认 10，余额低于该值时执行结果中带一条 warn 日志）。
- 能力：
  - `balance.get`：调用 `GET /user/balance`（去掉 `baseUrl` 末尾的 `/v1`），每 10 分钟定时执行一次，也可手动刷新；结果显示在渠道详情概览
    （总余额、赠送余额、充值余额、是否可调用）和渠道列表徽标（总余额）中。上游 401 / 402 会给出“API Key 无效”/“余额不足”的明确错误。
  - `models.list`：`GET /models`，在能力页以表格显示上游模型。
  - `health.check`：同样请求 `/models` 并返回延迟。
  - `usage.query`：DeepSeek 没有公开的用量查询 API，插件返回 `{unsupported: true}`，界面显示为“不支持”而不是错误。

### 9.8 备份与运维注意

- 插件、全部版本（含源码文件、编译产物、manifest、风险扫描与审批记录）、草稿、插件存储（`og.storage`）以及能力的最新结果都保存在 PostgreSQL
  （`plugins`、`plugin_versions`、`plugin_drafts`、`plugin_storage`、`capability_results` 表）中，随第 13 节的 `pg_dump` 一起备份，没有额外文件需要备份。
- 渠道的插件敏感字段与 API Key 一样用主密钥加密保存，恢复时同样需要配套的主密钥。
- 内置与随附插件在每次启动时由二进制补装：升级 OmniGate 后随附插件的新版本会自动出现并已批准，但渠道仍固定在原版本，需要按 [9.4](#94-渠道固定版本升级与回滚) 逐个切换。

### 9.9 自定义协议插件

上游既不兼容 OpenAI 也不兼容 Anthropic（例如厂商私有协议）时，可以写一个 `protocol: "custom"` 的插件实现完整协议（契约 `docs/contracts/phase9-api.md` §2，
SDK 说明 `docs/contracts/plugin-sdk.md` §5.1）：

- manifest 写 `"protocol": "custom"`（不写 `extends`），`hooks` 必须包含 `buildRequest`、`parseResponse`、`parseStream`，可选 `endStream`、`normalizeError`、`signRequest`。
- 用这类插件建的渠道类型为 `custom`，服务 `/v1/chat/completions`、`/v1/messages`、`/v1/responses`：网关把客户端请求统一转换为 Chat Completions
  交给 `buildRequest`，再把 `parseResponse` 的结果或 `parseStream` 的事件转换回客户端协议（流式请求的 `usage` 事件计入用量与计费）。
  不服务 embeddings、图片与音频接口；模型广场只为这类模型显示上述三种协议。
- `buildRequest` 返回 `{method, url, headers, body}`：`url` 以 `/` 开头时拼在渠道 `baseUrl` 之后；绝对地址必须是 https 且主机为 `baseUrl` 主机或
  在 `permissions.network` 中。网关**不加认证头**，用 `og.secret("apiKey")` 句柄写在请求头中（发送时替换为明文）。
- 一个请求的所有 Hook 在同一个运行时中执行，`parseStream` 的 `state` 在整个流期间保留；沙盒提供 `TextDecoder` 解码字节块。
- 超时与错误：见 [9.6](#96-资源限制)。插件抛错、超时（含合计超过 5 s）或返回格式错误时为 `502 plugin_error`：还没有向客户端写出任何内容时换渠道重试
  （与上游 5xx 相同，计入熔断），已写出后以流内错误事件结束。超时计为资源违规。
- 上游错误响应（非 2xx）由 `normalizeError` 映射为 `{status, message}`，按 `status` 决定重试分类（429 / 5xx 重试，401 / 403 为上游认证失败并触发渠道认证告警）；
  没有实现时按 HTTP 状态码分类。
- 渠道“测试连接”执行插件的 `health.check` 能力（没有时 `models.list`），“发现模型”执行 `models.list`；两者都没有时这两个操作返回 `409 capability_not_found`。
- 在线编辑器的“测试”面板支持 `buildRequest`、`parseResponse`、`normalizeError` 与流式用例（`hook: "parseStream"` + `chunks` 一组上游字节块，断言输出事件）。

### 9.10 计费插件

计费插件（manifest `kind` 包含 `"billing"`）为套餐规则提供自定义计量（契约 phase9-api.md §3，SDK 说明 plugin-sdk.md §5.2）：

- 计量在 manifest 的 `billing.meters` 中声明（名称、`label`、`unit`），代码实现 `billing.meters.<名称>.computeUnits(usage, ctx)`，返回非负数或十进制字符串。
- 套餐规则的 `meter` 写 `custom:<插件 ID>.<计量名>`，例如 `custom:community.billing-examples.weighted_tokens`。保存规则时校验插件已启用、
  有已批准版本声明该计量，并把该版本固定到规则中（规则的 `pluginVersionId`）；开通的订阅快照随之固定，之后升级或停用插件不改变已开通订阅的规则。
  可选计量列表：`GET /api/admin/billing/meters`。自定义计量的上限可以是小数，`modelWeights` 同样适用。
- 结算时（套餐覆盖的请求，在结算事务之前）调用 `computeUnits`：纯函数，没有 `og.fetch`、`og.storage`、`og.secret`，超时 5 ms。
  超时、抛错、返回值不合法，或插件已停用 / 版本不存在时，该次计量按 **0** 记，服务日志写 `billing plugin meter counted as 0` 警告，
  指标 `omnigate_billing_plugin_errors_total{plugin, meter, reason="timeout|exception|invalid|unavailable"}` 加 1。建议对该指标设置告警。
- `ctx`（BillingCtx）：`model`、`servedModel`、`channelId`、`channelTier`、`userGroup`（组名）、`inbound`、`imageCount`、`audioSeconds`。
- 随附示例 `community.billing-examples`：`weighted_tokens`（输出 token × 4 + 输入 token，输入含缓存读写）、`per_image`（图片张数）。

## 10. 计费模式

### 10.1 价格

- 价格分两种：**售价**（`sell`）按逻辑模型设置，决定向用户收取的费用；**成本价**（`cost`）按“渠道 + 上游模型”设置，只用于统计上游成本（仅持有 `stats.all` 的用户可见）。
- 每种价格包含每 100 万输入 / 输出 / 缓存读 / 缓存写 token 的单价，以及每次请求的固定费用，金额为结算币种的十进制数（最多 9 位小数，见第 4 节）。
- **价格有版本且不可修改、不可删除**：改价就是新增一个版本，可以指定将来的生效时间（`effectiveAt`，不能早于当前时间）。计价时取“生效时间 ≤ 请求开始时间”的最新版本，请求日志记录所用价格的 ID，因此改价不会影响历史账单。
- 由持有 `models.manage` 的管理员通过 `POST /api/admin/prices`（或控制台中的价格管理入口）设置。**没有售价的模型免费**：不预留、不扣费，即使开启了强制计费也不受余额限制。
- 用户可在控制台“模型”页面（`GET /api/models`）看到自己可用的模型及当前售价。
- 价格版本可以带**分时倍率**（`schedule`，如夜间半价），用户所在的**用户组**可以给平台渠道的售价打折或加价（`priceMultiplier`），
  见[第 17 节](#17-用户组价格倍率限额与分时价格)。

**图片计价**（售价与成本价都适用，见 [8.9](#89-图片接口)）：

| 字段 | 含义 | 未设置时 |
|---|---|---|
| `perImage` | 每张**输出**图片的价格 | 省略、`null` 或 `""` 等于 0（响应中为 `"0"`） |
| `imageInputPerM` | 每 100 万**图片输入** token 的单价 | 省略、`null` 或 `""` 表示按 `inputPerM` 计（响应中为 `null`） |

两者都是非负十进制数，最多 9 位小数（否则 `422`，details 为 `perImage` / `imageInputPerM`）。费用公式（成本价相同）：

```text
费用 = perRequest
     + (input − imageInputTokens) × inputPerM
     + imageInputTokens × (imageInputPerM ?? inputPerM)
     + output × outputPerM + 缓存读 / 缓存写（同前）
     + 图片张数 × perImage
```

- 上游没有返回 `usage` 时（如 `dall-e-3`）token 记为 0，只收 `perRequest` 与 `图片张数 × perImage`；因此这类模型应设置 `perImage`，否则免费。
- 强制计费下图片请求的预留额 = `perRequest` + `n` × `perImage`（`n` 缺省为 1，按 1–100 截断）+ 估算的文本输入（prompt 字节数 / 4 × `inputPerM`），
  与其他请求一样按可用余额封顶，只有可用余额 ≤ 0 时返回 `402 insufficient_balance`。
- 请求日志记录 `imageCount` 与 `usage.imageInputTokens`；由自己的或共享的渠道完成的图片请求同样不计费、不计入套餐配额。

**音频计价**（售价与成本价都适用，见 [8.10](#810-音频接口)）：

| 字段 | 含义 | 未设置时 |
|---|---|---|
| `audioInputPerM` | 每 100 万**音频输入** token 的单价 | 省略、`null` 或 `""` 表示按 `inputPerM` 计（响应中为 `null`） |
| `audioOutputPerM` | 每 100 万**音频输出** token 的单价 | 省略、`null` 或 `""` 表示按 `outputPerM` 计（响应中为 `null`） |
| `perMinute` | 每分钟输入音频的价格（转写 / 翻译，按秒折算） | 省略、`null` 或 `""` 等于 0 |
| `perMCharacters` | 每 100 万语音合成输入字符的价格 | 省略、`null` 或 `""` 等于 0 |

```text
费用 = perRequest
     + (input − imageInputTokens − audioInputTokens) × inputPerM + audioInputTokens × (audioInputPerM ?? inputPerM)
     + (output − audioOutputTokens) × outputPerM + audioOutputTokens × (audioOutputPerM ?? outputPerM)
     + 缓存读 / 缓存写 + 图片部分（同上）
     + audioSeconds × perMinute / 60
     + 字符数 × perMCharacters / 1,000,000
```

结果再乘以分时倍率与用户组倍率（成本价不乘用户组倍率）。例：`whisper-1` 设置 `perMinute: "0.006"`；`gpt-4o-transcribe` 设置
`inputPerM`（文本提示）、`audioInputPerM`、`outputPerM`；`tts-1` 设置 `perMCharacters: "15"`；`gpt-4o-mini-tts` 设置 `inputPerM` 与
`audioOutputPerM`（SSE 时按 token 计费）并同时设置 `perMCharacters`（二进制响应没有用量，按字符计费）。

### 10.2 `billing.enforce` 开关

计费模式由系统设置 `billing.enforce` 控制（“系统设置”页面 / `PATCH /api/admin/settings`，需要 `settings.write`；旧接口 `GET` / `PUT /api/admin/billing/settings` 保留为别名，需要 `billing.manage`，`PUT` 需带系统设置的整体 `version`），默认关闭。修改后本实例立即生效，其他实例在 5 秒内生效。

| | `enforce = false`（默认，适合个人 / 团队自用） | `enforce = true`（预付费） |
|---|---|---|
| 请求前 | 不检查余额 | 由钱包支付的请求（平台渠道、有售价、没有套餐覆盖）：可用余额（余额 − 预留）≤ 0 时拒绝，返回 `402 insufficient_balance`（OpenAI 格式 `type=insufficient_quota`，Anthropic 格式 `type=permission_error`）。**这一检查与估算额无关**：估算为 0 的请求（如只按分钟计价的转写、只设了输出单价且请求没有 `max_tokens`）同样需要可用余额 > 0。放行后**预留** `min(估算费用, 可用余额)`（请求体约 4 字节 / token 估算输入 + 请求中的 `max_tokens`，未提供 `max_tokens` 时不计输出），估算超过余额**不会**被拒绝 |
| 请求后 | 只在请求日志中记录本应收取的费用（`charge`），**不写账本、不改余额** | 释放预留，按实际用量扣费并写一条 `charge` 账本记录；实际费用超过预留时余额可以变为负数（最多透支进行中的请求的费用，之后可用余额 ≤ 0 会阻止新请求） |
| 失败的请求 | 不收费 | 不收费（含失败的回退尝试）；客户端中途断开但上游已产生用量的流式请求按已产生的用量收费 |

- **输出上限截断**：估算时 `max_tokens` / `max_completion_tokens` / `max_output_tokens` 最多按 **1,000,000**（`protocol.MaxTokensLimit`）计，
  更大的值按 1,000,000 估算（发往上游的请求体不变）。费用计算全程做溢出检查：仍然无法计价的请求（例如价格极高导致金额超出可表示范围）
  一律拒绝，返回 `400 invalid_request`，不论是否开启强制计费。
- 支出上限（用户组每日 / 每月、API Key）同样只看剩余额度：已用满即拒绝（`spend_limit_exceeded`），与估算额无关；预留额也不超过剩余额度。
- 预留在请求结束时释放；进程异常退出导致未结算的预留会在 15 分钟后自动释放。
- **结算不会静默丢失**（[ADR-0010](../adr/0010-durable-settlement.md)）：结算（扣费或释放预留、用量计数、套餐额度）与请求日志写入失败时先在进程内
  退避重试（结算 3 次、日志 3 次）；仍失败（数据库不可用）时追加到 `OMNIGATE_DATA_DIR/settlement-journal.jsonl`（JSON Lines，每条 fsync），
  启动时和之后每 5 秒（失败时退避到最长 1 分钟）按顺序重放，直到写入数据库。重放是幂等的：扣费按请求 ID 去重（账本），用量计数按
  `settled_requests` 去重，套餐额度按 `subscription_charges` 去重，请求日志按主键 `(id, started_at)` 去重，同一条记录重放多次也只生效一次。
  预留过期被释放后，重放的结算照样按实际用量扣费（扣费不依赖预留）。
  - 有待重放的条目时 `/readyz` 返回 `200` 且 `"status":"degraded"`，`journal.pending` 为条数（正常时为 `"ready"`、`pending: 0`）；
    指标 `omnigate_journal_pending`（待重放条数）、`omnigate_journal_replayed_total{kind}`（已重放）、`omnigate_settlement_failures_total{kind}`
    （进程内重试后仍失败而写入 journal 的次数，`kind` 为 `settlement` 或 `request_logs`）、`omnigate_journal_dead_total{kind}`。
    `omnigate_request_log_dropped_total` 只统计内存缓冲区满（等待 2 秒后）丢弃的日志。
  - 文件末尾不完整的一行（写入中途崩溃）启动时忽略并记警告；中间无法解析的行移到 `settlement-journal.jsonl.corrupt`；
    数据库可用但同一条目连续失败 5 次时移到 `settlement-journal.jsonl.dead` 并记错误日志，需要人工处理（不会被丢弃）。
  - 重放的结算不再发送支出上限提醒通知。journal 只属于一个实例：迁移或恢复时连同数据目录一起保留，删除前确认 `pending` 为 0。
- 切换开关不会补扣或退还已经发生的请求费用。
- 关闭状态下，钱包只会因兑换码和管理员调整而变化。
- 用户持有覆盖该模型、且额度未用完的套餐订阅时，请求**不经过钱包**（不预留、不扣费，不受余额限制），见[第 11 节](#11-套餐与周期配额)。
- 只有**平台渠道**（管理员的渠道）提供服务的请求才计费；由用户自己的渠道或其他普通用户共享的渠道完成的请求不检查余额、不预留、不扣费，
  见 [8.8](#88-渠道归属与计费)。

### 10.3 发放余额：兑换码

本节是钱包充值码（`kind: wallet_credit`）；开通套餐的套餐码见 [11.5](#115-发放套餐兑换码)。

1. 管理员（`billing.manage`）在控制台“兑换码”页面（`/billing/redeem-codes`）新建批次，或调用 `POST /api/admin/billing/redeem-batches`：
   ```bash
   # 控制面使用会话 Cookie 认证，写操作必须带 X-Requested-With 头
   curl -sS https://gateway.example.com/api/admin/billing/redeem-batches \
     -H 'Cookie: og_session=<浏览器中的会话值>' \
     -H 'X-Requested-With: XMLHttpRequest' -H 'Content-Type: application/json' \
     -d '{"amount":"10","count":50,"perUserLimit":1,"expiresAt":"2026-12-31T16:00:00Z","note":"内测用户"}'
   ```
   参数：每个码的面额 `amount`（> 0）、数量 `count`（1–1000）、每个码可兑换次数 `maxRedemptionsPerCode`（默认 1）、同一用户在本批次最多兑换次数
   `perUserLimit`（默认 1）、可选的 `validFrom` / `expiresAt` / `note`。
2. 响应中的 `codes`（形如 `OG-7K3QM-X2D9F-H4TNB-1P8RW`）是**唯一一次**能看到明文的机会，请立即导出并通过安全渠道分发；服务端只保存摘要，之后无法找回。
3. 用户在控制台“钱包与订阅”页面输入兑换码（或 `POST /api/billing/redeem`），余额立即增加并写一条 `grant` 账本记录。输入不区分大小写，空格和连字符会被忽略。
   每个用户每分钟最多尝试 5 次。
4. 停用整个批次：`PATCH /api/admin/billing/redeem-batches/{id}`，`{"status":"disabled"}`；已兑换的余额不受影响。
5. 手工调整余额（补偿、退款、扣回）：`POST /api/admin/billing/wallets/{userId}/adjust`，`{"amount":"-2.5","note":"原因","version":<钱包当前版本>}`，必须填写备注，会写审计日志。

## 11. 套餐与周期配额

套餐（Plan）用一组**周期配额规则**描述“买了之后能用多少”，例如“每 5 小时 200 次请求、每周 500 万 token”（类似 Claude Pro / Max 的用量窗口）。
用户持有套餐的一份**订阅**（Subscription）期间，被套餐覆盖的模型优先按配额使用，不扣钱包余额。OmniGate 没有在线支付；订阅可以由管理员开通、
通过**套餐兑换码**获得，或由用户**用钱包余额购买 / 续费 / 补差价升级**（§11.6.2）。设计见 ADR-0006 与 `docs/contracts/billing-and-quota.md`，
接口契约见 `docs/contracts/phase3-api.md`、`docs/contracts/phase15-api.md` 与 `openapi.yaml`。

### 11.1 套餐、订阅与规则

| 对象 | 说明 |
|---|---|
| 套餐 | 名称、说明、售价 `listPrice`（余额购买与续费的价格；为空或 0 时不能用余额购买）、每份有效期 `duration`、覆盖的逻辑模型 `models`（空 = 全部模型）、1–10 条规则 `rules`、`stackable`、状态 `active` / `archived` |
| 订阅 | 用户持有的一份套餐：`startsAt` / `endsAt`、来源（`admin` / `redeem` / `purchase`）、开通时**快照**的 `models` 与 `rules`。之后修改套餐**不影响**已有订阅（续期也不更新快照）；要让老用户用上新规则，需要取消后重新开通 |
| 规则 | `id`（套餐内唯一，`^[a-z0-9_-]{1,32}$`）、`label`、计量 `meter`、窗口 `window`、上限 `limit`、可选的 `models` / `modelWeights`、超额行为 `onExceed` |

时长（`duration`、窗口的 `duration` / `every`）的格式是 `<正整数><单位>`，单位 `m`（分钟）、`h`、`d`（24 小时），如 `5h`、`7d`、`30d`；
与环境变量的 Go duration 语法不同，**不支持** `1h30m` 这类组合。套餐 `duration` 范围 1h–366d。

**计量**（`meter`）：

| 计量 | 每个请求计入的值 |
|---|---|
| `requests` | 1 |
| `tokens.input` | 输入 + 缓存读 + 缓存写 token |
| `tokens.output` | 输出 token（含推理 token） |
| `tokens.total` | 以上两者之和 |
| `images` | 输出图片张数（图片接口，见 [8.9](#89-图片接口)；其他请求为 0），例如“每天 50 张图” |
| `audio_seconds` | 计费的输入音频秒数（音频接口，见 [8.10](#810-音频接口)；不足一秒按一秒，其他请求与无时长的请求为 0），例如“每天 3600 秒转写” |
| `charge` | 按该模型**售价**计算的金额（结算币种）。请求由订阅覆盖、不扣钱包时也照常计算，适合“每月 50 元额度”这类套餐；模型没有售价时为 0 |
| `custom:<插件 ID>.<计量名>` | 计费插件的 `computeUnits` 结果（见 [9.10](#910-计费插件)），失败时为 0 |

`requests` / `tokens.*` / `images` / `audio_seconds` 的 `limit` 必须是正整数，`charge` 的 `limit` 是金额（十进制字符串，最多 9 位小数）。
`modelWeights` 对 `images` 同样生效（例如让高清模型的一张图按 2 张计）。
`models` 限定规则只对哪些逻辑模型计量（空 = 套餐覆盖的全部模型）；`modelWeights` 是计量倍率（0–1000，未列出为 1），例如
`{"gpt-5.6-sol": "5"}` 让一次 `gpt-5.6-sol` 请求按 5 次计；倍率 0 表示该模型不计入这条规则。

**窗口**（`window`）：

| `kind` | 参数 | 行为 |
|---|---|---|
| `calendar` | `unit`：`day` / `week` / `month`；`timezone`：IANA 时区，默认 `UTC` | 自然日 / 周 / 月，**周从周一 00:00 开始**，按 `timezone` 对齐（例如 `Asia/Shanghai` 的周窗口在北京时间周一零点重置）。夏令时按时区规则处理 |
| `session` | `duration`：5m–31d | **会话窗口**（Claude 式“5 小时窗口”）：没有进行中的会话时，下一个计量的请求开启新会话（起点为该请求的开始时间），会话持续 `duration`，结束后用量清零；会话到期后的第一个请求再开启下一个会话。没有请求时窗口不会自动开始 |
| `rolling` | `duration`：5m–31d | 滑动窗口：统计最近 `duration` 内的用量，按 5 分钟分桶（因此精度约 5 分钟）。超额后，等最早的分桶陆续过期、用量回落到上限以下即恢复 |
| `period` | `every`：1h–366d | 从订阅开始时间起按固定周期对齐（例如 30d 套餐配 `every: 7d`，每 7 天一个窗口，与日历无关） |
| `lifetime` | — | 整个订阅期内累计，不重置（适合“体验包：共 100 次”） |

**超额行为**（`onExceed`，缺省 `block`）：`block` 拒绝请求；`overflow_to_wallet` 改为按钱包计费（见 11.2）。

### 11.2 网关如何使用订阅

每个请求在**第一次尝试平台渠道之前**、钱包预留之前检查订阅（由用户自己的或共享的渠道完成的请求不检查、不计量，见 [8.8](#88-渠道归属与计费)）：

1. 找出该用户所有**有效**（未取消、`startsAt ≤ 现在 < endsAt`）且**覆盖该逻辑模型**的订阅。
2. 对每份订阅，检查其中适用于该模型的规则：当前窗口用量都 **小于** 上限时，这份订阅可用。
3. 有可用订阅 → 使用 `endsAt` 最早的一份。本次请求**不预留、不扣钱包**，请求日志的 `subscriptionId` 为该订阅，`charge` 记为 0，
   按售价折算的金额记在 `quotaCharge`。请求成功结束后（包括客户端中途断开、但上游已产生用量的流式请求）把用量计入这份订阅的各条适用规则；
   失败的请求不计量。
4. 都不可用：
   - 若每份订阅中被超出的规则**全部**是 `overflow_to_wallet` → 本次请求按钱包处理，与没有订阅时完全相同；
   - 否则拒绝，返回 **`429 quota_exceeded`**，响应头 `Retry-After` 为最早有订阅恢复可用的秒数（一份订阅要等它所有超额规则都重置才算恢复，
     因此按 `Retry-After` 重试不会马上再被拒绝）；如果所有订阅在到期前都不会恢复（`lifetime` 用尽，或重置时间晚于 `endsAt`），返回
     **`429 quota_exhausted`**，不带 `Retry-After`。错误消息是中文，包含套餐名、规则名与重置时间（按规则的时区显示，非 calendar 规则为 UTC），
     OpenAI 格式的 `type` 为 `insufficient_quota`，Anthropic 格式为 `rate_limit_error`。
5. 没有任何覆盖该模型的订阅 → 按钱包处理。

与钱包 / 预付费的关系：

- **套餐不限制未覆盖的模型**。没有订阅、或订阅不覆盖的模型照常走钱包（见[第 10 节](#10-计费模式)）；只想让用户使用套餐时，应开启
  `billing.enforce` 并为模型设置售价，不给用户发放余额（没有售价的模型即使开启强制计费也是免费的）。
- `overflow_to_wallet` 的“按钱包计费”同样受 `billing.enforce` 约束：`enforce=false` 时只在请求日志中记录费用、不扣余额，
  **等同于超额后不限量**；`enforce=true` 时按预留 → 结算扣费，余额不足返回 `402 insufficient_balance`。
- 只要有一份订阅的超额规则是 `block` 且没有其他可用订阅，请求就会被 429 拒绝，**即使钱包有余额**。
- `charge` 计量依赖售价：模型没有售价时计量值为 0。计量值为 0 的请求（包括 `modelWeights` 为 0）不入账，也不会开启 session 窗口。
- 统计页与账本中的 `charge` 不包含订阅覆盖的请求；请求日志的 `quotaCharge` 可用于对账。

### 11.3 创建套餐

在控制台的套餐管理页面或通过 `POST /api/admin/billing/plans`（需要 `billing.manage`）创建。下例是一个类似 Claude Pro 的月卡：
每个 5 小时会话最多 200 次请求（`gpt-5.6-sol` 每次按 5 次计），每个自然周（北京时间周一零点重置）最多 500 万 token，周额度用完后转为按钱包计费：

```json
{
  "name": "Pro 月卡",
  "description": "每 5 小时 200 次请求，每周 500 万 token",
  "listPrice": "20",
  "duration": "30d",
  "models": [],
  "stackable": false,
  "rules": [
    {"id": "5h", "label": "5 小时窗口", "meter": "requests",
     "window": {"kind": "session", "duration": "5h"}, "limit": "200",
     "modelWeights": {"gpt-5.6-sol": "5"}, "onExceed": "block"},
    {"id": "weekly", "label": "每周", "meter": "tokens.total",
     "window": {"kind": "calendar", "unit": "week", "timezone": "Asia/Shanghai"}, "limit": "5000000",
     "onExceed": "overflow_to_wallet"}
  ]
}
```

其他常见写法：每月 50 元额度 `{"meter":"charge","window":{"kind":"calendar","unit":"month","timezone":"Asia/Shanghai"},"limit":"50"}`；
体验包共 100 次 `{"meter":"requests","window":{"kind":"lifetime"},"limit":"100"}`；最近 24 小时最多 100 万输入 token
`{"meter":"tokens.input","window":{"kind":"rolling","duration":"24h"},"limit":"1000000"}`。

- 修改套餐（`PATCH /api/admin/billing/plans/{id}`，带 `version`）只影响之后新开通的订阅。
- 下架：把 `status` 改为 `archived`。下架后不能再开通、续期或生成新的套餐码（返回 `409 plan_archived`），**已发出的套餐码兑换时也会失败**；
  已有订阅不受影响。改回 `active` 即恢复。套餐不能删除。
- 用户在控制台可以看到所有 `active` 套餐（`GET /api/plans`），以及自己的订阅和每条规则的已用量、剩余量与重置时间（`GET /api/billing/subscriptions`）。

### 11.4 开通、续期与取消

- **管理员开通**：`POST /api/admin/billing/subscriptions`，`{"userId": "...", "planId": "...", "periods": 1}`（`periods` 1–120，有效期 = `duration` × `periods`）。
- **续期规则**（开通与兑换相同）：套餐 `stackable=false` 且用户已有该套餐的有效订阅 → 延长那份订阅的 `endsAt`（返回 200，审计 `subscription.renew`），
  规则快照与已有用量不变；否则从当前时间开通一份新订阅（返回 201，审计 `subscription.grant`）。`stackable=true` 的套餐每次都生成新订阅，
  用户可以同时持有多份，网关优先使用最早到期的一份。
- **取消**：`POST /api/admin/billing/subscriptions/{id}/cancel`，可带 `{"note": "原因"}`，立即生效，不退款；已过期或已取消的订阅返回
  `409 subscription_not_active`。审计 `subscription.cancel`。
- 订阅到期后自动变为 `expired`（由时间推导，无需后台任务）。

### 11.5 发放套餐兑换码

与钱包兑换码共用批次接口，`kind` 设为 `plan`，用 `planId` + `periods` 代替 `amount`：

```bash
curl -sS https://gateway.example.com/api/admin/billing/redeem-batches \
  -H 'Cookie: og_session=<浏览器中的会话值>' \
  -H 'X-Requested-With: XMLHttpRequest' -H 'Content-Type: application/json' \
  -d '{"kind":"plan","planId":"<套餐 ID>","periods":1,"count":100,"perUserLimit":1,"expiresAt":"2026-12-31T16:00:00Z","note":"月卡"}'
```

- 套餐必须存在且为 `active`；`count`、`maxRedemptionsPerCode`、`perUserLimit`、`validFrom` / `expiresAt` 与钱包码相同，明文码同样只返回一次。
- 用户在兑换页面输入套餐码（`POST /api/billing/redeem`）后，按 11.4 的规则开通或续期（来源 `redeem`），响应为 `{"kind":"plan","subscription":{...}}`；
  钱包码的响应为 `{"kind":"wallet_credit","amount":...,"wallet":{...}}`。兑换写审计 `billing.redeem`。
- 批次列表中套餐码批次的 `amount` 为 null，`planName` 显示套餐的当前名称。

### 11.6 发福利：额度重置与批量延期

管理员（`billing.manage`）可以一次性给一批订阅“清空当前窗口用量”或“延长有效期”，例如服务故障后的补偿、节日活动。在控制台套餐 / 订阅管理页面操作，
或调用下列接口（契约见 `docs/contracts/phase7-api.md` §3）。两个接口的目标写法相同：

- `{"ids": [...]}`：指定订阅 ID（1–1000 个）；
- `{"planId": "<套餐 ID>" | null, "status": "active"}`：某个套餐（`null` = 全部套餐）的全部有效订阅；`planId` 不存在返回 `404 not_found`。

无论哪种写法，都只作用于**有效**订阅（状态 `active` 且 `endsAt` 晚于当前时间），指定的已取消 / 已过期订阅会被忽略。

**重置额度**：`POST /api/admin/billing/subscriptions/reset-quota`

```json
{"target": {"planId": null, "status": "active"}, "rules": null, "includeLifetime": false, "note": "10 月 8 日故障补偿"}
```

- `rules`：只重置这些规则 ID（1–50 个）；`null` = 全部规则。
- `includeLifetime`（默认 `false`）：`lifetime` 规则默认**不重置**，避免误把一次性额度（如体验包）清零。
- `note`：≤ 200 字，可以为空字符串；写入审计并出现在用户通知中。
- 一份订阅至少有一条被选中、且可重置（非 lifetime，或开启了 `includeLifetime`）的规则才计入 `affected`。

每条被选中的规则删除其**当前窗口**的用量计数（`quota_usage` 中窗口起点不早于当前窗口起点的行）：

| 窗口 | 重置效果 |
|---|---|
| `calendar` / `period` | 删除当前窗口的计数，窗口边界不变 |
| `rolling` | 删除窗口内的全部分桶 |
| `session` | 从重置时刻 A 重新开始一个空的会话：用量为 0，下一次刷新为 A + `duration`（如 5 小时窗口在 5 小时后刷新），与原来的刷新时间无关；之后的用量计入这一会话（锚定式重置，`docs/contracts/phase11-api.md` §1） |
| `lifetime` | 仅在 `includeLifetime: true` 时清零全部用量 |

网关每次请求都从数据库读取用量，因此被 `429 quota_exceeded` / `quota_exhausted` 阻断的用户**立即恢复可用**，多实例同样立即生效。

**批量延期**：`POST /api/admin/billing/subscriptions/extend`，`{"target": ..., "duration": "7d", "note": "..."}`。`duration` 格式与套餐有效期相同
（`<n>h` 或 `<n>d`，1h–366d），每份目标订阅的 `endsAt` 延后该时长；规则快照与已有用量不变。

**预览**：两个接口都支持 `?dryRun=true`（或 `1`）：做同样的校验与统计，返回 `affected` 与订阅 ID，但不修改数据、不写审计、不发通知。
结果取决于全部参数（包括 `rules`、`includeLifetime`），建议先预览再执行。

两个接口都返回 `{"affected": <数量>, "subscriptions": [...]}`（最多前 500 个 ID，最早开通的在前）。参数错误返回 `422`（details 为 `target` / `rules` /
`duration` / `note`）。

| | 审计 | 通知 |
|---|---|---|
| 重置 | 一条 `subscription.quota_reset`（目标、规则、`includeLifetime`、备注、数量、订阅 ID） | `subscription.quota_reset`：每次操作每位受影响用户一条（含套餐名、被重置的规则名与备注） |
| 延期 | 一条 `subscription.extend`（目标、时长、备注、数量、订阅 ID） | `subscription.extended`：每次操作每位受影响用户一条（含新的到期时间与备注） |

注意：

- 重置只删除当前窗口的计数，**请求日志中的历史用量与费用不受影响**，对账照常以请求日志为准；被删除的计数无法恢复。
- `quota.near_limit` / `quota.exhausted` 通知按窗口去重：calendar / period / lifetime 规则重置后窗口不变，同一窗口内不会再次提醒；session 规则重置后是从重置时刻开始的新窗口，会再次提醒。
- 延期不会让已过期或已取消的订阅恢复；需要恢复时请重新开通（见 [11.4](#114-开通续期与取消)）。

### 11.6.1 额度重置卡

除了管理员直接重置，还可以给用户发放**重置卡**，由用户在需要时自己使用（契约见 `docs/contracts/phase11-api.md`）。在控制台「计费 → 重置卡」发放与作废，
或调用 `POST /api/admin/billing/reset-cards/batches`（支持 `?dryRun=true` 预览人数）：

```json
{"kind": "5h", "quantity": 2, "expiresAt": "2026-11-01T00:00:00Z", "planIds": null, "note": "国庆福利",
 "target": {"type": "plan", "planId": "<套餐 ID>"}}
```

| 卡类型 | 重置的规则 |
|---|---|
| `5h`（5小时重置卡） | session / rolling 窗口、时长恰为 5 小时的规则 |
| `weekly`（周重置卡） | session / rolling 窗口、时长恰为 7 天的规则 |
| `both`（双重置卡） | 以上两类，同一时刻一起重置 |

- 发放对象：指定用户（`userIds`，最多 1000 个）、用户组（`groupId`）、某个套餐的有效订阅用户（`planId`）或全部用户（`all`）；只发给状态正常的普通用户 / 管理员
  （审计员、停用用户不会收到），“全部用户”是发放时已存在的用户。每人 1–100 张，单批最多 20 万张；可设置过期时间与限定套餐。
- 用户在「钱包与订阅 → 我的重置卡」中选择订阅使用，界面会显示每条额度“使用前 → 使用后”。使用时按 §11.6 的锚定式重置：session 窗口从使用时起重新计时，
  rolling 窗口清空。订阅没有匹配的规则、不在限定套餐内、订阅已结束时会被拒绝，**卡不会被消耗**；同一张卡并发使用只会成功一次。
- 作废批次后，其中未使用的卡（含已过期的）无法再使用，已使用的卡不受影响。
- 审计：`reset_card.issue`、`reset_card.revoke`、`reset_card.use`；收件人收到通知 `reset_card.issued`（默认站内 + 邮件）。在「用户」详情中可以查看某位用户的卡。

### 11.6.2 余额购买、续费与补差价升级

用户在控制台“购买套餐”页（`GET /api/billing/purchase/options`、`POST /api/billing/purchase`，契约见 `docs/contracts/phase15-api.md`）用钱包余额：

- **购买**：套餐在售（`active`）且 `listPrice > 0` 时，扣 `listPrice` 并开通一份新订阅（来源 `purchase`）。
- **续费**：已持有该（不可叠加）套餐的有效订阅时，同样扣 `listPrice`，到期时间顺延一个周期，规则快照与已用量不变（与兑换码续期相同）。
- **补差价升级**：从一份有效订阅升级到日均价格更高的在售套餐。升级即从现在起开始新套餐的完整周期，价格 = 新套餐售价 − 旧套餐未用完时间的价值
  （旧套餐 `listPrice` × 剩余时间 ÷ 周期；已下架的旧套餐也取其当前售价，没有售价按 0 计）。越早升级越划算，临近到期升级约等于新购。
  升级**原地替换**订阅的名称、模型与规则，同 `id` 规则的已用量保留，百分比按新上限计算。不支持降级。
- 扣款写一条 `charge` 账本记录（`refType = purchase`），不受 `billing.enforce` 影响；审计 `subscription.purchase`。余额不足返回 `403 insufficient_balance`。
- 只想通过兑换码发放、不允许自助购买的套餐，把 `listPrice` 留空即可。

### 11.6.3 邀请返利

系统设置 `billing` 组（默认关闭）：`referralEnabled`（开关）、`referralRate`（返利比例，百分比，0–100，最多 2 位小数，默认 10）、
`referralMinRecharge`（单次充值金额达到该值才返利，默认 0）。

- 每个用户在“邀请返利”页拿到固定的邀请码与链接 `{OMNIGATE_PUBLIC_URL}/login?invite=<code>`。新用户通过该链接**首次登录创建账号**时绑定邀请人
  （已有账号不会绑定，绑定后不可更改；关闭返利时也会绑定）。
- 被邀请人每次兑换**余额兑换码**且金额 ≥ `referralMinRecharge` 时，邀请人获得 `金额 × referralRate%` 的余额（`grant`，`refType = referral`），
  并收到 `wallet.credited` 通知。套餐兑换码、余额购买、管理员调整与注册赠送不返利。

### 11.7 注意事项与限制

- **配额是软上限**：请求开始前检查、成功结束后才异步入账，并发进行中的请求都能通过检查，所以用量可能略超上限（例如长时间的流式请求、
  同时发起的多个请求）；超出后的新请求会被阻断。入账按请求 ID 幂等，并锁定订阅行，避免并发请求开出两个重叠的 session。
- 用量计数保存在 PostgreSQL（`quota_usage`），每次检查都读数据库，多实例部署时语义一致，不依赖 Redis；每个网关请求因此多 1–2 次数据库查询（有效订阅、当前用量）。
- 当前只有“按订阅”的配额；按用户或按 Key 的配额、插件自定义计量（`custom:*`）尚未实现。Key 的 `rpm` 限流与套餐配额相互独立。
- 历史用量清理：后台每小时清理一次——订阅结束或取消满 30 天的全部计数、32 天前的 rolling / session 计数、400 天前的 calendar / period 计数、
  90 天前的入账幂等记录（对账以请求日志为准）；正在生效的计数不受影响。多实例部署时每个实例都会执行（操作幂等）。
- 窗口时间以服务器时钟为准（存储为 UTC）；calendar 窗口的边界按规则的 `timezone` 计算，阻断消息中的重置时间也按该时区显示。
- 套餐、订阅与用量都在 PostgreSQL 中（`plans`、`subscriptions`、`quota_usage`、`subscription_charges` 表），随第 13 节的 `pg_dump` 一起备份。

### 11.8 用目录种子批量导入价格、模型资料与套餐

`omnigate seed <file.json|-|builtin> [--dry-run]` 把一份 JSON 目录（`prices`、`modelInfo`、`plans`）通过与后台相同的服务层写入：
校验与审计一致（操作者 `seed`，审计 `metadata.source = "seed"`），只写差异、可重复执行，从不删除。价格与当前生效版本不同时新增一个立即生效的版本；
模型资料按模型名覆盖；套餐按名称匹配，存在且有差异时原地更新（已有订阅保留快照）。`omnigate seed export [--prices] [--model-info] [--plans] [--no-cost]`
以同样的格式只读导出当前目录，用于把开发环境的配置带到生产。命令不启动 HTTP 服务，需要与 `omnigate migrate` 相同的环境变量，可在服务运行时执行：

```bash
docker compose exec omnigate omnigate seed builtin --dry-run        # 内置 GPT 目录：试运行
docker compose exec omnigate omnigate seed builtin                  # 写入
docker compose exec -T omnigate omnigate seed - < catalog.json      # 自定义目录（标准输入）
docker compose exec -T omnigate omnigate seed export-builtin > catalog.json   # 导出内置目录（不连数据库）
```

`builtin` 是编译进二进制的目录（源文件 `server/internal/seed/builtin/catalog.json`）。设置 `OMNIGATE_SEED_ON_START=builtin` 时，
服务首次启动会自动导入一次（见 [1.2](#12-服务端变量)）；每次成功的 `seed` 也会更新 `seed.applied` 标记。

文件格式、幂等规则、导出方法与上线套餐说明见 [deploy/seed/README.md](../../deploy/seed/README.md)。

## 12. 客户端接入示例

先在控制台“API Keys”页面创建 Gateway Key（形如 `og-` + 43 个字符，**只显示一次**）。Key 可以限制可用模型、渠道、来源 IP、每分钟请求数，并选择跨协议兼容模式：
`strict`（默认）遇到无法转换的字段返回 400；`lenient` 丢弃这些字段并在响应头 `X-OmniGate-Compat-Warnings` 中列出。下文以 `PUBLIC_URL=https://gateway.example.com` 为例，`model` 一律使用渠道中配置的**逻辑模型名**。

| 客户端 | Base URL | Key 的传递方式 |
|---|---|---|
| OpenAI SDK / OpenAI 兼容工具 | `${PUBLIC_URL}/v1` | `Authorization: Bearer og-…` |
| Anthropic SDK | `${PUBLIC_URL}`（SDK 自己拼接 `/v1/messages`） | `x-api-key: og-…` |
| Claude Code | `ANTHROPIC_BASE_URL=${PUBLIC_URL}` | `ANTHROPIC_AUTH_TOKEN`（Bearer）或 `ANTHROPIC_API_KEY`（x-api-key） |

两种认证头在所有 `/v1` 接口上都可用；同时提供时以 `x-api-key` 为准。

### 12.1 OpenAI SDK

```python
from openai import OpenAI

client = OpenAI(base_url="https://gateway.example.com/v1", api_key="og-xxxxxxxx")
resp = client.chat.completions.create(
    model="claude-sonnet",          # 逻辑模型名；可以路由到 Anthropic 渠道（自动转换）
    messages=[{"role": "user", "content": "你好"}],
    stream=True,
)
for chunk in resp:
    print(chunk.choices[0].delta.content or "", end="")
```

```ts
import OpenAI from 'openai'
const client = new OpenAI({ baseURL: 'https://gateway.example.com/v1', apiKey: 'og-xxxxxxxx' })
```

`client.responses.create(...)` 可以使用任何渠道上的模型：开启了 `supportsResponses` 的 openai 渠道（或模型设为 `upstreamProtocol: "responses"`）
直通，其他渠道自动转换（`previous_response_id` 只在直通时可用，见 [8.7](#87-协议转换与嵌入接口)）。

```python
emb = client.embeddings.create(model="text-embedding-3-small", input=["你好", "OmniGate"])   # 只路由到 openai 渠道
print(len(emb.data[0].embedding))
```

### 12.2 Anthropic SDK

```python
from anthropic import Anthropic

client = Anthropic(base_url="https://gateway.example.com", api_key="og-xxxxxxxx")
msg = client.messages.create(
    model="claude-sonnet",          # 也可以是映射到 OpenAI 渠道的逻辑模型（自动转换）
    max_tokens=1024,
    messages=[{"role": "user", "content": "你好"}],
)
```

注意 Anthropic SDK 的 `base_url` **不带** `/v1`。

### 12.3 Claude Code

```bash
export ANTHROPIC_BASE_URL=https://gateway.example.com
export ANTHROPIC_AUTH_TOKEN=og-xxxxxxxx      # 以 Authorization: Bearer 发送
# 或者：export ANTHROPIC_API_KEY=og-xxxxxxxx   # 以 x-api-key 发送（Claude Code 首次使用时可能询问是否信任该 Key）
claude
```

- Claude Code 按自己的模型 ID 发请求，请在渠道中把这些 ID 配置为逻辑模型名（例如逻辑名 `claude-sonnet-4-5` → 上游同名），或用 `ANTHROPIC_MODEL` 指定一个已配置的逻辑模型名。
  “发现模型”可以帮你列出上游的真实模型 ID。
- Claude Code 会调用 `/v1/messages/count_tokens`：路由到 Anthropic 渠道时返回上游的精确值，否则返回网关的估算值（响应头 `X-OmniGate-Estimated: true`），不计费。
- 让 Claude Code 使用 OpenAI 兼容渠道时（协议转换；只支持 Responses 的模型在映射中设 `upstreamProtocol: "responses"` 即可），建议把该 Key 的兼容模式设为 `lenient`：`cache_control`、`context_management`、历史 `thinking` 块等提示类字段
  无论如何都会被丢弃，而 Anthropic 专有的服务端工具等无法转换的字段在 `strict` 下会导致 400。目标渠道是 o 系列等模型时记得设置 `maxTokensField=max_completion_tokens`。
- 反向代理必须对 `/v1` 关闭缓冲（见第 7 节），否则 Claude Code 会长时间无输出。

### 12.4 curl

```bash
# 模型列表（OpenAI 格式；加 -H 'anthropic-version: 2023-06-01' 返回 Anthropic 格式）
curl -sS https://gateway.example.com/v1/models -H 'Authorization: Bearer og-xxxxxxxx'

# OpenAI Chat Completions（流式，-N 关闭 curl 自身的缓冲）
curl -N https://gateway.example.com/v1/chat/completions \
  -H 'Authorization: Bearer og-xxxxxxxx' -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"你好"}]}'

# OpenAI Embeddings
curl -sS https://gateway.example.com/v1/embeddings \
  -H 'Authorization: Bearer og-xxxxxxxx' -H 'Content-Type: application/json' \
  -d '{"model":"text-embedding-3-small","input":"你好"}'

# OpenAI 文生图（只路由到 openai 渠道；gpt-image-* 返回 b64_json）
curl -sS https://gateway.example.com/v1/images/generations \
  -H 'Authorization: Bearer og-xxxxxxxx' -H 'Content-Type: application/json' \
  -d '{"model":"gpt-image-1","prompt":"一只在键盘上睡觉的橘猫","size":"1024x1024","n":1}'

# OpenAI 图片编辑（multipart：-F 上传文件，多张图用 image[]，mask 可选；请求体上限 64 MiB）
curl -sS https://gateway.example.com/v1/images/edits \
  -H 'Authorization: Bearer og-xxxxxxxx' \
  -F model=gpt-image-1 -F 'prompt=把背景换成海边' \
  -F 'image[]=@cat.png' -F 'image[]=@hat.png' -F 'mask=@mask.png'

# Anthropic Messages（-i 显示响应头）
curl -i https://gateway.example.com/v1/messages \
  -H 'x-api-key: og-xxxxxxxx' -H 'anthropic-version: 2023-06-01' -H 'Content-Type: application/json' \
  -d '{"model":"claude-sonnet","max_tokens":256,"messages":[{"role":"user","content":"你好"}]}'
```

排障时关注两个响应头：`X-OmniGate-Request-Id`（与控制台“请求日志”中的请求 ID 一致）和 `X-OmniGate-Compat-Warnings`（跨协议时被丢弃的字段）。
常见错误：`401`（Key 无效、已停用 / 过期）、`403`（IP 不在白名单或 Key 不允许该模型；`account_disabled` 表示所属用户被停用，消息含原因与到期时间，见 [2.7](#27-用户管理停用强制下线与批量操作)）、
`404 model_not_found`（没有可用渠道提供该逻辑模型；图片与嵌入接口只由 openai 渠道提供）、`413`（请求体超过 32 MiB，图片接口 64 MiB）、
`402`（强制计费下余额不足）、`429`（Key 的 RPM 限额、上游限流，或套餐配额用完：`quota_exceeded` 带 `Retry-After` 头，`quota_exhausted` 表示到期前不会恢复，见 [11.2](#112-网关如何使用订阅)）、
`502` / `504`（上游不可用 / 超时，所有备用渠道都已尝试）。

## 13. 备份与恢复

需要备份的内容：

1. **数据库**（PostgreSQL 或 SQLite 文件）：用户、身份、会话、审计日志、系统设置（含锁定的币种与 `billing.enforce`）、渠道（含加密的上游凭据）、
   Gateway Key 摘要、价格版本、请求日志、钱包与账本、兑换码摘要、套餐 / 订阅 / 配额用量、插件（全部版本源码、草稿、审批记录、插件存储、能力结果）等全部数据。
2. **主密钥**（`OMNIGATE_MASTER_KEY`）和整个 `.env`：与数据库备份**分开保存**，见 [3.3](#33-备份警告)。
3. `/data` 卷（`OMNIGATE_DATA_DIR`）中的结算日志通常为空，无需单独备份，但卷要保留；不使用 Redis。

SQLite（单容器）：服务运行中执行 `docker exec omnigate omnigate backup /data/backups/omnigate-$(date +%Y%m%d-%H%M%S).db`
（`VACUUM INTO`，生成一个独立、一致的数据库文件；目标文件已存在时拒绝覆盖，所以文件名带时间戳），再 `docker cp` 出来。
恢复时停止容器，用备份文件替换 `/data/omnigate.db`、移走同目录的 `omnigate.db-wal`、`omnigate.db-shm` 与 `settlement-journal.jsonl`，
并执行 `chown 65532:65532 /data/omnigate.db`（容器以 uid 65532 运行，属于 root 的文件会导致 `attempt to write a readonly database`），然后启动。
可直接复制的命令见 [deploy-production.md 9.2](deploy-production.md#92-sqlite恢复)。

PostgreSQL 备份（Compose）：

```bash
docker compose exec -T postgres pg_dump -U omnigate -d omnigate -Fc > omnigate-$(date +%Y%m%d-%H%M%S).dump
```

恢复：删库重建后导入（不要用 `pg_restore --clean`：请求日志是分区表，会报 `cannot drop inherited constraint` 并以非零状态结束）。

```bash
docker compose stop omnigate
docker compose exec -T postgres dropdb -U omnigate omnigate
docker compose exec -T postgres createdb -U omnigate -O omnigate omnigate
docker compose exec -T postgres pg_restore -U omnigate -d omnigate --no-owner --exit-on-error < omnigate-2026-10-08.dump
docker compose start omnigate
curl -fsS http://127.0.0.1:8080/readyz
```

- 恢复时要使用与备份**配套的主密钥**；否则渠道凭据无法解密，需要逐个重新填写。
- 恢复后启动的 OmniGate 版本不应低于备份时的版本；如果版本更高，启动时会自动执行后续迁移。
- 恢复后所有会话仍然有效（会话也在数据库中）。如需强制所有人重新登录，可在恢复后清空 `sessions` 表。
- 更换数据库类型（SQLite ⇄ PostgreSQL）不要用备份文件互相恢复，用 `omnigate migrate-db`（见 [1.5](#15-数据库postgresql-与-sqlite)）。
- 定期把备份恢复到一个临时实例中进行演练。

## 14. 升级与回滚

### 14.1 升级

1. **升级前先备份**数据库（见第 13 节），并确认主密钥备份可用。
2. 获取新版本后重新构建并启动：
   ```bash
   git pull
   OMNIGATE_VERSION=v0.2.0 docker compose up -d --build
   ```
3. `omnigate serve` 启动时会自动执行所有待执行的迁移（`OMNIGATE_AUTO_MIGRATE=false` 时除外）。迁移通过 PostgreSQL advisory lock 串行化，多个实例同时启动也是安全的。迁移完成之前服务不会开始监听；迁移失败时进程以非零状态退出（Compose 会按 `restart: unless-stopped` 反复重启，请查看日志）。若关闭了自动迁移且存在待执行迁移，`/readyz` 返回 `503 migrations_pending`。
4. 查看迁移状态：
   ```bash
   docker compose exec omnigate omnigate migrate status
   # {"dialect":"postgres","current":8,"latest":8,"pending":0}
   ```
   源码环境用 `make migrate-status`（读取 `.env.dev` 与 `.env.dev.local`）。

需要手动控制迁移时（例如滚动发布、或希望在维护窗口单独执行迁移）：给服务设置 `OMNIGATE_AUTO_MIGRATE=false`，再单独运行 `omnigate migrate`（Compose：`docker compose run --rm omnigate migrate`）。注意：数据库迁移到最新版本之前，`serve` 会在初始化阶段失败（启动时需要写入 `system_settings`）。

### 14.2 回滚

- 迁移文件中的 Down 部分**只用于开发环境**，CLI 也没有提供降级命令。生产环境不要在线回滚 schema。
- 生产回滚的做法：停止服务 → 用升级前的备份恢复数据库（第 13 节）→ 用旧版本镜像启动（如 `OMNIGATE_VERSION=v0.1.0 docker compose up -d`，前提是本地还保留着该版本的镜像，或检出对应的代码重新构建）。
- 不要让旧版本二进制直接连接已经被新版本迁移过的数据库，两者的兼容性没有保证。
- 升级之后写入的数据会随着回滚丢失。如果必须保留这些数据，请先评估再决定，必要时人工迁移。

## 15. 生产环境检查清单

- [ ] `OMNIGATE_ENV=production`
- [ ] `OMNIGATE_PUBLIC_URL` 为最终的 https 地址，且与 OAuth/OIDC 应用中登记的回调地址一致
- [ ] `OMNIGATE_MASTER_KEY` 已设置，且已与数据库备份分开存放
- [ ] `OMNIGATE_BOOTSTRAP_ADMINS` 使用 `github-id:` 或 `<oidc-id>:<sub>` 这类不可变标识
- [ ] `OMNIGATE_REGISTRATION_MODE` 符合预期（默认 `restricted`）
- [ ] `OMNIGATE_CURRENCY` 已确认（首次启动后锁定）
- [ ] 已配置至少一种登录方式，OAuth / OIDC 回调地址与 `PUBLIC_URL` 一致
- [ ] `OMNIGATE_TRUSTED_PROXIES` 已配置为代理到达容器时的精确地址（不是 `172.16.0.0/12` 这类大网段），审计日志中显示的是真实客户端 IP 前缀
- [ ] 反向代理已关闭响应缓冲（`proxy_buffering off; proxy_read_timeout 900s;` 或 Caddy `flush_interval -1`）、请求体上限 64m，9090 端口和直连 8080 端口未暴露到公网
- [ ] 提供图片编辑 / 变体接口时，临时目录（`$TMPDIR`，默认 `/tmp`）可写且空间足够容纳并发的大图片请求；容器使用只读根文件系统时已为 `/tmp` 挂载可写卷
- [ ] `OMNIGATE_CHANNELS_ALLOW_PRIVATE_NETWORK` 仅在确实需要访问内网模型服务时开启
- [ ] `plugins.manage` / `plugins.trust` 只授予受信任的管理员；升级后已检查随附插件是否有新版本需要在渠道上切换
- [ ] 已决定计费模式（`billing.enforce`），开启前已为需要计费的逻辑模型设置售价，并为用户发放了余额
- [ ] 使用目录种子时，已先 `omnigate seed … --dry-run` 确认变更、再正式写入，重复执行显示「无需变更」（§11.8）
- [ ] 使用套餐时，已确认 `onExceed` 与 `billing.enforce` 的组合符合预期（`enforce=false` 时 `overflow_to_wallet` 等于超额后不限量），calendar 规则的 `timezone` 已设置
- [ ] 需要邮件通知时已配置 SMTP（`starttls` 或 `tls`），并用设置页的“发送测试邮件”验证过；`OMNIGATE_PUBLIC_URL` 正确（邮件中的链接与退订链接基于它）
- [ ] 已配置定期备份（SQLite：`omnigate backup`；PostgreSQL：`pg_dump`），并做过恢复演练

## 16. 通知（邮件 / Webhook / 站内）

接口契约见 [phase6-api.md](../contracts/phase6-api.md)。

### 16.1 SMTP 与总开关

- 系统设置“邮件（SMTP）”分组：`notifications.smtp.{host,port,security,username,password,from}`、`notifications.enabled`（总开关，默认开）、
  `notifications.emailRateLimitPerHour`（每个用户每小时最多发送的邮件数，默认 20）。来源顺序：数据库 → `OMNIGATE_SMTP_*` → 默认值。
- 密码只写：保存时用主密钥加密（ADR-0008，关联数据绑定到设置项），读取只返回 `passwordSet`。在设置页清空密码会在数据库中记录“空密码”，
  **不会**回退到 `OMNIGATE_SMTP_PASSWORD`；点“恢复为环境变量”才会。更换主密钥后无法解密的密码视为未设置（日志警告）。
- 修改任一 SMTP 字段会写两条审计：`settings.update`（密码只记录是否设置）与 `notifications.smtp_update`。
- `POST /api/admin/settings/smtp-test {to}` 用当前设置立即发送一封测试邮件；连接超时 10 秒、整个会话 30 秒。
- SMTP 未配置（`host` 或 `from` 为空）或总开关关闭时不产生邮件投递，站内通知照常记录；总开关关闭时 Webhook 也不发送。

### 16.2 事件与接收者

| 事件 | 产生方式 | 接收者 |
|---|---|---|
| `account.status_changed` | 管理员停用 / 启用（含批量）、强制下线，或停用到期自动启用（[2.7](#27-用户管理停用强制下线与批量操作)）；`data` 含 `action`（`disabled` / `enabled` / `logout`）、`auto`、原因与期限 | 用户本人（**包括已停用的用户**） |
| `wallet.balance_low` | 余额变动提交后检查：可用余额从阈值以上降到阈值以下；恢复到阈值以上后才会再次触发 | 用户本人 |
| `wallet.credited` | 兑换码充值、管理员正向调整、新用户赠送 | 用户本人 |
| `subscription.expiring` / `subscription.expired` | 后台扫描（每 10 分钟）：到期前 3 天；到期或被取消（最长约 10 分钟延迟） | 订阅所有者 |
| `subscription.quota_reset` / `subscription.extended` | 管理员批量重置额度 / 批量延期（[11.6](#116-发福利额度重置与批量延期)），dry run 不发送；每次操作每位用户一条，列出涉及的订阅与备注 | 受影响订阅的所有者 |
| `reset_card.issued` | 管理员发放重置卡（[11.6.1](#1161-额度重置卡)），dry run 不发送；每批每位收件人一条（数量、卡类型、限定套餐、有效期与备注） | 收件人 |
| `quota.near_limit` / `quota.exhausted` | 每次记账后：某条规则在当前窗口达到 80% / 100%，每个窗口一次 | 订阅所有者 |
| `model.price_changed` | 新增售价版本（含未来生效）且金额变化 | 近 30 天通过平台渠道成功调用过该模型的用户 |
| `model.removed` / `model.added` | 渠道快照变化时（以及每 10 分钟）比对：近 30 天调用过的模型不再可用；平台广场新增模型（默认关闭，需用户开启） | 调用过的用户 / 全部用户 |
| `key.expiring` | 后台扫描：到期前 7 天 | Key 所有者 |
| `channel.unhealthy` / `channel.recovered` | 熔断打开（含健康测试连续失败）/ 恢复 | 渠道所有者；平台渠道（所有者为管理员）另通知所有 `channels.manage` 管理员；被共享者不通知 |
| `channel.auth_failed` | 上游返回 401/403，每个渠道 6 小时内最多一次 | 同上 |
| `upstream.balance_low` | 插件 `balance` 能力结果低于渠道的 `alerts.balanceBelow`（按插件返回的币种解释）；恢复后才再次触发 | 同上 |
| `plugin.pending_approval` | 发布的插件版本需要审批 | 持有 `plugins.trust` 的管理员 |
| `account.group_changed` | 管理员修改用户所在组（单个或批量），或删除了用户所在的组（成员移到默认组）；`data` 含 `from`、`to`、`priceMultiplier`、`limits` | 用户本人 |
| `channel.share_invited` | 有人把渠道共享给你（新邀请或重新邀请），等待你接受（[8.3](#83-可见性与共享)）；同一渠道对同一用户 1 小时内最多一次；`data` 含 `channelId`、`channelName`、`owner`、`models` | 被邀请的用户 |
| `limit.spend_near` / `limit.spend_reached` | 结算后：组的每日 / 每月消费限额或 Key 的消费上限在当前窗口达到 80% / 达到上限，每个限额每个窗口各一次（一次越过上限时只发 `reached`）；`data.kind` 为 `daily` / `monthly` / `key` | 用户本人（Key 所有者） |

每个事件有去重键（唯一约束），同一个键只通知一次；重复的扫描、多实例同时检测都是幂等的。用户偏好在 `/console/notifications/settings`
修改（邮件 / Webhook / 站内三个开关；Webhook 默认全部关闭），只能开启有资格接收的事件。

Round 6（续）新增的三个事件：

| 事件 | 类别 | 默认开关 | 说明 |
|---|---|---|---|
| `account.status_changed` | 账户（新类别，所有用户都有资格接收） | 邮件 + 站内开，Webhook 关 | 告警类（不进入每日摘要）；**站内开关锁定为开启**（保存偏好时强制 `inApp=true`），邮件 / Webhook 可关闭。停用、强制下线为 warning 级别，启用为 info；链接 `/console`。这是唯一会投递给已停用用户的事件（站内、邮件与 Webhook 都会发送），用户因此能知道账户被停用的原因 |
| `subscription.quota_reset` | 计费（需要 `billing.own`） | 邮件 + 站内开，Webhook 关 | 非告警类，可合并进每日摘要；链接 `/console/billing` |
| `subscription.extended` | 计费（需要 `billing.own`） | 邮件 + 站内开，Webhook 关 | 同上 |
| `reset_card.issued` | 计费（需要 `billing.own`） | 邮件 + 站内开，Webhook 关 | 同上 |

Round 6 新增的事件（见[第 17 节](#17-用户组价格倍率限额与分时价格)）：

| 事件 | 类别 | 默认开关 | 说明 |
|---|---|---|---|
| `account.group_changed` | 账户 | 仅站内开 | info 级别，可关闭；链接 `/console` |
| `limit.spend_near` | 计费（需要 `billing.own`） | 仅站内开 | info 级别 |
| `limit.spend_reached` | 计费（需要 `billing.own`） | 邮件 + 站内开，Webhook 关 | warning 级别、告警类（不进入每日摘要） |

安全修订（`docs/contracts/phase5-api.md` §5.5）新增的事件：

| 事件 | 类别 | 默认开关 | 说明 |
|---|---|---|---|
| `channel.share_invited` | 渠道共享（新类别，所有用户都有资格接收） | 仅站内开 | info 级别、非告警类，可关闭；链接 `/console/channels?tab=shared`。正文包含隐私提示（共享者能看到经由其渠道的请求内容）。不计入“渠道告警”摘要 |

### 16.3 邮件

- 中文 HTML + 纯文本两部分，主题为 `[站点名称] 标题`，站点名称取 `site.name`，链接基于 `OMNIGATE_PUBLIC_URL`。
- 收件地址：用户验证过的自定义通知邮箱；否则账户邮箱（身份提供方报告为已验证时）。都没有时不发送。
  自定义邮箱通过 6 位验证码验证（10 分钟有效，每小时最多 5 次，只保存摘要）。
- 每封通知邮件带“管理通知设置”链接与签名的一键退订链接（同时作为 `List-Unsubscribe` 头）：
  `GET /api/notifications/unsubscribe?token=…` 公开、幂等，关闭该事件的邮件通知；摘要邮件的链接关闭全部邮件通知。
- 每日摘要（`digest: daily`）：告警类事件（`account.status_changed`、`channel.*`、`upstream.*`、`quota.exhausted`、`wallet.balance_low`）立即发送，其余事件在用户时区的次日
  09:00 合并为一封。
- 频率限制：每个用户每小时最多 `emailRateLimitPerHour` 封（摘要邮件不计），超出的通知暂存，在窗口释放时合并为一封“N 条通知摘要”。
- 失败按 1、2、4、8 分钟退避重试，最多 5 次。

### 16.4 Webhook

- 每个用户一个地址与格式：`json`（请求体 `{id, type, title, body, url, data, createdAt}`）、`feishu`（卡片）、`dingtalk`（Markdown）、
  `wecom`（Markdown）、`slack`（text）。请求头 `X-OmniGate-Event: <type>`。
- 设置了密钥（只写，主密钥加密）时带 `X-OmniGate-Signature: sha256=<hex HMAC-SHA256(secret, 原始请求体)>`；飞书 / 钉钉另外按各自机器人的签名规则签名
  （飞书在请求体中加 `timestamp`、`sign`，钉钉在 URL 上加 `timestamp`、`sign`）。接收方应使用常量时间比较校验签名。
- 防 SSRF：与渠道相同的私网规则（只有开启 `OMNIGATE_CHANNELS_ALLOW_PRIVATE_NETWORK` 且用户持有 `channels.manage` 时才能访问内网地址），
  校验在拨号阶段进行，不跟随重定向；配置了 `OMNIGATE_UPSTREAM_PROXY` 时同样经过代理。超时 10 秒。
- 非 2xx（或机器人接口返回非 0 的 `code` / `errcode`）视为失败，按 1、5、30 分钟重试 3 次。

### 16.5 投递管道与运维

- 事件写入 `notification_events` → 站内通知 `notifications` 与发件箱 `notification_deliveries`。后台工作协程每 5 秒（有新事件时立即）领取到期投递：
  PostgreSQL 用 `FOR UPDATE SKIP LOCKED`，多实例不会重复发送；SQLite 在单写事务中领取。领取后 5 分钟内未完成的投递会被重新领取。
- 指标：`omnigate_notifications_sent_total{channel="email|webhook|inapp",type,result="sent|failed|retry|skipped|rate_limited"}`。
- 保留：站内通知与事件 90 天、已完成的投递 30 天、验证码 1 天，随请求日志保留任务每天执行一次。
- 渠道熔断状态在每个实例内存中维护，多实例部署时同一次故障可能由不同实例各通知一次（去重键按实例的打开时间生成）。

## 17. 用户组、价格倍率、限额与分时价格

Round 6（接口契约 `docs/contracts/phase8-api.md` §1–§3）。用户组用于给一批用户统一设置**平台渠道的价格倍率**和**限额**；
价格版本可以设置**分时倍率**（例如上游夜间半价时同步给用户打折）。

### 17.1 用户组

- 每个用户属于**恰好一个**组。升级到本版本时（数据库迁移 `00012`）自动创建名为“默认”的组（`isDefault: true`、倍率 1、不限额），
  现有用户全部放入；之后首次登录的新用户加入当前的默认组。
- 管理：控制台“用户组”页面，或 `GET/POST /api/admin/groups`、`PATCH/DELETE /api/admin/groups/{id}`（查看需要 `users.read`，修改需要 `users.write`）。
  修改带 `version`（乐观锁）；`PATCH` 中 `limits` 整体替换。把另一个组设为 `isDefault: true` 即切换默认组（原默认组的成员不变）；
  默认组不能删除（`409 group_is_default`），也不能直接取消默认（`422`）。删除其他组时成员移到默认组，共享给该组的渠道随之取消共享。
- 移组：`PUT /api/admin/users/{id}/group` 或批量 `set_group`；用户收到 `account.group_changed` 通知。审计动作：`group.create`、`group.update`、
  `group.delete`、`user.group_change`（批量另有一条 `user.batch`）。
- 生效时间：移组立即生效（本实例清空 Key 认证缓存并重新加载渠道快照；其他实例最多 15 秒）。组的倍率与限额修改在本实例立即生效，其他实例最多 10 秒。
- 用户在 `/api/me` 的 `group` 与 `GET /api/billing/limits` 中看到自己所在的组、倍率与限额。

| 字段 | 说明 |
|---|---|
| `name` | 1–50 个字符，唯一（`409 group_name_exists`） |
| `description` | ≤ 200 个字符 |
| `priceMultiplier` | 平台渠道售价倍率，0–100，最多 9 位小数，默认 `"1"`；`"0.8"` = 八折，`"0"` = 平台渠道免费 |
| `limits.rpm` | 每个用户每分钟最多请求数（所有 Key 合计）；`null` = 不限 |
| `limits.rpd` | 每个用户每天最多请求数（按组的 `timezone` 计日） |
| `limits.dailySpend` / `limits.monthlySpend` | 每个用户每天 / 每月最多消费（结算币种）；`"0"` = 禁止使用平台渠道 |
| `timezone` | IANA 时区，默认 `Asia/Shanghai`，决定日 / 周 / 月窗口的边界 |

### 17.2 价格倍率

```text
售价金额 = 价格版本的金额 × 分时倍率（按请求开始时间） × 用户组倍率      （一次舍入到 9 位小数）
成本金额 = 成本价版本的金额 × 成本价自己的分时倍率                        （不乘用户组倍率）
```

- 只作用于**平台渠道**；用户自己的渠道和普通用户共享的渠道仍然免费，不受倍率影响。
- 套餐覆盖的请求按乘过倍率的金额计入 `charge` 计量（`quotaCharge`）；钱包预留同样按乘过倍率的估算金额。
- 请求日志 `priceMultiplier` 记录本次生效的“组倍率 × 分时倍率”（如 `"0.4"`），`charge` / `quotaCharge` 已乘过它；免费渠道、失败或未定价的请求为空。
- 模型广场：`/api/plaza/models` 显示原价；“我的模型”（`/api/plaza/mine`）的 `price` 为乘过组倍率的单价，另给出 `basePrice`（原价）与 `priceMultiplier`。
- 修改倍率不会影响已经发生的请求。

### 17.3 限额

| 限额 | 统计范围 | 超出时 | 重置 |
|---|---|---|---|
| 组 `rpm` | 用户的全部请求（所有 Key、所有渠道档位） | `429 rate_limited`，`Retry-After` 为下一个令牌的等待秒数 | 令牌桶，每分钟 `rpm` 个 |
| 组 `rpd` | 用户当天通过限额检查并实际尝试了渠道的请求（所有档位；被限额、余额、配额拒绝的请求不计） | `429 user_request_limit`，`Retry-After` 为到组时区次日零点的秒数 | 组时区每天 0 点 |
| 组 `dailySpend` / `monthlySpend` | 用户当天 / 当月**平台渠道由钱包支付**的计费金额 | `429 spend_limit_exceeded`，消息说明是哪个限额及重置时间，`Retry-After` 为到重置的秒数 | 组时区每天 0 点 / 每月 1 日 0 点 |
| Key `policy.spendLimit` | 该 Key 在窗口内平台渠道由钱包支付的计费金额 | 同上（`total` 窗口不带 `Retry-After`，消息提示不会重置） | `day` / `week`（周一）/ `month`：按 Key 所有者所在组的时区；`total` 不重置 |

- 检查位置：`rpm` / `rpd` 在第一次尝试任何渠道之前（自有、共享渠道同样受约束）；消费限额在第一次尝试平台渠道之前，与套餐 / 钱包检查同一位置，
  只对由钱包付费的已定价请求检查——自有 / 共享渠道、套餐覆盖的请求和没有售价的模型不受消费限额约束，套餐覆盖的金额也不计入消费限额。
- 消费限额与 `billing.enforce` 无关：关闭强制计费时同样按请求日志的计费金额统计和拦截。强制计费时，钱包预留额除了不超过可用余额，
  还不超过各消费限额中最小的剩余额度。
- 都是**软上限**：计数在请求结算时累加（与钱包扣费在同一事务中，表 `usage_counters`），并发请求在入账前都能通过检查，可能略微超出。
- `rpm` 是每个实例进程内的令牌桶：多实例部署时每个实例各自允许 `rpm` 次（总上限约为 `rpm × 实例数`）。Key 自己的 `policy.rpm` 另外生效。
- 换组后使用新组的时区计算窗口；不同时区的“当天”是不同的窗口（换到时区不同的组相当于开始新的一天）。
- 达到 80% 与达到上限时分别发送 `limit.spend_near` / `limit.spend_reached`（每个限额每个窗口一次，见 [16.2](#162-事件与接收者)）。
- 用户在控制台查看 `GET /api/billing/limits`：组的限额、今日已用请求数、今日 / 本月消费、重置时间，以及每个 Key 的消费上限、当前窗口已用金额与重置时间。
- 计数表的旧窗口随每日保留任务清理（日 / 周窗口保留 60 天，月窗口保留 400 天，`total` 永久保留）。

示例：给“试用”组设置每分钟 20 次、每天 500 次、每天最多消费 2 元：

```bash
curl -sS https://gateway.example.com/api/admin/groups \
  -H 'Cookie: og_session=<会话>' -H 'X-Requested-With: XMLHttpRequest' -H 'Content-Type: application/json' \
  -d '{"name":"试用","priceMultiplier":"1","timezone":"Asia/Shanghai",
       "limits":{"rpm":20,"rpd":500,"dailySpend":"2","monthlySpend":null}}'
```

给 CI 用的 Key 设置每周 20 元的上限（`PATCH /api/keys/{id}`，`policy` 整体替换，需带上其他策略字段与 `version`）：

```json
{"policy": {"allowedModels": [], "allowedChannels": [], "ipAllowlist": [], "rpm": null, "compatMode": "strict",
            "quotaOverflow": "", "spendLimit": {"amount": "20", "window": "week"}}, "version": 3}
```

### 17.4 分时价格

价格版本（售价与成本价）可以带 `schedule` 与 `scheduleTimezone`（IANA，默认 `Asia/Shanghai`）：

| 字段 | 说明 |
|---|---|
| `days` | 0 = 周日 … 6 = 周六；空数组 = 每天。指时段**开始**的那一天 |
| `start` / `end` | `HH:MM`，`start` 含、`end` 不含；`end` 可以是 `24:00`；`start > end` 表示跨零点（零点后的部分属于第二天） |
| `multiplier` | 0–10 的十进制，作用于该版本的全部单价（含 `perRequest`、`perImage`） |

- 按**请求开始时间**（在 `scheduleTimezone` 中）匹配第一个命中的时段，未命中时倍率为 1；时段之间不允许重叠（`422 details.schedule`，
  消息指出第几个时段与哪个时段重叠）。例如 `{"days":[5,6],"start":"22:00","end":"02:00"}` 覆盖周五、周六 22:00–24:00 以及周六、周日 00:00–02:00。
- 分时倍率是价格版本的一部分：修改时段就是新增一个价格版本（见 10.1），历史账单不受影响。
- 售价的分时倍率之后再乘用户组倍率；成本价只乘自己的分时倍率。路由策略 `lowest_cost` 比较成本价时也会乘上当前的分时倍率。
- 模型广场的 `price` 带 `schedule`、`scheduleTimezone` 与此刻生效的 `currentMultiplier`（单价本身是未乘分时倍率的价格）。

**示例：DeepSeek 低谷价**。DeepSeek 在北京时间 00:30–08:30 按半价收费。设置平台渠道的成本价与对用户的售价同步打五折：

```bash
# 成本价（渠道 <channel-id> 上的上游模型 deepseek-chat）
curl -sS https://gateway.example.com/api/admin/prices \
  -H 'Cookie: og_session=<会话>' -H 'X-Requested-With: XMLHttpRequest' -H 'Content-Type: application/json' \
  -d '{"kind":"cost","model":"deepseek-chat","channelId":"<channel-id>","inputPerM":"2","outputPerM":"8",
       "cacheReadPerM":"0.5","scheduleTimezone":"Asia/Shanghai",
       "schedule":[{"days":[],"start":"00:30","end":"08:30","multiplier":"0.5"}]}'

# 售价（逻辑模型 deepseek-chat）
curl -sS https://gateway.example.com/api/admin/prices \
  -H 'Cookie: og_session=<会话>' -H 'X-Requested-With: XMLHttpRequest' -H 'Content-Type: application/json' \
  -d '{"kind":"sell","model":"deepseek-chat","inputPerM":"2.4","outputPerM":"9.6","cacheReadPerM":"0.6",
       "scheduleTimezone":"Asia/Shanghai",
       "schedule":[{"days":[],"start":"00:30","end":"08:30","multiplier":"0.5"}]}'
```

北京时间 03:00 开始的请求按 1.2 / 4.8（每百万输入 / 输出 token）计费；“VIP”组（倍率 0.8）的用户在同一时间按 0.96 / 3.84 计费，
请求日志的 `priceMultiplier` 为 `"0.4"`；09:00 开始的请求按原价（VIP 为 1.92 / 7.68，`priceMultiplier` 为 `"0.8"`）。
如果上游的低谷时段只在工作日，把 `days` 设为 `[1,2,3,4,5]`。

### 17.5 按上下文长度的阶梯价格

价格版本（售价与成本价）可以带最多 5 档 `tiers`（`docs/contracts/phase10-api.md` §1）。请求的**提示 token 数**（输入 + 缓存读取 + 缓存写入）
**大于**某档的 `aboveInputTokens` 时，这次请求的**全部** token（输入、输出、缓存、图片 / 音频 token）都按该档计价；命中多档时取门槛最高的一档，
等于门槛仍按较低一档。`perRequest`、`perImage`、`perMinute`、`perMCharacters` 不分档；分时倍率与用户组倍率照常相乘。

| 字段 | 说明 |
|---|---|
| `aboveInputTokens` | 门槛，1–1 000 000 000 的整数，各档严格递增 |
| `inputPerM` / `outputPerM` | 本档单价，必填 |
| `cacheReadPerM` / `cacheWritePerM` / `imageInputPerM` / `audioInputPerM` / `audioOutputPerM` | 可选；省略或 `null` 沿用基础价格的同名字段 |

- 预扣按估算的提示 token 数选档，结算按上游报告的实际用量选档；套餐的 `charge` 计量同样按选中的档。
- 请求日志的 `priceTier` 记录售价命中的档（门槛），控制台显示为“长上下文档 >272K”。
- 模型广场的 `price.tiers` 已展开继承并乘用户组倍率。

**示例：OpenAI 长上下文价格**（超过 272K 输入 token 时输入 / 缓存翻倍、输出 ×1.5）：

```bash
curl -sS https://gateway.example.com/api/admin/prices \
  -H 'Cookie: og_session=<会话>' -H 'X-Requested-With: XMLHttpRequest' -H 'Content-Type: application/json' \
  -d '{"kind":"sell","model":"gpt-6-astra","inputPerM":"10","outputPerM":"50","cacheReadPerM":"1","cacheWritePerM":"12.5",
       "tiers":[{"aboveInputTokens":272000,"inputPerM":"20","outputPerM":"75","cacheReadPerM":"2","cacheWritePerM":"25"}]}'
```

300 000 输入 token、1 000 输出 token 的请求按 300 000 × 20 / 1M + 1 000 × 75 / 1M = 6.075 计费；272 000 输入 token 的请求按基础价
272 000 × 10 / 1M + 1 000 × 50 / 1M = 2.77 计费。

## 18. 会话亲和与上游号池缓存命中

契约见 [phase12-api.md](../contracts/phase12-api.md)。

### 18.1 作用

提示词缓存（prompt cache）只在**同一个上游账号**上生效。OmniGate 在多个渠道间轮询、加权随机或故障切换时，同一段对话的请求会落到不同渠道，
上游缓存命中率随之下降。会话亲和按客户端**自己的**会话标识把同一会话固定到上次成功处理它的渠道：

- Codex CLI：请求体 `prompt_cache_key`（会话 id），或请求头 `Session_id` / `Session-Id`；
- Claude Code：请求体 `metadata.user_id`（含会话 id），或请求头 `X-Claude-Code-Session-Id`；
- 其他客户端（普通 OpenAI Chat 客户端等）什么都不发送时：**对话锚点**——开头的 system / developer 指令加第一条用户消息的指纹，
  同一段对话后续轮次不变，不同对话不同。

默认开启，内置三条规则（都是“优先保持”：绑定的渠道失败时仍按正常重试规则换渠道，可用性优先），按顺序：

1. `gpt session`：所有 `gpt-*` 模型，不限入口（Chat、Responses、Claude Code 走 `/v1/messages` 调用 GPT 也算），依次取上面的标识，
   最后用对话锚点兜底；并给 OpenAI 格式的上游请求补全会话标识（见 18.2）；
2. `codex cli trace`、`claude cli trace`：其他模型的 Codex CLI / Claude Code 请求。

规则命中时还会把会话相关的客户端请求头（`Session_id`、`X-Codex-Turn-Metadata`、`X-Stainless-*`、`Anthropic-Beta`、客户端的 `User-Agent` 等）
转发给上游。

### 18.2 上游号池缓存命中

AxonHub 等上游网关 / 账号池同样是根据客户端原生的请求头和字段（`Session_id`、`prompt_cache_key`、`metadata.user_id` 等）识别会话，
并把同一会话留在同一个上游账号上。以前 OmniGate 只向 OpenAI 类渠道转发极少的客户端请求头，上游因此认不出会话；现在内置规则默认透传这些
标识，`prompt_cache_key` / `safety_identifier` 在 Responses ⇄ Chat 转换时也会保留，**不需要任何针对具体上游的配置**。
如果渠道本身配置了同名请求头（渠道 `config.headers` 或插件），默认保留渠道的值（`keep_origin`）。

客户端自己没有会话标识、或标识在协议转换中丢失时（Claude Code 调用 GPT：Anthropic → OpenAI 转换把 `metadata.user_id` 变成哈希后的
`user`，号池不把它当作会话），`gpt session` 规则给 **OpenAI 格式**（Chat / Responses）的上游请求补全两个标准标识：

- 请求头 `Session_id`：按对话稳定的 UUID。它是 Codex 原生的会话请求头，OpenAI 格式号池（例如默认配置的 AxonHub）与 Codex 后端都以它
  识别会话，所以普通 Chat 客户端和 Claude Code → GPT 的请求在上游也会留在同一个账号上，**上游无需任何配置或重启**；
- 请求体 `prompt_cache_key`：`og-` 开头的稳定值（OpenAI 的标准字段，用于提示词缓存路由）。

两者都按用户、按对话由绑定键派生（不发送原始会话标识），客户端已带上的值（或渠道配置的同名请求头）永远不会被覆盖；Anthropic 格式的上游不补全。
规则编辑抽屉中对应“补全 prompt_cache_key”开关与“补全会话请求头”输入框，自定义规则也可以使用。

可选（**不是必需的**）：如果上游号池支持把请求体字段当作会话标识，也可以让它额外识别标准字段 `prompt_cache_key`，例如 AxonHub 的
`server.trace.extra_trace_body_fields`（环境变量 `AXONHUB_SERVER_TRACE_EXTRA_TRACE_BODY_FIELDS=prompt_cache_key`）。这只是通用上游选项的
一个例子，OmniGate 不依赖它。

部署在 nginx 后面时，务必开启 `underscores_in_headers on;`（见 [7. 反向代理](#7-反向代理)），否则 `Session_id` 这类请求头在到达 OmniGate 前就被丢弃。

### 18.3 配置

在“系统设置 → 会话亲和”中编辑（需要 `settings.write`），也可以通过 `PATCH /api/admin/settings` 的 `gateway.affinity` 修改；保存后立即生效（其他实例 5 秒内）。

- 规则格式与 new-api 的“渠道亲和”相同，new-api 的 JSON 可以直接粘贴到 JSON 编辑器。Key 来源支持 `gjson`（请求体路径）、`request_header`
  与 OmniGate 扩展的 `anchor`（对话锚点，建议放在最后兜底）；规则的 OmniGate 扩展选项 `inject_prompt_cache_key`、`inject_session_header` 见 18.2；
  `param_override_template` 只支持 `pass_headers`；`Authorization`、`x-api-key`、`Cookie`、`Host`、`Content-Length` 等不能透传。
- 会话保持模式：`off`（只透传请求头）、`prefer`（默认）、`strict`（绑定渠道失败直接返回错误，缓存优先、可用性较低）。规则可单独设置，
  也可继承全局；new-api 的 `skip_retry_on_failure: true` 等同于 `strict`。
- 绑定键总是包含用户本身（不同用户的会话互不影响），并按规则勾选的作用域加入用户组、模型、规则名称；内存中只保存其 SHA-256，日志不记录原始值。
- “填充模板”恢复内置预设（同名规则被替换，其他规则保留；新规则按预设顺序插入）；“恢复默认”删除数据库中的值，回到内置预设。
- **升级提示**：会话亲和设置保存过（来源为“数据库”）的实例不会自动获得新的 `gpt session` 预设。点击“填充模板”加入它，确认它排在
  第一位（或至少排在其他会处理 GPT 请求的规则之前，可用“上移”），再保存。

### 18.4 运维

- 绑定保存在进程内存中（LRU，默认最多 100 000 条，每次命中续期，默认 TTL 3600 秒），**重启后清空**；只支持单实例部署
  （多实例时各实例各自绑定，彼此不共享）。设置页底部显示当前条目数，可清空全部或某条规则的绑定（写审计日志 `affinity.clear`）。
- 绑定只影响同一层级内的顺序：自有 → 共享 → 平台的层级顺序（计费边界）不变；权限、模型、熔断等筛选照常生效。绑定的渠道被停用、熔断或不再可用时
  按正常路由处理（默认丢弃绑定；开启“渠道不可用时保留绑定”则保留，渠道恢复后会话回到原渠道）。
- 请求日志记录每个请求的结果（`hit` 命中、`new` 新绑定、`rebound` 改绑、`failover` 未命中、`broken` 绑定失效、`strict_failed` 严格失败、
  `miss` 未绑定、`off` 仅透传）与规则名称，可在请求日志页按结果过滤。统计页显示提示词缓存命中率（缓存读取 ÷ 输入 token，含缓存读写），
  按渠道细分，以及会话亲和命中率，用来确认效果。

