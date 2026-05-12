# v0.2 Trader Design — mu B/C 策略完整出场系统

**编写日期:** 2026-05-12 BJT  
**编写人:** Claude Code (claude-sonnet-4-6) + mu  
**基于:** Phase 4 v0.1 acceptance + v0.1.x ATR-based 灾难止损 + mu B/C 策略真盘 forward

---

## mu v0.2 入口决策备忘

| 决策项 | 选项 | 备注 |
|---|---|---|
| 策略类型 | B/C (中线 10-30% / 长线 30-100% 山寨币) | 真盘 forward 验证中 |
| Module B TRAILING | 4 stage 分阶段 (3%/5%/10%/15%) | S3/S4 trader 自实施 |
| Module A TP | 山寨币保守化 TP1 +10%/20% qty + TP2 +25%/20% qty | 剩 60% → TRAILING |
| 实施时机 | forward 1-2 周后 | 校准阈值后再启动 |

---

## §1 总览

### 1.1 v0.1 现状

v0.1 (已主网部署) 出场逻辑：

```
出场类型          条件                                    实现
灾难止损 (v0.1.x) ATR-based clip(ATR/price×2.0, 6%, 7.5%) Algo Service STOP_MARKET (进场时挂)
soft_timeout      持仓≥24h 且 当前亏损                   exit_manager 1min cron
hard_timeout      持仓≥72h 无条件                         exit_manager 1min cron
MARGIN_CALL       margin_ratio > 0.8                      position_manager 1min cron
```

**v0.1 已有工程预埋 (v0.2 直接利用):**
- `internal/pkg/indicator/atr.go` + `ema.go` — Wilder ATR + EMA 算法
- Redis `atr:{symbol}` + `ema20:{symbol}` — 5min 写入，30min TTL
- `config.Exit.TrailingDistanceATRMult` / `MinStopPct` / `MaxStopPct` — 配置就绪
- DB `initial_atr` / `initial_stop_loss` 字段 — 入场时记录
- DB `initial_take_profit_1/2` 字段 — 已 reserved（写 NULL），v0.2 填写

### 1.2 v0.2 范围：4 Module

```
Module A: TP_STAGE        — 部分止盈 TP1 +10%/20%qty + TP2 +25%/20%qty
Module B: TRAILING        — 4 stage 移动止损 3%/5%/10%/15%
Module C: SIGFAIL         — 信号失效出场 (OI drop / EMA20 / 5min low)
Module D: WS User Data    — 实时 Order/Account/AlgoOrder 事件流替换轮询
```

每个 Module 独立可部署 (vertical slice)，按 Round 1→4 顺序。

### 1.3 工时估算

| Module | 内容 | 估算 |
|---|---|---|
| Round 0 | 设计文档 (本次) | 2-3h |
| Round 1 | Module B TRAILING 4 stage (复杂度最高，优先) | 4-6h |
| Round 2 | Module A TP_STAGE 山寨币保守化 | 3-4h |
| Round 3 | Module C SIGFAIL | 4-6h |
| Round 4 | Module D WS User Data Stream | 10-15h |
| Round 5 | 集成测试 + testnet smoke | 3-4h |
| Round 6 | mainnet 部署 + forward 评估 | — |
| Round 7 | acceptance + tag `phase-trader-v0.2` | 1h |
| **总计** | | **~27-39h Claude Code, ~3-5 周 wall-clock** |

### 1.4 实施时机

```
T+0 (2026-05-12):  规划完成 (本 Round 0)
T+1-2 周:          forward 真盘数据累积 (RIFUSDT/ESPORTSUSDT + 新 entry)
T+2 周:            mu 阈值校准决策 (TP1/TP2/TRAIL 4 stage 真实分布)
T+2-5 週:          Round 1-7 实施
T+5 週:            mainnet v0.2 上线，tag phase-trader-v0.2
```

---

## §2 Module A — 部分止盈 TP_STAGE (山寨币保守化)

### 2.1 业务逻辑

```
入场 qty = Q (全部仓位)

TP1: 涨 +10% → 卖出 0.2Q (20% qty) → TAKE_PROFIT_MARKET
TP2: 涨 +25% → 卖出 0.2Q (20% qty) → TAKE_PROFIT_MARKET
剩余: 0.6Q → 由 Module B TRAILING 保护

平仓优先级:
  灾难止损 (兜底全Q) > TP1 > TP2 > TRAILING (0.6Q)
```

**设计意图 (mu 山寨币 B/C 策略):**
- +10% 止盈：山寨币多次验证到的"第一波"涨幅，先锁定收益
- +25% 止盈：第二波，继续锁定
- 剩 60% 走 TRAILING：让利润奔跑 (mu B/C 中线/长线目标)

### 2.2 Binance API

```
2025-12-09 后，TAKE_PROFIT_MARKET 必须通过 Algo Service:
POST /fapi/v1/algoOrder
  type       = TAKE_PROFIT        (Algo Service 类型)
  side       = SELL
  symbol     = BTCUSDT
  quantity   = qty_partial        (0.2Q, round to stepSize)
  stopPrice  = entry_price × (1 + TP_PCT)  (round to tickSize)
  reduceOnly = true               (防止仓位反转)

ref: references/binance/urls.md §「New Algo Order」
```

