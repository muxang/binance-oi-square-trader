# Phase 4 Design — Execution Layer v0.1

**编写日期:** 2026-05-11 BJT  
**编写人:** Claude Code (claude-sonnet-4-6) + mu  
**基于:** PHASE_DEPLOYMENT_V01_ACCEPTANCE.md + mu Phase 4 入口 6 问决策

---

## mu Phase 4 入口 6 问决策备忘

| Q | 决策 | 选项 |
|---|---|---|
| Q1 代码模式 | 代码 100% 真盘逻辑，testnet 是 .env TRADER_MODE 开关 | A |
| Q2 testnet 验证期 | 代码 review 通过后立即切 mainnet（不跑 7-14 天） | A |
| Q3 初始资金 | mainnet 1000U 起步（full 投入，max 5 × 50U = 250U 同时锁定） | A |
| Q4 熔断恢复 | 自动 halt + 24h 自恢复（Phase 5 TG 未上线前无法手动 /resume） | A |
| Q5 对账策略 | 双向对账，不一致 halt + mu RCA | C |
| Q6 出场逻辑 | v0.1 简化：灾难止损 6% + 时间出场 soft 24h / hard 72h | B |

---

## §1 Phase 4 业务目标

**Phase 4 = 把 Phase 3 v0.1 决策层输出（`entered_full` / `entered_half` 信号）真转化为币安下单，持仓管理，和出场执行。**

### 实施范围

```
Phase 3 decision_engine
    └─ signal.Decision == "entered_full" / "entered_half"
           │
           ▼
Phase 4 (本 Phase)
    ├── execution/order.go      → 下单 + 灾难止损 (Algo Service)
    ├── execution/position.go   → 1min cron 持仓同步 + 双向对账
    ├── execution/exit.go       → 出场判断 (v0.1 简化)
    └── execution/circuit_breaker.go → 5 项熔断 trip + 24h 自恢复
```

### 边界

- **不动:** Phase 2/3 采集 / 信号 / 决策逻辑（内容冻结）
- **Phase 4 只做:** 下单 + 持仓状态维护 + 出场执行 + 熔断保护
- **v0.1 不做（v0.2 升级）:** 部分止盈（TP_STAGE1/TP_STAGE2）、移动止损（TRAILING）、信号失败止损（OI drop）

### 资金参数

| 参数 | 值 | 来源 |
|---|---|---|
| MARGIN_PER_TRADE_FULL | 50 USDT | .env.example |
| MARGIN_PER_TRADE_HALF | 25 USDT | .env.example |
| LEVERAGE | 10x | .env.example |
| MAX_CONCURRENT_POSITIONS | 5 | .env.example |
| 最大同时锁定 | 250 USDT (5 × 50) | 推算 |
| 账户留底 | ~750 USDT | 1000 - 250 |

---

## §2 模块拆分

### 4.1 真下单 — `execution/order.go` (~12-15h Claude Code)

**核心职责:** 把 `signals` 表的 `entered_full` / `entered_half` 决策转化为币安下单 + 灾难止损。

**下单流程 (ARCH §6.2 entry flow):**

```
Step 1: setMarginType(symbol, ISOLATED)  [幂等，已 isolated 不报错]
Step 2: setLeverage(symbol, 10)          [幂等]
Step 3: INSERT trades (status='entering', client_order_id)
Step 4: PlaceMarketOrder(symbol, LONG, qty)
Step 5: Wait fill → UPDATE trades (entry_price, qty, status='open')
Step 6: PlaceConditionalOrder(disaster stop)  [Algo Service, 2026-05-11 已过迁移日期]
Step 7: UPDATE trades (binance_disaster_stop_order_id)
Step 8: INSERT position_states (current_qty, entry_oi)
Step 9: 如果 Step 6 失败 → 立即市价平仓 + status='failed' + halt signal
```

**关键实现细节:**

**Qty 计算:**
```go
// margin × leverage / price → round down to stepSize
notional := margin.Mul(decimal.NewFromInt(leverage))
qty       := notional.Div(price).RoundDown(symbolInfo.QtyPrecision)
// 校验 qty × price >= MIN_NOTIONAL (一般 5U 或 10U)
```

**client_order_id 幂等:**
```
client_order_id = fmt.Sprintf("t%d_r%d", signal.ID, retryCount)
// Binance 拒绝重复 client_order_id → 捕获 -2022 错误视为已下单
```

