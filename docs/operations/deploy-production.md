# 生产部署手册：gate.example.com（单镜像、单实例）

本文按顺序一步步把 OmniGate 部署到 `https://gate.example.com`。所有文件都在仓库的 `deploy/` 目录：

| 文件 | 作用 |
|---|---|
| `deploy/docker-compose.yml` | 默认只有 `omnigate` 一个服务（镜像 `ghcr.io/okysu/omnigate`、`/data` 卷、端口只绑定 `127.0.0.1:8080`、健康检查、停机宽限 40 秒、日志轮转、内存上限）；可选的捆绑服务用 profile 开启：`--profile postgres`（PostgreSQL）、`--profile caddy`（自动 HTTPS） |
| `deploy/omnigate.env.example` | OmniGate 的全部环境变量模板，逐项带说明（示例域名 `gate.example.com`，换成你的域名） |
| `deploy/.env.example` | Compose 自己用的变量：镜像 tag、开启哪些 profile、域名、PostgreSQL 密码 |
| `deploy/docker-compose.postgres.yml`、`deploy/docker-compose.caddy.yml` | 兼容旧命令的薄覆盖文件（`-f … -f …`），等同于对应的 profile；新部署不需要 |
| `deploy/Caddyfile` | Caddy 配置（宿主机或容器均可用；域名取环境变量 `OMNIGATE_DOMAIN`） |
| `deploy/nginx/omnigate.conf` | nginx 配置示例（配合 certbot / Let's Encrypt；把其中的 `gate.example.com` 换成你的域名） |
| `deploy/seed/README.md` | 价格与套餐目录：镜像内置的 GPT 目录、首次启动自动导入（`OMNIGATE_SEED_ON_START`）、`omnigate seed` 用法，见第 8 步 |

部署时需要的两份配置由模板复制得到：`deploy/omnigate.env`（OmniGate 变量，含主密钥等敏感信息，权限设为 600）与 `deploy/.env`（Compose 变量，如 `OMNIGATE_DOMAIN`）。

变量的完整参考见 [configuration.md](configuration.md)，镜像细节、SQLite ⇄ PostgreSQL 数据迁移见 [deployment.md](deployment.md)。

## 0. 先弄清楚：镜像里有什么、没有什么

- **一个镜像 = 前端 + 后端 + 内置价格与套餐目录**。管理后台编译进 Go 二进制，一个端口（容器内 8080）同时提供页面、`/api`、`/v1`、`/healthz`、`/readyz`。
  基础镜像是 distroless（没有 shell），以 uid/gid `65532` 运行。
- **镜像里没有 PostgreSQL，也没有任何数据库服务。** 用哪个数据库只由一个环境变量 `OMNIGATE_DATABASE_URL` 决定：
  - `sqlite:///data/omnigate.db`（镜像默认值）：SQLite 是编译进二进制的嵌入式数据库，数据就是 `/data` 卷里的一个文件，不需要额外的服务；
  - `postgres://用户:密码@主机:5432/库名?sslmode=…`：连接一个**外部的** PostgreSQL（云数据库、已有实例，或用 `--profile postgres` 顺带起一个容器）。
- **“all in one” 靠部署编排实现，而不是把其他服务塞进镜像**：镜像始终只有 OmniGate 本身。需要数据库时，SQLite 已编译在二进制里（不设
  `OMNIGATE_DATABASE_URL` 即使用 `/data` 卷中的 SQLite）；想要 PostgreSQL 或自动 HTTPS，就用 `deploy/docker-compose.yml` 的 profile
  （`--profile postgres`、`--profile caddy`）在同一个 Compose 项目里一起启动，各自独立升级、备份。
- **只支持单实例部署**：同一时间只运行一个 OmniGate 容器（限流、熔断、后台任务、本地结算日志都在进程内）。不要扩容成多副本。
- 不需要 Redis。

请求路径：

```text
浏览器 / SDK ──HTTPS──▶ Caddy 或 nginx（gate.example.com:443，证书）──HTTP──▶ 127.0.0.1:8080 ──▶ omnigate 容器 :8080
                                                                                    └─ /data 卷（SQLite 文件、结算日志、备份）
                                                                                    └─ 可选：PostgreSQL
```

## 1. 准备服务器

1. **服务器**：Linux x86_64 或 arm64（发布的镜像同时包含两种架构），1 核 / 1 GB 内存起步，建议 2 核 / 2 GB；磁盘按请求日志量预留（SQLite 文件随日志增长，保留天数可在后台设置）。
2. **Docker Engine + Compose 插件**（`docker compose version` ≥ 2.24；profile 依赖的 `depends_on.required: false` 需要 ≥ 2.20）：
   ```bash
   curl -fsSL https://get.docker.com | sh
   docker compose version
   ```
3. **DNS**：在你的域名（示例 `example.com`）的 DNS 中添加 A 记录 `gate` → 服务器公网 IPv4（有 IPv6 时再加 AAAA）。确认生效：
   ```bash
   dig +short gate.example.com
   ```
   如果用了 Cloudflare 等 CDN 代理（橙色云朵），先设为“仅 DNS”，否则证书签发与真实 IP 识别都会受影响。
4. **防火墙**：只对公网开放 22、80、443。80 用于证书签发与跳转 HTTPS。8080（OmniGate）与 9090（指标）**不要**开放——compose 已把 8080 只绑定在 `127.0.0.1`，9090 根本没有发布。
5. **服务器能访问上游**：确认能访问你要接入的模型服务（如 `curl -I https://api.openai.com`）。不能直连时在第 4 步设置 `OMNIGATE_UPSTREAM_PROXY`。

