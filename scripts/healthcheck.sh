#!/usr/bin/env bash
# =============================================================================
# healthcheck.sh — 全链路健康检查
#
# 1. trader /health
# 2. PG 连通 + 表数
# 3. Redis 连通
# 4. Prometheus / Grafana / Loki 连通
# 5. 币安 API 连通(若代理配置 → 走代理测)
# 6. TG bot getMe
#
# Usage: bash scripts/healthcheck.sh
# Exit 0 = 全部通过, exit 1 = 至少一项失败
# =============================================================================

set -uo pipefail   # 不用 -e, 失败不退出, 全跑完汇总

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

red()    { printf "\033[31m%s\033[0m\n" "$*"; }
green()  { printf "\033[32m%s\033[0m\n" "$*"; }
yellow() { printf "\033[33m%s\033[0m\n" "$*"; }

PASS=0
FAIL=0
check() {
    local name="$1"; shift
    if "$@" >/dev/null 2>&1; then
        green "✓ $name"
        ((PASS++))
    else
        red   "✗ $name"
        ((FAIL++))
    fi
}

set -a; source .env 2>/dev/null || true; set +a

echo "=== Trader Healthcheck ==="
echo "TRADER_MODE: ${TRADER_MODE:-<unset>}"
echo "PROXY_MODE:  ${BINANCE_PROXY_MODE:-<unset>}"
echo "Time (BJT):  $(TZ=Asia/Shanghai date)"
echo "Time (UTC):  $(TZ=UTC date)"
echo

# 1. trader /health
check "trader /health" curl -fs http://localhost:8080/health

# 2. PG
check "postgres pg_isready" \
    docker compose -f deploy/docker-compose.yml exec -T postgres pg_isready -U trader

# 3. Redis
check "redis ping" \
    docker compose -f deploy/docker-compose.yml exec -T redis redis-cli ping

# 4. Prometheus
check "prometheus /-/ready" curl -fs http://localhost:9090/-/ready

# 5. Grafana
check "grafana /api/health" curl -fs http://localhost:3001/api/health

# 6. Loki
check "loki /ready" curl -fs http://localhost:3100/ready

# 7. 币安 API 连通(走应用层探针)
check "binance api reachable (via trader)" \
    curl -fs http://localhost:8080/health/binance

# 8. TG bot 连通
if [[ -n "${TG_BOT_TOKEN:-}" ]]; then
    check "telegram bot getMe" \
        curl -fs "https://api.telegram.org/bot${TG_BOT_TOKEN}/getMe"
else
    yellow "- TG_BOT_TOKEN 未设置, 跳过 telegram 检查"
fi

echo
echo "=========================="
echo "Pass: $PASS   Fail: $FAIL"
echo "=========================="

if [[ $FAIL -gt 0 ]]; then
    red "❌ 健康检查未通过"
    echo
    echo "排查建议:"
    echo "  docker compose -f deploy/docker-compose.yml ps"
    echo "  docker compose -f deploy/docker-compose.yml logs --tail=100 trader"
    exit 1
fi

green "✅ 全部通过"
exit 0