**Algo Service 灾难止损:**
```
POST /fapi/v1/algoOrder
  type=STOP        (条件止损市价)
  side=SELL
  symbol=BTCUSDT
  quantity=qty
  stopPrice=entry_price × (1 - DISASTER_STOP_PCT)  [=×0.94]
// 2025-12-09 后 STOP_MARKET 已迁移到 Algo Service
// PlaceConditionalOrder() 内部按 BINANCE_ALGO_MIGRATION_DATE 切换
// 今日 2026-05-11 > 2025-12-09，直接走 Algo Service
```

**Retry 策略:**
```go
// network / 5xx / rate_limit(-1003) → 重试 3 次，指数退避 1s/2s/4s
// 4xx 业务错误(-2019 insufficient margin / -1111 precision) → 不重试，标 failed
```

**refs:**
```
// ref: references/binance/urls.md §「New Order」POST /fapi/v1/order
// ref: references/binance/urls.md §「New Algo Order」POST /fapi/v1/algoOrder
// ref: references/binance/urls.md §「Change Margin Type」POST /fapi/v1/marginType
// ref: references/binance/urls.md §「Change Initial Leverage」POST /fapi/v1/leverage
```

---

### 4.2 持仓管理 — `execution/position.go` + 1min cron (~8-12h)

**核心职责:** 每分钟同步币安持仓状态，双向对账，异常 halt。

**cron tick 流程:**
```
1. GET /fapi/v3/positionRisk → binance_positions (所有持仓)
2. SELECT trades WHERE status IN ('open','partial') → local_positions
3. 双向对账:
   a. local 有 / binance 无 → 可能被清算 → mark orphan + halt
   b. binance 有 / local 无 → 未记录持仓 → halt + mu RCA prompt
   c. qty / direction 不一致 → halt + mu RCA prompt
4. 一致: UPDATE position_states (current_qty, highest_price, last_check_ts)
5. 检查 MARGIN_CALL: margin_ratio > 0.8 → 触发灾难止损 exit (不 halt)
6. 更新 Redis latest_price (供 exit.go 使用)
```

**Redis sync:**
```go
// ZADD positions_active <entered_at_unix> <trade_id>
// 用于 decision_engine 查询当前持仓数 (MAX_CONCURRENT_POSITIONS 检查)
```

**双向对账 halt 条件 (Q5 C):**
- qty 差异 > 0.001（最小合约精度）
- direction 不匹配
- local open 但 binance 无此 symbol 持仓 → 可能被强平

**ref:**
```
// ref: references/binance/urls.md §「Position Information V3」GET /fapi/v3/positionRisk
```

---

### 4.3 出场逻辑 v0.1 简化 — `execution/exit.go` (~3-5h, Q6 B)

**v0.1 实施的出场条件（仅 2 项）:**

| 出场类型 | 条件 | 实现 |
|---|---|---|
| 灾难止损 | 浮亏 > DISASTER_STOP_PCT (6%) | Algo Service 条件单（Step 6，进场时已挂） |
| soft 时间出场 | 持仓 ≥ SOFT_TIMEOUT_HOURS (24h) 且当前亏损 | position_manager cron 检测 → 市价平仓 |
| hard 时间出场 | 持仓 ≥ HARD_TIMEOUT_HOURS (72h) 无条件 | position_manager cron 检测 → 市价平仓 |

**v0.1 明确不实施（v0.2 加）:**
```go
// TODO(v0.2): TP_STAGE1 +5% 平 30% + TP_STAGE2 +12% 平 30%
// TODO(v0.2): TRAILING_ACTIVATE 3% 后移动止损 (ATR × 2.0 distance)
// TODO(v0.2): SIGNAL_FAIL 出场 (OI drop 8% / price < EMA20 / price < 5min_low × 0.97)
```

**平仓流程:**
```
1. Cancel Algo Order (灾难止损单，防止重复成交)
2. POST /fapi/v1/order type=MARKET side=SELL qty=current_qty
3. Wait fill confirmation
4. UPDATE trades (exit_ts, exit_price, exit_reason, status='closed', realized_pnl, fees)
5. INSERT trade_exits (type=exit_reason, qty, price, pnl)
6. DELETE position_states WHERE trade_id=?
7. ZREM positions_active <trade_id>
8. UPDATE circuit_breaker_state: consecutive_losses + daily_pnl
```

**refs:**
```
// ref: references/binance/urls.md §「Cancel Algo Order」
// ref: references/binance/urls.md §「New Order」POST /fapi/v1/order (MARKET SELL)
```

