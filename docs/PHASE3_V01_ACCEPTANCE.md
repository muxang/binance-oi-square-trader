# Phase 3 v0.1 Acceptance — 决策引擎

> Status: **PARTIAL PASS,可进 Phase 4**
> commit: `c262519` (Phase 3 v0.1 收官 — Round 4 engine + collector)
> 编写日期: 2026-05-10 BJT

---

## §1. 总览

Phase 3 v0.1 目标(SPEC §Phase 3 acceptance L378-387):**signal 触发时正确产生 TradeOrder 或 RejectionRecord,TradeOrder 不真正下单只打日志,跑 1 天看决策日志拒绝原因分布合理**。

完成度对照:
- ✅ Round 0-4 全 5 阶段 + ARCH §6 channel note(共 5 commits,§2 表)
- ✅ filters.go 3 项过滤(SPEC §全局过滤 v0.1 subset)+ sizing.go(SPEC §仓位规则)+ engine.go(EvaluateOne + RunTick)+ decision_engine collector(§3-§4)
- ✅ 31 unit tests (filters 11 + sizing 11 + engine 9) × race count=2 = 62 PASS;2 collector unit tests + 2 collector adapter integration tests (BEGIN/ROLLBACK INTEGRATION_PG=1) PASS;总计 35 distinct tests / 70 PASS
- ✅ collectors=8 → collectors=9 wire-up 真数据验证(§4)
- ⚠️ **PARTIAL PASS — 0 entered_* signals(Phase 2 v0.1 PARTIAL 数据基线导致)→ RunTick 走 no_entered_signals 路径,trade_entering 真路径留 v0.2 forward**

**一句话结论**:Phase 3 v0.1 决策引擎实施完整,framework + 算法 + collector 全验证,真数据下 RunTick `no_entered_signals` 路径正常工作。`trade_entering` 真路径需 Phase 2 v0.2 forward 部署外网代理稳定后真 entered_* signals 出现才能验。**Phase 4 真下单数据 contract 满足,可进**。

---

## §2. Round 0-4 完成清单

| Round | 内容 | Commit | 关键产出 |
|---|---|---|---|
| **0** | SPEC drift 检查 0 项 + ARCH §6 channel note(Phase 2/3 v0.1 用 DB 替代 channel-based eventbus) | `46c616f` | ARCH.md +2 行 |
| **1** | SymbolService.GetTradingFilters 扩展(LOT_SIZE/MIN_NOTIONAL/PRICE_FILTER 4 字段) + sqlc queries(signals/trades/circuit_breaker)| `63ecd47` | 8 文件 / 12 PASS race count=2 |
| **2** | filters.go EvaluateGlobalFilters(BTC熔断 + 仓位上限 + 24h 不二次入场)+ HasRecent24hAttemptForSymbol JOIN signals.ts(catch trades 无 created_at)| `6cbf804` | 196 行 + 210 行测试 / 22 PASS / 8 ReasonXxx outcome label |
| **3** | sizing.go SizeTrade pure function(Margin × Leverage / step round)+ 11 测试(BTC/PEPE × full/half + 边界 + invariant) | `0956354` | 149 行 + 139 行测试 / 22 PASS / step round 偏差 BTC 4% / PEPE 0% 实测 |
| **4** | engine.go EvaluateOne + RunTick + collector decision_engine + main register(collectors=9)+ metrics(16 outcome label + sizing_deviation histogram)| `c262519` | 213 + 189 + 213 + 143 + 21 + 10 = 789 行 net / 9 unit (engine) + 1 integration + 2 unit collector + 2 integration adapter |

跨度: `46c616f` 19:08 BJT → `c262519` 20:32 BJT,实施 ~3.5h(算法 + IO 层 5 round 分阶段)。

---

## §3. 算法层验证 — 单测全 PASS

### §3.1 filters.go(Round 2,SPEC §全局过滤 v0.1 subset)