**⚠️ qty 精度：** `0.2 × Q` 必须向下 round 到 symbol `stepSize`。
如果 `0.2Q < minQty` (极少数精度高 symbol) → skip TP，直接用 TRAILING 保护全仓。

### 2.3 DB Schema 变更

`initial_take_profit_1` / `initial_take_profit_2` 已在 migration 0001 中 reserved，
v0.2 在入场时写入真实值（当前写 NULL）：

```sql
-- 无需新 migration，仅更改 UpdateTradeOpen SQL
UPDATE trades SET
  initial_take_profit_1 = $stop_price_tp1,   -- entry × 1.10
  initial_take_profit_2 = $stop_price_tp2,   -- entry × 1.25
WHERE id = $trade_id;
```

同时追踪 TP 订单 ID（需新字段，migration 0010 一并加）：

```sql
-- migration 0010 中加 (跟 Module B trail 字段一起)
ALTER TABLE trades ADD COLUMN binance_tp1_algo_id   TEXT;
ALTER TABLE trades ADD COLUMN binance_tp2_algo_id   TEXT;
```

### 2.4 TP 触发后处理

Algo Service `TAKE_PROFIT` 触发后，`algo_reconciler` 已有 `FINISHED` 检测路径。
但 TP 是**部分平仓**，不同于灾难止损的全仓平仓：

```
algo_reconciler FINISHED 检测:
  type = tp1 → InsertTradeExit(type="tp1", qty=0.2Q, price=actual_price)
              → UpdateTradeClosed 不调用 (仓位未全关)
              → UpdateTradePartial(remaining_qty -= 0.2Q)
              → 更新 position_states.current_qty
              → 清零 binance_tp1_algo_id (标记已触发)
```

**新增 DB 查询:** `UpdateTradePartialClose(trade_id, new_qty, exit_type, pnl)`

### 2.5 工程量 ~3-4h

```
文件改动:
  internal/execution/order.go          — placeDisasterStop 后加 placeTPOrders
  internal/execution/algo_reconciler.go — TP1/TP2 FINISHED partial close 路径
  internal/storage/postgres/queries/trades.sql — UpdateTradePartialClose query
  internal/storage/postgres/migrations/0010_v02_trail_tp.up.sql — TP algo ID 字段
  测试: algo_reconciler_test.go — TP partial close scenario
```

---

## §3 Module B — TRAILING 移动止损 4 Stage

> **⚠️ 最高复杂度 Module，Round 1 优先实施。**

### 3.1 业务逻辑

```
mu B/C 策略: 中线 10-30% / 长线 30-100% 山寨币
目标: 让利润奔跑，不被小回调止损，但大回调时锁定收益

4 Stage 设计:
  Stage 1 (S1): 涨 +3%  激活 → callbackRate 3%   → Binance 原生 TRAILING_STOP_MARKET
  Stage 2 (S2): 涨 +15% 升级 → callbackRate 5%   → Binance 原生 (上限)
  Stage 3 (S3): 涨 +30% 升级 → callbackRate 10%  → trader 自实施 STOP_MARKET
  Stage 4 (S4): 涨 +60% 升级 → callbackRate 15%  → trader 自实施 STOP_MARKET

保护范围: 剩余 0.6Q (TP1+TP2 火后；若 TP 未触发则全 Q)
灾难止损 (ATR-based): 始终并存，作为兜底
```

**Stage 设计意图：**
- S1: 保本线。涨 3%，最多回落 3%，止损在 ~持平位
- S2: 涨 15% 后，拉宽至 5%，允许更大波动（中线震荡）
- S3: 涨 30% 后，trader 自管 10% 回撤，山寨币大行情允许更大呼吸
- S4: 涨 60% 后，trader 自管 15% 回撤，极端行情锁定利润

### 3.2 Binance API 分层

**S1/S2: Binance 原生 TRAILING_STOP_MARKET (via Algo Service)**

```
POST /fapi/v1/algoOrder
  type            = TRAILING_STOP_MARKET
  side            = SELL
  symbol          = BTCUSDT
  quantity        = trail_qty       (0.6Q 或全Q)
  activationPrice = activation_price (entry × 1.03 for S1)
  callbackRate    = 3               (S1) / 5 (S2)   [单位: %, Binance 范围 0.1-5]
  reduceOnly      = true

Binance 自动追踪最高价，当回落 callbackRate% 时触发 MARKET SELL
algo_reconciler 现有 FINISHED 检测路径自动处理

ref: references/binance/urls.md §「New Algo Order」
```

**⚠️ `callbackRate` 限制：** Binance 原生 TRAILING_STOP_MARKET 最大 5%。
S2 设 5% 是上限；S3/S4 超出上限，必须 trader 自实施。

**S3/S4: Trader 自实施 (trail_upgrader 5min cron)**

```
trader 不依赖 Binance 原生 trailing，自己追踪：

trail_upgrader.go (5min cron):
  1. SELECT trades WHERE trail_stage IN (3, 4)
  2. 对每笔 trade:
     a. GET current_mark_price (from position_states.highest_price 或 GET /fapi/v1/premiumIndex)
     b. UPDATE trail_high_price = max(trail_high_price, current_price)
     c. stop_trigger = trail_high_price × (1 - callback_rate)
        S3 callback = 0.10; S4 callback = 0.15
     d. IF current_price <= stop_trigger:
          → place STOP_MARKET via Algo Service (qty = remaining current_qty)
          → INSERT trade_exits(type="trail_sN")
          → UPDATE trade closed
```