---

### 4.4 熔断 trip — `execution/circuit_breaker.go` (~5-8h)

**5 项熔断 trip (SPEC §8):**

| Trip | 条件 | 参数 |
|---|---|---|
| 日亏损 | daily_pnl ≤ -DAILY_LOSS_HALT_PCT × 账户余额 | 5% |
| 连续亏损 | consecutive_losses ≥ CONSECUTIVE_LOSS_HALT_COUNT | 5 次 / 24h |
| BTC 暴跌 | BTC 30min 内跌幅 ≥ BTC_CRASH_HALT_PCT | 3% |
| 总浮亏 | 所有持仓 unrealized_pnl 合计 ≤ -TOTAL_FLOAT_LOSS_HALT_PCT × 余额 | 8% |
| API 错误率 | api_errors 最近 1min 计数 ≥ API_ERROR_RATE_LIMIT | 3 次/min |

**Halt 动作:**
```go
// 1. UPDATE circuit_breaker_state SET trading_halted=true, halt_reason=?, halt_until=NOW()+24h
// 2. 不强制平仓现有持仓（持仓继续跑，只停新开仓）
// 3. Log WARN 级别 (Phase 5 飞书告警从这里触发)
```

**24h 自恢复 (Q4 A):**
```go
// circuit_breaker.go CheckHalt():
//   if trading_halted && halt_until != nil && NOW() > halt_until:
//     UPDATE SET trading_halted=false, halt_reason=NULL, halt_until=NULL
//     log.Info().Msg("circuit breaker auto-recovered after 24h")
```

**MARGIN_CALL (v0.1 降级处理):**
```go
// v0.1: 仅靠灾难止损 (Algo Service) 防止强平
// 若被强平: position_manager 对账发现 local 有 / binance 无 → orphan → halt + mu RCA
// TODO(v0.2): WS User Data Stream margin call event 主动触发
```

---

## §3 数据库 Schema 变更

**现有 migration 0001_init.up.sql 已有:**
- `trades` ✓（但缺 `client_order_id`）
- `position_states` ✓（含 trailing/TP 字段，v0.1 不用）
- `circuit_breaker_state` ✓（含 `halt_until`，Q4 auto-recovery 预支持）
- `trade_exits` ✓

**需要 migration 0002：**

```sql
-- migration 0002: add client_order_id to trades for idempotency
ALTER TABLE trades
    ADD COLUMN IF NOT EXISTS client_order_id TEXT,
    ADD CONSTRAINT trades_client_order_id_unique UNIQUE (client_order_id);
```

**现有 `trades` 字段 Phase 4 映射:**

| 字段 | 用途 | 阶段 |
|---|---|---|
| `client_order_id` | 幂等 key = `t{signal_id}_r{retry}` | **需 migration 0002** |
| `binance_disaster_stop_order_id` | Algo Order ID | Step 7 |
| `status` | entering / open / closed / orphan / failed | 贯穿 |
| `initial_stop_loss` | entry × 0.94，下单前计算 | Step 3 |
| `initial_take_profit_1/2` | 写 NULL（v0.1 不用） | Step 3 |
| `trailing_stop_active` | FALSE（v0.1 不用） | position_states |

**sqlc 需新增查询文件:** `internal/storage/postgres/queries/trades.sql` / `positions.sql` / `circuit_breaker.sql`

---

## §4 安全护栏

| 护栏 | 实现 |
|---|---|
| 不可重复下单 | `client_order_id` UNIQUE constraint + Binance -2022 错误处理 |
| 双向对账 halt | position_manager 每 tick 对账，不一致立即 halt (Q5 C) |
| testnet smoke 强制 | 每 Round 末尾跑 5-10min testnet smoke，不过不 commit |
| mu review 通过才 commit | 跟 Phase 2/3 同纪律 |
| Algo Service only | 2026-05-11 已过 2025-12-09 迁移日期，灾难止损全走 Algo |
| mainnet 切换 mu 手工 | .env TRADER_MODE=testnet → mainnet + TRADER_MAINNET_CONFIRM=I_UNDERSTAND |
| panic recover | runner.go 已有 per-collector recover；execution cron 同样接 recover |
| 金额精度 | 全程 `decimal.Decimal`（`shopspring/decimal`），严禁 float64 |

---

## §5 Round 拆分 (9 Round，估算 42-50h)