11 case race count=2 = 22 PASS:
- 4 决策路径(AllPass / BTCCrash trip + reject / PositionLimit / Recent24h)
- 3 边界(AlreadyHalted / HaltExpired auto-reset / BTCRegimeUnavailable fail-safe)
- 2 边界扩展(BTCDropEdge 严格 > 0.03 / PositionLimitEdge 4 vs 5)
- 2 fail-safe(GetStateError / CountActiveError)

8 ReasonXxx outcome 标签:`btc_5m_crash` / `btc_regime_unavailable` / `circuit_breaker_state_unavailable` / `already_halted` / `position_limit` / `recent_24h_trade` / `trades_count_unavailable` / `trades_24h_lookup_unavailable`。诊断精度优先。

### §3.2 sizing.go(Round 3,SPEC §仓位规则)

11 case = 22 PASS:
- 4 主路径(BTC full / BTC half / PEPE full / PEPE half)
- 3 边界(QtyBelowMinQty / NotionalBelowMinNotional / StepRoundExactBoundary)
- 2 业务 reject(InvalidDecision / ZeroPrice)
- 2 invariant err(NegativeLeverage / NegativeMargin)

step round 偏差实测:
- BTC `notional=480 / target=500` = **4% 偏差**(price 80000, stepSize 0.001)
- PEPE `notional=500 / target=500` = **0% 偏差**(price 0.00001, stepSize 10000 整除)
- v0.2 校准(Round 3 #3 决策)留外网真数据后看分布。

### §3.3 engine.go(Round 4)

9 unit tests / 18 PASS race count=2:
- 4 决策路径(NoSignals / OneEnteredFull / OneEnteredHalf / FiltersReject_BTCCrash)
- 3 错误传播(SizingFails / PerSignalError / GetSignalsError)
- 1 PriceReadFails(sizing_zero_price)
- 1 HaltExpired 跨 step(auto-reset 验证)

EvaluateOne 6 阶段流程:filter → price read → filters read → sizing → outcome dispatch。
RunTick:read entered_* signals → loop EvaluateOne → write trade for trade_entering → return TickReport。

---

## §4. decision_engine 集成验证 — 真数据 1:1 对账

trader pid 87706(2026-05-10 21:07 BJT 启,PROXY=pool,**collectors=9**)→ 21:17:49 BJT graceful shutdown(6s 内 4 步 sequence 完整)。

**真数据时刻**(2 ticks × 5min):

| Tick | BJT | signals_read | trade_entering | rejected_by_filter | rejected_by_sizing | internal_error |
|---|---|---|---|---|---|---|
| t1 | 21:10:00 | 0 | 0 | 0 | 0 | 0 |
| t2 | 21:15:00 | 0 | 0 | 0 | 0 | 0 |

**`no_entered_signals` 路径完美触发**:
- log: `decision_engine tick complete: no_entered_signals` × 2
- metric: `trader_decision_evaluations_total{outcome="no_entered_signals"} = 2`
- DB:`signals` 表 228 行全 `rejected`(entered_*=0)→ `GetRecentEnteredSignals` 5min 窗 → 0 rows
- DB:`trades` 表 0 行(无 trade_entering 写入,符合)

**framework 完整证据**:
- ✅ collectors=9 启动日志(T1-T7 + signal_engine + decision_engine)
- ✅ */5 cron 准时触发(21:10:00.272 / 21:15:00.222,signal_engine + decision_engine 同时刻并发)
- ✅ panic=0 全程 ~10min uptime,无任何 collector panic
- ✅ shutdown 4 步序列(SIGINT 21:17:49 → http stopped 21:17:59 → metrics stopped → shutdown complete @21:17:59)
- ✅ ports release(:8080 + :2112 关闭后 connection refused)

**总测试**: 31 unit (decision pkg, race count=2) + 4 collector (2 unit race count=2 + 2 integration) = 35 distinct tests / 70 PASS, 0 FAIL.

---

## §5. metric 设计 — 16 outcome + sizing_deviation histogram

```
trader_decision_evaluations_total{outcome=...} 16 bounded enum:
  3 special: trade_entering / no_entered_signals / internal_error
  8 filter: rejected_btc_5m_crash / rejected_btc_regime_unavailable /
            rejected_circuit_breaker_state_unavailable / rejected_already_halted /
            rejected_position_limit / rejected_recent_24h_trade /
            rejected_trades_count_unavailable / rejected_trades_24h_lookup_unavailable
  5 sizing: sizing_invalid_decision / sizing_zero_price / sizing_zero_step_size /
            sizing_below_min_qty / sizing_below_min_notional

trader_decision_sizing_deviation_pct{symbol_class=...} histogram:
  buckets: [0, 0.1, 0.5, 1, 2, 5, 10, 20] (%) — 8 explicit
  symbol_class enum: high_price (≥1000) / mid_price (1-1000) / low_price (<1)
  emit only on trade_entering path (sizing OK reached)
  cardinality 详细计算:
    _bucket{symbol_class, le=Y}: 8 explicit + 1 implicit (+Inf) = 9 per class
    _count{symbol_class}: 1 per class
    _sum{symbol_class}: 1 per class
    per class total: 9 + 1 + 1 = 11 series
    3 symbol_class × 11 = 33 series
```

**1.8 cardinality 纪律一致**:无 symbol label。
- counter `decision_evaluations_total`: 16 outcome × 1 = 16 series
- histogram `decision_sizing_deviation_pct`: 3 symbol_class × 11 = 33 series
- **Phase 3 v0.1 新增 series 总计 49**,全 bounded enum,无 unbounded label。

---

## §6. 已知 limitation 清单

### §6.1 trade_entering 真路径未验(留 v0.2 forward)

Phase 2 v0.1 PARTIAL 数据基线(228 signals 全 rejected,0 entered_*)→ Phase 3 v0.1 RunTick 始终走 `no_entered_signals` 分支,**trade_entering → InsertEnteringTrade 真生产数据路径未触发**(单测 + integration 测试覆盖 + 算法层完整;真生产数据 0 entered_* signals 导致 RunTick 始终走 no_entered_signals 分支)。

trade_entering 路径**单测覆盖完整**(`TestRunTick_OneEnteredFull_FiltersPass_TradeEntering` 等 + 集成测试 `TestDecisionEngineAdapter_FullChain_RoundTrip`),**算法层完整**;真数据触发需 Phase 2 v0.1 → v0.2 forward 部署外网代理稳定后 hashtag 数据累积充分,真正出现 hot=true → entered_full 信号。

### §6.2 跟 Phase 1 §8 + Phase 2 §6.1 RCA 一致

**本机 VPN 链路限制**(per Phase 2 v0.1 §6.1 勘误):VPN 链路在高频全采集场景下不稳定,公共 socks5 代理本身在外网服务器直连场景下应能支撑。Phase 3 v0.1 也受此限制 — 数据基线 0 entered → 0 trade_entering。

### §6.3 池大小 19 stale(Phase 2 §6.3 同问题)

`watchlist:current` 19 stale symbols(早期累积,T4 cron 失败未刷新)。Phase 3 v0.1 评估的 signals 全来自 19 stale symbols × Phase 2 信号引擎 5min × 多 ticks → 全 rejected(数据稀疏,OISurge 走 low_growth_from_min)。

### §6.4 cron 同时刻并发(decision_engine + signal_engine)

cron `*/5 * * * *` 让 signal_engine + decision_engine 在同一秒触发(21:10:00.272 双 trace_id)。decision_engine 读"过去 5min" signals 实际是上一个 signal_engine tick(21:05)的产出 — 5min 滞后属性已知,与 SPEC §信号 5min 决策窗口对齐。**v0.2 forward 真 entered 出现时,可能因 1 tick 滞后增加平均决策延迟 ~5min**,但不影响算法正确性。

---

## §7. SPEC drift 处理

Round 0 SPEC drift 检查:**0 项真 drift**(SPEC §仓位规则 / §全局过滤 / §风控熔断 跟 ARCH/migration/Round 1-4 实施 100% 同步)。

收尾修订:
- `ARCH.md §6` 决策引擎块末尾加 1 行 note(commit `46c616f`):`Phase 2/3 v0.1 用 DB 读写 signals 替代 channel-based eventbus,Phase 4 上线后视性能决定是否切 channel + ARCH 同步`。

**Phase 3 v0.1 SPEC 总变化**:0 行(无 SPEC.md 改动)。**ARCH.md +2 行**(channel note)。SPEC 跟 implementation 100% 同步,无 drift 修订需求。

---

## §8. Phase 3 v0.1 → v0.2 forward 启动 condition

跟 Phase 2 v0.1 §8 5 项 forward condition 一致(部署外网后):
1. trader 部署到外网 VPS(无 VPN,代理直连)
2. T3 全采集成功率 ≥ 80%(square_hashtag_history 累积 ≥ 24h)
3. T1/T7 全采集成功率 ≥ 95%(OI / klines history dense)
4. signal_engine 真生 entered_full / entered_half signals(Phase 2 v0.2)
5. decision_engine 跑 7-14 天,统计 trade_entering / rejected_* / sizing_* 分布,验证 SPEC §Phase 3 acceptance 第 5 项 "拒绝原因分布合理"

**Phase 3 v0.2 校准维度**:
- FilterConfig 阈值(BTCDropThreshold / PositionLimit / Recent24hWindow)
- SizingConfig 阈值(FullMargin / HalfMargin / Leverage)
- step round 偏差容忍度(< 5% 接受 / > 10% 警告 / > 20% reject)
- Phase 3 v0.1 未实施过滤 #2/#6/#7/#8/#9/#10(per Round 4 #3 决策,留 v0.2 / Phase 4)

---

## §9. Phase 4 入口准备

Phase 4 真下单(SPEC §Phase 4 acceptance L389-398)读 trades 表的 contract:

| 字段 | Phase 4 用法 |
|---|---|
| `id` / `signal_id` / `symbol` / `direction` / `margin` / `notional` / `leverage` / `status='entering'` | Phase 3 写入,Phase 4 读取作为下单参数 |
| `entry_ts` / `entry_price` / `binance_position_id` | Phase 4 真下单成交后 UPDATE 填充 |
| `initial_atr` / `initial_stop_loss` / `initial_take_profit_*` | Phase 4 挂条件单时填(SPEC §出场逻辑 第 1 层)|
| `binance_disaster_stop_order_id` | Phase 4 STOP_MARKET / Algo Order 挂单 ID |
| `status='open'` | Phase 4 真下单成交后 UPDATE,跨 phase 防重(CountActive 仍包含)|
| `status='partial' / 'closed' / 'orphan'` | Phase 4-5 出场流程 |

**24h 不二次入场过滤跨 phase 切换**(per Round 2 #4 决策):
- Phase 3 v0.1:`HasRecent24hAttemptForSymbol`(JOIN signals.ts,trades.entry_ts NULL)
- Phase 4 真下单后:切回 `HasRecent24hTradeForSymbol`(用 trades.entry_ts)
- 切换在 Phase 4 实施时改 adapter 一行,filters.go 算法不变

**Phase 4 不依赖 v0.2 forward**(算法 + 引擎已稳定,Phase 4 接管 trades 'entering' → 真下单 → 'open')。**Phase 4 实盘下单依赖 v0.2 forward**(部署外网 + 7-14 天 forward 评估通过后才允许真下单到主网)。

**Phase 3 v0.1 PARTIAL PASS**(算法 + 引擎完整,数学对账零偏差;trade_entering 真路径留外网),**Phase 4 数据 contract 满足,可进**。
