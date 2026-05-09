#!/usr/bin/env bash
# scripts/e2e-phase1-longrun.sh — Phase 1 (1.9) end-to-end 6h long-run.
# Mode: PROXY=pool, real Binance + PG + Redis, no mocks.
# Exit: 0=acceptance written, 1=preflight failed, 2=longrun aborted, 3=prereq failed.
#
# shellcheck disable=SC2015,SC2012
# SC2015 (A && B || C): ok/ko are pure echo + counter increment — never fail,
#                       so the pattern is safe here (concise verdict reporting).
# SC2012 (use find not ls): we count files in dirs we own (snap_t* / incident_*),
#                           filenames are predictable, ls | wc -l is fine.

set -euo pipefail

# Go 不在 WSL 默认 login shell PATH (装在 /usr/local/go/bin), 显式补上
# 这样无论调用环境 (cron / CI / 直接交互) 都能找到 go.
export PATH="/usr/local/go/bin:$PATH"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

DURATION=6
SKIP_PREFLIGHT=false
ENV_INTERVAL=30
SNAP_INTERVAL=30
PROXY_MODE=pool
ABORT_ON_PANIC=true
DRY_RUN=false
OUTPUT_DIR=""

PASS=0; FAIL=0; ALARMS=0
TRADER_PID=""; ENV_MON_PID=""
SHUTDOWN_ELAPSED_MS=""; POST_SHUTDOWN_CURL=""
START_TS=""; END_TS=""; ELAPSED_HOURS=""

usage() {
    cat <<EOF
Usage: $0 [--duration H=6] [--skip-preflight] [--output-dir P]
  [--env-monitor-interval S=30] [--metrics-snapshot-interval M=30]
  [--proxy-mode none|single|pool=pool] [--abort-on-panic true|false=true]
  [--dry-run] [-h|--help]
EOF
}

step()  { echo; echo "==> $*"; echo "[STEP] $*" >> "$OUTPUT_DIR/run.log" 2>/dev/null || true; }
ok()    { echo "[PASS] $*"; PASS=$((PASS+1)); }
ko()    { echo "[FAIL] $*"; FAIL=$((FAIL+1)); }
alarm() { echo "[ALARM] $(date '+%F %T') $*" | tee -a "$OUTPUT_DIR/alarm.log" >&2; ALARMS=$((ALARMS+1)); }

prereq_check() {
    for c in go docker curl lsof md5sum awk bc; do
        command -v "$c" >/dev/null 2>&1 || { echo "missing: $c"; exit 3; }
    done
    docker exec trader-postgres pg_isready -U trader >/dev/null 2>&1 || { echo "postgres down"; exit 3; }
    docker exec trader-redis redis-cli ping 2>/dev/null | grep -q PONG || { echo "redis down"; exit 3; }
}

build_and_test() {
    go build -o bin/trader ./cmd/trader && ok "build" || { ko "build"; exit 3; }
    if go test -count=1 ./internal/... >/dev/null 2>&1; then ok "test"; else ko "test"; exit 3; fi
}

# get_metric file collector outcome → echo numeric value (or 0)
get_metric() {
    awk -v c="\"$2\"" -v o="\"$3\"" \
        '$0 ~ /trader_collector_runs_total/ && $0 ~ "collector="c && $0 ~ "outcome="o {print $2+0; exit}' "$1"
}

monitor_env() {
    local interval="$1" out="$2" baseline cur ts inc
    baseline=$(md5sum .env | awk '{print $1}')
    echo "[ENV] baseline=$baseline pid=$$ start=$(date '+%F %T')" >> "$out/env_audit.log"
    while true; do
        sleep "$interval"
        grep -qE '^BINANCE_API_KEY=.+' .env || alarm "ENV: BINANCE_API_KEY EMPTY"
        cur=$(md5sum .env | awk '{print $1}')
        [ "$cur" = "$baseline" ] && continue
        ts=$(date +%s)
        inc="$out/env_incidents/incident_${ts}"
        mkdir -p "$inc"
        cp .env "$inc/env_t1.snap"
        ps -ef > "$inc/ps_t1.snap"
        lsof .env > "$inc/lsof_t1.snap" 2>&1 || echo "(no holders)" > "$inc/lsof_t1.snap"
        sleep 5
        cp .env "$inc/env_t2.snap"
        diff "$inc/env_t1.snap" "$inc/env_t2.snap" > "$inc/diff_t1_t2.txt" 2>&1 || true
        alarm "ENV CHANGED $baseline -> $cur DIR=$inc"
        echo "[ENV-INCIDENT] ts=$ts dir=$inc $baseline -> $cur" >> "$out/env_audit.log"
        baseline="$cur"
    done
}

