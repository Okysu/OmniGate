# syntax=docker/dockerfile:1
# OmniGate 单镜像构建：前端（web/dist）嵌入 Go 二进制，一个端口同时提供页面、/api、/v1。
#
#   docker build --build-arg VERSION=v0.3.0 -t omnigate:v0.3.0 .     # 或 make docker
#   可选：--build-arg COMMIT=$(git rev-parse HEAD) --build-arg BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)
#   （`omnigate version` 与启动日志会打印这三项；CI 发布流程 .github/workflows/release.yml 会自动传入）
#
# 运行时镜像为 distroless static（nonroot，uid 65532）：无 shell、无包管理器；
# CA 证书随基础镜像提供，IANA 时区库编译进二进制（time/tzdata）。

#
# 基础镜像固定到精确版本 + 摘要（多架构 index digest），保证可复现构建与供应链可审计。
# 升级时同时修改 tag 与 digest：docker pull <tag> && docker image inspect --format '{{.RepoDigests}}' <tag>
# Go 版本须 ≥ server/go.mod 的 toolchain 行（安全修复：go1.26.9 修复了 net/http、crypto/tls、html/template 的已知漏洞）。
ARG NODE_IMAGE=node:24.21.0-alpine@sha256:ebfe2f90462722a7a4de65e91990e97fe0d401c70e0e762c5b53302f905ec1c1
ARG GO_IMAGE=golang:1.26.9-alpine@sha256:cdfd4fe2da6b225d8b40c6b7a105736e548e83ff56d5d8f9394446eeb5eb84e0
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

# 多架构构建：前端与 Go 编译都在构建机本机架构上运行（--platform=$BUILDPLATFORM），Go 交叉编译到目标架构
# （纯 Go，CGO_ENABLED=0）；运行时阶段只复制文件。因此 arm64 镜像不需要 QEMU 模拟，构建快很多。

# ---------- 1. 前端（与架构无关，只构建一次） ----------
FROM --platform=$BUILDPLATFORM ${NODE_IMAGE} AS web
WORKDIR /src/web
ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=0 \
    PNPM_HOME=/pnpm \
    CI=true
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./
RUN --mount=type=cache,id=omnigate-pnpm,target=/pnpm/store \
    pnpm config set store-dir /pnpm/store && \
    pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build
COPY scripts/precompress.mjs /src/scripts/precompress.mjs
RUN node /src/scripts/precompress.mjs dist

# ---------- 2. 后端（嵌入前端，交叉编译到目标架构） ----------
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS server
WORKDIR /src
# 官方 golang 镜像默认 GOTOOLCHAIN=local，会忽略 go.mod 的 toolchain 行。改为 auto：
# 镜像自带的 Go 低于 toolchain 行时自动下载该版本（经 GOPROXY 校验），保证产物不低于 go1.26.9。
ENV GOTOOLCHAIN=auto
COPY server/go.mod server/go.sum ./
RUN --mount=type=cache,id=omnigate-gomod,target=/go/pkg/mod go mod download
COPY server/ ./
COPY --from=web /src/web/dist/ ./internal/webui/dist/
ARG VERSION=dev
ARG COMMIT=
ARG BUILD_TIME=
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,id=omnigate-gomod,target=/go/pkg/mod \
    --mount=type=cache,id=omnigate-gobuild,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -trimpath \
      -ldflags "-s -w -X omnigate/internal/app.Version=${VERSION} -X omnigate/internal/app.Commit=${COMMIT} -X omnigate/internal/app.BuildTime=${BUILD_TIME}" \
      -o /out/omnigate ./cmd/omnigate \
 && go version /out/omnigate \
 && mkdir -p /out/data

# ---------- 3. 运行时 ----------
FROM ${RUNTIME_IMAGE}
ARG VERSION=dev
ARG COMMIT=
ARG BUILD_TIME=
# CI（docker/metadata-action）会用同名标签覆盖这些值。
LABEL org.opencontainers.image.title="OmniGate" \
      org.opencontainers.image.description="自托管的统一大模型 API 网关（单镜像：网关 + 管理后台，内置 GPT 价格与套餐目录）" \
      org.opencontainers.image.source="https://github.com/Okysu/OmniGate" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_TIME}"
COPY --from=server /out/omnigate /usr/local/bin/omnigate
# /data：持久化目录。未设置 OMNIGATE_DATABASE_URL 时使用其中的 SQLite 数据库（单容器、零依赖，ADR-0009）；
# 使用 PostgreSQL 时设置 OMNIGATE_DATABASE_URL=postgres://… 覆盖。
# 这就是 OmniGate 的 "all-in-one"：同一个镜像，不设数据库变量即自带 SQLite，不需要内嵌 PostgreSQL。
COPY --from=server --chown=65532:65532 /out/data /data
ENV OMNIGATE_HTTP_ADDR=:8080 \
    OMNIGATE_DATABASE_URL=sqlite:///data/omnigate.db
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8080
USER 65532:65532
HEALTHCHECK --interval=15s --timeout=5s --start-period=30s --retries=3 \
  CMD ["/usr/local/bin/omnigate", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/omnigate"]
CMD ["serve"]