**S3/S4 相比 S1/S2 的工程差异：**
- S1/S2: algo_reconciler 已有 FINISHED path，自动处理
- S3/S4: trail_upgrader 主动检测 + 下单，类似 exit_manager.closePosition

### 3.3 Stage 升级流程

```
trail_upgrader 5min cron 同时负责 Stage 升级:

当前 S1 (trail_stage=1):
  IF highest_price / entry_price >= 1.15:
    → Cancel S1 Binance Algo Order (DELETE /fapi/v1/algoOrder)
    → Place S2 TRAILING_STOP_MARKET (callbackRate=5)
    → UPDATE trades SET trail_stage=2, binance_trail_algo_id=new_id
    → log "trail.upgrade: S1→S2"

当前 S2 (trail_stage=2):
  IF highest_price / entry_price >= 1.30:
    → Cancel S2 Binance Algo Order
    → NO new Binance algo (S3 trader 自实施)
    → UPDATE trades SET trail_stage=3, binance_trail_algo_id=NULL
    → log "trail.upgrade: S2→S3"

当前 S3 (trail_stage=3):
  IF highest_price / entry_price >= 1.60:
    → (无 Binance algo 需取消)
    → UPDATE trades SET trail_stage=4
    → log "trail.upgrade: S3→S4"

S4: 最终 stage，无升级
```

### 3.4 Trail 激活时机

```
TRAILING 由 trail_upgrader 在 S0→S1 时激活：

IF trail_stage=0 AND highest_price / entry_price >= 1.03:
  qty_remaining = current_qty  (从 position_states 取; 如 TP 已触发则已减少)
  activation_price = entry_price × 1.03

  如果 qty_remaining < minQty (极小 symbol):
    log.Warn "trail: qty_remaining below minQty, skip trail (disaster stop covers)"
    return

  → Place S1 TRAILING_STOP_MARKET
  → UPDATE trades SET
      trail_stage=1,
      trail_activation_price=activation_price,
      trail_high_price=highest_price,
      binance_trail_algo_id=new_algo_id
```

### 3.5 DB Schema 变更 (migration 0010)

```sql
-- migration 0010_v02_trail_tp.up.sql
ALTER TABLE trades ADD COLUMN trail_stage          SMALLINT  NOT NULL DEFAULT 0;
  -- 0=inactive 1=S1(Binance 3%) 2=S2(Binance 5%) 3=S3(trader 10%) 4=S4(trader 15%)

ALTER TABLE trades ADD COLUMN binance_trail_algo_id TEXT;
  -- S1/S2: Binance algo ID (取消时用); S3/S4: NULL (trader 自管)

ALTER TABLE trades ADD COLUMN trail_high_price     NUMERIC(36, 18);
  -- 自激活起追踪的最高价 (trail_upgrader 维护)

ALTER TABLE trades ADD COLUMN trail_activation_price NUMERIC(36, 18);
  -- S1 激活时的价格 (entry × 1.03)

ALTER TABLE trades ADD COLUMN binance_tp1_algo_id  TEXT;
ALTER TABLE trades ADD COLUMN binance_tp2_algo_id  TEXT;
  -- Module A TP algo IDs

-- DOWN:
ALTER TABLE trades DROP COLUMN trail_stage;
ALTER TABLE trades DROP COLUMN binance_trail_algo_id;
ALTER TABLE trades DROP COLUMN trail_high_price;
ALTER TABLE trades DROP COLUMN trail_activation_price;
ALTER TABLE trades DROP COLUMN binance_tp1_algo_id;
ALTER TABLE trades DROP COLUMN binance_tp2_algo_id;
```

### 3.6 TrailUpgrader 接口设计

```go
// internal/execution/trail_upgrader.go

// TrailUpgraderDeps — 接口在使用方定义 (CLAUDE.md §18)
type TrailUpgraderDeps interface {
    ListOpenTradesWithTrail(ctx context.Context) ([]gen.ListOpenTradesWithTrailRow, error)
    UpdateTrailState(ctx context.Context, arg gen.UpdateTrailStateParams) error
    UpdateTrailHighPrice(ctx context.Context, arg gen.UpdateTrailHighPriceParams) error
}

type TrailBinanceClient interface {
    PlaceAlgoConditionalStop(ctx context.Context, symbol, qty, stopPrice string) (binance.AlgoOrderResult, error)
    PlaceAlgoTrailingStop(ctx context.Context, symbol, qty, activationPrice string, callbackRate float64) (binance.AlgoOrderResult, error)
    CancelAlgoOrder(ctx context.Context, symbol string, algoID int64) error
}

type TrailUpgrader struct {
    db    TrailUpgraderDeps
    bc    TrailBinanceClient
    rdb   *redis.Client
    cfg   TrailConfig
    log   zerolog.Logger
    nowFn func() time.Time
}

type TrailConfig struct {
    Stage1ActivatePct  decimal.Decimal // 0.03
    Stage2UpgradePct   decimal.Decimal // 0.15
    Stage3UpgradePct   decimal.Decimal // 0.30
    Stage4UpgradePct   decimal.Decimal // 0.60
    Stage1CallbackRate decimal.Decimal // 0.03 (Binance native)
    Stage2CallbackRate decimal.Decimal // 0.05 (Binance max)
    Stage3CallbackRate decimal.Decimal // 0.10 (trader self)
    Stage4CallbackRate decimal.Decimal // 0.15 (trader self)
}
```

