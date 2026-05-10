#!/usr/bin/env bash
# =============================================================================
# ssh-tunnel.sh — mu 本机 → VPS PG/Redis SSH tunnel (deployment v0.1 Round 3)
#
# 本机 → VPS 安全访问数据库 (per Round 0 决策 5):
#   PG     5432  (VPS 127.0.0.1 only)  → 本机 :15432
#   Redis  6379  (VPS 127.0.0.1 only)  → 本机 :16379
#
# 本机 .env.dev 配置:
#   DATABASE_URL=postgres://trader:trader@localhost:15432/trader?sslmode=disable
#   REDIS_URL=redis://localhost:16379/0
#
# Usage:
#   bash scripts/ssh-tunnel.sh up [vps-host]    # 后台启动 (nohup + PID 文件)
#   bash scripts/ssh-tunnel.sh down              # 停止
#   bash scripts/ssh-tunnel.sh status            # PID + 端口监听状态
#
# vps-host 默认 $TRADER_VPS_HOST 环境变量 (用 ~/.ssh/config 别名最便利)
# =============================================================================

set -uo pipefail

PID_FILE="/tmp/trader-ssh-tunnel.pid"
LOG_FILE="/tmp/trader-ssh-tunnel.log"
DEFAULT_HOST="${TRADER_VPS_HOST:-vps}"

cmd="${1:-status}"
host="${2:-$DEFAULT_HOST}"

case "$cmd" in
    up)
        if [[ -f "$PID_FILE" ]] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
            echo "tunnel already up (pid=$(cat "$PID_FILE"))"
            exit 0
        fi
        echo "▶ 启动 tunnel → $host (PG :15432, Redis :16379)"
        nohup ssh -N \
            -L 15432:localhost:5432 \
            -L 16379:localhost:6379 \
            -o ServerAliveInterval=60 \
            -o ExitOnForwardFailure=yes \
            "$host" >"$LOG_FILE" 2>&1 &
        echo $! > "$PID_FILE"
        sleep 2
        if kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
            echo "✓ tunnel up (pid=$(cat "$PID_FILE"))"
            echo "  本机连 PG:    psql postgres://trader:trader@localhost:15432/trader"
            echo "  本机连 Redis: redis-cli -p 16379"
        else
            echo "✗ tunnel 启动失败, 看 $LOG_FILE"
            rm -f "$PID_FILE"
            exit 1
        fi
        ;;
    down)
        if [[ ! -f "$PID_FILE" ]]; then
            echo "tunnel not running (no PID file)"
            exit 0
        fi
        PID=$(cat "$PID_FILE")
        if kill -0 "$PID" 2>/dev/null; then
            kill "$PID"
            echo "✓ tunnel down (pid=$PID killed)"
        fi
        rm -f "$PID_FILE"
        ;;
    status)
        if [[ -f "$PID_FILE" ]] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
            echo "✓ tunnel up (pid=$(cat "$PID_FILE"))"
            ss -tln 2>/dev/null | grep -E ':(15432|16379)' || echo "  ⚠ 端口未监听"
        else
            echo "✗ tunnel down"
        fi
        ;;
    *)
        echo "Usage: $0 {up|down|status} [vps-host]" >&2
        exit 1
        ;;
esac
