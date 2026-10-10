# OmniGate

可自托管、面向个人与团队的统一大模型 API 网关。渠道即代码（Channel as Code）：每个厂商渠道都是一个可版本化、可审计的插件。

主要能力：

- OpenAI Chat Completions / Responses、Anthropic Messages 三种协议入口并自动互转，另有嵌入、图片、音频与文本补全（Completions / FIM）接口
- 多渠道路由（优先级、协议匹配度、权重、熔断、回退、路由规则），用户自带渠道优先且不计费
- 插件化渠道：TypeScript 插件（沙盒运行）、原子能力（余额 / 模型 / 健康）、自定义协议、计费插件、在线编辑器与审批
- 预付费钱包、兑换码、套餐与周期配额（5 小时会话、每周等）、用户组倍率、分时价格、限额
- 多用户与 RBAC、审计日志、OIDC / GitHub 登录、邮件 / Webhook / 站内通知、模型广场

## 快速开始

OmniGate 以**一个 Docker 镜像、一个端口**交付：管理后台编译进二进制，同一端口提供页面、`/api`、`/v1`。
**镜像里不包含数据库服务**：数据库只由 `OMNIGATE_DATABASE_URL` 决定——默认 `sqlite:///data/omnigate.db`（嵌入式 SQLite，数据就是 `/data` 卷里的一个文件，零依赖），
或 `postgres://…` 连接外部 PostgreSQL。只支持**单实例部署**（[ADR-0009](docs/adr/0009-sqlite-lightweight-mode.md)）。

镜像内置一份价格与套餐目录（8 个 GPT 模型售价、模型资料、6 个套餐，USD），设置 `OMNIGATE_SEED_ON_START=builtin` 后首次启动自动导入（每个数据库只导入一次，见 [deploy/seed/README.md](deploy/seed/README.md)）。

### 生产部署（Docker Compose）

镜像由 GitHub Actions 发布到 `ghcr.io/okysu/omnigate`（linux/amd64 + arm64）。完整步骤（HTTPS 域名、反向代理、备份、升级）见
[docs/operations/deploy-production.md](docs/operations/deploy-production.md)，最短路径：

```bash
# 服务器上只需要仓库的 deploy/ 目录
cd deploy
cp .env.example .env                                  # Compose 变量：OMNIGATE_TAG、COMPOSE_PROFILES、OMNIGATE_DOMAIN
install -m 600 omnigate.env.example omnigate.env      # OmniGate 变量：数据库、主密钥、GitHub OAuth、引导管理员……
docker run --rm ghcr.io/okysu/omnigate:latest keygen  # 主密钥，填入 omnigate.env
docker compose pull && docker compose up -d           # 只有 omnigate：外部 PostgreSQL（OMNIGATE_DATABASE_URL）或内置 SQLite
docker compose --profile caddy up -d                  # 再加容器版 Caddy：自动 HTTPS
docker compose --profile postgres --profile caddy up -d   # 再加一个捆绑的 PostgreSQL
```

“all in one” 由部署编排完成：镜像始终只有 OmniGate 本身（SQLite 已编译在内），PostgreSQL 与 Caddy 是 `deploy/docker-compose.yml` 中可选的
profile 服务，各自独立升级与备份。`deploy/omnigate.env` 与 `deploy/.env`（你的真实配置）已被 Git 忽略。

### 本机试用

development 模式，http://localhost:8080：

```bash
docker run --rm ghcr.io/okysu/omnigate:latest keygen          # 生成主密钥
curl -s https://api.github.com/users/<你的 GitHub 用户名> | grep '"id"'   # 查询不可变的数字 ID
docker run -d --name omnigate -p 127.0.0.1:8080:8080 -v omnigate-data:/data --stop-timeout 40 \
  -e OMNIGATE_MASTER_KEY='<keygen 输出的整行>' \
  -e OMNIGATE_AUTH_GITHUB_CLIENT_ID=… -e OMNIGATE_AUTH_GITHUB_CLIENT_SECRET=… \
  -e OMNIGATE_BOOTSTRAP_ADMINS=github-id:<数字 ID> \
  -e OMNIGATE_SEED_ON_START=builtin \
  ghcr.io/okysu/omnigate:latest
open http://localhost:8080                   # GitHub OAuth App 回调：http://localhost:8080/api/auth/github/callback
docker exec omnigate omnigate backup /data/backups/omnigate-$(date +%Y%m%d-%H%M%S).db   # 在线备份（VACUUM INTO）
```

