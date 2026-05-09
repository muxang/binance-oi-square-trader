#!/usr/bin/env bash
# scripts/e2e-phase0.sh — Phase 0 end-to-end acceptance check.
# Runs from WSL/Linux only. Requires: make, go, docker, migrate, curl.
# No mocks: real postgres+redis containers, real binary, real /health.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

PASS=0
FAIL=0
TRADER_PID=""
TRADER_LOG="$(mktemp)"

step() { echo; echo "==> $*"; }
ok()   { echo "[PASS] $*"; PASS=$((PASS + 1)); }
ko()   { echo "[FAIL] $*"; FAIL=$((FAIL + 1)); }

cleanup() {
    if [ -n "$TRADER_PID" ] && kill -0 "$TRADER_PID" 2>/dev/null; then
        kill -KILL "$TRADER_PID" 2>/dev/null
    fi
    docker compose -f deploy/docker-compose.yml down >/dev/null 2>&1 || true
    rm -f "$TRADER_LOG"
}
trap cleanup EXIT

require() {
    command -v "$1" >/dev/null 2>&1 || { echo "missing prerequisite: $1"; exit 2; }
}
for c in make go docker migrate curl; do require "$c"; done

# Auto-provision .env with dummy required values if absent.
if [ ! -f .env ]; then
    cp .env.example .env
    sed -i 's/^BINANCE_API_KEY=$/BINANCE_API_KEY=dummy-e2e-key/'    .env
    sed -i 's/^BINANCE_API_SECRET=$/BINANCE_API_SECRET=dummy-e2e-secret/' .env
    sed -i 's/^TG_BOT_TOKEN=$/TG_BOT_TOKEN=000000:dummy/'           .env
    sed -i 's/^TG_CHAT_ID=$/TG_CHAT_ID=12345/'                      .env
fi

step "1. make typecheck"
if make typecheck >/dev/null 2>&1; then ok "typecheck"; else ko "typecheck"; fi

step "2. make test"
if make test >/dev/null 2>&1; then ok "test"; else ko "test"; fi

step "3. make build"
if make build >/dev/null 2>&1; then ok "build"; else ko "build"; fi

step "4. docker compose up postgres + redis"
if docker compose -f deploy/docker-compose.yml up -d postgres redis >/dev/null 2>&1; then
    ok "docker up"
else
    ko "docker up"
fi

step "5. wait for postgres"
ready=0
for i in $(seq 1 30); do
    if docker exec trader-postgres pg_isready -U trader >/dev/null 2>&1; then
        ok "postgres ready (${i}s)"
        ready=1
        break
    fi
    sleep 1
done
[ "$ready" = "1" ] || ko "postgres not ready after 30s"

step "6. make migrate"
set -a; source .env; set +a
if make migrate >/dev/null 2>&1; then ok "migrate"; else ko "migrate"; fi

step "7. verify 12 business tables"
N=$(docker exec trader-postgres psql -U trader -d trader -tAc \
    "SELECT count(*) FROM information_schema.tables \
     WHERE table_schema='public' AND table_type='BASE TABLE' \
     AND table_name <> 'schema_migrations';" \
    | tr -d '[:space:]')
if [ "$N" = "12" ]; then ok "12 tables present"; else ko "tables: expected 12 got $N"; fi

step "8. start trader (background)"
nohup ./bin/trader > "$TRADER_LOG" 2>&1 &
TRADER_PID=$!
ok "trader pid=$TRADER_PID"

step "9. wait 5s for startup"
sleep 5

step "10. curl /health"
HEALTH="$(curl -sS --max-time 5 http://localhost:8080/health || echo FAIL)"
echo "$HEALTH" | grep -q '"status":"ok"'        && ok "/health status=ok"          || ko "/health status: $HEALTH"
echo "$HEALTH" | grep -q '"mode":"testnet"'     && ok "/health mode=testnet"       || ko "/health mode: $HEALTH"
echo "$HEALTH" | grep -q '"pg":"ok"'            && ok "/health deps.pg=ok"         || ko "/health pg: $HEALTH"
echo "$HEALTH" | grep -q '"redis":"ok"'         && ok "/health deps.redis=ok"      || ko "/health redis: $HEALTH"

step "11. curl /metrics"
CODE="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 5 http://localhost:8080/metrics)"
[ "$CODE" = "200" ] && ok "/metrics http=200" || ko "/metrics http=$CODE"

step "12. startup-banner field check"
# Strip ANSI escapes (zerolog pretty format colours field names) before grep,
# otherwise "mode=testnet" splits across reset codes and matches fail.
H20="$(head -20 "$TRADER_LOG" | sed -E 's/\x1b\[[0-9;]*m//g')"
for field in "mode=testnet" "proxy_mode=" "timezone=Asia/Shanghai" "utc_now=" "bjt_now="; do
    echo "$H20" | grep -q "$field" && ok "banner contains $field" || ko "banner missing: $field"
done

step "13. graceful shutdown (SIGINT)"
kill -INT "$TRADER_PID"
exited=0
for i in $(seq 1 10); do
    if ! kill -0 "$TRADER_PID" 2>/dev/null; then exited=1; break; fi
    sleep 1
done
if [ "$exited" = "1" ]; then
    wait "$TRADER_PID" 2>/dev/null
    EXIT=$?
    [ "$EXIT" -eq 0 ] && ok "trader exited 0" || ko "trader exit=$EXIT"
    grep -q "shutdown complete" "$TRADER_LOG" && ok "shutdown-complete logged" \
        || ko "shutdown-complete missing"
    TRADER_PID=""
else
    ko "trader still running 10s after SIGINT"
fi

step "14. docker compose down (cleanup)"
if docker compose -f deploy/docker-compose.yml down >/dev/null 2>&1; then
    ok "docker down"
else
    ko "docker down"
fi

echo
echo "===================="
echo "  Pass: $PASS"
echo "  Fail: $FAIL"
echo "===================="
[ "$FAIL" -eq 0 ] && exit 0 || exit 1
