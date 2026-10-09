#!/usr/bin/env sh
# 生成 .env：复制 .env.example 并填入随机主密钥与数据库密码。不会覆盖已有 .env。
#
#   ./scripts/init-env.sh                         # 本机试用（development，http://localhost:8080）
#   ./scripts/init-env.sh https://gw.example.com  # 服务器部署（自动设为 production）
set -eu
cd "$(dirname "$0")/.."
if [ -f .env ]; then
  echo ".env 已存在，未做修改。" >&2
  exit 1
fi
public_url="${1:-}"
key="k$(date -u +%Y%m%d)-$(head -c 3 /dev/urandom | od -An -tx1 | tr -d ' \n'):$(head -c 32 /dev/urandom | base64 | tr -d '\n')"
pw="$(head -c 24 /dev/urandom | base64 | tr -d '\n/+=' )"
sed -e "s|^OMNIGATE_MASTER_KEY=.*|OMNIGATE_MASTER_KEY=${key}|" \
    -e "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=${pw}|" .env.example > .env
case "$public_url" in
  "") ;;
  https://*)
    sed -i.bak -e "s|^OMNIGATE_ENV=.*|OMNIGATE_ENV=production|" \
               -e "s|^OMNIGATE_PUBLIC_URL=.*|OMNIGATE_PUBLIC_URL=${public_url%/}|" .env && rm -f .env.bak
    echo "已设为生产模式：OMNIGATE_ENV=production, OMNIGATE_PUBLIC_URL=${public_url%/}" ;;
  *)
    rm -f .env
    echo "服务器部署必须使用 https 地址，例如 ./scripts/init-env.sh https://gw.example.com" >&2
    exit 1 ;;
esac
chmod 600 .env
echo "已生成 .env（权限 600）。请填写 GitHub OAuth App 与 OMNIGATE_BOOTSTRAP_ADMINS 后执行 docker compose up -d --build"
echo "注意：请妥善备份 OMNIGATE_MASTER_KEY（与数据库备份分开保管），丢失后已加密的渠道凭据将无法解密。"
