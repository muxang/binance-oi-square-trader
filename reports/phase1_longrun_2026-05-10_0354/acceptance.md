# Phase 1 长跑验收 (commit 1206d2f, 2026-05-10 04:36 CST)

## 1. 总览
实跑 .69h / 计划 6h; PASS=9 FAIL=1 ALARMS=2
6 标准: PASS=__ PARTIAL=__ FAIL=__   ← mu 手填总评
一句话结论: ← mu 手填

## 2. Proxy 预检 (10min)
verdict: PASS
详情见 preflight/metrics_t10.txt

## 3. 7 collector 终态 (last: snap_t1)
- btc_regime: ticks=41 success=10 error=31 panic=0 rate=24%
- oi: ticks=8 success=6 error=2 panic=0 rate=75%
- klines: ticks=8 success=1 error=7 panic=0 rate=12%
- square_feed: ticks=1 success=0 error=1 panic=0 rate=0%
- square_hashtag: ticks=9 success=9 error=0 panic=0 rate=100%
- watchlist: ticks=1 success=0 error=1 panic=0 rate=0%
- position_price: ticks=42 success=42 error=0 panic=0 rate=100%

## 4. metrics 趋势 (2 份快照)
counter 单调? p95? proxy active 衰减? ← mu 手填

## 5. 异常清单 (共 2)
[ALARM] 2026-05-10 04:36:07 ABORT: btc_regime 30min rate=12% (s=4 e=27)
[ALARM] 2026-05-10 04:36:07 ABORT triggered at t1, generating partial acceptance

## 6. .env 防腐
md5 变化次数: 0
1.7 RCA 真根因是否解开: ← mu 手填 Y/N + 一句话结论

## 7. shutdown
SIGINT exit 10013ms; :2112 后 curl=000000

## 8. Phase 1 收尾
进 1.10 OK / 阻塞项: ← mu 手填