## 2. 获取镜像

镜像由 GitHub Actions 自动构建并发布到 GitHub Container Registry（见 README 的「CI / 发布」）：

| 标签 | 含义 |
|---|---|
| `ghcr.io/okysu/omnigate:0.1.0` | 正式版本（Git 标签 `v0.1.0`）。**生产推荐固定到具体版本** |
| `ghcr.io/okysu/omnigate:0.1` | 该次版本的最新补丁版 |
| `ghcr.io/okysu/omnigate:latest` | 最新的正式版本或 `main` 分支构建 |
| `ghcr.io/okysu/omnigate:main`、`:sha-<7位提交号>` | `main` 分支的最新构建 / 某一次构建 |

```bash
docker pull ghcr.io/okysu/omnigate:0.1.0
docker run --rm ghcr.io/okysu/omnigate:0.1.0 version   # 版本号、提交号、构建时间
```

- 镜像为 `linux/amd64` + `linux/arm64` 多架构，附带 SBOM 与构建来源证明（provenance），发布前经过 Trivy 扫描（有可修复的严重漏洞时不发布）。
- 仓库是公开的，GHCR 上的包首次发布后默认是**私有**的：在 GitHub 的 Packages → omnigate → Package settings 中改为 Public，
  服务器即可匿名拉取；保持私有时，服务器先 `docker login ghcr.io -u <GitHub 用户名>`（密码用只有 `read:packages` 权限的 PAT）。

也可以自己从源码构建（例如没有网络访问 ghcr.io 时）：

```bash
git clone https://github.com/Okysu/OmniGate.git && cd OmniGate
make docker IMAGE=omnigate:v1.0.0 VERSION=v1.0.0      # 会同时写入提交号与构建时间
docker run --rm omnigate:v1.0.0 version
# 不经过镜像仓库，直接传到服务器：
docker save omnigate:v1.0.0 | gzip | ssh root@gate.example.com 'gunzip | docker load'
```

然后在 `deploy/.env` 中设置 `OMNIGATE_IMAGE=omnigate:v1.0.0`（优先于 `OMNIGATE_TAG`）。构建器镜像固定为 `golang:1.26.9-alpine`、`node:24.21.0-alpine`（带摘要），
产物用 Go 1.26.9 编译；arm64：`docker buildx build --platform linux/arm64 --build-arg VERSION=v1.0.0 -t omnigate:v1.0.0 --load .`

**生产使用固定的版本标签**（`0.1.0`、`0.2.0`…），不要只用 `latest`：回滚时需要明确的旧版本。

## 3. 选择数据库

| | SQLite（推荐起步） | PostgreSQL |
|---|---|---|
| 需要部署什么 | 什么都不用，数据是 `/data` 卷里的一个文件 | 一个 PostgreSQL 14+（外部实例，或 `--profile postgres`） |
| `OMNIGATE_DATABASE_URL` | `sqlite:///data/omnigate.db`（默认） | `postgres://omnigate:密码@主机:5432/omnigate?sslmode=…` |
| 适合 | 个人、小团队、内部工具：中低写入量 | 请求量大、需要数据库自身的高可用 / PITR 备份、已有 DBA 运维的 PostgreSQL |
| 吞吐参考（单实例，计费开启） | 约 1,700 请求/秒 | 受单钱包行锁与网络往返影响，同场景约 300–500 请求/秒，但多用户并发写更平稳 |
| 备份 | `omnigate backup`（运行中可做），复制出一个文件 | `pg_dump` 或数据库自己的备份 |
| 注意 | 卷必须在本地磁盘（不要 NFS/SMB）；断电可能丢最后几个已提交事务（不会损坏） | 需要自己维护数据库 |

