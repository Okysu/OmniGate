SHELL := /bin/bash
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS = -s -w -X omnigate/internal/app.Version=$(VERSION) -X omnigate/internal/app.Commit=$(COMMIT) -X omnigate/internal/app.BuildTime=$(BUILD_TIME)
DOCKER_BUILD_ARGS = --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_TIME=$(BUILD_TIME)
TEST_DB ?= postgres://omnigate:omnigate-dev@localhost:5432/postgres?sslmode=disable

.PHONY: help dev-deps dev-env dev-server dev-web test test-unit test-integration test-sqlite lint lint-server lint-web vulncheck \
        build web-build docker docker-build up down logs keygen migrate migrate-status clean

help: ## 显示可用命令
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

dev-deps: ## 启动本地开发依赖（PostgreSQL、模拟 OIDC）
	docker compose --profile dev up -d postgres mock-oidc

dev-env: ## 生成 .env.dev.local（固定的本地开发主密钥，已被 git 忽略）
	@test -f .env.dev.local || { printf '# 本地开发专用（不要提交）。固定主密钥，保证重启后仍能解密渠道 Key。\nOMNIGATE_MASTER_KEY=kdev-%s:%s\n' "$$(head -c 3 /dev/urandom | od -An -tx1 | tr -d ' \n')" "$$(head -c 32 /dev/urandom | base64 | tr -d '\n')" > .env.dev.local; chmod 600 .env.dev.local; echo "已生成 .env.dev.local"; }

dev-server: dev-env ## 宿主机运行后端（读取 .env.dev 与 .env.dev.local）
	cd server && set -a && source ../.env.dev && source ../.env.dev.local && set +a && go run ./cmd/omnigate serve

dev-web: ## 运行前端开发服务器 http://localhost:5173（inotify 耗尽时自动改用轮询）
	@if [ -z "$$VITE_USE_POLLING" ] && ! ./scripts/check-inotify.sh; then export VITE_USE_POLLING=1; fi; \
	cd web && pnpm install && pnpm dev

test: test-unit test-integration test-sqlite ## 运行全部测试（PostgreSQL 与 SQLite 各跑一遍集成测试）

test-unit: ## 后端单元测试 + 前端测试
	cd server && go test -race -count=1 ./...
	cd web && pnpm test

test-integration: ## 后端集成测试（PostgreSQL，默认使用 dev-deps 的实例）
	cd server && OMNIGATE_TEST_DATABASE_URL='$(TEST_DB)' go test -race -count=1 ./...

test-sqlite: ## 后端集成测试（SQLite，每个测试一个临时数据库文件，无需外部服务）
	cd server && OMNIGATE_TEST_DATABASE_URL=sqlite go test -race -count=1 ./...

lint: lint-server lint-web ## 全部静态检查

lint-server:
	cd server && test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
	cd server && go vet ./...
	cd server && go run honnef.co/go/tools/cmd/staticcheck@latest ./...

vulncheck: ## 扫描 Go 标准库与依赖的已知漏洞（govulncheck，CI 同样执行）
	cd server && go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...

lint-web:
	cd web && pnpm lint && pnpm typecheck

web-build:
	cd web && pnpm install --frozen-lockfile && pnpm build

build: web-build ## 构建嵌入前端的单二进制 bin/omnigate（web 构建 → 复制到 webui/dist → 预压缩 → go build）
	find server/internal/webui/dist -mindepth 1 ! -name .gitkeep -exec rm -rf {} +
	cp -r web/dist/. server/internal/webui/dist/
	node scripts/precompress.mjs server/internal/webui/dist
	cd server && CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o ../bin/omnigate ./cmd/omnigate

IMAGE ?= omnigate:dev

docker: ## 构建 Docker 镜像（默认 omnigate:dev，可用 IMAGE=... 覆盖）
	docker build $(DOCKER_BUILD_ARGS) -t $(IMAGE) .

docker-build: ## 构建带版本号 tag 的 Docker 镜像 omnigate:$(VERSION)
	docker build $(DOCKER_BUILD_ARGS) -t omnigate:$(VERSION) .

up: ## docker compose 启动完整服务
	docker compose up -d --build

down: ## 停止 compose 服务（保留数据卷）
	docker compose down

logs:
	docker compose logs -f omnigate

keygen: ## 生成新的主密钥条目
	@cd server && go run ./cmd/omnigate keygen

# DATABASE_URL=... 覆盖 .env.dev 中的数据库，例如 make migrate DATABASE_URL=sqlite:///tmp/omnigate.db
DB_OVERRIDE = $(if $(DATABASE_URL),export OMNIGATE_DATABASE_URL='$(DATABASE_URL)' &&,)

migrate: dev-env ## 执行迁移（默认 .env.dev 的 PostgreSQL；DATABASE_URL=sqlite:///… 可用于 SQLite）
	cd server && set -a && source ../.env.dev && source ../.env.dev.local && set +a && $(DB_OVERRIDE) go run ./cmd/omnigate migrate

migrate-status: dev-env ## 查看迁移版本（同上，支持 DATABASE_URL 覆盖）
	cd server && set -a && source ../.env.dev && source ../.env.dev.local && set +a && $(DB_OVERRIDE) go run ./cmd/omnigate migrate status

clean:
	rm -rf bin web/dist
	find server/internal/webui/dist -mindepth 1 ! -name .gitkeep -exec rm -rf {} +