- `--stop-timeout 40`：停止时给进行中的请求（含流式响应）最多 30 秒收尾。
- 引导管理员请用 `github-id:<数字 ID>`，不要用 `github:<用户名>`（用户名可被改名、被他人重新注册）。
- 备份文件名带时间戳：目标文件已存在时 `omnigate backup` 会拒绝覆盖。恢复步骤（含 `chown 65532:65532`）见部署手册第 9 节。
- 自己构建镜像：`make docker`（得到 `omnigate:dev`）。SQLite 与 PostgreSQL 的差异见 [deployment.md](docs/operations/deployment.md#sqlite-与-postgresql-差异)，
  全部环境变量见 [configuration.md](docs/operations/configuration.md)。从源码一键试用（网关 + PostgreSQL）仍可在仓库根目录执行
  `./scripts/init-env.sh && docker compose up -d --build`。

## 接入方式

在后台创建渠道和 API Key（`og-` 开头）后：

```bash
# OpenAI SDK / 兼容客户端
export OPENAI_BASE_URL=http://localhost:8080/v1 OPENAI_API_KEY=og-...
# Anthropic SDK / Claude Code
export ANTHROPIC_BASE_URL=http://localhost:8080 ANTHROPIC_AUTH_TOKEN=og-...
```

同一个 Key 可以通过任一协议访问任意渠道的模型：例如用 Claude Code 调用 OpenAI 兼容渠道上的模型，网关自动转换协议。

## 本地开发

需要 Go 1.26+（`server/go.mod` 的 `toolchain go1.26.9` 会让 go 命令自动下载并使用 1.26.9）、Node 24+、pnpm、Docker。

```bash
make dev-deps      # 启动 PostgreSQL、模拟 OIDC（http://localhost:9000）
make dev-server    # 后端 :8080（读取 .env.dev）
make dev-web       # 前端 http://localhost:5173 —— 选择“本地模拟 OIDC”，用户名填 admin 即为管理员
make test          # 单元 + 集成测试（集成测试在 dev-deps 的 PostgreSQL 与临时 SQLite 上各跑一遍）
make test-sqlite   # 只在 SQLite 上跑集成测试（无需外部服务）
make lint          # gofmt / go vet / staticcheck / eslint / vue-tsc
make vulncheck     # govulncheck：Go 标准库与依赖的已知漏洞
make build         # 生成嵌入前端的单二进制 bin/omnigate
make docker        # 构建单镜像 omnigate:dev（写入版本、提交号与构建时间）
```

直接 `go build` 得到的二进制不含前端（访问页面会提示“前端未构建”），API 不受影响；`make build` 与 Docker 镜像会嵌入前端。

## CI / 发布

GitHub Actions 只有一条流水线 `Build and Publish`（[.github/workflows/release.yml](.github/workflows/release.yml)），先调用
[ci.yml](.github/workflows/ci.yml)，全部通过才构建镜像：

| 阶段 | 内容 |
|---|---|
| CI（每次 push / PR） | gofmt、go vet、staticcheck、govulncheck；`go test -race` 分别在 PostgreSQL 服务与 SQLite 上运行（PostgreSQL 一轮带覆盖率：Job Summary 表格 + `coverage-report` 构件）；前端 lint / typecheck / test / build |
| 构建 | 本机架构镜像 → 冒烟检查（版本、内置目录、Go 工具链 ≥ go1.26.9）→ Trivy 扫描（有可修复的 CRITICAL 漏洞即失败）→ `linux/amd64,linux/arm64` 多架构构建，附 SBOM 与 provenance，GHA 缓存 |
| 推送（非 PR） | `ghcr.io/okysu/omnigate`：`main` 分支 → `latest`、`main`、`sha-<7位>`；标签 `v1.2.3` → `1.2.3`、`1.2`、`latest`、`sha-<7位>`；其他分支 → `<分支名>`、`sha-<7位>` |
| GitHub Release（`v*` 标签） | 自动生成更新说明；带 `-` 的标签（如 `v0.2.0-rc.1`）标记为预发布 |
| 部署（可选） | `main` / `v*` 推送成功后重启雨云 RCA 应用。需在仓库 Settings → Secrets and variables → Actions 中设置 `RAINYUN_API_KEY` 与 `RAINYUN_OMNIGATE_RCA_APP_ID`；未设置时自动跳过 |

只用内置的 `GITHUB_TOKEN`，不需要额外的 Secret。Dependabot（[.github/dependabot.yml](.github/dependabot.yml)）每周检查 Go 模块、
pnpm 依赖、GitHub Actions 与 Dockerfile 基础镜像。

发布一个版本：

```bash
git tag -a v0.1.0 -m "OmniGate v0.1.0"
git push origin v0.1.0        # 约 10–20 分钟后 ghcr.io/okysu/omnigate:0.1.0 可用，并生成 GitHub Release
```

首次发布后在 GitHub 的 Packages → omnigate → Package settings 中把包设为 Public（否则服务器需要 `docker login ghcr.io`）。
提交前在本地跑一遍与 CI 相同的检查：`make lint vulncheck test`（需要 `make dev-deps` 的 PostgreSQL）与 `cd web && pnpm lint && pnpm typecheck && pnpm test && pnpm build`。

## 目录

```
server/     Go 后端（模块化单体；cmd/omnigate 为入口；internal/seed/builtin 为内置价格与套餐目录）
web/        Vue 3 + TypeScript + shadcn-vue 管理后台
deploy/     生产部署：docker-compose.yml（含 postgres / caddy profile）、环境变量模板、Caddy / nginx 配置、目录说明
docs/       架构、威胁模型、ER、ADR、契约、运维文档
scripts/    初始化脚本
.github/    CI / 发布流水线、Dependabot
```

## 设计文档

从 [docs/README.md](docs/README.md) 开始阅读。关键决策：
插件引擎 Goja（[ADR-0002](docs/adr/0002-plugin-engine-goja.md)）、
声明式插件 UI（[ADR-0003](docs/adr/0003-plugin-ui-declarative.md)）、
三级租户（[ADR-0004](docs/adr/0004-tenancy-rbac.md)）、
OIDC + GitHub 登录（[ADR-0005](docs/adr/0005-auth-oidc-github.md)）、
计费与周期配额（[ADR-0006](docs/adr/0006-billing-quota-currency.md)，提议中）。