take_snapshot() {
    local tag="$1" out="$2" snap="$2/snap_$1"
    mkdir -p "$snap"
    curl -s --max-time 5 "http://localhost:2112/metrics" > "$snap/metrics.txt" || true
    docker exec trader-postgres psql -U trader -d trader -tAc "
        SELECT 'oi_history='||COUNT(*) FROM oi_history UNION ALL
        SELECT 'klines='||COUNT(*) FROM klines UNION ALL
        SELECT 'square_posts='||COUNT(*) FROM square_posts UNION ALL
        SELECT 'square_hashtag_history='||COUNT(*) FROM square_hashtag_history UNION ALL
        SELECT 'watchlist_snapshots='||COUNT(*) FROM watchlist_snapshots;
    " > "$snap/db_counts.txt" 2>&1 || true
    {
        echo "DBSIZE=$(docker exec trader-redis redis-cli DBSIZE 2>/dev/null)"
        echo "EXISTS_watchlist_current=$(docker exec trader-redis redis-cli EXISTS watchlist:current 2>/dev/null)"
        echo "EXISTS_btc_5m_change=$(docker exec trader-redis redis-cli EXISTS btc_5m_change 2>/dev/null)"
        for prefix in atr ema20 latest_price; do
            local n
            n=$(docker exec trader-redis redis-cli --scan --pattern "${prefix}:*" 2>/dev/null | wc -l)
            echo "${prefix}_count=${n}"
        done
    } > "$snap/redis_keys.txt"
    if [ -f "$out/trader.log" ]; then
        sed -r 's/\x1B\[[0-9;]*[a-zA-Z]//g' "$out/trader.log" \
            | grep -iE 'level=err|^ERR |panic|fatal' | tail -50 > "$snap/log_errors.txt" || true
    fi
    if [ -n "$TRADER_PID" ] && kill -0 "$TRADER_PID" 2>/dev/null; then
        ps -o pid,rss,vsz,pcpu,etime,comm -p "$TRADER_PID" > "$snap/process.txt"
    else
        echo "(trader not running)" > "$snap/process.txt"
    fi
    echo "[SNAP] tag=$tag dir=$snap $(date '+%F %T')" >> "$out/run.log"
}

# evaluate_health cur prev → 0=continue, 1=alarm, 2=abort
evaluate_health() {
    local cur="$1" prev="${2:-}" rc=0 pn bs be btot
    if [ -n "$TRADER_PID" ] && ! kill -0 "$TRADER_PID" 2>/dev/null; then
        alarm "ABORT: trader gone (pid=$TRADER_PID)"; return 2
    fi
    pn=$(awk '/outcome="panic"/ {s+=$2} END {print s+0}' "$cur/metrics.txt")
    if [ "$ABORT_ON_PANIC" = "true" ] && [ "$pn" -gt 0 ]; then
        alarm "ABORT: panic counter=$pn"; return 2
    fi
    bs=$(get_metric "$cur/metrics.txt" btc_regime success); bs=${bs:-0}
    be=$(get_metric "$cur/metrics.txt" btc_regime error); be=${be:-0}
    btot=$((bs + be))
    if [ "$btot" -ge 5 ] && [ "$bs" -eq 0 ]; then
        alarm "ABORT: btc_regime tot=$btot success=0 (proxy/IP-ban catastrophe)"; return 2
    fi
    [ -z "$prev" ] && return 0
    local s_n s_p e_n e_p sd ed total rate
    s_n=$(get_metric "$cur/metrics.txt" btc_regime success); s_n=${s_n:-0}
    s_p=$(get_metric "$prev/metrics.txt" btc_regime success); s_p=${s_p:-0}
    e_n=$(get_metric "$cur/metrics.txt" btc_regime error); e_n=${e_n:-0}
    e_p=$(get_metric "$prev/metrics.txt" btc_regime error); e_p=${e_p:-0}
    sd=$((s_n - s_p)); ed=$((e_n - e_p)); total=$((sd + ed))
    if [ "$total" -ge 5 ]; then
        rate=$((sd * 100 / total))
        if   [ "$rate" -lt 50 ]; then alarm "ABORT: btc_regime 30min rate=$rate% (s=$sd e=$ed)"; return 2
        elif [ "$rate" -lt 90 ]; then alarm "btc_regime 30min rate=$rate% (50-90%)"; rc=1
        fi
    fi
    return $rc
}

