# Trader 业务逻辑全景 (mu owner 视角)

**最后更新**: 2026-05-13 (Round R.5 + Phase 5.2 Round 2.z + Round 4)
**真盘配置版本**: live .env on VPS + admin_overrides

本文档用通俗语言讲清 trader 在每个时刻在做什么、看什么数据、为什么做这个决定、用什么数值阈值。**目的: 让 mu 不用读代码就能完整理解系统行为**。

---

## 30 秒全景

```
                ┌──────────────┐
   币安行情     │  6 个 cron   │   信号/决策      下单 4 个 algo
  (OI/价格/    →│   每分钟跑    │ →  通过 3 关  →  挂到币安
   推文)        │   各干各的   │     入场过滤
                └──────────────┘
                       │
                       ↓
                 1 个 cron 每分钟
                 对账 + 自愈
                       │
                       ↓
                 出场: TP/Trail/Disaster/Timeout/SIGFAIL
                       │
                       ↓
                 写 DB → 累加 daily_pnl → 触发熔断检查
```

**真实节奏**: 每分钟有 10+ 个 cron 在跑。entry 5min 评估一次,exit/sync 1min 一次。trade 平均生命周期: 几小时到 26 小时(soft timeout)。

---

## §1 数据采集层 (5 个 cron)

trader 不知道任何东西,所以第一件事是把币安的数据搬过来。

### 1.1 OI 持仓量采集 (`oi_collector`) — 1 分钟一次

- **干什么**: 拉币安每个 watchlist symbol 的 Open Interest (USDⓈ-M 永续未平仓合约量)
- **存哪**: `oi_history` 表 (timescaledb hypertable)
- **保留**: 30 天
- **为什么**: 信号引擎判定 "OI 暴涨" 需要近 1 小时 OI 序列

### 1.2 K 线 + ATR 采集 (`klines_collector`) — 5 分钟一次

- **干什么**: 拉 15min K 线 + 计算 ATR (Average True Range, 14 周期)
- **存哪**: `klines` 表 + Redis key `atr:{symbol}` (TTL 30min)
- **为什么**: ATR 用来算 disaster_stop 价格 (不固定 6%,跟 symbol 波动率走)

### 1.3 Square 推文采集 (`square_feed` + `square_hashtag`) — 1 小时 + 15 分钟

- **干什么**:
  - `square_feed` (1h): 抓币安 Square 全平台热门帖,提取里面的 cashtag (`$BTC` `$SOL` 这种)
  - `square_hashtag` (15min): 对每个 watchlist symbol 数有多少帖
- **存哪**: `square_posts` / `square_mentions` / `square_hashtag_history`
- **为什么**: 信号引擎判定 "社区热度" 用这个 ratio

### 1.4 候选池采集 (`watchlist_collector`) — 1 小时一次

- **干什么**: 从币安 exchangeInfo 拿所有 USDⓈ-M 永续合约,过滤掉:
  - 上线 < 30 天的新币
  - 24h 成交额 < 某阈值
  - mu admin Web UI 手工 exclude 的 symbol
- **加上**: mu admin Web UI 手工 include 的 symbol
- **存哪**: Redis `watchlist:current` (set)
- **为什么**: signal_engine 只评估池中 symbol,不评估全市场

### 1.5 BTC 行情采集 (`btc_regime`) — 5 分钟一次

- **干什么**: 拉 BTCUSDT 5min K 线
- **用途**: 算 30 分钟 BTC 跌幅,熔断条件 #3 (BTC 砸盘保护)

---

## §2 信号引擎 (`signal_engine`) — 5 分钟一次

输入: watchlist 池 (~30-50 个 symbol)
输出: 每个 symbol 一条 `signals` 记录,标记 `entered_full` / `entered_half` / `rejected`

### 2.1 OI 暴涨判定 (4 个条件,**全过**才算触发)