### 3.7 config 新增字段

```go
// internal/config/config.go ExitConfig 加:
TrailStage1ActivatePct  decimal.Decimal `mapstructure:"TRAIL_STAGE1_ACTIVATE_PCT"`
TrailStage2UpgradePct   decimal.Decimal `mapstructure:"TRAIL_STAGE2_UPGRADE_PCT"`
TrailStage3UpgradePct   decimal.Decimal `mapstructure:"TRAIL_STAGE3_UPGRADE_PCT"`
TrailStage4UpgradePct   decimal.Decimal `mapstructure:"TRAIL_STAGE4_UPGRADE_PCT"`
TrailStage1CallbackRate decimal.Decimal `mapstructure:"TRAIL_STAGE1_CALLBACK_RATE"`
TrailStage2CallbackRate decimal.Decimal `mapstructure:"TRAIL_STAGE2_CALLBACK_RATE"`
TrailStage3CallbackRate decimal.Decimal `mapstructure:"TRAIL_STAGE3_CALLBACK_RATE"`
TrailStage4CallbackRate decimal.Decimal `mapstructure:"TRAIL_STAGE4_CALLBACK_RATE"`

// 默认值 (setDefaults):
"TRAIL_STAGE1_ACTIVATE_PCT":   "0.03",
"TRAIL_STAGE2_UPGRADE_PCT":    "0.15",
"TRAIL_STAGE3_UPGRADE_PCT":    "0.30",
"TRAIL_STAGE4_UPGRADE_PCT":    "0.60",
"TRAIL_STAGE1_CALLBACK_RATE":  "3",    // Binance API 单位: %, 非小数
"TRAIL_STAGE2_CALLBACK_RATE":  "5",
"TRAIL_STAGE3_CALLBACK_RATE":  "0.10", // trader 自实施用小数
"TRAIL_STAGE4_CALLBACK_RATE":  "0.15",
```

### 3.8 Binance PlaceAlgoTrailingStop 新增

```go
// internal/binance/orders.go 新增:
// POST /fapi/v1/algoOrder type=TRAILING_STOP_MARKET
// ref: references/binance/urls.md §「New Algo Order」
func (c *Client) PlaceAlgoTrailingStop(
    ctx context.Context,
    symbol, qty, activationPrice string,
    callbackRate float64,  // 单位: % (e.g. 3.0 = 3%)
) (AlgoOrderResult, error)
```

**⚠️ callbackRate Binance 约束：**
- 范围：0.1% ~ 5%（原生 TRAILING_STOP_MARKET）
- S2 设 5 = 上限，测试时需确认 Binance 是否接受
- S3/S4 不走此 API（trader 自实施）

### 3.9 trade_exits type 扩展

```sql
-- trade_exits.type 已有: 'disaster', 'emergency_close', 'soft_timeout', 'hard_timeout'
-- v0.2 新增:
  'tp1'        — Module A TP1 触发
  'tp2'        — Module A TP2 触发
  'trail_s1'   — Module B S1 trailing 触发
  'trail_s2'   — Module B S2 trailing 触发
  'trail_s3'   — Module B S3 trailing 触发 (trader 自实施)
  'trail_s4'   — Module B S4 trailing 触发 (trader 自实施)
  'sigfail'    — Module C 信号失效触发
```

### 3.10 工程量 ~4-6h

```
文件改动 (新增/修改):
  internal/execution/trail_upgrader.go (新)   — 核心逻辑 (~180 行)
  internal/execution/trail_upgrader_test.go (新) — S0→S4 + 升级 + 自实施触发
  internal/binance/orders.go                  — PlaceAlgoTrailingStop 新增
  internal/config/config.go                   — 8 新配置字段
  internal/storage/postgres/migrations/0010_v02_trail_tp.up.sql (新)
  internal/storage/postgres/queries/trades.sql — 新 queries
  cmd/trader/main.go                           — 注册 trail_upgrader cron (*/5)
```

---

## §4 Module C — SIGFAIL 信号失效出场

### 4.1 业务逻辑

持仓期间若原始入场信号失效，触发市价平仓：

```
SIGFAIL 出场条件 (当前默认: OR 关系, 触发任一即平仓):
  条件 A: OI 较入场时 drop ≥ 8%
          current_oi < initial_oi × (1 - SIGNAL_FAIL_OI_DROP_PCT)
  条件 B: 5 根 15m K 线收盘价连续 < EMA20
          closes_last_5 全部 < ema20 (from Redis ema20:{symbol})
  条件 C: 当前价 < 最近 5min 内最低价 × (1 - SIGNAL_FAIL_PRICE_LOW_BUFFER_PCT)
          (注: 防止瞬时插针误触)

OR vs AND: ⚠️ 留 forward 数据校准后 mu 决策
  · OR: 更激进，误平率高但保护更强
  · AND: 更保守，误平率低但可能错过真实失效
  · 初始默认 OR，留 config 可切 (SIGNAL_FAIL_LOGIC=or|and)
```

### 4.2 工程预埋利用