run_preflight() {
    local out="$1" pf="$1/preflight" active r0 r10 bs be btot rt to pct
    mkdir -p "$pf"
    BINANCE_PROXY_MODE=pool BINANCE_PROXY_POOL_FILE=deploy/proxies.txt \
        setsid ./bin/trader > "$out/trader.log" 2>&1 < /dev/null &
    TRADER_PID=$!
    sleep 5
    if ! kill -0 "$TRADER_PID" 2>/dev/null; then
        ko "preflight: trader 启动后秒退"
        tail -20 "$out/trader.log" > "$pf/startup_error.txt"
        echo FAIL > "$pf/verdict.txt"
        TRADER_PID=""; return 1
    fi
    curl -s "http://localhost:2112/metrics" > "$pf/metrics_t0.txt" || true
    sleep 600
    curl -s "http://localhost:2112/metrics" > "$pf/metrics_t10.txt" || true
    active=$(awk '/^binance_proxy_active_count/ {print $2+0; exit}' "$pf/metrics_t10.txt"); active=${active:-0}
    [ "$active" -ge 35 ] && ok "V1 active=$active >=35" || ko "V1 active=$active (need >=35)"
    r0=$(awk '/^binance_proxy_requests_total\{.*outcome="success"/{s+=$2} END{print s+0}' "$pf/metrics_t0.txt")
    r10=$(awk '/^binance_proxy_requests_total\{.*outcome="success"/{s+=$2} END{print s+0}' "$pf/metrics_t10.txt")
    [ "$r10" -gt "$r0" ] && ok "V2 success_total $r0 -> $r10 (递增)" || ko "V2 stuck $r0 -> $r10"
    bs=$(get_metric "$pf/metrics_t10.txt" btc_regime success); bs=${bs:-0}
    be=$(get_metric "$pf/metrics_t10.txt" btc_regime error); be=${be:-0}
    btot=$((bs + be))
    [ "$bs" -ge 8 ] && ok "V3 btc_regime $bs/$btot (>=8 ok)" || ko "V3 btc_regime $bs/$btot <8"
    rt=$(awk '/^binance_proxy_requests_total\{/ {s+=$2} END{print s+0}' "$pf/metrics_t10.txt")
    to=$(awk '/^binance_proxy_failures_total\{.*error_type="timeout"/{s+=$2} END{print s+0}' "$pf/metrics_t10.txt")
    pct=0; [ "$rt" -gt 0 ] && pct=$((to * 100 / rt))
    [ "$pct" -lt 5 ] && ok "V4 timeout=$to/$rt ($pct%)" || ko "V4 timeout $pct% >=5%"
    if [ "$FAIL" -gt 0 ]; then
        echo FAIL > "$pf/verdict.txt"
        kill -INT "$TRADER_PID" 2>/dev/null || true
        wait "$TRADER_PID" 2>/dev/null || true
        TRADER_PID=""
        return 1
    fi
    echo PASS > "$pf/verdict.txt"
    return 0  # 复用 trader 不杀, longrun 接手
}

monitor_30min_loop() {
    local out="$1" snap_count i tag prev cur rc
    snap_count=$((DURATION * 60 / SNAP_INTERVAL))
    take_snapshot "t0" "$out"
    prev="$out/snap_t0"
    for i in $(seq 1 "$snap_count"); do
        sleep "$((SNAP_INTERVAL * 60))"
        tag="t$i"
        take_snapshot "$tag" "$out"
        cur="$out/snap_${tag}"
        rc=0
        evaluate_health "$cur" "$prev" || rc=$?
        if [ "$rc" = "2" ]; then
            alarm "ABORT triggered at $tag, generating partial acceptance"
            return 2
        fi
        prev="$cur"
    done
    return 0
}