| Round | 内容 | 估算 |
|---|---|---|
| **0** | SPEC drift 审 + 设计文档（本 Round） | 1-2h |
| **1** | 4.1 完整下单 v0.1：Step 1-9 全流程（margin/leverage + market buy + symbol filter + Algo Service 灾难止损挂单）；smoke 可跑完整 1 单 | 6-7h + smoke |
| **2** | 4.1 cont：client_order_id 幂等 + retry 策略 + 错误处理细节调优；smoke 验证重复下单不重复 | 5-6h + smoke |
| **3** | 4.2 持仓管理：1min cron + GET /fapi/v3/positionRisk + Redis sync | 5h + smoke |
| **4** | 4.2 cont：双向对账逻辑 + halt RCA prompt | 5h + smoke |
| **5** | 4.3 出场 v0.1：soft_timeout + hard_timeout + 平仓流程 + trade_exits | 4h + smoke |
| **6** | 4.4 熔断 trip 5 项实现 | 6h + smoke |
| **7** | 4.4 cont：24h 自恢复 + 整体集成 + restart recovery | 5h + smoke |
| **8** | testnet 综合 smoke（5-10 全路径真 testnet 单） | 3h |
| **9** | PHASE_4_V01_ACCEPTANCE.md + commit + tag phase-4-v0.1 | 2h |

**总估算:** 42-50h Claude Code  
**mu 协作:** ~5h 总（每 Round ~30min review + smoke 确认）  
**wall-clock:** 1-2 周

---

## §6 mainnet 上线 SOP

Phase 4 完成后，mu 决策时机，按以下步骤切换：

```
1. Round 8 testnet smoke 全路径验证无 bug
2. mu review 全 commit + PHASE_4_V01_ACCEPTANCE.md 通过
3. mu 决策 mainnet 切换时机（立即 / 等其它）
4. 币安账户:
   a. API key IP 白名单加 VPS IP (43.133.173.17)
   b. API 权限: Futures + (可选 Withdraw for 查余额)
5. VPS .env 修改:
   TRADER_MODE=testnet  →  mainnet
   BINANCE_API_KEY=<mainnet key>
   BINANCE_API_SECRET=<mainnet secret>
   TRADER_MAINNET_CONFIRM=I_UNDERSTAND
6. docker compose restart trader
7. 观察 30min:
   - bootstrap.sh healthcheck 全 PASS
   - status.sh 8 services Up
   - logs 无 ERROR/FATAL
   - 首单进场后确认 trades 表有记录
8. 验证 1h 不出错 → 正式真盘运行 ✓
```

---

## §7 v0.2 升级路径

Phase 4 v0.1 完成后，v0.2 按需加：

| 功能 | 优先级 | 依赖 |
|---|---|---|
| 部分止盈 TP_STAGE1 (+5%/30%) + TP_STAGE2 (+12%/30%) | 高 | v0.1 平稳运行 1 周 |
| 移动止损 TRAILING_ACTIVATE (3% 后 ATR × 2.0) | 高 | TP_STAGE 同步 |
| 信号失败止损 (OI drop 8% / EMA20 / 5min_low) | 中 | 数据齐全验证 |
| MARGIN_CALL WS User Data Stream 主动触发 | 中 | Phase 5 同期 |
| SAME_SYMBOL_COOLDOWN 24h 验证完整 | 低 | 已有逻辑，验证 edge case |

v0.2 不新增 Phase，在 Phase 4 基础上 patch Round 并重新 acceptance。

---

## §8 Phase 4 + Phase 5 协调

| 项目 | 时序 |
|---|---|
| Phase 4 Round 1-9 | 独立实施，不依赖 Phase 5 |
| Phase 5 (飞书告警) 启动条件 | Phase 4 v0.1 acceptance 通过 + mainnet 上线 + 1 周稳定 |
| Phase 5 内容 | 飞书 webhook + 5 项熔断 trip alert + 日报 + /halt /resume 命令 |
| 熔断 halt log 预留 | Phase 4 circuit_breaker.go 的 log.Warn() 输出已标 "CIRCUIT_BREAKER_HALT" → Phase 5 grep 触发推送 |

---

## §9 Round 0 SPEC Drift 审计输出

### Catch 1 ✅ Binance Algo Service migration（强制）— Round 1 已实施