```
已有:
  Redis ema20:{symbol}       — klines_writers.go 5min 写入 (FULLY BUILT)
  config.Exit.SignalFailOIDropPct          — 已在 ExitConfig
  config.Exit.SignalFailPriceLowBufferPct  — 已在 ExitConfig
  exit_manager.go:13 注释 "signal_fail (OI drop / EMA20 / 5min_low)"

待实现:
  OI 数据对比 (需 initial_oi 字段 + 当前 OI 获取)
  EMA20 读取 (Redis GET ema20:{symbol} → parse indicatorPayload)
  5min low 计算 (Redis klines 或 position_price collector 数据)
```

### 4.3 initial_oi 字段 (migration 0011)

```sql
-- migration 0011_v02_sigfail_oi.up.sql
ALTER TABLE trades ADD COLUMN initial_oi NUMERIC(36, 18);
  -- 入场时的 openInterest (OI), 由 InsertTrade / UpdateTradeOpen 写入
  -- 来源: GET /fapi/v1/openInterest (或 collector 写入 Redis)

-- DOWN:
ALTER TABLE trades DROP COLUMN initial_oi;
```

**initial_oi 写入路径：**

```go
// internal/execution/order.go placeDisasterStop 之前:
// GET /fapi/v1/openInterest → 写入 DB initial_oi
// 如获取失败: NULL (SIGFAIL OI 条件跳过，不阻断入场流程)
```

### 4.4 SIGFAIL 检测 — exit_manager 集成

```go
// internal/execution/exit_manager.go SignalFailCheck 新增:

type SigFailDeps interface {
    GetCurrentOI(ctx context.Context, symbol string) (decimal.Decimal, error)
    // OI from Redis oi:{symbol} (OI collector 已写)
}

func (em *ExitManager) checkSignalFail(ctx, trade, log) bool {
    // 条件 A: OI drop
    if trade.InitialOI.IsPositive() {
        currentOI, err := em.sf.GetCurrentOI(ctx, trade.Symbol)
        if err == nil {
            drop := trade.InitialOI.Sub(currentOI).Div(trade.InitialOI)
            if drop.GreaterThan(em.cfg.SignalFailOIDropPct) {
                log.Info()...Msg("exit.sigfail: OI drop triggered")
                return true
            }
        }
    }

    // 条件 B: EMA20 跌破 (5 连收盘 < EMA20)
    ema20, err := em.getEMA20(ctx, trade.Symbol)
    if err == nil && !ema20.IsZero() {
        closes, err := em.getLast5Closes(ctx, trade.Symbol)
        if err == nil && len(closes) == 5 {
            allBelow := true
            for _, c := range closes { if c.GreaterThan(ema20) { allBelow = false; break } }
            if allBelow {
                log.Info()...Msg("exit.sigfail: EMA20 consecutive break triggered")
                return true
            }
        }
    }

    // 条件 C: 5min 低点跌破
    // (具体数据来源 forward 校准后实施)

    return false
}
```

### 4.5 工程量 ~4-6h

```
文件改动:
  internal/execution/exit_manager.go         — checkSignalFail 方法
  internal/storage/postgres/migrations/0011  — initial_oi 字段
  internal/storage/postgres/queries/trades.sql — initial_oi write
  internal/execution/exit_manager_test.go    — sigfail unit tests
  internal/config/config.go                  — SIGNAL_FAIL_LOGIC=or|and
```

---

## §5 Module D — WS User Data Stream

### 5.1 业务价值

```
当前 v0.1: 全部出场检测依赖 1min cron 轮询
  exit_manager     → 1min cron
  position_manager → 1min cron
  algo_reconciler  → 1min cron

v0.2 WS: 实时事件 → 延迟从 ~30-60s → <1s
  ORDER_TRADE_UPDATE (Algo 触发、TP 触发) → 立即处理
  ACCOUNT_UPDATE (仓位变化)               → 立即更新 position_states
  MARGIN_CALL                             → 立即告警
  AlgoOrder Update (2025-12-09 后)        → 灾难止损/TP/TRAILING 状态实时
```

### 5.2 User Data Stream 接口

```
ref: references/binance/urls.md §「User Data Streams」

生命周期:
  POST /fapi/v1/listenKey        → 获取 listenKey (60min 有效)
  PUT  /fapi/v1/listenKey        → 每 30min keepalive (延长有效期)
  DELETE /fapi/v1/listenKey      → 关闭流

WS 连接:
  Production: wss://fstream.binance.com/ws/{listenKey}
  Testnet:    wss://stream.binancefuture.com/ws/{listenKey}

关键事件:
  ORDER_TRADE_UPDATE             — 订单状态变更 (FILLED/EXPIRED/CANCELED)
  ACCOUNT_UPDATE                 — 仓位/余额变化
  MARGIN_CALL                    — 保证金告警
  listenKeyExpired               — 流过期，需重连
  Algo Order Update (AlgoOrderUpdate 事件) — Algo Service 状态
```

### 5.3 架构设计