shutdown_trader() {
    local t0 t1 i
    [ -z "$TRADER_PID" ] && return 0
    t0=$(date +%s%3N)
    kill -INT "$TRADER_PID" 2>/dev/null || return 0
    for i in $(seq 1 30); do
        kill -0 "$TRADER_PID" 2>/dev/null || break
        sleep 1
    done
    if kill -0 "$TRADER_PID" 2>/dev/null; then
        ko "shutdown: trader still alive after 30s, KILLing"
        kill -KILL "$TRADER_PID" 2>/dev/null
    else
        t1=$(date +%s%3N)
        SHUTDOWN_ELAPSED_MS=$((t1 - t0))
        ok "shutdown: exit in ${SHUTDOWN_ELAPSED_MS}ms"
    fi
    POST_SHUTDOWN_CURL=$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 http://localhost:2112/metrics 2>/dev/null || echo 000)
    [ "$POST_SHUTDOWN_CURL" = "000" ] && ok ":2112 closed" || ko ":2112 still serving (curl=$POST_SHUTDOWN_CURL)"
    TRADER_PID=""
}

generate_acceptance() {
    local out="$1" commit last_snap inc snap_n c s e p tot rate
    commit=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
    last_snap=$(ls -td "$out"/snap_t* 2>/dev/null | head -1)
    inc=$(ls "$out/env_incidents" 2>/dev/null | wc -l)
    snap_n=$(ls "$out"/snap_t*/metrics.txt 2>/dev/null | wc -l)
    {
        printf '# Phase 1 长跑验收 (commit %s, %s)\n\n' "$commit" "$(date '+%F %H:%M %Z')"
        printf '## 1. 总览\n实跑 %sh / 计划 %sh; PASS=%d FAIL=%d ALARMS=%d\n' "${ELAPSED_HOURS:-?}" "$DURATION" "$PASS" "$FAIL" "$ALARMS"
        printf '6 标准: PASS=__ PARTIAL=__ FAIL=__   ← mu 手填总评\n一句话结论: ← mu 手填\n\n'
        printf '## 2. Proxy 预检 (10min)\nverdict: %s\n详情见 preflight/metrics_t10.txt\n\n' "$(cat "$out/preflight/verdict.txt" 2>/dev/null || echo SKIPPED)"
        printf '## 3. 7 collector 终态 (last: %s)\n' "$(basename "${last_snap:-?}")"
        if [ -n "$last_snap" ]; then
            for c in btc_regime oi klines square_feed square_hashtag watchlist position_price; do
                s=$(get_metric "$last_snap/metrics.txt" "$c" success); s=${s:-0}
                e=$(get_metric "$last_snap/metrics.txt" "$c" error); e=${e:-0}
                p=$(get_metric "$last_snap/metrics.txt" "$c" panic); p=${p:-0}
                tot=$((s+e+p)); rate=0; [ "$tot" -gt 0 ] && rate=$((s*100/tot))
                printf -- '- %s: ticks=%d success=%d error=%d panic=%d rate=%d%%\n' "$c" "$tot" "$s" "$e" "$p" "$rate"
            done
        fi
        printf '\n## 4. metrics 趋势 (%d 份快照)\ncounter 单调? p95? proxy active 衰减? ← mu 手填\n\n' "$snap_n"
        printf '## 5. 异常清单 (共 %d)\n' "$ALARMS"
        grep ALARM "$out/alarm.log" 2>/dev/null | head -20 || echo "(none)"
        printf '\n## 6. .env 防腐\nmd5 变化次数: %d\n' "$inc"
        [ "$inc" -gt 0 ] && ls "$out/env_incidents/" | sed 's|^|  - |'
        printf '1.7 RCA 真根因是否解开: ← mu 手填 Y/N + 一句话结论\n\n'
        printf '## 7. shutdown\nSIGINT exit %sms; :2112 后 curl=%s\n\n' "${SHUTDOWN_ELAPSED_MS:-?}" "${POST_SHUTDOWN_CURL:-?}"
        printf '## 8. Phase 1 收尾\n进 1.10 OK / 阻塞项: ← mu 手填\n'
    } > "$out/acceptance.md"
}

archive_logs() {
    local out="$1"
    [ -f "$out/trader.log" ] && gzip -f "$out/trader.log" 2>/dev/null || true
    [ -f "$out/run.log" ]    && gzip -f "$out/run.log"    2>/dev/null || true
}

cleanup_on_exit() {
    [ -n "$ENV_MON_PID" ] && kill "$ENV_MON_PID" 2>/dev/null || true
    [ -n "$TRADER_PID" ] && kill "$TRADER_PID" 2>/dev/null || true
    sync
}

