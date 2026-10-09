# OmniGate 部署指南（单镜像）

> 要把 OmniGate 部署到生产域名（本项目为 `https://gate.example.com`），请直接按
> [deploy-production.md](deploy-production.md) 一步步操作（准备服务器、配置、反向代理与 HTTPS、首次登录、备份恢复、升级、排障）。
> 本文是镜像与部署方式的参考。

OmniGate 以**一个 Docker 镜像、一个端口**交付：前端页面编译进 Go 二进制，同一个端口同时提供管理后台、控制面 API（`/api`）、
模型网关（`/v1`）和健康检查。数据库有两种选择（[ADR-0009](../adr/0009-sqlite-lightweight-mode.md)）：

- **SQLite（镜像默认，零依赖）**：不设置 `OMNIGATE_DATABASE_URL` 时使用卷中的 `/data/omnigate.db`，一个容器即可运行，
  适合个人、小团队和内网工具（中低写入量），见[第 2 节](#2-快速开始单容器--sqlite零依赖)。
- **PostgreSQL**：较高写入量、或希望使用数据库自身的高可用与备份时使用，见第 3、4 节。

**镜像中不包含 PostgreSQL 或任何数据库服务**，数据库只由 `OMNIGATE_DATABASE_URL` 选择。**只支持单实例部署**：无论使用哪种数据库，
同一时间只运行一个 OmniGate 容器（限流、熔断、后台任务与本地结算日志都在进程内）。Redis 当前版本未使用，不需要部署。

环境变量的完整说明见 [configuration.md](configuration.md)，反向代理见其[第 7 节](configuration.md#7-反向代理)，
登录配置见[第 2 节](configuration.md#2-登录配置)。本文只讲部署、升级与备份。

## 1. 镜像与端口

| 路径 | 说明 |
|---|---|
| `/v1/*` | 模型网关（OpenAI / Anthropic 协议），使用 `og-` 开头的 API Key |
| `/api/*` | 控制面 API（会话 Cookie + CSRF 校验）；未知接口返回 JSON 404 |
| `/healthz` | 存活检查，只说明进程在运行 |
| `/readyz` | 就绪检查：数据库可连接且没有待执行迁移，否则 503；结算 journal 有待重放条目时仍为 200，但 `status` 为 `degraded`（`journal.pending` 为条数，见 [configuration.md 10.2](configuration.md#102-billingenforce-开关)） |
| `/assets/*` | 前端静态资源（文件名带内容哈希，长期缓存） |
| 其他任何路径 | 前端页面（`/`、`/login`、`/console/...` 等，由前端路由决定） |

- 容器内监听 `8080`（`OMNIGATE_HTTP_ADDR`），镜像只 `EXPOSE 8080`。
- Prometheus 指标在**另一个端口** `9090`（`OMNIGATE_METRICS_ADDR`，可设为 `off`），镜像不暴露、Compose 不发布，只供同一网络内的 Prometheus 抓取；主端口上的 `/metrics` 返回 404。
- 运行时基础镜像为 `gcr.io/distroless/static-debian12:nonroot`：以 uid/gid `65532` 运行，没有 shell 和包管理器；CA 证书由基础镜像提供，IANA 时区库编译在二进制内（套餐的 calendar 窗口依赖时区），无需设置 `TZ`。
- `VOLUME /data`：工作目录与持久化目录，**请始终挂载卷**。镜像设置了 `OMNIGATE_DATABASE_URL=sqlite:///data/omnigate.db`，
  SQLite 模式下全部数据（`omnigate.db` 及 WAL 文件 `omnigate.db-wal`、`omnigate.db-shm`）都在这里；使用 PostgreSQL 时
  设置 `OMNIGATE_DATABASE_URL=postgres://…` 覆盖即可。无论哪种数据库，`/data` 还保存结算日志 `settlement-journal.jsonl`
  （`OMNIGATE_DATA_DIR`，数据库暂时不可用时尚未写入的结算，恢复后自动重放）与可选的自定义前端（见[第 7 节](#7-前端页面)），卷必须保留。
- 启动时先校验配置（生产模式下缺少主密钥、引导管理员或登录方式等会在**连接数据库之前**退出），再执行数据库迁移
  （`OMNIGATE_AUTO_MIGRATE=false` 可关闭），迁移完成后才开始监听。
- 镜像内置 `HEALTHCHECK`：`omnigate healthcheck` 子命令请求本机 `/healthz`（读取 `OMNIGATE_HTTP_ADDR` 推导端口），非 200 时退出码为 1。
- 收到 `SIGTERM` 后最多等待 30 秒让进行中的请求（包括流式响应）完成；`docker stop` 请给足时间（`--time 40`，Compose 已设置 `stop_grace_period: 40s`）。

### 构建镜像

```bash
make docker                          # 构建 omnigate:dev
make docker IMAGE=omnigate:v0.3.0 VERSION=v0.3.0
# 等价于：
docker build --build-arg VERSION=v0.3.0 -t omnigate:v0.3.0 .
```

构建分三个阶段：Node（`pnpm install --frozen-lockfile`、`pnpm build`、预压缩）→ Go（把 `web/dist` 复制进
`server/internal/webui/dist` 后 `CGO_ENABLED=0 go build`）→ distroless 运行时。`VERSION` 会写进二进制，即 `/api/system/info`
返回的 `version`。三个基础镜像都固定到精确版本与摘要（`node:24.21.0-alpine`、`golang:1.26.9-alpine`、distroless），构建阶段设置
`GOTOOLCHAIN=auto`，保证二进制不低于 `server/go.mod` 的 `toolchain go1.26.9`（`go version -m` 可核对；CI 同时运行 `govulncheck`）。`.dockerignore` 排除了 `.env*`、`*.local`、`.git`、`node_modules`、本地构建产物和文档，镜像中不包含任何密钥。

发布的镜像：GitHub Actions 在 `main` 与 `v*` 标签上构建并推送 `ghcr.io/okysu/omnigate`（amd64 + arm64，见 README「CI / 发布」），
构建参数 `VERSION`、`COMMIT`、`BUILD_TIME` 写进二进制（`omnigate version`、启动日志）。

离线分发：`docker save omnigate:v0.3.0 | gzip > omnigate-v0.3.0.tar.gz`，目标机器上 `docker load < omnigate-v0.3.0.tar.gz`。

不用 Docker 时，`make build` 生成同样嵌入前端的单个二进制 `bin/omnigate`（需要 Go、Node.js 与 pnpm）。

## 2. 快速开始：单容器 + SQLite（零依赖）

不需要数据库实例，只要一个卷和一个主密钥（完整的环境文件模板：`deploy/omnigate.env.example`；用 Compose 部署见 [deploy-production.md](deploy-production.md)）：

```bash
docker run --rm omnigate:v0.3.0 keygen        # 输出形如 k20261008-a1b2c3:BASE64...
install -m 600 /dev/null omnigate.env
cat > omnigate.env <<'EOF'
OMNIGATE_MASTER_KEY=k20261008-a1b2c3:BASE64...
OMNIGATE_ENV=production
OMNIGATE_PUBLIC_URL=https://gateway.example.com
OMNIGATE_BOOTSTRAP_ADMINS=github-id:583231
OMNIGATE_AUTH_GITHUB_CLIENT_ID=...
OMNIGATE_AUTH_GITHUB_CLIENT_SECRET=...
EOF

docker run -d --name omnigate --restart unless-stopped \
  --env-file omnigate.env \
  -p 127.0.0.1:8080:8080 \
  -v omnigate-data:/data \
  --stop-timeout 40 \
  omnigate:v0.3.0

curl -fsS http://127.0.0.1:8080/readyz     # {"migrations":{"dialect":"sqlite",...},"status":"ready"}
```

- 本机试用可以只设 `OMNIGATE_MASTER_KEY` 和一种登录方式，`OMNIGATE_ENV` 留空（`development`）、
  `OMNIGATE_PUBLIC_URL=http://localhost:8080`。
- 数据库文件是 `omnigate-data` 卷中的 `/data/omnigate.db`（首次启动自动创建并迁移）。卷必须在**本地文件系统**上，
  不要放在 NFS/SMB 等网络文件系统上（SQLite 的文件锁在其上不可靠）。
- 只能运行**一个**实例：不要让两个容器同时挂载同一个卷运行（升级时先停旧容器再启动新容器）。
- 换位置：`OMNIGATE_DATABASE_URL=sqlite:///data/sub/omnigate.db`（绝对路径三个斜杠；`sqlite://relative.db` 相对工作目录 `/data`），
  目录不存在时自动创建。
- 备份见[第 8 节](#8-备份)：`docker exec omnigate omnigate backup /data/backups/omnigate-$(date +%Y%m%d-%H%M%S).db`。

### SQLite 与 PostgreSQL 差异

| | SQLite | PostgreSQL |
|---|---|---|
| 部署 | 单容器，零依赖 | 需要数据库实例 |
| 实例数 | **单实例**（不能多个进程/容器共享一个数据库文件） | **单实例**（多实例部署不在支持范围内） |
| 写入 | **整库串行**：写事务一律 `BEGIN IMMEDIATE`，并在进程内排队；事务都很短，读不受影响（WAL） | 行级锁，并发写 |
| 吞吐参考 | 开发机压测（假上游、计费开启、32 并发）约 2,200–2,400 请求/秒 | 同场景（单个钱包行锁 + 每条语句一次网络往返）约 300–500 请求/秒；多用户并发写入更平稳 |
| 请求日志 | 单表，按保留天数 `DELETE` | 按月分区，过期分区直接删除 |
| 配额用量 | 十进制文本，在写事务内用 Go 精确相加 | `numeric(38,9)` |
| 持久性 | WAL + `synchronous=NORMAL`：断电可能丢失最后几个已提交事务，数据库不会损坏 | 每次提交落盘（默认） |
| 备份 | `omnigate backup <文件>`（`VACUUM INTO`，运行中可用），或停机复制卷 | `pg_dump` |
| 相互迁移 | `omnigate migrate-db`（停机迁移，双向），见[第 9 节](#9-sqlite-迁移到-postgresql以及反向) | 同左 |

两种数据库上统计结果一致（请求数、金额汇总、分位延迟、按日/模型/渠道分组）；自动化测试在两种数据库上各跑一遍。
写入量较高、或需要数据库层面的高可用与时间点恢复时请使用 PostgreSQL。小差异：SQLite 的 `lower()` / `LIKE` 只对 ASCII 字母做大小写折叠
（搜索非 ASCII 大小写字母时可能不匹配）；文本排序为字节序（PostgreSQL 取决于数据库排序规则）。

## 3. 快速开始：`docker run` + 已有 PostgreSQL

适合已经有 PostgreSQL（云数据库或现有实例）的情况。先为 OmniGate 建库和用户：

```sql
CREATE USER omnigate WITH PASSWORD '强密码';
CREATE DATABASE omnigate OWNER omnigate;
```

生成主密钥并写入一个只有自己可读的环境文件：

```bash
docker run --rm omnigate:v0.3.0 keygen        # 输出形如 k20261008-a1b2c3:BASE64...
install -m 600 /dev/null omnigate.env
cat > omnigate.env <<'EOF'
OMNIGATE_ENV=production
OMNIGATE_PUBLIC_URL=https://gateway.example.com
OMNIGATE_DATABASE_URL=postgres://omnigate:强密码@db.example.com:5432/omnigate?sslmode=require
OMNIGATE_MASTER_KEY=k20261008-a1b2c3:BASE64...
OMNIGATE_BOOTSTRAP_ADMINS=github-id:583231
OMNIGATE_AUTH_GITHUB_CLIENT_ID=...
OMNIGATE_AUTH_GITHUB_CLIENT_SECRET=...
EOF
```

启动：

```bash
docker run -d --name omnigate --restart unless-stopped \
  --env-file omnigate.env \
  -p 127.0.0.1:8080:8080 \
  -v omnigate-data:/data \
  --stop-timeout 40 \
  omnigate:v0.3.0

docker logs -f omnigate                  # 看到 "omnigate listening" 即启动完成
curl -fsS http://127.0.0.1:8080/readyz
```

- 数据库在宿主机本机时，容器内的 `localhost` 不是宿主机：Linux 上加 `--add-host=host.docker.internal:host-gateway`，
  连接串主机写 `host.docker.internal`，并确认 PostgreSQL 监听了 Docker 网桥地址、`pg_hba.conf` 放行了对应网段。
- 连接串中的密码含 `@`、`/`、`:`、`#` 等字符时需要百分号编码（如 `@` → `%40`）。
- 本机试用可以不设 `OMNIGATE_ENV`（默认 `development`），`OMNIGATE_PUBLIC_URL=http://localhost:8080`，此时允许 http。
- 前面有反向代理时还要设置 `OMNIGATE_TRUSTED_PROXIES` 为代理到达容器时的**精确地址**（不要用 `172.16.0.0/12` 这类大网段），
  见 [deploy-production.md 4.7](deploy-production.md#47-反向代理与真实-ip)。
- 管理命令直接以子命令运行：`docker exec omnigate omnigate migrate status`、`docker run --rm omnigate:v0.3.0 version`。

## 4. Compose 部署（`deploy/`）

`deploy/docker-compose.yml` 默认只启动 `omnigate` 一个服务（镜像 `ghcr.io/okysu/omnigate:${OMNIGATE_TAG:-latest}`）：端口只绑定 `127.0.0.1:8080`、`/data` 卷、`stop_grace_period: 40s`、
健康检查、日志轮转、内存上限，以及固定网段 `172.30.88.0/24`（让 `OMNIGATE_TRUSTED_PROXIES` 可以精确写成网关地址 `172.30.88.1`）。
OmniGate 的配置在 `deploy/omnigate.env`（模板 `omnigate.env.example`），Compose 自己的变量（镜像 tag 等）在 `deploy/.env`。

可选的捆绑服务定义在同一个文件里，用 profile 开启（“all in one” 由编排完成，镜像本身始终只有 OmniGate）：

| profile | 作用 |
|---|---|
| `postgres` | 顺带运行一个 PostgreSQL（不发布端口）；`omnigate.env` 中连接串主机写 `postgres` |
| `caddy` | 用容器运行 Caddy（自动 HTTPS，发布 80/443，域名取 `OMNIGATE_DOMAIN`） |

`docker compose --profile postgres --profile caddy up -d`，或在 `deploy/.env` 中设置 `COMPOSE_PROFILES=postgres,caddy` 后直接 `docker compose …`。
旧的叠加写法 `-f docker-compose.yml -f docker-compose.postgres.yml`（自动按 `POSTGRES_PASSWORD` 设置连接串）与 `-f … -f docker-compose.caddy.yml` 仍然可用。
完整步骤见 [deploy-production.md](deploy-production.md)。

> 仓库根目录的 `docker-compose.yml` 是源码构建试用 + 本地开发依赖（PostgreSQL、模拟 OIDC）的组合，
> `docker compose --profile dev up -d postgres mock-oidc` 用于本地开发，见 [configuration.md 第 5、6 节](configuration.md#5-本地开发流程)。

## 5. 反向代理与 HTTPS

生产环境必须由反向代理（nginx、Caddy、Traefik）终止 TLS，再转发到 OmniGate 的单个端口。所有路径转发到同一个上游即可，
无需为前端单独配置静态目录。现成配置：`deploy/Caddyfile`、`deploy/nginx/omnigate.conf`（步骤见 [deploy-production.md 第 6 节](deploy-production.md#6-反向代理与-https)）。
要点（详见 [configuration.md 第 7 节](configuration.md#7-反向代理)）：

- 关闭响应缓冲（SSE 流式响应）、读超时 ≥ 10 分钟，请求体上限 64 MiB（图片编辑、语音转写）；
- 设置 `OMNIGATE_TRUSTED_PROXIES` 为代理到达容器时的精确地址；
- OmniGate 端口只绑定 `127.0.0.1` 或用防火墙屏蔽，9090 不对外。

## 6. 健康检查

| 检查 | 用途 |
|---|---|
| `GET /healthz` → 200 `{"status":"ok"}` | 存活探针 / 容器 `HEALTHCHECK`（`omnigate healthcheck`） |
| `GET /readyz` → 200 或 503 | 就绪探针、负载均衡摘除；数据库不可达或有待执行迁移时 503 |

Kubernetes 可直接用 `httpGet` 探针；Compose / `docker run` 使用镜像内置的 `HEALTHCHECK` 即可。

## 7. 前端页面

- 页面由二进制内嵌提供，与后端版本严格一致；升级镜像即升级前端。
- 缓存：`/assets/*`（带哈希的文件名）返回 `Cache-Control: public, max-age=31536000, immutable`；`index.html` 及其他文件返回
  `no-cache` + `ETag`（每次协商，未变化时 304）。构建时为 JS/CSS 等生成了 `.br` / `.gz` 预压缩版本，按 `Accept-Encoding` 返回。
- 路由回退：不以 `/api`、`/v1`、`/healthz`、`/readyz`、`/metrics` 开头、且不对应实际文件的 GET/HEAD 请求一律返回 `index.html`，
  前端路由（`/login`、`/console/...` 等）可以直接刷新或分享链接。例外：`/assets/` 下不存在的文件、以及看起来像静态文件
  （`.js`、`.css`、`.png`、`.txt` 等扩展名）的路径返回 404，避免旧版本页面把 HTML 当脚本加载；非 GET/HEAD 请求返回 405。
- 安全头（仅 HTML）：`Content-Security-Policy`（脚本只允许本站与 `index.html` 中内联脚本的 sha256 哈希；样式允许内联，
  因为 UI 组件与 Monaco 编辑器会写入内联样式；图片允许 `https:` 以显示 GitHub/OIDC 头像；`connect-src 'self'`；
  Worker 允许本站与 `blob:`；`frame-ancestors 'none'`）、`X-Frame-Options: DENY`、`Referrer-Policy: same-origin`、
  `Cross-Origin-Opener-Policy: same-origin`、`Permissions-Policy`。所有响应带 `X-Content-Type-Options: nosniff`。
- `OMNIGATE_WEB_ENABLED=false`：不提供页面，非 API 路径返回 JSON 404（纯 API 部署，或由反向代理在同一域名下另行托管前端时使用）。
- `OMNIGATE_WEB_DIR=/data/web`：用磁盘上的一份前端构建替代内嵌版本（例如定制品牌），目录中必须有 `index.html`。
  例如把自定义的 `web/dist` 复制到 `omnigate-data` 卷的 `web/` 子目录。替换文件即时生效，无需重启。
  自定义前端需要额外的外部资源（字体 CDN、统计脚本）时会被 CSP 拦截，请改为同源托管。

## 8. 备份

需要备份的只有两样：**数据库**和**主密钥（整个 `.env`）**，二者分开保存。备份内容的详细说明与恢复注意事项见
[configuration.md 第 13 节](configuration.md#13-备份与恢复)。

SQLite（单容器）：运行中即可生成一致的副本（`VACUUM INTO`，不阻塞网关），再把它拷出容器。目标文件已存在时会拒绝覆盖，所以文件名带时间戳：

```bash
ts=$(date +%Y%m%d-%H%M%S)
docker exec omnigate omnigate backup /data/backups/omnigate-$ts.db
docker cp omnigate:/data/backups/omnigate-$ts.db .
```

恢复：停止容器，把备份放回卷中并**改为 uid 65532 所有**（容器以 65532 运行；文件属于 root 时启动报
`attempt to write a readonly database`），同时移走旧的 `-wal`、`-shm` 与结算日志：

```bash
docker stop -t 40 omnigate
docker run --rm -v omnigate-data:/data -v "$PWD":/backup:ro alpine:3 sh -euc '
  old=/data/pre-restore-$(date +%Y%m%d-%H%M%S); mkdir -p "$old"
  for f in omnigate.db omnigate.db-wal omnigate.db-shm settlement-journal.jsonl; do
    if [ -e "/data/$f" ]; then mv "/data/$f" "$old/"; fi
  done
  cp "/backup/$1" /data/omnigate.db && chown 65532:65532 /data/omnigate.db && chmod 600 /data/omnigate.db
' restore omnigate-20261009-030000.db
docker start omnigate
```

不要在运行中直接复制 `omnigate.db`（WAL 中可能还有尚未合并的数据）；停机后复制整个卷也可以。
Compose 部署（卷名 `omnigate_omnigate-data`）的完整命令与定时备份见 [deploy-production.md 第 9 节](deploy-production.md#9-备份与恢复)。

PostgreSQL（Compose 的 `postgres` profile，在 `deploy/` 目录下执行）：

```bash
docker compose exec -T postgres pg_dump -U omnigate -d omnigate -Fc > omnigate-$(date +%Y%m%d-%H%M%S).dump
# 恢复：删库重建后导入。不要用 pg_restore --clean：请求日志是分区表，会报 "cannot drop inherited constraint"
docker compose stop omnigate
docker compose exec -T postgres dropdb -U omnigate omnigate
docker compose exec -T postgres createdb -U omnigate -O omnigate omnigate
docker compose exec -T postgres pg_restore -U omnigate -d omnigate --no-owner --exit-on-error < omnigate-20261009-030000.dump
docker compose start omnigate
```

外部数据库：用数据库自身的备份机制，或 `pg_dump "$OMNIGATE_DATABASE_URL" -Fc > omnigate.dump`；恢复到一个空库（`pg_restore --no-owner --exit-on-error`）。

## 9. SQLite 迁移到 PostgreSQL（以及反向）

`omnigate migrate-db` 把一个库的全部数据复制到另一个**空库**，两个方向都支持（SQLite → PostgreSQL、PostgreSQL → SQLite，
同类之间也可以）。典型场景：单容器 SQLite 起步，用户和流量多了以后换成 PostgreSQL。

```text
omnigate migrate-db --from <源库地址> --to <目标库地址> [--batch 1000] [--dry-run] [--force-empty-check=false]
```

地址写法与 `OMNIGATE_DATABASE_URL` 相同（`sqlite:///data/omnigate.db`、`postgres://user:pass@host:5432/db?sslmode=…`）。
这个子命令不读取 `.env` 中的其他变量，也不需要主密钥。

它做的事情：

1. 读取两边的结构版本；任意一边比当前二进制新（用新版本写过）就拒绝——请用与线上服务**同一版本**的镜像执行。
2. 把落后的一边迁移到最新版本（已是最新的源库不会被写入）。
3. 检查目标库是空的：除迁移自带的初始数据外，所有表都没有数据；否则拒绝（见下文 `--force-empty-check`）。
4. 在**一个只读快照**中读取源库（PostgreSQL：`REPEATABLE READ READ ONLY` 事务、会话默认只读；SQLite：`mode=ro` 只读打开），
   在**一个事务**中写入目标库：按外键依赖顺序逐表复制，PostgreSQL 目标用 `COPY` 批量写入，SQLite 目标在复制期间关闭外键检查、
   提交前执行 `PRAGMA foreign_key_check`。任何错误都整体回滚，目标库保持原样。
5. 表、列和外键顺序都从目标库的结构中自动读取，类型按列转换：`uuid` ↔ 文本、`timestamptz` ↔ RFC 3339 文本（UTC、9 位小数）、
   `jsonb` ↔ JSON 文本、`numeric(38,9)` ↔ 十进制文本、`bytea` ↔ BLOB、`boolean` ↔ 0/1。请求日志写入 PostgreSQL 时，
   先为源数据涉及的每个月建好月分区；复制后重置自增序列（如有）。
6. 校验：每张表的行数、所有金额列（`*_nano`，如钱包余额、流水、请求日志 charge）的合计在两边必须一致，否则报错并以非零退出码结束。

加密数据（渠道凭据、SMTP 密码、Webhook 签名密钥等）**原样复制**，目标库必须使用**同一个 `OMNIGATE_MASTER_KEY`** 才能解密。

### 9.1 步骤（SQLite → PostgreSQL）

以第 2 节的单容器部署为例（卷 `omnigate-data`、环境文件 `omnigate.env`、镜像 `omnigate:v0.3.0`）：

1. **准备空的 PostgreSQL 库**（第 3 节的 `CREATE USER` / `CREATE DATABASE`）。这个库不能被 OmniGate 启动过——
   服务首次启动会写入币种设置、内置插件等数据，库就不再是空的。
2. **停服务**。迁移按快照复制，停机之后的写入不会丢，停机期间网关不可用（数据量小时通常不到一分钟）：
   ```bash
   docker stop -t 40 omnigate
   ```
3. **备份**源库（第 8 节；服务已停，复制卷中的数据库文件即可，连同可能存在的 `-wal`、`-shm` 文件），并确认主密钥备份可用：
   ```bash
   docker run --rm -v omnigate-data:/data -v "$PWD":/backup busybox sh -c 'cp /data/omnigate.db* /backup/'
   ```
4. **试运行**：只检查版本与空库、统计各表行数，两边都不改动：
   ```bash
   docker run --rm -v omnigate-data:/data omnigate:v0.3.0 migrate-db \
     --from sqlite:///data/omnigate.db \
     --to 'postgres://omnigate:强密码@db.example.com:5432/omnigate?sslmode=require' --dry-run
   ```
   数据库在宿主机本机时加 `--network host`（或第 3 节的 `host.docker.internal` 写法）。
5. **迁移**：去掉 `--dry-run` 再执行一次。输出每张表的行数和耗时，最后一行是校验结果：
   ```text
   source: sqlite /data/omnigate.db (schema version 12)
   target: postgres db.example.com:5432/omnigate (schema version 0)
   migrating target from version 0 to 12
   ensured 2 monthly partitions of request_logs (2026-09 – 2026-10)
   copying 40 tables (batch 1000)
     [1/40] audit_logs                            1532 rows  41ms
     …
   verified: 40 tables, 48211 rows; row counts and money sums match (3.2s)
     wallets                          balance_nano=14976000000 reserved_nano=0
     …
   ```
   失败时（例如校验不一致）以非零退出码结束并说明原因，目标库的事务已回滚，可以修正后重来。
6. **切换数据库地址**：在 `omnigate.env` 中加入（或修改）
   `OMNIGATE_DATABASE_URL=postgres://omnigate:强密码@db.example.com:5432/omnigate?sslmode=require`，
   `OMNIGATE_MASTER_KEY` 保持不变。
7. **启动**并验证：
   ```bash
   docker rm omnigate && docker run -d --name omnigate ...   # 与之前相同的参数（env 文件已改）
   curl -fsS http://127.0.0.1:8080/readyz                      # "dialect":"postgres"
   ```
   登录管理后台，抽查：用户与登录（已有会话仍然有效）、钱包余额与流水、请求日志与统计、套餐用量、渠道（发一个测试请求，
   能调用说明渠道密钥解密正常）。
8. 确认无误后再清理旧的 SQLite 文件；在此之前随时可以把 `OMNIGATE_DATABASE_URL` 改回 SQLite 回退（迁移不修改已是最新版本的源库）。

改用 Compose 自带的 PostgreSQL（`deploy/` 中的 `postgres` profile）时，先停 omnigate、只启动数据库，再在 Compose 网络中运行迁移：

```bash
cd deploy                                  # .env 中 COMPOSE_PROFILES=postgres，并设置 POSTGRES_PASSWORD
docker compose stop omnigate
docker compose up -d postgres
docker run --rm --network omnigate_omnigate -v omnigate_omnigate-data:/data omnigate:v0.3.0 migrate-db \
  --from sqlite:///data/omnigate.db \
  --to 'postgres://omnigate:<POSTGRES_PASSWORD>@postgres:5432/omnigate?sslmode=disable'
# omnigate.env：OMNIGATE_DATABASE_URL=postgres://omnigate:<POSTGRES_PASSWORD>@postgres:5432/omnigate?sslmode=disable
docker compose up -d
```

`omnigate_omnigate` 是 Compose 项目 `omnigate` 的网络，`omnigate_omnigate-data` 是其中 SQLite 所在的卷（单容器 `docker run` 部署时为 `omnigate-data`）。

### 9.2 反向：PostgreSQL → SQLite

步骤相同，交换两个地址，目标写一个新文件：

```bash
docker stop -t 40 omnigate
pg_dump "$OLD_DATABASE_URL" -Fc > omnigate-before-sqlite.dump          # 备份
docker run --rm -v omnigate-data:/data omnigate:v0.3.0 migrate-db \
  --from 'postgres://omnigate:强密码@db.example.com:5432/omnigate?sslmode=require' \
  --to sqlite:///data/omnigate.db
# omnigate.env：删除 OMNIGATE_DATABASE_URL（镜像默认即 sqlite:///data/omnigate.db），主密钥不变，然后启动
```

源 PostgreSQL 在一个只读事务中读取，不会被修改（版本落后时除外：会先迁移到最新版本）。注意 SQLite 只适合单实例。

### 9.3 注意事项

- **目标必须是空库**。`--force-empty-check=false` 跳过检查，此时目标库中已有的数据会在同一事务中**被删除并替换**为源库的数据（输出警告，列出被替换的表）。
- 时间精度：SQLite 保存纳秒，PostgreSQL 保存微秒；迁移到 PostgreSQL 时时间截断到微秒，对业务无影响。
- 请求日志较多时迁移时间主要花在这张表上；可以先在管理后台缩短保留天数并让保留任务清理（或接受较长的停机时间）。
  `--batch` 调整每批行数（默认 1000），一般不需要改。
- 迁移工具与服务使用同一套迁移文件；请用与线上相同版本的镜像执行，升级版本和迁移数据库不要同时进行。
- 迁移前后的主密钥必须相同；主密钥轮换（configuration.md 第 3.2 节）请在迁移前或迁移后单独进行。

## 10. 升级与回滚

1. 升级前先备份数据库（第 8 节），确认主密钥备份可用。
2. 获取新镜像（`docker pull ghcr.io/okysu/omnigate:0.4.0` / `docker load` / `make docker IMAGE=omnigate:v0.4.0 VERSION=v0.4.0`）。
3. 切换镜像并重启：
   ```bash
   # Compose（deploy/ 目录）：修改 .env 中的 OMNIGATE_TAG=0.4.0（自建镜像改 OMNIGATE_IMAGE）
   docker compose pull omnigate && docker compose up -d
   # docker run：
   docker stop -t 40 omnigate && docker rm omnigate
   docker run -d --name omnigate ... omnigate:v0.4.0      # 参数与之前相同
   ```
4. 新版本启动时自动执行迁移，完成后才开始监听（单实例：`docker compose up -d` 会先停旧容器再启动新容器）。
   查看状态：`docker compose exec omnigate omnigate migrate status`。
5. 浏览器中已打开的旧页面会继续使用旧的前端资源；刷新后加载新版本（`index.html` 不缓存）。
   旧页面按需加载已不存在的旧分块时会得到 404，刷新即可。

回滚：不支持降级数据库结构，旧版本连接已被新版本迁移过的数据库会拒绝启动（`database schema is newer than this OmniGate binary`）。需要回滚时停止服务 → 用升级前的备份恢复数据库 → 用旧版本镜像启动，详见
[configuration.md 14.2](configuration.md#142-回滚)。因此**请保留上一个版本的镜像**（不要只用 `latest` 一个 tag）。

## 11. 排障

- 镜像内没有 shell：查看日志用 `docker logs`；需要在容器网络里调试时用临时容器，例如
  `docker run --rm --network container:omnigate curlimages/curl -s localhost:8080/readyz`。
- 启动即退出：多半是配置校验失败，日志第一行会列出全部错误（如生产环境缺少 `OMNIGATE_MASTER_KEY`、`OMNIGATE_BOOTSTRAP_ADMINS`、登录方式，`PUBLIC_URL` 不是 https）。
  更多现象与处理见 [deploy-production.md 第 13 节](deploy-production.md#13-排障)。
- `/readyz` 返回 `database_unavailable`：检查 `OMNIGATE_DATABASE_URL`、网络与 `pg_hba.conf`。
- SQLite 启动报 `create sqlite directory` / `unable to open database file`：卷不可写。镜像以 uid 65532 运行，用宿主机目录
  绑定挂载时先 `chown -R 65532:65532 <目录>`（命名卷会自动继承镜像中 `/data` 的属主）。
- SQLite 日志出现 `timed out waiting for the database write lock` 或 `database is locked`：写入量超出单文件能力，
  或有其他进程（如手工打开的 `sqlite3`）长时间持有写锁；负载高时请改用 PostgreSQL。
- 页面显示“前端未构建”：该二进制是在没有前端产物的情况下编译的（直接 `go build`），请使用 `make build` 或 Docker 镜像。