```
internal/execution/ws_stream.go   — User Data Stream 核心
  · NewUserDataStream(bc, rdb, deps, log)
  · Run(ctx) — goroutine: connect → dispatch → reconnect
  · 内部 keepalive ticker (30min)
  · 断线重连: 指数退避 1s/2s/4s，最大 60s

事件分发:
  ORDER_TRADE_UPDATE(FILLED) + side=SELL
    → 判断 trade_id (from client_order_id)
    → 如果是灾难止损/TP/TRAILING → 调用 onAlgoFired(tradeID, price, qty)

  ACCOUNT_UPDATE
    → 更新 Redis position_states 镜像 (供 position_manager 参考)

  MARGIN_CALL
    → 立即告警 (Feishu, 现有 notifier)
    → 如果 margin_ratio > 0.8 → 触发 emergencyClose

  listenKeyExpired
    → 重新 POST /fapi/v1/listenKey
    → 重连 WS
```

### 5.4 1min cron 与 WS 并存 (渐进替换)

**v0.2 Round 4 策略：WS 作为主路径，cron 作为兜底。**

```
WS 收到 ORDER_TRADE_UPDATE (Algo FINISHED):
  → 立即调用 autoCloseFromAlgo() (algo_reconciler 逻辑)

algo_reconciler 1min cron:
  → 继续运行，但 InsertTradeExitIdempotent ON CONFLICT DO NOTHING
     防止 WS + cron 双重处理 (幂等已实现 ✅)

完全替换 cron:
  → 稳定运行 1-2 周后，mu 决策是否关闭 algo_reconciler cron
  → position_manager 1min cron 保留 (对账用途，非出场)
```

### 5.5 工程量 ~10-15h (最高)

```
高复杂度原因:
  · WS 连接管理 (reconnect + keepalive + context cancel)
  · 事件 JSON 解析 (多种 event type)
  · 与现有 cron 幂等并存
  · testnet 真实验证 (需 WS endpoint 连通)
  · goroutine 安全 (CLAUDE.md §17 严禁裸 go func)

文件改动 (新增/修改):
  internal/execution/ws_stream.go (新)       — ~250 行
  internal/execution/ws_stream_test.go (新)  — mock WS server 测试
  internal/binance/ws.go (新)               — WS dialer + proxy 支持
  internal/binance/client.go                 — CreateListenKey / KeepaliveListenKey
  cmd/trader/main.go                         — 注册 UserDataStream goroutine
```

---

## §6 阈值校准计划

### 6.1 Forward 数据目标 (T+1-2 周)

```
累积 ≥ 10 笔真实 entry 的完整出场数据：
  · 最高浮盈 (peak_unrealized_pnl_pct)
  · 触发出场类型 (disaster / timeout)
  · 持仓时长分布
  · 入场到峰值的价格路径
```

### 6.2 需校准的阈值

**Module A TP:**
| 参数 | 当前假设 | 校准依据 |
|---|---|---|
| TP1_PCT | 10% | 真实涨幅中 >10% 的笔数占比 |
| TP1_QTY_RATIO | 20% | 锁定量 vs 错过追涨 trade-off |
| TP2_PCT | 25% | 真实涨幅中 >25% 的笔数占比 |
| TP2_QTY_RATIO | 20% | 同上 |

**Module B TRAILING:**
| 参数 | 当前假设 | 校准依据 |
|---|---|---|
| Stage1 激活 +3% | 3% | 保本线；真实入场后平均多久到 +3% |
| Stage2 升级 +15% | 15% | 中线目标；真实分布 |
| Stage3 升级 +30% | 30% | 长线目标 |
| Stage4 升级 +60% | 60% | 极端行情 |
| S1 callback 3% | 3% | 真实回撤分布 P25 |
| S2 callback 5% | 5% | Binance 上限，观察误触率 |
| S3 callback 10% | 10% | 山寨币大行情回撤 P50 |
| S4 callback 15% | 15% | 极端大行情回撤保护 |

**Module C SIGFAIL:**
| 参数 | 当前假设 | 校准依据 |
|---|---|---|
| OI drop threshold | 8% | OI 下降后价格表现分析 |
| EMA20 consecutive bars | 5 | 假阴性 (误平率) vs 真阳性 |
| OR vs AND | OR | 误平率数据支撑 |

### 6.3 校准工具 (T+2 周 mu 决策)

```sql
-- 真实出场分布查询
SELECT
  (MAX(ps.highest_price) - t.entry_price) / t.entry_price AS peak_pct,
  te.type AS exit_type,
  t.symbol,
  EXTRACT(EPOCH FROM (te.ts - t.entry_ts)) / 3600 AS hold_hours
FROM trades t
JOIN trade_exits te ON te.trade_id = t.id
JOIN position_states ps ON ps.trade_id = t.id
WHERE t.status = 'closed'
ORDER BY t.entry_ts DESC;
```

---

## §7 Round 拆分

### Round 0: 规划 (本次) ✓
- 设计文档 `docs/V0_2_TRADER_DESIGN.md`
- tag: 不打 tag (实施时打 `phase-trader-v0.2`)

### Round 1: Module B TRAILING 4 stage (~4-6h)

**优先级最高，理由:** 核心收益保护机制，且复杂度最高需最多验证时间。

