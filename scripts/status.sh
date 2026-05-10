#!/usr/bin/env bash
# =============================================================================
# status.sh — VPS 全栈状态摘要 (5 段输出)
#
# 1. Container 状态     (docker compose ps + State + healthy)
# 2. Disk / Memory      (df / free / deploy/data 占用)
# 3. Activity           (last 5min 9 collector tick complete 行数)
# 4. Metrics 摘要        (curl :2112/metrics | grep 关键 counter)
# 5. Errors             (last 1h ERROR / FATAL / panic, 最近 10 条)
#
# Usage: bash scripts/status.sh
# Exit 0 = 全 OK / 部分 WARN, exit 非 0 = container 不可达
# =============================================================================

set -uo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

cyan()   { printf "\033[36m%s\033[0m\n" "$*"; }
green()  { printf "\033[32m%s\033[0m\n" "$*"; }
red()    { printf "\033[31m%s\033[0m\n" "$*"; }
yellow() { printf "\033[33m%s\033[0m\n" "$*"; }
section() { echo; cyan "=== $* ==="; }

COMPOSE="docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml"

# 1. Container
section "Container 状态"
$COMPOSE ps --format 'table {{.Service}}\t{{.Status}}\t{{.State}}' 2>/dev/null \
    || { red "compose ps 失败 (docker daemon 未运行?)"; exit 2; }

# 2. Disk / Memory
section "Disk / Memory"
df -h / 2>/dev/null | head -2
echo
free -h 2>/dev/null | grep -E '^Mem|^Swap'
echo
DATA_SIZE=$(du -sh deploy/data 2>/dev/null | cut -f1)
BACKUP_SIZE=$(du -sh backups 2>/dev/null | cut -f1)
printf "  deploy/data:  %s\n  backups/:     %s\n" "${DATA_SIZE:-N/A}" "${BACKUP_SIZE:-N/A}"

# 3. Activity — 9 collector completed (last 5min)
# collector names match c.Name() in each collector's Name() method:
#   oi (not oi_history), square_feed (not square)
section "Activity (last 5min, 9 collectors)"
LOGS_5M=$($COMPOSE logs --since=5m --tail=2000 trader 2>/dev/null)
for c in oi btc_regime klines square_feed square_hashtag watchlist position_price signal_engine decision_engine; do
    # runner.go always logs: "collector completed collector=<name>" (pretty)
    # or {"message":"collector completed","collector":"<name>",...}  (json)
    COUNT=$(echo "$LOGS_5M" | grep -cE "collector completed collector=${c}( |$)|\"collector\":\"${c}\".*collector completed" 2>/dev/null || true)
    printf "  %-18s %s ticks\n" "$c" "$COUNT"
done

# 4. Metrics 摘要
section "Metrics 摘要 (key counters)"
if curl -fs --max-time 3 http://localhost:2112/metrics > /tmp/.trader_metrics 2>/dev/null; then
    grep -E '^trader_(collector_runs_total|decision_evaluations_total|panic_total|circuit_breaker_state|signals_total)' /tmp/.trader_metrics \
        | head -20
    rm -f /tmp/.trader_metrics
else
    red "  :2112/metrics 不可达"
fi

# 5. Errors
section "Errors (last 1h, 最近 10 条)"
ERR=$($COMPOSE logs --since=1h --tail=2000 trader 2>/dev/null \
    | grep -E '"level":"error"|"level":"fatal"|panic:' \
    | tail -10 || true)
if [[ -z "$ERR" ]]; then
    green "  ✓ 无 ERROR / FATAL / panic"
else
    echo "$ERR"
fi

echo