```
当前 OI = oi[-1]
近期 6 期 = oi[-6 : -1]
回溯 10 期最低点 = min(oi[-10:])

条件 1: 距最低点涨幅 ≥ 5%       (OI_SURGE_FROM_LOW_PCT)
        即 (current - min) / min ≥ 0.05

条件 2: 近 6 期累计涨幅 ≥ 3%     (OI_SURGE_RECENT_GROWTH_PCT)
        即 (current - oi[-6]) / oi[-6] ≥ 0.03

条件 3: 近 6 期里至少 3 期是上涨的  (OI_SURGE_MIN_GROWING_RATIO=0.5)
        即 sum(oi[i] > oi[i-1] for i in -5..-1) ≥ 3

条件 4: 当前价 > 1 小时前价        (防止接顶反弹陷阱)
```

**真实例子 (VELVET #69 入场时,signal_id=21766)**:
```
OI 涨幅 (距最低点): 7.86% ✓ (>5%)
近 6 期涨幅:        7.53% ✓ (>3%)
连续上涨期数:       5 期 ✓ (>3)
价格上涨:           ✓
→ OI 触发 = true
```

### 2.2 Square 热度判定 (3 mode, 任意一个 hot=true 就算触发)

按数据量自适应:

| Mode | 数据点 | 阈值 |
|---|---|---|
| Standard (≥ 24h) | ≥ 96 点 | 近 60min 平均 / 24h 基线 ≥ 2.0 (`SQUARE_HOT_MULTIPLIER`) **且** 加速度 ≥ 0.6 |
| Medium (6-24h) | 24-96 点 | 同 ratio + 同加速度 |
| Short (2-6h) | 8-24 点 | ratio ≥ 2.5 (无加速度要求) |
| Fallback (< 2h) | < 8 点 | hot = false (数据不足) |

### 2.3 决策

| OI 触发 | Square hot | 决策 | margin |
|---|---|---|---|
| ✓ | ✓ | `entered_full` | $50 |
| ✓ | ✗ | `entered_half` | $25 |
| ✗ | — | `rejected` (写 rejection_reason) | — |

---

## §3 入场决策引擎 (`decision_engine`) — 5 分钟一次

输入: signals 表里近 5 分钟的 `entered_*` 信号
对每条信号跑 **3 关全局过滤**,全过才真下单。

### 3.1 三关过滤

```
关 1: 熔断检查
      circuit_breaker_state.trading_halted = false?
      ↓ 否则 reject (reason=circuit_breaker_halted)

关 2: 持仓数 < 上限
      open + partial 的 trade 数 < MAX_CONCURRENT_POSITIONS = 5
      ↓ 否则 reject (reason=position_limit_full)

关 3: 同 symbol 24h 不二次入场
      该 symbol 是否有 24h 内的 signal 关联 trade(任何状态: entering/open/partial/closed)?
      ↓ 是 reject (reason=recent_24h_trade)
```

**注: 关 3 的 cutoff 是上次 signal 产生时间,不是平仓时间。** 即使刚平仓 5 分钟,只要 signal 距今 < 24h 仍被拒。

### 3.2 仓位计算 (sizing)

```
margin = $50 (entered_full) 或 $25 (entered_half)
leverage = 5x  (mu owner Round R.1 决策)
notional = margin × leverage = $250 (full) 或 $125 (half)

qty_raw = notional / entry_price
qty = floor(qty_raw / stepSize) × stepSize  ← 对齐到 Binance LOT_SIZE

实际 notional = qty × entry_price (取整后略小于 target)
```

**真实例子 (VELVET #69, entered_half)**:
```
margin = $25, notional 目标 = $125
entry = 0.118740
qty_raw = 125 / 0.118740 = 1052.7
stepSize = 1 → qty = 1052 → 实际 notional = $124.97
```

---

## §4 入场下单流程 (`executor.PlaceEntry`) — 9 步

trigger: decision_engine 给一笔通过过滤的信号

```
Step 1  setMarginType ISOLATED  (隔离保证金)
Step 2  setLeverage 5x
Step 3  (跳过)
Step 4  PlaceMarketOrder BUY qty
Step 5  waitFill (最多 10s)
Step 6  落 DB: trades.status = open, entry_ts, entry_price
Step 7  挂 灾难止损 (STOP_MARKET)
Step 8  挂 trail S1 (TRAILING_STOP_MARKET)
Step 9  挂 TP1 + TP2 (TAKE_PROFIT_MARKET × 2)
```

**4 个 algo 各自的作用** (mu 真实 ARPA #68 / VELVET #69 都是这 4 个):

### 4.1 Algo #1 — 灾难止损 (STOP_MARKET)

```
triggerPrice = entry × (1 - stop_pct)
stop_pct = clip(ATR / entry × 2, MIN=6%, MAX=12%)
qty = 全仓 (reduceOnly)

例: VELVET #69 entry 0.118740, ATR 给出 7.5% → stop = entry × 0.925 = 0.10984
    实际下单后向下取整到 tickSize → 0.10980 (近似)
```

**保护场景**: 价格跌穿 stop → Binance 自动市价卖全部仓位。最坏情况止损。

### 4.2 Algo #2 — Trail S1 (TRAILING_STOP_MARKET)

```
activatePrice = entry × (1 + TRAIL_STAGE1_ACTIVATE_PCT) = entry × 1.05
callbackRate  = TRAIL_STAGE1_CALLBACK_RATE = 3.0%
qty = 全仓 (reduceOnly)

工作方式:
  step 1: 价格涨到 activatePrice 前,trail 不动
  step 2: 价格 ≥ activatePrice 后,Binance 开始追踪 highestPrice
  step 3: 当价格回撤 ≥ 3% (即 latestPrice ≤ highestPrice × 0.97) → 触发市价 SELL
```

### 4.3 Algo #3 — TP1 (TAKE_PROFIT_MARKET)

```
triggerPrice = entry × (1 + TP1_PCT) = entry × 1.10
qty = 原仓 × TP1_RATIO = 原仓 × 0.20  (= 部分平仓 20%)
取整到 stepSize

价到 1.10× 时触发: 市价卖 20% → 剩 80% 继续持有
```

### 4.4 Algo #4 — TP2 (TAKE_PROFIT_MARKET)

```
triggerPrice = entry × 1.25 (即 +25%)
qty = 原仓 × 0.20  (再卖 20%)

价到 1.25× 时触发: 剩 80% → 60%
```

### 4.5 完整出场表 (理想情况)

```
开仓:  100%
↓ 价涨 +10%
TP1 触发卖 20% → 剩 80%
↓ 价涨 +25%
TP2 触发卖 20% → 剩 60%
↓ 价涨 +5% 起 (实际触发时一定 > 25% 才到这里)
Trail S1 已激活,跟踪 high,3% 回撤触发
↓ Trail 不断升级 (见 §6)
S2 (+20%) / S3 (+35%) / S4 (+65%)
↓ 最终触发 trail
剩余 60% 全部平掉
```

---

## §5 持仓管理 (`position_manager`) — 1 分钟一次

trigger: cron 每分钟
干什么: **对账** — 把 DB 里我们认为的仓位 vs Binance 实际仓位比对

### 5.1 对账流程

对每笔 `open` / `partial` 状态的 trade:

```
1. Binance.GetPositionRisk(symbol) → 拿当前实际 qty + markPrice + unrealizedPnl
2. 跟 DB.position_states.current_qty 比较
3. 计算偏差 = |db_qty - binance_qty| / db_qty
```

### 5.2 偏差等级处理

| 偏差 | 处理 |
|---|---|
| < 1% | 静默,正常 |
| 1-5% | 日志 WARN (噪音容忍) |
| > 5% | **Bug B+A 修复后 (Round R.5)**: 先查 TP 是否刚 FINISHED → 是就跳过(同 tick race);否则 trip drift_exceeded halt + **不覆盖 DB qty** |

### 5.3 仓位不见保护 (local_only_orphan)

```
Binance 上找不到这个 symbol 的仓位,但 DB 说 open?
  → Round R.4/R.5 修复后: 先查 trail / disaster algo 状态
    任一 FINISHED → 视为正常平仓 (auto-close,用正确的 trail_sN exit_reason)
    都不 FINISHED → trip local_only_orphan halt
```

### 5.4 MARGIN_CALL 保护

```
margin_ratio = -unrealized_pnl / margin
  > 0.8 (即浮亏达到本金 80%) → 紧急市价平仓 (Binance 强平之前抢救)
```

实际触发条件: 单笔亏损达到 $40 (margin $50 时) / $20 (margin $25 时)。结合 5x 杠杆 + 6-12% disaster_stop,正常情况下不会到 80%。这是兜底网。

---

## §6 Trail 升级 (`trail_upgrader`) — 1 分钟一次

trigger: cron 每分钟
干什么: 价格继续涨时把 trail 收紧

### 6.1 4 级阶梯

```
S0 → S1  价格 ≥ entry × 1.05  (启动 trail, callback 3%)
       └─ 这一步通常在入场时就直接挂了 (executor Step 8),不靠 cron
S1 → S2  价格 ≥ entry × 1.20  (callback 提到 5% — Binance 原生最大)
S2 → S3  价格 ≥ entry × 1.35  (callback 提到 10% — Binance 不支持 >5%,
                              trader 自己管,根据 trail_high 算 stop_price 重挂)
S3 → S4  价格 ≥ entry × 1.65  (callback 15%)
```

**Round 2.z 改动** (mu owner 真实诉求,2026-05-13): 把原 +3% / +15% / +30% / +60% 阈值提高到 +5% / +20% / +35% / +65%,给真实趋势更多发展空间。

### 6.2 ratchet 防抖

S3/S4 是 trader-managed (Binance 不原生支持 >5% callback),所以每分钟根据 trail_high 重算 stop_price:

```
新 stop_price = trail_high × (1 - callback_pct)

防抖: 只有当 trail_high 比 DB 记录的旧 high 涨 ≥ 0.5% (RatchetMinPct)
      才重新去 Binance 改单。避免每分钟 API 抖动。
```

---

## §7 出场流程 (3 条路径)

trade 退出的 3 种方式:

### 7.1 Algo 自然触发 (`algo_polling`) — 1 分钟一次

trigger: cron 每分钟
干什么: 查每个 algo 在 Binance 的状态

```
for trade in open_trades:
  for algo_id in [tp1, tp2, disaster, trail]:
    query Binance → status?
    if FINISHED:
      tp1/tp2  → partialClose (减 qty 20%)
      disaster → autoClose (全平 + status=closed + 写 trade_exits)
      trail    → autoClose (全平 + 用 trail_sN exit_reason)
```

**真实 PnL 计算**:
```
realized_pnl = (close_price - entry_price) × qty - fees
LONG 多头: close > entry 是正,close < entry 是负
```

### 7.2 超时强平 (`exit_manager`) — 1 分钟一次

时间到了 trader 主动平仓:

```
soft_timeout = 26 小时  (持仓超过这么久 → 市价 SELL)
hard_timeout = 76 小时  (软超时执行失败后,72h 总长后再试一次)
```

> ⚠️ .env 现有 `HARD_TIMEOUT_HOURS=72` `SOFT_TIMEOUT_HOURS=24`,加上 retry buffer 落在 +2h 容差范围。

### 7.3 SIGFAIL 信号失效 (Round 3 Module C) — 5 分钟一次

trader 主动跑路:开仓后发现 OI 跌回去或价格破位:

```
SIGFAIL 触发条件 (任一):
  · OI 跌 ≥ 9% (SIGNAL_FAIL_OI_DROP_PCT vs entry_oi snapshot)
  · 价格跌破 entry × (1 - 3%) 持续 — 用 EMA 判断 (Round 3.x Part 2 B/C)

触发 → 市价平仓 exit_reason='sigfail',不等 disaster/trail
```

### 7.4 mu 手工平仓 (Round 2.x Part 3)

mu 在 admin Web UI 点 🚨 手工平仓:

```
1. admin endpoint POST /api/admin/trades/:id/close
2. DB 预设 trades.status='closing' + exit_reason='manual_close'
3. exit_manager 下次 1min cron tick 看到 → cancel 所有 algo + 市价 SELL
4. 全程 ≤ 1 分钟完成
```

---

## §8 风控熔断 (5 项,任一触发 halt 24h)

trigger: 跟 decision_engine 同一 tick (每 5min) 跑

```
1. API 错误率   1 分钟内 ≥ 3 次 API 错误 → halt
   (api_error_rate)

2. 连续亏损     连续亏 ≥ 8 次 且 最近一次亏损在 24h 内 → halt
   (CONSECUTIVE_LOSS_HALT_COUNT)
   
3. 日内亏损     daily_pnl / 账户余额 ≤ -6% → halt
   (DAILY_LOSS_HALT_PCT 现 admin_override=0.06, .env baseline 0.08)
   实际: 余额 $870 × -6% = -$52 触发
   
4. 总浮亏       所有 open trade 的 unrealized 之和 / 余额 ≤ -12% → halt
   (TOTAL_FLOAT_LOSS_HALT_PCT=0.12)
   实际: 余额 $870 × -12% = -$104 触发
   
5. BTC 砸盘     BTC 30 分钟内跌幅 ≥ 3% → halt
   (BTC_PANIC_DROP_PCT 现 admin_override=0.03)
```

### 8.1 halt 类型

| halt 来源 | duration | auto-reset? |
|---|---|---|
| 5 项熔断 | 24h | ✅ 24h 后自动 |
| 持仓对账 (drift/orphan) | 1h | ✅ 1h 后自动 |
| mu 手工 (admin Web UI ⏸️) | mu 指定 1-168h | ✅ 到期 |

### 8.2 halt 期间行为

```
✅ 不停: position_manager 对账 / algo_polling / trail_upgrader
        (持仓继续被守护)
❌ 停掉: decision_engine 不下新单
        (signal_engine 仍跑,记 rejection_reason='circuit_breaker_halt')
```

---

## §9 真实运维数值 — 当前 live 配置

### 9.1 单笔 trade 经济参数

```
margin    $50 (full) / $25 (half)
leverage  5x
notional  $250 (full) / $125 (half)
单笔风险敞口最大: notional × MAX_STOP_PCT = $250 × 12% = $30 (full)
                                          = $125 × 12% = $15 (half)
```

### 9.2 账户层风险

```
余额            ~$870 USDT (current)
日内最大亏损    $52 (-6% halt) — admin_override 比 .env baseline 8% 紧
总浮亏最大      $104 (-12% halt)
最大并发持仓    5 笔
理论最大单日敞口  $250 × 5 = $1250 (5 笔 entered_full 全开)
                          但通常 entered_half 多,实际 ~$625
```

### 9.3 时间维度

```
信号评估      每 5 分钟一次
入场决策      每 5 分钟一次 (跟 signal 同 tick)
持仓对账      每 1 分钟一次
Trail 升级    每 1 分钟一次
Algo 状态轮询  每 1 分钟一次
超时检查      每 1 分钟一次 (Soft/Hard timeout)
SIGFAIL 检测  每 5 分钟一次
熔断评估      每 5 分钟一次
飞书日报      每天 BJT 00:00
24h cooldown  同 symbol 24 小时内不二次入场
```

### 9.4 12 个 mu admin Web UI 可调阈值 (Phase 5.2 12 wired keys)

```
熔断 5 项:
  DAILY_LOSS_HALT_PCT          当前 0.06 (admin override)
  CONSECUTIVE_LOSSES_HALT      当前 8
  TOTAL_FLOAT_LOSS_HALT_PCT    当前 0.12
  BTC_PANIC_DROP_PCT           当前 0.03
仓位 + ATR 2 项:
  MAX_STOP_PCT                 当前 0.12
  LEVERAGE                     当前 5
信号 2 项:
  OI_GROWTH_FROM_MIN_PCT       当前 0.06 (admin override, 比 .env 0.05 严)
  SQUARE_HOT_MULTIPLIER        当前 2.0
Trail 4 项 (Round 2.z):
  TRAIL_STAGE1_ACTIVATE_PCT    当前 0.05
  TRAIL_STAGE2_UPGRADE_PCT     当前 0.20
  TRAIL_STAGE3_UPGRADE_PCT     当前 0.35
  TRAIL_STAGE4_UPGRADE_PCT     当前 0.65
```

admin Web UI 改任一 → 1 分钟内 config_reloader cron 把新值 swap 进 runtime → 下次评估生效。**不需重启 trader**。

---

## §10 自愈机制 (4 类 catch)

### 10.1 启动自愈 (`restart_recovery`)

trader 启动时:
```
1. 扫 trades.status='entering' 的卡死单
   → 查 Binance 看实际是否成交,reconcile
2. position_manager.SyncTick 立即跑 (不等 1 分钟)
3. exit_manager.EvaluateTick 立即跑 (catch 重启时已到期的 timeout)
4. circuit_breaker.EvaluateAll 立即跑 (catch 昨天 halt 状态)
```

### 10.2 孤儿 Algo 清理 (`orphan_algo_cleaner`, Round R.3) — 1 分钟一次

```
查 Binance 所有 reduceOnly SELL algo:
  if 该 symbol 在 Binance 上无 position
     且 algo 还是 NEW/WORKING (没自动 cancel)
  → cancel 这个 algo (防 mu 看到一堆没用的挂单)
```

### 10.3 同 tick race 防护

3 个修复 (Round R.4 + R.5):

| race | 表象 | 防护 |
|---|---|---|
| TP fill vs position_manager (drift) | binance qty 减了,DB 还没减,position_manager 误判 drift halt | drift > 5% 时先查 TP algo,FINISHED 就 skip halt + skip sync (R.5 Bug B+A) |
| trail FINISHED vs position_manager (orphan) | trade 已被 algo_reconciler 关闭,position_manager 看 Binance 仓位空误判 orphan | local_only_orphan 检测先 consult trail + disaster algo (R.4 F1) |
| TryReconcile exit_reason | 上面这条 defense 跑通后,exit_reason 总记为 "disaster" (即使是 trail 关的) | 显式传 expectedReason (R.5 Bug C) |

### 10.4 配置热重载 (`config_reloader`, Phase 5.2 Round 2.x) — 1 分钟一次

```
每分钟读 admin_overrides 表 (mu admin Web UI 写的覆盖值)
应用到 atomic.Pointer[Runtime]
trader 所有读阈值的代码都用 runtime.Get() → 自动用新值

降级:  runtime 字段为 0 → fallback 到 .env baseline
       admin_overrides 表无该 key → 同上
```

---

## §11 mu owner 工具汇总

### 11.1 Web UI (https://trader.letsagent.net/admin/)

| Page | 功能 | 权限 |
|---|---|---|
| Dashboard | 余额 / 今日 PnL / 持仓数 / collector 健康度 / 4 个写按钮 | 公开读 + 写需 Caddy basic auth |
| 当前持仓 | open trades + 24h 内 closed | 公开读 |
| 历史仓位 | filter (symbol/exit_reason/PnL 方向/时段) + 分页 | 公开读 |
| PnL 分析 | 累计曲线 / Symbol 排名 / 平仓原因饼图 / 胜率统计 | 公开读 |
| Square 热点 | 24h 提及数 trending | 公开读 |
| 市场扫描 | watchlist + OI/Square 变化 | 公开读 |
| ⚙️ 设置 | 12 阈值 + watchlist include/exclude form | 写需 basic auth |
| 📋 操作历史 | admin_audit_log 全量审计 | 公开读 (informed-risk transparency) |
| 单 trade 详情 | 信号链路 / 持仓状态 / 历史 exits / API errors + 🚨 手工平仓 | 公开读 + 写需 auth |

### 11.2 关键写操作 button

```
🔓 解除 halt          (Dashboard, HALTED 时显示)
⏸️ 主动 halt          (Dashboard, NORMAL 时显示, 自定义 1-168h)
↺ 重置今日 PnL        (Dashboard)
↺ 重置连亏计数        (Dashboard)
🚨 手工平仓           (Trade Detail, open/partial 时)
✓ 加入/✗ 排除 watchlist (Settings)
应用修改 (12 阈值)    (Settings)
✅ 已解决/🔍 调查中/⚪ 忽略 RCA (Dashboard, 当有未 ack 的 halt_rca)
```

### 11.3 飞书告警 (Round 4, mu 需配 FEISHU_WEBHOOK_URL)

```
🔴 critical  halt 触发 / 单笔 ≤ -$50 (deferred)
🟡 warning   SIGFAIL / margin_call (deferred wiring)
🟢 info      mainnet 入场 (已 wired)
⚪ daily     BJT 00:00 余额 + PnL + 胜率日报
```

### 11.4 紧急情况快捷链路

```
mu 在外面 → 飞书收 🔴 critical
   ↓ 点 deep link
mobile admin Web UI → 自动跳 RCA 页 (Round 6 mobile responsive)
   ↓ 看 context_json
ack 为 resolved/investigating/ignored (写 audit log)
   ↓ 决定
🔓 解除 halt (如果 mu 判定 RCA 已处理)
```

---

## §12 当前 mu 真盘历史 (forward 评估数据)

| Trade | Symbol | exit_reason | Net PnL | 备注 |
|---|---|---|---|---|
| #66 | INJUSDT | trail_s1 | -$0.41 | bug 期 (activatePrice 错) |
| #67 | TURBOUSDT | trail_s1 | +$0.63 | bug 期 |
| #59 | ESPORTSUSDT | trail_s2 | +$30.94 | bug 期 (大利,trail 立即活情况下) |
| #70 | COSUSDT | trail_s1 + bug | +$11.52 真 / +$20.42 虚 | Bug C 触发 (虚高 $8.91,DB 记账偏差) |
| #69 | VELVETUSDT | trail_s1 | +$2.02 | ✅ 第一笔 "all fixes applied" 干净周期 |
| #68 | ARPAUSDT | open | (待) | activatePrice fix 后 rebuild,等 forward 验证 |

**累计真实净盈利** (排除 bug 虚高): ~$45 USDT

---

## §13 已知边界 (mu 应了解的限制)

1. **callback rates 还没 wire 到 runtime** — TRAIL_STAGE{N}_CALLBACK_RATE 改 .env 不会热生效,需重启。Round 2.w 任务待 mu 决策时机。

2. **SAME_SYMBOL_COOLDOWN_HOURS 无效** — .env 值不会被读;硬编码 24h。要改需要 wire-up (现已有 task 但未实施)。

3. **TP1/TP2 是固定价位 TAKE_PROFIT_MARKET,不是 trail** — 设计如此 (Round 2 Module A "山寨币保守化")。VELVET 现状 4 个 algo 是正确的。

4. **trail callback 在 S1/S2 阶段是 Binance 原生** (3% / 5% 上限);S3/S4 阶段是 trader 自己 1min cron 算 stop_price 重挂。S3+ 触发延迟 ≤ 1 分钟。

5. **drift halt 现在 (R.5) 会 skip sync overwrite** — 如果出现真实 DB↔Binance 数据偏差(非 TP race),DB 会保持差异状态等 mu 决策,不会自动覆盖。这是设计选择 (审计 > 自动 fix)。

6. **飞书 critical 触发**: halt trip (已 wire) + 单笔 ≤ -$50 (deferred) + disaster placement failed (deferred)。后两者目前不会通知,mu 只能从 Web UI 看。

---

## §14 用一句话总结

> trader 每分钟看一眼币安找 OI 暴涨 + 社区热点的山寨币,挂 4 个 algo(灾难止损 + trail + 2 个分档止盈),靠 5 项熔断保护账户,所有操作记 audit + 手机能调阈值能急停,mu 真盘 ~$870 仓本金,单笔最大 $30 风险敞口。
