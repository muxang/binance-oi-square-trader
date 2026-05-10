#!/usr/bin/env bash
# =============================================================================
# update.sh — 增量更新 (代码更新后, deployment v0.1 Round 2 自 deploy.sh rename)
#
# 1. git pull
# 2. 校验 .env 必需项
# 3. 重 build trader 镜像
# 4. 重启 trader 容器(其它基础设施不动)
# 5. 跑迁移(若有新迁移)
# 6. 健康检查
#
# Usage: bash scripts/update.sh
# =============================================================================

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

red()    { printf "\033[31m%s\033[0m\n" "$*"; }
green()  { printf "\033[32m%s\033[0m\n" "$*"; }
yellow() { printf "\033[33m%s\033[0m\n" "$*"; }
blue()   { printf "\033[34m%s\033[0m\n" "$*"; }

step() { blue "▶ $*"; }
ok()   { green "  ✓ $*"; }
err()  { red "  ✗ $*"; exit 1; }

# -----------------------------------------------------------------------------
# 0. 校验 .env (避免误推不完整配置)
# -----------------------------------------------------------------------------
step "校验 .env"
if [[ ! -f .env ]]; then
    err ".env 不存在"
fi

set -a; source .env; set +a

REQUIRED_VARS=(BINANCE_API_KEY BINANCE_API_SECRET TG_BOT_TOKEN TG_CHAT_ID DATABASE_URL REDIS_URL)
for var in "${REQUIRED_VARS[@]}"; do
    if [[ -z "${!var:-}" ]]; then
        err ".env 缺少 $var"
    fi
done

if [[ "${TRADER_MODE:-}" == "mainnet" ]] && [[ "${TRADER_MAINNET_CONFIRM:-}" != "I_UNDERSTAND" ]]; then
    err "TRADER_MODE=mainnet 必须配合 TRADER_MAINNET_CONFIRM=I_UNDERSTAND"
fi

ok ".env 校验通过 (TRADER_MODE=$TRADER_MODE)"

# -----------------------------------------------------------------------------
# 1. 拉代码
# -----------------------------------------------------------------------------
step "git pull"
git fetch
LOCAL_HEAD="$(git rev-parse HEAD)"
git pull --ff-only
NEW_HEAD="$(git rev-parse HEAD)"

if [[ "$LOCAL_HEAD" == "$NEW_HEAD" ]]; then
    yellow "  ⚠ 没有新提交, 仍执行 rebuild + restart"
else
    ok "更新到 $NEW_HEAD"
fi

# -----------------------------------------------------------------------------
# 2. 重 build + 重启 trader
# -----------------------------------------------------------------------------
step "重新 build trader 镜像"
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml \
    build trader
ok "镜像 build 完成"

step "重启 trader 容器"
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml \
    up -d --no-deps trader
ok "trader 已重启"

# -----------------------------------------------------------------------------
# 3. 跑新迁移(若有)
# -----------------------------------------------------------------------------
step "运行数据库迁移"
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml \
    run --rm --no-deps --entrypoint="" trader \
    sh -c '/app/migrate -path /app/migrations -database "$DATABASE_URL" up' \
    || err "迁移失败"
ok "迁移完成 (无新迁移则无操作)"

step "等待 trader /health 就绪 (最多 60s)"
for i in $(seq 1 20); do
    if curl -fs --max-time 3 http://localhost:8080/health >/dev/null 2>&1; then
        ok "trader /health OK"
        break
    fi
    sleep 3
    if [[ $i -eq 20 ]]; then
        yellow "  ⚠ trader 60s 未就绪"
        yellow "    docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml logs --tail=50 trader"
    fi
done

# -----------------------------------------------------------------------------
# 4. 健康检查
# -----------------------------------------------------------------------------
step "健康检查"
bash scripts/healthcheck.sh

# -----------------------------------------------------------------------------
# 完成
# -----------------------------------------------------------------------------
echo
green "✅ Deploy 完成"
echo "查看日志: docker compose -f deploy/docker-compose.yml logs -f --tail=100 trader"