拿不准就选 **SQLite**：之后可以用 `omnigate migrate-db` 停机迁移到 PostgreSQL（双向，见 [deployment.md 第 9 节](deployment.md#9-sqlite-迁移到-postgresql以及反向)）。

使用外部 PostgreSQL 时先建库和用户：

```sql
CREATE USER omnigate WITH PASSWORD '只用字母数字的强密码';
CREATE DATABASE omnigate OWNER omnigate;
```

## 4. 准备部署目录与配置

服务器上只需要 `deploy/` 目录的内容（不需要源码，镜像从 ghcr.io 拉取）。下文统一放在 `/opt/omnigate`，**之后所有命令都在这个目录中执行**：

```bash
sudo mkdir -p /opt/omnigate && sudo chown "$USER" /opt/omnigate
# 从本机复制：scp -r deploy/. root@gate.example.com:/opt/omnigate/   （或在服务器上 clone 仓库后 cp -r deploy/. /opt/omnigate/）
cd /opt/omnigate
cp .env.example .env
install -m 600 omnigate.env.example omnigate.env
```

`.env` 是给 Compose 的（OmniGate 不读）：

```dotenv
# 镜像版本：ghcr.io/okysu/omnigate:<OMNIGATE_TAG>
OMNIGATE_TAG=0.1.0
# 一起启动的捆绑服务（profile），不需要时保持注释：
# COMPOSE_PROFILES=caddy            # 容器版 Caddy，自动 HTTPS
# COMPOSE_PROFILES=postgres,caddy   # 再加一个 PostgreSQL（同时设置 POSTGRES_PASSWORD）
# OMNIGATE_DOMAIN=gate.example.com  # caddy profile 的站点域名
# POSTGRES_PASSWORD=
```

也可以不写 `COMPOSE_PROFILES`，每条命令带 `--profile caddy` 等参数。

`omnigate.env` 是 OmniGate 的配置。文件里每一项都有注释，下面按分组说明需要你动手的地方。
写法：`KEY=VALUE`，**值不要加引号，不要在值后面写注释**，值为空等于未设置。

### 4.1 基本（已预填）

```dotenv
OMNIGATE_ENV=production
OMNIGATE_PUBLIC_URL=https://gate.example.com
```

`OMNIGATE_PUBLIC_URL` 必须与浏览器地址栏里的地址完全一致（https、无端口、无路径、无结尾 `/`），OAuth 回调地址、CSRF 来源校验、邮件里的链接都由它决定。

### 4.2 数据库

- SQLite：保持 `OMNIGATE_DATABASE_URL=sqlite:///data/omnigate.db`。
- 外部 PostgreSQL：改成 `OMNIGATE_DATABASE_URL=postgres://omnigate:密码@db.example.internal:5432/omnigate?sslmode=require`。
  密码里的 `@ / : #` 等需要百分号编码。数据库就在这台宿主机上时，主机写 `host.docker.internal`（compose 已配置
  `extra_hosts: host.docker.internal:host-gateway`），同时确认 PostgreSQL 监听了 Docker 网桥地址、`pg_hba.conf` 放行了 `172.30.88.0/24`。
- Compose 顺带运行 PostgreSQL（`--profile postgres`）：在 `.env` 设置 `POSTGRES_PASSWORD`（只用字母数字，`head -c 24 /dev/urandom | base64 | tr -dc 'A-Za-z0-9'`），
  `omnigate.env` 写 `OMNIGATE_DATABASE_URL=postgres://omnigate:<同一个密码>@postgres:5432/omnigate?sslmode=disable`。
  （旧写法 `-f docker-compose.yml -f docker-compose.postgres.yml` 仍可用，它会根据 `POSTGRES_PASSWORD` 自动覆盖连接串。）

### 4.3 主密钥（必填）

主密钥加密所有渠道的上游 API Key、SMTP 密码、Webhook 签名密钥等。生成：

```bash
docker run --rm ghcr.io/okysu/omnigate:latest keygen
# k20261009-a1b2c3:BASE64…（一整行）
```

把整行填进 `OMNIGATE_MASTER_KEY=`。然后**立刻**把它另存到密码管理器（1Password、Bitwarden 等），与数据库备份分开保存：

- **丢失主密钥 = 所有已加密的渠道凭据永久无法解密**（只能逐个重新填写上游 Key），数据库备份也救不回来；
- 主密钥和数据库备份同时泄露 = 所有上游 Key 泄露。

### 4.4 GitHub 登录（OAuth App）

1. 打开 <https://github.com/settings/developers> → **OAuth Apps** → **New OAuth App**（组织所有的应用在组织设置的同名位置创建）。
2. 填写：
   - Application name：`OmniGate`（随意）
   - Homepage URL：`https://gate.example.com`
   - Authorization callback URL：**`https://gate.example.com/api/auth/github/callback`**（必须一字不差）
3. 创建后点 **Generate a new client secret**，把 Client ID 与 Client secret 填入：
   ```dotenv
   OMNIGATE_AUTH_GITHUB_CLIENT_ID=Ov23li…
   OMNIGATE_AUTH_GITHUB_CLIENT_SECRET=…
   ```

也可以（或同时）使用企业 OIDC（Keycloak、Authentik、Google、Entra ID 等）：取消 `omnigate.env` 中 4.2 段的注释，
在 IdP 登记重定向 URI `https://gate.example.com/api/auth/sso/callback`（`sso` 换成你选的 ID），生产环境 Issuer 必须是 https。

生产环境**至少要有一种登录方式**，否则拒绝启动。

### 4.5 引导管理员（必填）

首次登录即成为系统管理员的身份。推荐用 **不可变的 GitHub 数字 ID**：

```bash
curl -s https://api.github.com/users/<你的GitHub用户名> | grep '"id"'
#   "id": 583231,
```

```dotenv
OMNIGATE_BOOTSTRAP_ADMINS=github-id:583231
```

- 不要用 `github:<用户名>`：GitHub 用户名可以改名，原用户名释放后可能被别人注册并以管理员身份登录。生产环境使用时会在启动日志中警告。
- OIDC 写 `<提供方ID>:<sub>`；也可以用 `email:<已验证邮箱>`。
- 启动时会检查：列表里至少有一项能匹配已配置的登录方式（例如只配了 GitHub 却写了 `sso:…` 会拒绝启动）；`github-id:` 后面必须是数字。
- 只在该身份**首次**登录时生效；之后的角色调整在后台“用户”页面完成。

### 4.6 注册模式

上线初期建议：

```dotenv
OMNIGATE_REGISTRATION_MODE=restricted
OMNIGATE_AUTH_ALLOWED_IDENTITIES=github-id:1003,github-id:1004
OMNIGATE_AUTH_ALLOWED_EMAIL_DOMAINS=
```

`restricted` 只让引导管理员和允许名单中的身份（或已验证邮箱域名）注册；名单里的人都注册完后，可以改为 `closed`（只有已有账号能登录）并重启。
`open` 允许任何 GitHub 用户注册，只在公开服务时使用。修改后重启生效（`docker compose up -d`）。

### 4.7 反向代理与真实 IP

```dotenv
OMNIGATE_TRUSTED_PROXIES=172.30.88.1
```

OmniGate 只在直连来源属于这个列表时才信任 `X-Forwarded-For`（用于登录限流、会话与审计日志里的 IP、API Key 的 IP 白名单）。

- 反向代理装在宿主机上（第 6 步方案 A、C）：经 `127.0.0.1:8080` 端口映射进入容器的连接，来源地址固定是 compose 网络的网关
  **`172.30.88.1`**（`docker-compose.yml` 固定了网段 `172.30.88.0/24`）。
- 容器版 Caddy（方案 B）：Caddy 容器的固定地址 **`172.30.88.10`**。
- **不要**写 `172.16.0.0/12` 这类大网段：同网段的其他容器都能伪造客户端 IP。
- 端口必须保持只绑定 `127.0.0.1`：如果改成对公网发布，外部连接经端口映射后也可能显示为网关地址，从而能伪造 `X-Forwarded-For`。
- 如果本机已有网络占用了 `172.30.88.0/24`（启动时报 `Pool overlaps`），在 `.env` 中设置 `OMNIGATE_SUBNET_PREFIX`（如 `172.30.99`），并同步修改这里的地址。

### 4.8 结算币种（首次启动后锁定）

```dotenv
# 美元 USD（内置目录中的价格与套餐均以 USD 计价）；人民币为 CNY
OMNIGATE_CURRENCY=USD
```

首次启动时写入数据库并**永久锁定**，之后改这个变量不再生效（只会打印警告）。启动前确认好：
内置目录要求 `USD`，币种不一致时自动导入只记录错误并跳过，`omnigate seed` 也会拒绝导入。

### 4.9 首次启动自动导入价格与套餐

```dotenv
OMNIGATE_SEED_ON_START=builtin
```

镜像内置一份目录：8 个 GPT 模型的售价（含 `gpt-image-2` 按张计费）、8 条模型资料、6 个套餐（Go → Elite），以 USD 计价。
设为 `builtin` 后，服务在迁移完成、结算币种锁定之后导入它，并在 `system_settings` 中记录标记 `seed.applied`（来源、内容哈希、时间）：

- **每个数据库只导入一次**：之后的启动看到标记就跳过，后台对价格、套餐的修改不会被覆盖。
- 数据库里已经有同样的数据（例如之前手动执行过 `omnigate seed`）时，不会产生新的价格版本，只写入标记。
- 以后升级的镜像带来新目录时**不会自动导入**，启动日志提示 `the catalog has changed since`；确认后手动执行 `omnigate seed builtin`（见第 8 步）。
- 币种不是 USD、目录无效或写入失败时只记录 ERROR 日志，服务照常启动（失败时下次启动会重试）。
- 也可以写成容器内的文件路径（如 `/data/catalog.json`）导入自定义目录；不需要时留空。

### 4.10 邮件（SMTP，可选）

```dotenv
OMNIGATE_SMTP_HOST=smtp.example.com
OMNIGATE_SMTP_PORT=465
OMNIGATE_SMTP_SECURITY=tls
OMNIGATE_SMTP_USERNAME=noreply@example.com
OMNIGATE_SMTP_PASSWORD=…
OMNIGATE_SMTP_FROM=OmniGate <noreply@example.com>
```

端口 587 配 `starttls`，465 配 `tls`；生产不允许 `none`。留空则只有站内通知。也可以启动后在后台“系统设置 → 邮件（SMTP）”中填写（数据库中的值优先，密码用主密钥加密保存）。

### 4.11 其他（一般保持默认）

| 变量 | 建议值 | 说明 |
|---|---|---|
| `OMNIGATE_METRICS_ADDR` | `:9090` | Prometheus 指标，只在容器网络内可达；不用就设 `off` |
| `OMNIGATE_UPSTREAM_PROXY` | 空 | 服务器不能直连上游时填 `http://…` 或 `socks5://…` |
| `OMNIGATE_CHANNELS_ALLOW_PRIVATE_NETWORK` | `false` | 需要接本机 / 内网模型（如 Ollama）时才开 |
| `OMNIGATE_LOG_FORMAT` / `OMNIGATE_LOG_LEVEL` | `json` / `info` | 排障时临时改 `debug` |
| `OMNIGATE_SESSION_TTL` / `OMNIGATE_SESSION_IDLE_TTL` | `720h` / `168h` | 会话绝对有效期 / 空闲超时 |
| `OMNIGATE_COOKIE_SECURE` | 不设置 | 由 https 自动推导为 `true`；生产设 `false` 会拒绝启动 |
| `OMNIGATE_DATA_DIR` | 不设置（`/data`） | 本地状态目录（结算日志），即 `omnigate-data` 卷；**用 PostgreSQL 也要保留这个卷** |
| `OMNIGATE_WEB_ENABLED` / `OMNIGATE_WEB_DIR` | `true` / 不设置 | 关闭页面或用自定义前端构建 |
| `OMNIGATE_PLUGIN_HEAP_LIMIT_MB` | `256` | 插件内存看门狗阈值 |

内存：`docker-compose.yml` 默认限制容器 1 GB、`GOMEMLIMIT=800MiB`，可在 `.env` 用 `OMNIGATE_MEM_LIMIT`、`OMNIGATE_GOMEMLIMIT` 调整（保持约 80% 的比例）。

## 5. 启动

```bash
cd /opt/omnigate
docker compose config -q && docker compose pull && docker compose up -d
#（不进入目录时等价于：docker compose -f /opt/omnigate/docker-compose.yml up -d；需要捆绑服务时加 --profile caddy / --profile postgres）
docker compose ps                        # omnigate 应为 Up … (healthy)
docker compose logs -f omnigate          # 迁移完成后出现 "catalog seeded on first start"（首次）与 "omnigate listening"
curl -fsS http://127.0.0.1:8080/readyz
# {"journal":{"pending":0},"migrations":{"dialect":"sqlite","current":16,"latest":16,"pending":0},"status":"ready"}
```

配置有误时容器会立即退出并反复重启，`docker compose logs omnigate` 第一行列出**全部**配置错误。生产模式下这些检查发生在打开数据库、执行迁移**之前**，
所以修正配置后直接 `docker compose up -d` 即可，不会留下半初始化的数据库。例如：

```text
omnigate: OMNIGATE_MASTER_KEY is required in production (generate one with `omnigate keygen`)
OMNIGATE_BOOTSTRAP_ADMINS is required in production (e.g. github-id:12345)
no login provider configured: production requires OMNIGATE_AUTH_GITHUB_CLIENT_ID/_SECRET or OMNIGATE_AUTH_OIDC (nobody could sign in)
```

## 6. 反向代理与 HTTPS

三种方案任选其一。共同要求（三份配置都已写好）：自动跳转 HTTPS、HSTS、请求体上限 64 MiB（图片编辑、语音转写）、
**流式响应不缓冲**（SSE，否则 Claude Code 等客户端会“卡住”直到整段回答生成完）、读超时 ≥ 10 分钟、传递 `X-Forwarded-For` / `X-Forwarded-Proto`、不改写 `Host` 与 `Origin`。

### 方案 A：宿主机 Caddy（最省事，自动申请证书）

```bash
# 安装：https://caddyserver.com/docs/install （Debian/Ubuntu 用官方 apt 源）
sudo cp /opt/omnigate/Caddyfile /etc/caddy/Caddyfile
# 域名：把文件中的 {$OMNIGATE_DOMAIN:gate.example.com} 改成你的域名，或在 caddy 服务的环境中设置 OMNIGATE_DOMAIN
sudo systemctl reload caddy
journalctl -u caddy -f        # 看到 "certificate obtained successfully" 即证书就绪
```

`omnigate.env`：`OMNIGATE_TRUSTED_PROXIES=172.30.88.1`。

### 方案 B：容器版 Caddy（不在宿主机装任何东西）

`.env` 中设置 `COMPOSE_PROFILES=caddy`（同时用 PostgreSQL 时写 `postgres,caddy`）与 `OMNIGATE_DOMAIN=<你的域名>`，
`omnigate.env` 中设置 `OMNIGATE_TRUSTED_PROXIES=172.30.88.10`，然后：

```bash
docker compose --profile caddy up -d   # 已写 COMPOSE_PROFILES 时可省略 --profile
docker compose logs -f caddy           # 证书申请过程
```

要求宿主机 80、443 端口空闲。证书保存在 `omnigate_caddy-data` 卷中，不要删除。

### 方案 C：nginx + certbot

```bash
sudo apt install nginx certbot
sudo mkdir -p /var/www/certbot
# 1) 先只启用 80 端口（证书还不存在，完整配置会让 nginx -t 失败）
printf 'server {\n listen 80;\n server_name gate.example.com;\n location /.well-known/acme-challenge/ { root /var/www/certbot; }\n}\n' \
  | sudo tee /etc/nginx/conf.d/omnigate.conf
sudo nginx -t && sudo systemctl reload nginx
# 2) 申请证书，续期后自动重载 nginx
sudo certbot certonly --webroot -w /var/www/certbot -d gate.example.com --deploy-hook 'systemctl reload nginx'
# 3) 换成完整配置
sudo sed 's/gate\.example\.com/<你的域名>/g' /opt/omnigate/nginx/omnigate.conf | sudo tee /etc/nginx/conf.d/omnigate.conf >/dev/null
sudo nginx -t && sudo systemctl reload nginx
sudo certbot renew --dry-run      # 验证自动续期
```

`omnigate.env`：`OMNIGATE_TRUSTED_PROXIES=172.30.88.1`。nginx ≥ 1.25.1 会提示 `listen … http2` 已弃用，按文件中的注释改成 `http2 on;` 即可。
前面还有 CDN / 负载均衡时，把 `X-Forwarded-For $remote_addr` 改为 `$proxy_add_x_forwarded_for`，并把它们的地址加入 `OMNIGATE_TRUSTED_PROXIES`。

### 验证

```bash
curl -sSI https://gate.example.com/ | grep -iE '^HTTP|strict-transport'     # HTTP/2 200 + HSTS
curl -sS  https://gate.example.com/api/system/info
# {"currency":{"code":"CNY",…},"name":"OmniGate","registrationMode":"restricted","version":"v1.0.0",…}
curl -sSI http://gate.example.com/ | head -1                                  # 301/308 跳转到 https
```

## 7. 首次登录

1. 浏览器打开 <https://gate.example.com>，选择“使用 GitHub 登录”（或你的 OIDC）。
2. 用 `OMNIGATE_BOOTSTRAP_ADMINS` 中的那个账号授权，回来后即为系统管理员。
3. 登录失败时地址栏会带 `?error=…`：`not_allowed`（不在允许名单）、`registration_closed`、`state_mismatch`（Cookie 问题，见第 13 步）、`oauth_failed`（回调地址或 Client Secret 错误，看日志）。

## 8. 上线前检查清单

在后台逐项确认：

- [ ] **注册模式**：`/api/system/info` 的 `registrationMode` 符合预期（`restricted` 或 `closed`）。
- [ ] **币种**：`/api/system/info` 的 `currency` 正确（已锁定，改不了，错了只能清库重来）。
- [ ] **主密钥**已离线备份，并与数据库备份分开保存。
- [ ] **SMTP**：系统设置 → 邮件（SMTP）→ “发送测试邮件”，确认收到。
- [ ] **渠道**：渠道页面添加上游（OpenAI / Anthropic / 兼容服务），填写上游 API Key，点“测试”，用“发现模型”配置逻辑模型名。
- [ ] **价格与套餐目录**：设置了 `OMNIGATE_SEED_ON_START=builtin` 时首次启动已自动导入（日志 `catalog seeded on first start`）。
  没有设置时手动导入（以 USD 计价，要求 `OMNIGATE_CURRENCY=USD`；可重复执行，第二次应显示「无需变更」）：
  ```bash
  cd /opt/omnigate
  docker compose exec omnigate omnigate seed builtin --dry-run   # 试运行：变更清单
  docker compose exec omnigate omnigate seed builtin             # 正式写入
  ```
  之后在后台「套餐」与「模型广场」确认 6 个套餐、售价和模型资料已出现（模型广场只显示有可用渠道的模型）。详见 [deploy/seed/README.md](../../deploy/seed/README.md)。
- [ ] **价格与计费**：为要计费的逻辑模型设置售价（上一步已导入的模型除外）；决定是否开启 `billing.enforce`（预付费），开启前先给用户发放余额（兑换码）。
- [ ] **测试 Key**：API Keys 页面创建一个 Key（`og-…`，只显示一次），然后：
  ```bash
  # OpenAI 协议，流式（-N 关闭 curl 自身缓冲；应逐字输出，而不是最后一次性出现）
  curl -N https://gate.example.com/v1/chat/completions \
    -H 'Authorization: Bearer og-xxxxxxxx' -H 'Content-Type: application/json' \
    -d '{"model":"<逻辑模型名>","stream":true,"messages":[{"role":"user","content":"你好"}]}'

  # Anthropic 协议（同一个 Key 可以调用任意渠道的模型，网关自动转换协议）
  curl https://gate.example.com/v1/messages \
    -H 'x-api-key: og-xxxxxxxx' -H 'anthropic-version: 2023-06-01' -H 'Content-Type: application/json' \
    -d '{"model":"<逻辑模型名>","max_tokens":256,"messages":[{"role":"user","content":"你好"}]}'
  ```
  Anthropic SDK：
  ```python
  from anthropic import Anthropic
  client = Anthropic(base_url="https://gate.example.com", api_key="og-xxxxxxxx")   # base_url 不带 /v1
  print(client.messages.create(model="<逻辑模型名>", max_tokens=256,
                               messages=[{"role": "user", "content": "你好"}]).content[0].text)
  ```
  OpenAI SDK 用 `base_url="https://gate.example.com/v1"`；Claude Code：`export ANTHROPIC_BASE_URL=https://gate.example.com ANTHROPIC_AUTH_TOKEN=og-xxxxxxxx`。
- [ ] **请求日志**：后台“请求日志”能看到刚才的请求；审计日志里的 IP 是你的真实公网 IP 前缀，而不是 `172.30.88.x`（否则检查 `OMNIGATE_TRUSTED_PROXIES`）。
- [ ] **端口**：从外网 `curl http://<服务器IP>:8080/healthz` 和 `:9090` 都应连接失败。
- [ ] **备份**：按第 9 步做一次备份**并做一次恢复演练**，配置好定时备份。

## 9. 备份与恢复

需要备份两样东西，分开保存：**数据库** 和 **`omnigate.env`（含主密钥）**。下面的命令都在 `/opt/omnigate` 中执行，
卷名前缀 `omnigate_` 来自 compose 项目名。

### 9.1 SQLite：备份

`omnigate backup` 在服务运行时生成一致的副本（`VACUUM INTO`，不阻塞网关）。目标文件已存在时会拒绝覆盖，所以文件名带上时间戳：

```bash
cd /opt/omnigate
ts=$(date +%Y%m%d-%H%M%S)
docker compose exec -T omnigate omnigate backup /data/backups/omnigate-$ts.db
mkdir -p backups
docker compose cp omnigate:/data/backups/omnigate-$ts.db backups/
# 可选：删除卷里的那份副本（镜像里没有 shell，借一个 alpine 容器）
docker run --rm -v omnigate_omnigate-data:/data alpine:3 rm /data/backups/omnigate-$ts.db
```

再把 `backups/omnigate-$ts.db` 复制到异地（对象存储、另一台机器）。有 `sqlite3` 时可以校验：`sqlite3 -readonly backups/omnigate-$ts.db 'PRAGMA integrity_check'` → `ok`。
不要在运行中直接复制 `/data/omnigate.db`（WAL 中可能还有未合并的数据）。

每天 3 点自动备份并保留 14 天（`crontab -e`，注意 crontab 中 `%` 要写成 `\%`）：

```cron
0 3 * * * cd /opt/omnigate && ts=$(date +\%Y\%m\%d-\%H\%M\%S) && docker compose exec -T omnigate omnigate backup /data/backups/omnigate-$ts.db && docker compose cp omnigate:/data/backups/omnigate-$ts.db backups/ && docker run --rm -v omnigate_omnigate-data:/data alpine:3 rm /data/backups/omnigate-$ts.db && find backups -name 'omnigate-*.db' -mtime +14 -delete
```

### 9.2 SQLite：恢复

容器以 uid **65532** 运行，恢复的文件必须属于 `65532:65532`，否则启动报
`attempt to write a readonly database` 并不断重启。旧文件会被移到 `/data/pre-restore-<时间>/` 保留，确认无误后再删。

```bash
cd /opt/omnigate
docker compose stop omnigate
docker run --rm -v omnigate_omnigate-data:/data -v "$PWD/backups":/backup:ro alpine:3 sh -euc '
  old=/data/pre-restore-$(date +%Y%m%d-%H%M%S)
  mkdir -p "$old"
  for f in omnigate.db omnigate.db-wal omnigate.db-shm settlement-journal.jsonl; do
    if [ -e "/data/$f" ]; then mv "/data/$f" "$old/"; fi
  done
  cp "/backup/$1" /data/omnigate.db
  chown 65532:65532 /data/omnigate.db
  chmod 600 /data/omnigate.db
' restore omnigate-20261009-030000.db          # ← 要恢复的备份文件名（在 backups/ 目录中）
docker compose start omnigate
curl -fsS http://127.0.0.1:8080/readyz
```

- 恢复时 `omnigate.db-wal`、`omnigate.db-shm` 必须一起移走（它们属于旧数据库）。
- 结算日志 `settlement-journal.jsonl` 记录的是“数据库暂时不可用时尚未写入的结算”，属于旧数据库的时间线，同样移走；
  正常情况下它是空的（`/readyz` 中 `journal.pending` 为 0）。
- 必须使用与备份配套的主密钥。恢复后会话仍然有效（会话也在数据库中）。

### 9.3 PostgreSQL（Compose 自带的 `postgres` 服务）

```bash
cd /opt/omnigate
mkdir -p backups
docker compose exec -T postgres pg_dump -U omnigate -d omnigate -Fc > backups/omnigate-$(date +%Y%m%d-%H%M%S).dump

# 恢复：删库重建后导入（不要用 pg_restore --clean：请求日志是分区表，会报 "cannot drop inherited constraint"）
docker compose stop omnigate
docker compose exec -T postgres dropdb -U omnigate omnigate
docker compose exec -T postgres createdb -U omnigate -O omnigate omnigate
docker compose exec -T postgres pg_restore -U omnigate -d omnigate --no-owner --exit-on-error < backups/omnigate-20261009-030000.dump
docker compose start omnigate
```

### 9.4 外部 PostgreSQL

用数据库自身的备份（云数据库快照 / PITR），或：

```bash
docker run --rm postgres:17-alpine pg_dump 'postgres://omnigate:密码@db.example.internal:5432/omnigate?sslmode=require' -Fc > omnigate.dump
```

恢复时同样先停 omnigate，恢复到一个**空库**（由数据库管理员删库重建，或新建一个库后修改 `OMNIGATE_DATABASE_URL`），
`pg_restore --no-owner --exit-on-error -d <空库地址> omnigate.dump`，再启动。

使用 PostgreSQL 时 `/data` 卷中只有结算日志（通常为空），不需要备份，但卷本身要保留。

## 10. 升级与回滚

升级：

```bash
cd /opt/omnigate
# 1. 备份（第 9 步），确认主密钥备份可用
# 2. 修改 .env：OMNIGATE_TAG=0.2.0（自己构建的镜像改 OMNIGATE_IMAGE）
docker compose pull omnigate
docker compose up -d                 # 先停旧容器（SIGTERM，最多等 40 秒让进行中的请求完成），再启动新容器
docker compose logs -f omnigate      # 新版本启动时自动执行数据库迁移，完成后才开始监听
docker compose exec omnigate omnigate version
curl -fsS http://127.0.0.1:8080/readyz
```

- 只有一个实例，`docker compose up -d` 会先停旧容器再起新容器，升级期间有几秒到几十秒不可用。
- 已打开的浏览器页面刷新后加载新版前端。
- 新版本内置的价格与套餐目录不会自动覆盖已有数据（启动日志会提示目录有变化）；需要时执行 `omnigate seed builtin --dry-run` 查看差异后再导入。

回滚：**数据库结构不能降级**。旧版本二进制连接已被新版本迁移过的数据库会拒绝启动
（`database schema is newer than this OmniGate binary`），这是有意的保护。回滚步骤：

```bash
docker compose stop omnigate
# 用升级前的备份恢复数据库（第 9.2 / 9.3 步）
# .env 改回 OMNIGATE_TAG=0.1.0
docker compose up -d
```

升级后产生的数据会随回滚丢失。所以请保留上一个版本的镜像与升级前的备份。

## 11. 监控

| 检查 | 说明 |
|---|---|
| `https://gate.example.com/healthz` → 200 `{"status":"ok"}` | 存活；外部拨测（UptimeRobot 等）用它 |
| `https://gate.example.com/readyz` → 200 / 503 | 就绪：数据库可连接、没有待执行迁移；`journal.pending` > 0 表示有结算在等数据库恢复 |
| `docker compose ps` | 容器内置健康检查（`omnigate healthcheck`，每 15 秒） |
| `omnigate:9090/metrics` | Prometheus 指标，**只在容器网络内**可达 |

查看指标（临时容器加入 compose 网络）：

```bash
docker run --rm --network omnigate_omnigate curlimages/curl -s http://omnigate:9090/metrics | grep '^omnigate_'
```

Prometheus 也以容器运行时，把它加入 `omnigate_omnigate` 网络并抓取 `omnigate:9090`。值得告警的指标：
`omnigate_settlement_failures_total`、`omnigate_journal_pending`、`omnigate_journal_dead_total` 增长，以及 `/readyz` 非 200。

## 12. 日志

```bash
docker compose logs -f omnigate                 # 实时
docker compose logs --since 1h omnigate | grep '"level":"ERROR"'
```

- JSON 格式，输出到 stdout；compose 已配置轮转：单文件 20 MB、保留 5 个。
- 访问日志不记录请求头、查询串、请求体与 API Key。
- 临时排障：`omnigate.env` 中 `OMNIGATE_LOG_LEVEL=debug` → `docker compose up -d`，排查完改回。
- 启动时的配置警告以 `config:` 开头（例如使用了 `github:<用户名>` 引导管理员）。

## 13. 排障

| 现象 | 原因与处理 |
|---|---|
| 容器反复重启，日志第一行是配置错误 | 按提示修改 `omnigate.env`，`docker compose up -d`。生产模式下这些检查在打开数据库之前进行，不会留下脏数据 |
| GitHub 授权页提示 “The redirect_uri is not associated with this application” | OAuth App 的回调地址必须是 `https://gate.example.com/api/auth/github/callback`，且 `OMNIGATE_PUBLIC_URL=https://gate.example.com`（无结尾 `/`） |
| 登录后又回到登录页，或 `?error=state_mismatch` | Cookie 带 `Secure`，只能经 https 访问：始终用 `https://gate.example.com` 打开，不要用 IP 或 `http://`；确认代理传了 `X-Forwarded-Proto` 且没有改写 `Host` |
| 后台操作报 403 `csrf_rejected`（“请求来源不被允许”） | 浏览器的 `Origin` 与 `OMNIGATE_PUBLIC_URL` 不一致：用别的域名 / 端口访问了，或代理改写了 `Origin`。其他来源确需访问时加入 `OMNIGATE_ALLOWED_ORIGINS` |
| 审计日志里所有人的 IP 都是 `172.30.88.x`；登录频繁 429 | `OMNIGATE_TRUSTED_PROXIES` 与代理的实际来源不符（见 4.7）；nginx 需要 `proxy_set_header X-Forwarded-For` |
| API 返回 403 “this API key is not allowed from your IP address” | Key 设置了 IP 白名单，而网关看到的是代理地址（同上），或客户端出口 IP 不在白名单中 |
| 流式响应很久才一次性出来 / Claude Code 卡住 | 代理缓冲了 SSE：nginx 需要 `proxy_buffering off`，Caddy 需要 `flush_interval -1`，且不要开 gzip/encode；前面有 CDN 时也要关闭其缓冲 |
| 长回答在中途断开（约 60 秒） | 代理读超时太短：nginx `proxy_read_timeout 900s`；CDN 的超时也要放宽 |
| 上传图片 / 音频报 413 | 代理请求体上限：nginx `client_max_body_size 64m`，Caddy `request_body max_size 64MiB`；超过 64 MiB 是 OmniGate 本身的上限 |
| `/readyz` 返回 `database_unavailable`，或日志 `ping database: … password authentication failed` | 检查 `OMNIGATE_DATABASE_URL`（密码编码、主机名、`sslmode`）、网络与 `pg_hba.conf` |
| SQLite 日志 `attempt to write a readonly database` / `unable to open database file` | 文件属主不是 65532：按 9.2 的命令 `chown 65532:65532`；用宿主机目录做绑定挂载时先 `sudo chown -R 65532:65532 <目录>` |
| 启动即退出，日志 `OMNIGATE_DATA_DIR "/data": journal: open /data/settlement-journal.jsonl: permission denied` | 平台挂到 `/data` 的卷属主是 root，而镜像以 uid 65532 运行。任选其一：不挂 `/data` 卷（使用外部 PostgreSQL 时 `/data` 只放结算补偿日志）；或设置 `OMNIGATE_DATA_DIR=/tmp/omnigate`；或把卷的属主改为 `65532:65532` |
| `database schema is newer than this OmniGate binary` | 用旧版本镜像连接了新版本的数据库，见第 10 步回滚 |
| 启动报 `Pool overlaps with other one on this address space` | `172.30.88.0/24` 与本机其他网络冲突，见 4.7 |
| 渠道测试返回 `409 secret_unavailable` | 主密钥与加密时的不一致（换了或丢了主密钥），恢复正确的 `OMNIGATE_MASTER_KEY`，或在渠道中重新填写上游 Key |
| 访问上游超时 | 服务器无法直连上游，设置 `OMNIGATE_UPSTREAM_PROXY` |

镜像内没有 shell，调试网络时借用临时容器：
`docker run --rm --network container:omnigate-omnigate-1 curlimages/curl -s localhost:8080/readyz`。

## 附：不用 Compose 时的等价命令

```bash
docker network create --subnet 172.30.88.0/24 --gateway 172.30.88.1 omnigate_omnigate
docker run -d --name omnigate --restart unless-stopped \
  --network omnigate_omnigate \
  --env-file /opt/omnigate/omnigate.env \
  -e GOMEMLIMIT=800MiB --memory 1g \
  -p 127.0.0.1:8080:8080 \
  -v omnigate_omnigate-data:/data \
  --stop-timeout 40 \
  --log-opt max-size=20m --log-opt max-file=5 \
  ghcr.io/okysu/omnigate:0.1.0
```

此时上文中的 `docker compose exec omnigate …` 换成 `docker exec omnigate …`，`docker compose stop/start omnigate` 换成 `docker stop -t 40 omnigate` / `docker start omnigate`。
注意 `docker run --env-file` 不会去掉值两边的引号，模板中的值本来就没有引号，保持即可。