```
Acceptance Criteria:
  ✅ migration 0010 (trail_stage + binance_trail_algo_id + trail_high_price + tp_algo_ids)
  ✅ PlaceAlgoTrailingStop Binance API 实现 (callbackRate 1-5%)
  ✅ TrailUpgrader S0→S1 激活路径 (5min cron)
  ✅ TrailUpgrader S1→S2 upgrade + Cancel + Reorder
  ✅ TrailUpgrader S2→S3 upgrade (取消 Binance algo, 切 trader 自实施)
  ✅ TrailUpgrader S3→S4 upgrade
  ✅ S3/S4 trader 自实施触发 STOP_MARKET
  ✅ algo_reconciler S1/S2 FINISHED 路径 (trail_sN type)
  ✅ 单元测试 (所有 stage + 升级 + 自实施触发 + fallback)
  ✅ testnet smoke (≥ 2 trade: 主流币 S1 触发 + 山寨币 S2 升级)
```

### Round 2: Module A TP_STAGE (~3-4h)

```
Acceptance Criteria:
  ✅ placeTPOrders (TP1 + TP2 Algo TAKE_PROFIT_MARKET, qty=0.2Q each)
  ✅ algo_reconciler TP1/TP2 FINISHED partial close 路径
  ✅ UpdateTradePartialClose DB query
  ✅ initial_take_profit_1/2 写入 DB
  ✅ 单元测试 (TP1/TP2 partial + minQty skip 路径)
  ✅ testnet smoke (≥ 1 trade TP 触发验证)
```

### Round 3: Module C SIGFAIL (~4-6h)

```
Acceptance Criteria:
  ✅ migration 0011 (initial_oi 字段)
  ✅ initial_oi 入场时写入
  ✅ exit_manager SIGFAIL 检测 (条件 A OI + 条件 B EMA20 + 条件 C price_low)
  ✅ SIGNAL_FAIL_LOGIC=or|and 配置
  ✅ 单元测试 (3 条件各自触发 + OR/AND 逻辑)
  ✅ testnet 验证 (手动降 OI threshold 触发)
```

### Round 4: Module D WS User Data Stream (~10-15h)

```
Acceptance Criteria:
  ✅ WS 连接 + keepalive (30min) + 断线重连
  ✅ ORDER_TRADE_UPDATE FILLED 处理 (灾难止损/TP/TRAILING)
  ✅ ACCOUNT_UPDATE 仓位镜像更新
  ✅ MARGIN_CALL 实时告警 + emergencyClose
  ✅ listenKeyExpired 自动重连
  ✅ 与 algo_reconciler 1min cron 幂等并存
  ✅ testnet smoke (Algo 触发 → WS 事件 → 立即关仓)
  ✅ 灾难止损 fallback cron 保留
```

### Round 5: 集成测试 + testnet smoke (~3-4h)

```
Module A + B + C + D 联动测试:
  场景 1: 入场 → TP1 触发 → TP2 触发 → TRAILING S1 激活 → S2 升级 → 触发
  场景 2: 入场 → TP1 触发 → SIGFAIL OI drop → 全仓关闭
  场景 3: 入场 → TRAILING S1 直接触发 (TP 未触发)
  场景 4: 入场 → 灾难止损触发 (全部其他出场均未触发)
  回归: soft_timeout / hard_timeout 仍正常工作
```

### Round 6: mainnet 部署 + forward 评估

```
部署步骤:
  1. 运行 migration 0010 + 0011 (make migrate)
  2. 更新 .env (新增 TRAIL_* + SIGNAL_FAIL_LOGIC 配置)
  3. docker compose build + recreate
  4. 验证启动 banner (trail config + ws connected)
  5. 第一笔新 entry 观察 trail_stage=0 → S1 激活路径
```

### Round 7: acceptance + tag

```
Acceptance 标准:
  ✅ ≥ 5 笔真实 entry 走过 v0.2 出场路径 (非 fallback)
  ✅ TP1/TP2/TRAIL 至少各触发 1 次 (testnet or mainnet)
  ✅ 无 false positive (误触 SIGFAIL 或误升级 trail stage)
  ✅ 灾难止损 fallback 仍在线 (任何情况下兜底)
  ✅ 回归: v0.1 soft/hard timeout 正常

完成后:
  git tag phase-trader-v0.2
  commit: "acceptance(trader): phase-trader-v0.2 完整出场系统 mainnet ready"
```

---

## §8 兼容性 v0.1 → v0.2

### 8.1 平仓优先级

```
优先级  出场类型                 触发条件                        实现
──────  ───────────────────────  ─────────────────────────────── ────────────────────────
1       SIGFAIL                  信号失效 (OI/EMA20/price_low)   exit_manager check (v0.2)
2       灾难止损 (ATR-based)     浮亏 > stop_pct                 Algo STOP_MARKET (v0.1.x)
3       MARGIN_CALL (Binance)    margin_ratio → 强平             Binance 强制 + halt
4       TRAILING S1-S4           移动止损触发                    trail_upgrader (v0.2)
5       TP1 / TP2                部分止盈                        Algo TAKE_PROFIT (v0.2)
6       soft_timeout 24h         持仓 ≥ 24h 且亏损               exit_manager (v0.1)
7       hard_timeout 72h         持仓 ≥ 72h                      exit_manager (v0.1)
```

### 8.2 现有持仓兼容

```
v0.2 上线时如有 v0.1 持仓 (trail_stage=NULL):
  → trail_stage 默认 0 (migration 0010 ADD COLUMN ... DEFAULT 0)
  → trail_upgrader 正常开始追踪 (+3% 激活)
  → TP1/TP2 binance_tp1_algo_id=NULL → placeTPOrders 仅对 NEW entry 执行
  → 灾难止损 (已挂的 Algo) 不受影响

注: RIFUSDT/ESPORTSUSDT 当前持仓 (v0.1 历史 6%) → algo 不动
    新 entry 从 v0.2 全功能路径走
```