- **2025-12-09 起:** STOP_MARKET 等条件单必须通过 `POST /fapi/v1/algoOrder` 下单（旧路径 `POST /fapi/v1/order + type=STOP_MARKET` 已废弃）
- **措辞澄清 (2026-05-11 Round 1 review):** Algo Service 内 `type=STOP_MARKET` 仍为合法参数值（触发后市价成交）。"废弃"指的是**端点** `/fapi/v1/order` 的该路径，而非 order type 字符串本身。
- **代码不在** `/fapi/v1/order` 写 STOP_MARKET 分支。
- **Round 1 实施:** `internal/binance/orders.go PlaceAlgoConditionalStop`
  - endpoint: `/fapi/v1/algoOrder` ✓
  - `algoType=CONDITIONAL` ✓
  - `type=STOP_MARKET` (Algo Service 合法 type，触发后市价成交) ✓
  - `workingType=MARK_PRICE` ✓
  - `reduceOnly=true` ✓
- **ref:** `references/binance/urls.md §「2025-12-09 后启用的 Algo Service 接口」`；Binance 官方文档 2026-05-11 re-fetched 确认

### Catch 2 ⚠️ trades 表缺少 client_order_id（HIGH）

- **现象:** 0001_init.up.sql 未包含 client_order_id 列
- **影响:** 无法做幂等 key，重试可能重复下单（资金风险）
- **处理:** Round 1 前先建 migration 0002，ADD COLUMN client_order_id TEXT + UNIQUE
- **schema:** `t{signal_id}_r{retry_count}`（trade id + retry count）

### Catch 3 BY DESIGN Q6 B 简化出场 vs SPEC 3 层出场

- **现象:** SPEC 定义 Layer 2（信号失败）+ Layer 3（移动止损）+ 部分止盈
- **v0.1 决策:** 仅实施 Layer 1（灾难止损）+ 时间出场（soft/hard）
- **风险:** 单笔亏损最大 6%（灾难止损），无移动止损可能回吐盈利
- **处理:** 代码加 `// TODO(v0.2)` 注释，acceptance 明确标 BY DESIGN
- **mu 认知:** 1000U × 6% × 5 仓 = 最大单次损失 30U（可接受）

### Catch 4 ⚠️ MARGIN_CALL 熔断无 WS 实现（MEDIUM）— mu 决策 v0.1 PARTIAL 接受

- **现象:** SPEC §8 列 MARGIN_CALL 为熔断触发，需 WS User Data Stream
- **v0.1 处置:** 依赖币安 Algo Service 灾难止损（6%）先于强平触发；若被强平，position_manager 双向对账发现 orphan → halt
- **残余风险:** 极端行情下灾难止损与强平几乎同时，可能 orphan 状态滞后 1min
- **mu 决策 (2026-05-11):** v0.1 PARTIAL 接受；v0.2 与部分止盈 / 移动止损 / 信号失败止损一起实施 WS User Data Stream
- **处理:** acceptance 标 PARTIAL（同 Phase 1 1.10 纪律），v0.2 补
- **ref:** `references/binance/urls.md §「User Data Streams」`

### Catch 5 ✓ circuit_breaker_state halt_until 已有（CLEAR）

- **现象:** mu Q4 需要 24h 自恢复，担心 schema 缺字段
- **实测:** 0001_init.up.sql 已有 `halt_until TIMESTAMPTZ`，`halt_reason TEXT`
- **处理:** 无需 migration，直接用现有字段实现 24h 自恢复

### Catch 6 ✓ position_states 有 trailing/TP 字段（CLEAR，v0.1 不用）

- **现象:** `trailing_stop_active`, `trailing_stop_price`, `tp_stage1_done`, `tp_stage2_done` 已有
- **v0.1 处置:** 写入 FALSE / NULL，不做任何计算
- **处理:** 无 schema 变更，v0.2 启用

### Catch 7 ℹ️ Position API 版本 V3（INFO）

- **现象:** ARCH 提到 GET /fapi/v2/positionRisk，但 references/binance/urls.md 已更新为 `/fapi/v3/positionRisk`
- **处理:** Round 3 前 web_fetch `/fapi/v3/positionRisk` 文档，用 V3（V2 逐步淘汰）
- **ref:** `references/binance/urls.md §「Position Information V3」`

---

*文档由 Claude Code (claude-sonnet-4-6) 生成，mu review 后 commit。*  
*Round 0 SPEC drift 审基于 SPEC.md + ARCHITECTURE.md + internal/storage/postgres/migrations/0001_init.up.sql 实测。*