parse_args() {
    while [ $# -gt 0 ]; do
        case "$1" in
            --duration) DURATION="$2"; shift 2;;
            --skip-preflight) SKIP_PREFLIGHT=true; shift;;
            --output-dir) OUTPUT_DIR="$2"; shift 2;;
            --env-monitor-interval) ENV_INTERVAL="$2"; shift 2;;
            --metrics-snapshot-interval) SNAP_INTERVAL="$2"; shift 2;;
            --proxy-mode) PROXY_MODE="$2"; shift 2;;
            --abort-on-panic) ABORT_ON_PANIC="$2"; shift 2;;
            --dry-run) DRY_RUN=true; shift;;
            -h|--help) usage; exit 0;;
            *) echo "unknown flag: $1"; usage; exit 3;;
        esac
    done
    [ -z "$OUTPUT_DIR" ] && OUTPUT_DIR="reports/phase1_longrun_$(date +%F_%H%M)"
}

main() {
    parse_args "$@"
    mkdir -p "$OUTPUT_DIR/env_incidents"
    : > "$OUTPUT_DIR/alarm.log"; : > "$OUTPUT_DIR/run.log"
    if [ "$DRY_RUN" = "true" ]; then
        cat <<EOF
=== DRY RUN ===
OUTPUT_DIR=$OUTPUT_DIR
DURATION=${DURATION}h SNAP=${SNAP_INTERVAL}min ENV_MON=${ENV_INTERVAL}s
PROXY=$PROXY_MODE SKIP_PREFLIGHT=$SKIP_PREFLIGHT ABORT_ON_PANIC=$ABORT_ON_PANIC
Steps:
  0 prereq_check (go/docker/curl/lsof/md5sum/awk/bc + PG/Redis ping)
  1 build_and_test (go build + go test ./internal/...)
  2 $([ "$SKIP_PREFLIGHT" = "false" ] && echo 'run_preflight (10min PROXY=pool, 4 verdicts; fail→exit 1)' || echo 'SKIPPED; start trader fresh')
  3 monitor_env (bg ${ENV_INTERVAL}s) + monitor_30min_loop ($((DURATION*60/SNAP_INTERVAL))+1 snapshots)
  4 shutdown_trader (SIGINT, wait 30s, verify :2112 closed)
  5 generate_acceptance + archive_logs (gzip)
EOF
        exit 0
    fi
    trap cleanup_on_exit EXIT
    START_TS=$(date +%s)
    step "Step 0: prereq"; prereq_check; ok "prereq"
    step "Step 1: build_and_test"; build_and_test
    if [ "$SKIP_PREFLIGHT" = "false" ]; then
        step "Step 2: run_preflight (10min)"
        if ! run_preflight "$OUTPUT_DIR"; then
            echo; echo "PREFLIGHT FAILED — see $OUTPUT_DIR/preflight/"
            generate_acceptance "$OUTPUT_DIR"; archive_logs "$OUTPUT_DIR"
            exit 1
        fi
        ok "preflight passed; reusing trader (pid=$TRADER_PID)"
    else
        step "Step 2: skipped; start trader fresh"
        BINANCE_PROXY_MODE="$PROXY_MODE" BINANCE_PROXY_POOL_FILE=deploy/proxies.txt \
            setsid ./bin/trader > "$OUTPUT_DIR/trader.log" 2>&1 < /dev/null &
        TRADER_PID=$!
        sleep 5
        kill -0 "$TRADER_PID" 2>/dev/null || { ko "trader 秒退"; exit 2; }
    fi
    step "Step 3: env monitor (bg) + 30min loop"
    monitor_env "$ENV_INTERVAL" "$OUTPUT_DIR" &
    ENV_MON_PID=$!
    local rc=0
    monitor_30min_loop "$OUTPUT_DIR" || rc=$?
    END_TS=$(date +%s)
    ELAPSED_HOURS=$(echo "scale=2; ($END_TS-$START_TS)/3600" | bc)
    step "Step 4: shutdown"; shutdown_trader
    step "Step 5: acceptance + archive"
    generate_acceptance "$OUTPUT_DIR"; archive_logs "$OUTPUT_DIR"
    echo; echo "==== PASS=$PASS FAIL=$FAIL ALARMS=$ALARMS ===="
    echo "Output: $OUTPUT_DIR/acceptance.md"
    [ "$rc" = "2" ] && exit 2
    [ "$FAIL" -eq 0 ] && exit 0 || exit 1
}

main "$@"