### 8.3 不破坏 v0.1 逻辑

```
✅ exit_manager soft/hard timeout — 不改动 (v0.2 在前层拦截)
✅ algo_reconciler FINISHED 路径 — 扩展 (加 TP/TRAIL type 分支)
✅ InsertTradeExitIdempotent — 继续防重 (WS + cron 并存时)
✅ position_manager 对账 — 不改动
✅ circuit_breaker — 不改动
```

---

## §9 风险评估

### 9.1 工程风险 (Module B TRAILING)

| 风险 | 概率 | 影响 | 缓解 |
|---|---|---|---|
| Cancel + Reorder 时价格已过触发价 | 中 | 中 | Cancel 前检查; 失败 → 保留旧 algo + log |
| callbackRate=5 Binance 上限误差 | 低 | 低 | testnet 实测; 下调 4.9% fallback |
| S3/S4 trail_upgrader 5min 窗口太粗 | 中 | 中 | 配置 TRAIL_UPGRADER_INTERVAL 可调 |
| qty_remaining < minQty 跳过 trail | 低 | 低 | log + 灾难止损兜底 |
| 多 trade 并发升级 API rate limit | 低 | 中 | 批量 Cancel + 延迟策略 |

### 9.2 运营风险

| 风险 | 缓解 |
|---|---|
| SIGFAIL 误平 (OR 逻辑) | 初期高 threshold + AND 逻辑备选 |
| WS 断连 → 出场延迟 | cron fallback 保留；重连 <60s |
| forward 阈值偏差 | ≥10 笔 before 校准，不急上线 |

### 9.3 资金安全

```
灾难止损 (v0.1.x ATR-based) 始终作为最终兜底:
  · 入场即挂 Algo STOP_MARKET
  · 即使 trail_upgrader / exit_manager 全部失败
  · 只要 Binance Algo Service 正常 → 止损有效

最坏情况: WS 断 + cron 失败 + trail_upgrader 失败
  → 灾难止损 Algo 自动触发 (Binance 侧)
  → 仓位不会裸奔
```

---

## §10 实施时机 + 工程旅程

### 时间轴

```
2026-05-12 (T+0)
├── v0.1.x ATR-based 灾难止损 部署 ✅
├── v0.2 规划文档 ✅ (本次)
└── forward 评估开始

2026-05-19 ~ 2026-05-26 (T+1-2 周)
├── trader 真盘运行，累积 ≥ 10 笔 entry 数据
├── forward 指标跟踪:
│   - peak_pct 分布 (最高浮盈)
│   - exit_type 分布 (灾难止损/timeout)
│   - hold_hours 分布
└── mu 阈值校准决策

2026-05-26 ~ 2026-06-23 (T+2-6 周)
├── Round 1: Module B TRAILING (~4-6h)
├── Round 2: Module A TP_STAGE (~3-4h)
├── Round 3: Module C SIGFAIL (~4-6h)
├── Round 4: Module D WS (~10-15h)
├── Round 5: 集成测试 (~3-4h)
└── Round 6: mainnet 部署

~2026-06-23 (T+6 週)
└── tag phase-trader-v0.2
    trader v1.0 production-ready
```

### v0.2 vs 真盘 forward 并行

```
forward 期间 (T+1-2 周):
  · trader 主网正常运行 (v0.1.x 状态)
  · 新 entry 用 ATR-based 灾难止损 + soft/hard timeout
  · 不开发 v0.2 代码 (等数据)
  · mu 看 Admin Web UI forward 指标

v0.2 实施期 (T+2-6 週):
  · 每个 Round 完成后 testnet smoke
  · mainnet 仅 Round 6 一次性切换
  · 不影响进行中的真实持仓 (兼容性保证 §8)
```

---

## Appendix: 关键 Binance API 参考

| API | 用途 | ref |
|---|---|---|
| `POST /fapi/v1/algoOrder` type=TAKE_PROFIT | Module A TP | references/binance/urls.md §Algo |
| `POST /fapi/v1/algoOrder` type=TRAILING_STOP_MARKET | Module B S1/S2 | references/binance/urls.md §Algo |
| `DELETE /fapi/v1/algoOrder` | Stage 升级取消旧单 | references/binance/urls.md §Algo |
| `GET /fapi/v1/algoOrder` | AlgoReconciler 状态查询 | 已实现 ✅ |
| `POST /fapi/v1/listenKey` | Module D WS 初始化 | references/binance/urls.md §WS |
| `PUT /fapi/v1/listenKey` | Module D keepalive | references/binance/urls.md §WS |
| WS `ORDER_TRADE_UPDATE` | Module D 主事件 | references/binance/urls.md §WS |
| WS `Algo Order Update` | Module D Algo 触发 | references/binance/urls.md §WS |

---

**文档状态:** Round 0 ✅ FULL  
**阈值状态:** ⚠️ PARTIAL — 当前值为初始假设，待 forward 1-2 周真实数据校准后 mu 决策  
**SIGFAIL OR/AND:** ⚠️ 待 mu 决策 (forward 数据支撑)  
