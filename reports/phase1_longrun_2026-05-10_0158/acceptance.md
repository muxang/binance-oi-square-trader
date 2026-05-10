# Phase 1 长跑验收 (PARTIAL — preflight FAIL, commit 44a1cb5, 2026-05-10 02:09 BJT)

## 1. 总览
实跑 ~10min preflight only / 计划 6h
PASS=2 FAIL=2 ALARMS=0
verdict: **PREFLIGHT FAILED, 不进 6h longrun**

## 2. Proxy 预检 (10min)
verdict: FAIL
- V1 active=41 >=35 ✅
- V2 success_total 0 -> 1234 (递增) ✅
- V3 btc_regime 5/10 <8 ❌
- V4 timeout 14% (208/1442) >=5% ❌

详情见 preflight/metrics_t10.txt

## 3. 7 collector 终态 (preflight 10min 抽样)
- btc_regime: ticks=10 success=5 error=5 panic=0 rate=50%   ← V3 fail
- oi: ticks=1 success=1 error=0 panic=0 rate=100%
- klines: ticks=1 success=1 error=0 panic=0 rate=100%
- square_feed: ticks=1 success=1 error=0 panic=0 rate=100%  ← 1h cron 边界命中
- square_hashtag: ticks=2 success=2 error=0 panic=0 rate=100%
- watchlist: ticks=1 success=1 error=0 panic=0 rate=100%    ← 1h cron 边界命中
- position_price: ticks=10 success=10 error=0 panic=0 rate=100%

## 4. metrics 趋势
preflight 仅 t0/t10 两点, 不构 6h 趋势分析。

## 5. 异常清单
- alarm.log: 空 (preflight 期间 env_monitor 未启动)
- proxy timeout 集中于多个节点 (top 10 各 6-7 次 timeout, 共 208 次)

## 6. .env 防腐
md5 变化次数: 0 (preflight 期间未启动 env_monitor; 跑前/跑后 8e72121e069f083b61bb36cee196b612 一致)
1.7 RCA 真根因是否解开: N (preflight 中断, 未达 6h 长跑窗口)

## 7. shutdown 验证
preflight FAIL 后 SIGINT trader, 已退出 (ports 释放, 进程 gone)。
未走 shutdown_trader 完整路径 (preflight 内嵌 SIGINT)。

## 8. Phase 1 收尾
进 1.10 NOT OK — 阻塞项:
- 代理池质量 14% timeout 不可接受 (V4)
- btc_regime 50% success 是 V3 直接表象
- 解法待定: 换代理池 / 加 retry / 提高 V3/V4 阈值容忍 / 调长 trader timeout
