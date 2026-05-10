# Phase 2 v0.1 Acceptance — 信号引擎

> Status: **PARTIAL PASS,可进 Phase 3**
> commit: `1fe1f8e` (Phase 2 v0.1 收官 — SPEC schema sync)
> 编写日期: 2026-05-10 BJT

---

## §1. 总览

Phase 2 v0.1 目标(SPEC §信号 + §Phase 2 acceptance):**OI 暴涨主信号 + Square 自适应 hot 辅助信号 + 5min cron 决策 → signals 表落库,纯计算不交易**。

完成度对照:
- ✅ Round 0-5 全 6 阶段 + 2 收官 housekeeping(`.gitignore` + SPEC schema),共 8 commits(§2 表)
- ✅ 算法层 v0.1 完整实施 + 数学对账验证(§3)
- ✅ signal_engine 协调器接入 collectors=8,真数据 1:1 对账(§4)
- ✅ oi_data + square_data JSONB schema 落地 + SPEC §主/辅信号 同步(commit `1fe1f8e`)
- ⚠️ **PARTIAL PASS — 本机 VPN 链路限制开发环境长跑,12h 数据 gate 留外网部署后,算法层 + 信号引擎 v0.1 完整**

**一句话结论**:Phase 2 v0.1 算法 + 引擎落地完整,真数据数学对账 100%,可进 Phase 3 决策引擎。VPN 链路限制下未跑 12h 长跑,留 v0.2 forward 评估部署外网后启动。

---

## §2. Round 0-5 完成清单

| Round | 内容 | Commit | 关键产出 |
|---|---|---|---|
| **0** | SPEC §辅助信号 自适应 hot 算法 + T3 row 60s/全采集 同步 | `801d6e8` | SPEC.md +51/-11(算法重构 + drift 修复)|
| **1** | T3 SquareHashtagCollector 全采集 + 15min cron | `50690ff` | 3 文件改动 / 13 测试 26 PASS / 真数据 PARTIAL(代理崩塌但框架行为正确)|
| **2** | `internal/signal/oi_surge.go` 4 条规则 pure function | `516755b` | 142 行 + 124 行测试 / 18 PASS race count=2 / JS L194-237 1:1 锚定 |
| **3** | `internal/signal/square_hot.go` 自适应曲率 3 模式 + fallback | `563717a` | 281 行 + 220 行测试 / 26 PASS / 中窗 acceleration 量化 3 档 catch |
| **4** | `internal/signal/compound.go` + sqlc queries | `42d78b2` | 142 行 + 195 行测试 + 60 行 integration / 9 unit + 1 integration PASS |
| **5** | `internal/collector/signal_engine.go` 协调器 + adapter + main register | `33fbba0` | 246 行 + 258 行测试 / 4 unit + 3 integration PASS / collectors=8 ✓ |
| 收官 1 | `.gitignore` 排除 trader binary 误入 git | `408a731` | 1 行 housekeeping |
| 收官 2 | SPEC §主/辅信号 加 oi_data + square_data JSONB schema(snake_case 7+7 字段 + 中窗 acceleration 量化注释)| `1fe1f8e` | SPEC.md +58 行 |

跨度: `801d6e8` ~ 18:00 BJT 05-10 → `1fe1f8e` ~ 18:40 BJT 05-10,实施 ~6 小时。

---

## §3. 算法层验证 — 真数据数学对账

### §3.1 OISurge(Round 2,SPEC §主信号 1:1 锚定 contract-monitor.js)

**单测覆盖**:9 case `-race -count=2` = 18 PASS:
- 4 条规则各 fail 一次(low_growth_from_min / recent_flat / no_uptrend / price_not_moved_up)
- 3 边界(insufficient_oi_history / zero_min_oi / zero_close_60min_ago)
- AnchoredToContractMonitorJS 锚定(JS hand-calc 复算,InDelta 0.001 通过)
- AllConditionsMet 全过 → triggered=true

**真数据手算对账**(2 distinct symbols 深度验):

`BANANAUSDT 17:55:00`(全平 case):
- 15 行 OI history(15:40 - 17:30,含 16:05-16:40 代理 gap)
- ASC c[1..15],lookback 10 → c[6..15] min = c[15] = current = 809281.7
- `growthFromMin = 0` ✅ **匹配 oi_data "growth_from_min: '0'"**
- recentGrowth = (809281.7 - 827806.3) / 827806.3 = `-0.022377...` ✅ **匹配 "-0.0223779403466729"**(精度完美)
- growingPeriods=0 ✅(全部下降)
- failed_reason=`low_growth_from_min`(cond 1 fail first)✅

`PTBUSDT 17:55:00`(刚低于阈值 case):
- 15 行 OI history,c[6]=4890401606=min,current=5086523557
- `growthFromMin = 196121951 / 4890401606 = 0.0401034...` ✅ **匹配 oi_data "0.0401034448294347"**
- recentGrowth = `0.0312366...` ✅ **匹配**
- growingPeriods=5(c[10]→c[15] 全 5 对递增)✅
- failed_reason=`low_growth_from_min`(4.01% < 5% threshold,first fail)✅
- **"just below threshold" 边界 case 算法正确 reject** — 不接受 4% 当作 5%

**算法横扫**:19 distinct symbols,growth_from_min ∈ [0, 0.0409],全部 < 5% threshold,全 `low_growth_from_min` reject。INXUSDT 4.09%(最接近)+ PTBUSDT 4.01% 都正确 reject,**算法零误判**。

### §3.2 SquareHot(Round 3,SPEC §辅助信号 自适应)

**单测覆盖**:13 case race count=2 = 26 PASS:
- 4 模式(Standard burst / Linear low_ratio / Medium burst / Short burst)
- 阈值 gate(ratio / acceleration 各独立)
- 3 边界(insufficient_samples / empty_input / zero_baseline_median)
- 2 切换(BoundaryAt6h / BoundaryAt24h)
- cfg invalid + downgrade Standard→Medium + Medium accel 量化

**真数据 fallback 验证**:`square_hashtag_history=0 rows`(VPN 链路崩塌,T3 全采集 100% 失败),190 signals 全部 `square_data.mode=fallback hot=false failed_reason="insufficient_samples: have 0 need 8"` ✅ — fallback 路径正确触发,字段完整,`ratio=0 acceleration=0 sample_count=0 data_span_hours=0`(SPEC §辅助信号 fallback mode 行为对齐)。

**未验证**:standard / medium / short 真数据路径(需 hashtag ≥ 2h 数据)— **单测覆盖完整(`TestSquareHot_StandardMode_Burst_Hot` / `MediumMode_Burst_Hot` / `ShortMode_Burst_Hot` 等),真数据验证留 v0.2 forward**。区分:算法层完整 ≠ 真数据验证完整。

### §3.3 Compound(Round 4)

**单测覆盖**:9 case race count=2 = 18 PASS + 1 integration:
- 4 决策路径(EntersFull / EntersHalf / Rejected / InsufficientOIData)
- 3 错误传播(GetOIError / GetHashtagError / InsertSignalError)
- 2 JSONB schema sanity(OIData / SquareData round-trip + grep snake_case keys)
- INTEGRATION_PG=1 InsertSignal_RoundTrip(BEGIN/ROLLBACK 不污染 prod)

decision 逻辑(SPEC §入场决策 L77-82)1:1:
- `oi_alert && hot` → `entered_full`,`rejection_reason=NULL`
- `oi_alert && !hot` → `entered_half`,`rejection_reason=NULL`
- `!oi_alert` → `rejected`,`rejection_reason = oi_data.failed_reason`(同值)

---

## §4. signal_engine 集成验证 — 真数据 1:1 对账

trader pid 83414(2026-05-10 17:54 BJT 启,PROXY=pool,collectors=8)→ 18:43 BJT graceful shutdown(SIGINT → 4 步 sequence,5s 内完成)。

**真数据时刻**(10 ticks × 5min 间隔):

| Tick | BJT | pool_size | entered_full | entered_half | rejected | error |
|---|---|---|---|---|---|---|
| t1 | 17:55:00 | 19 | 0 | 0 | 19 | 0 |
| t2 | 18:00:00 | 19 | 0 | 0 | 19 | 0 |
| t3-t10 | 18:05~18:40 | 19 | 0 | 0 | 19 | 0 |
| **共 10 ticks** | | | **0** | **0** | **190** | **0** |

**rectangular check**:`SELECT COUNT(*) FROM signals` → 190 = 10 × 19 ✓ 0 重复(每 (symbol, tick) 唯一)。

> 注:10 ticks × 19 = 190 全 rejected 是**预期行为,非 algorithm bug**:
> - 代理失败导致 OI history 稀疏 + 池 19 stale(§6.3 §6.4)
> - OISurge 4 条规则全 fail(大部分 `low_growth_from_min`,§3.1 hand-calc 对账)
> - 算法严格 reject 4-5% 增长(PTBUSDT 4.01% / INXUSDT 4.09%)
>
> v0.2 forward 数据稳定后 entered_full / half 比例自然出现(SPEC §Phase 2 5-30/天 预期)。

**metrics 对账**:`trader_signal_evaluations_total{outcome="rejected"}=190` ⇔ DB rows=190 **1:1 完美对账**(同 1.8 metrics 纪律 — counter 跟 log 100% 自洽)。

**signal_engine framework 完整证据**:
- ✅ collectors=8 启动日志(T1-T7 + signal_engine)
- ✅ */5 cron 准时触发(17:55:00.149 / 18:00:00.117 / ...,每 5min 极小漂移)
- ✅ Run() elapsed 60-105ms(评估 19 symbols × Evaluate (3 reads + 1 write))
- ✅ panic=0 全程 50 min uptime,无任何 collector panic
- ✅ shutdown 4 步 sequence 完整(signal received → http stopped → metrics stopped → shutdown complete)

---

## §5. signals JSONB schema 落地

ARCH §7 仅声明 `oi_data JSONB / square_data JSONB`,未规定内部结构。Phase 2 v0.1 由:
- `internal/signal/OISurgeResult` 定义 oi_data(commit `516755b`,7 字段)
- `internal/signal/SquareHotResult` 定义 square_data(commit `563717a`,7 字段)

SPEC §主信号 + §辅助信号 commit `1fe1f8e` 加同步段落(7+7 字段表 + 7+5 failed_reason 枚举 + 中窗 acceleration 量化 3 档 + mode 降级注释)。Phase 3 决策引擎可直接读 SPEC schema 段引用 commit hash 找到 Go struct 源码。

**schema 强证据**:`integration_test.go TestSignalEngineAdapter_InsertSignal_RoundTrip` BEGIN/ROLLBACK 实测 InsertSignalParams.OiData(JSON.Marshal of OISurgeResult)→ PG JSONB → SELECT 反序列化 → 字段值与原始 struct 一致(triggered, growing_periods)。

---

## §6. 已知 limitation 清单

### §6.1 Phase 1 §8 RCA 勘误(VPN 链路 vs 公共代理)

> **Phase 1.10 PHASE1_ACCEPTANCE §8** 把 1.9 长跑代理崩塌归因为 "公共 socks5 代理池稳定性"(active 41→3 in 30min)。
>
> **Phase 2 v0.1 实施期间 mu 提供进一步 RCA**:本机开发环境走 VPN 出境,VPN 链路在高频全采集场景下不稳定(30min 内崩塌)。**真因是 VPN 链路,不是公共代理本身**。代理在 VPN 后端稳定,部署到外网服务器(无 VPN,代理直连)后崩塌问题消失。
>
> **影响**:Phase 1 §8 列的 3 部署方案(自架代理 / 付费稳定 / 真 key + 配额)**可能都不需要** — 现有公共代理池在外网服务器直连场景下足以支撑。Phase 2 v0.2 forward 评估部署到外网服务器后才能确认。
>
> **历史诚实**:Phase 1 时基于现有证据归因 "代理池",Phase 2 mu 提供新证据修正。不掩饰 Phase 1 归因偏差,在 Phase 2 acceptance 里勘误。

### §6.2 12h 长跑数据 gate 未达成

Round 0 决策 Round 6 启动条件:`square_hashtag_history` 跨度 ≥ 12h。本机 VPN 链路限制下 T3 全采集 100% 失败,`hashtag rows = 0`,12h gate **不可达**。

按 mu 决策**不长跑**:Round 6 真数据时刻**改算法验证模式**(本 acceptance §3 完成),12h 长跑留 v0.2 forward 部署外网后开始。

### §6.3 池大小 19 stale(T4 watchlist 未刷新)

`watchlist:current` 19 symbols 来自 02:00 BJT 残留(Round 1 之前累积),T4 cron 18:00 跑过但 ticker24h 调用代理 timeout 失败。signal_engine 实际只评估 19 stale symbols(SPEC 设计 ~150 上限)。**Round 6 抽查池小 acceptance 打折但算法验证不依赖池大小**(§3 已验证)。

### §6.4 OI / klines / hashtag 数据稀疏

VPN 限制下 T1 oi 99.6% 失败(每 tick 仅 ~2/529 symbols 成功)/ T7 klines 100% 失败 / T3 hashtag 100% 失败。已累积数据(oi_history 5万行+klines 33000+square_hashtag 0)历史值有限,但够 §3 OISurge 算法对账(15 rows × 19 symbols 足够 4 条规则计算)。

---

## §7. SPEC drift 处理

Round 0 修订 SPEC.md(commit `801d6e8`):
- §辅助信号 算法重构(单条件幅度 → 自适应曲率组合判定)
- §数据采集 T3 row(5min 池内 → 15min 全 USDT 永续)

收官修订(commit `1fe1f8e`):
- §主信号 加 oi_data JSONB schema 段
- §辅助信号 加 square_data JSONB schema 段 + 中窗 acceleration 量化注释 + mode 降级注释

**Phase 2 v0.1 SPEC 总变化**:+109 行(801d6e8 +51 + 1fe1f8e +58),0 删除。SPEC 跟 implementation 100% 同步。

---

## §8. Phase 2 v0.2 forward 启动 condition

**条件**(部署到外网服务器后启动):
1. trader 部署到外网 VPS(无 VPN,代理直连)
2. T3 全采集成功率 ≥ 80%(per `signal_engine` tick `square_hashtag tick complete failure_rate < 0.20`)
3. `square_hashtag_history` 累积 ≥ 24h(支撑 SquareHot standard 模式真数据评估)
4. T1/T7 全采集成功率 ≥ 95%(OI / klines history dense 不稀疏)
5. signal_engine 跑 **7 天(最少)- 14 天(推荐)**,统计 entered_full / entered_half / rejected 分布,验证 SPEC §Phase 2 acceptance 5-30 entered/天 预期

**v0.2 校准维度**(forward 真数据后):
- OISurgeConfig 阈值(GrowthFromMinMin / RecentGrowthMin)— 看真假阳性率
- SquareHotConfig 阈值(各模式 ratio / acceleration / MinDataPoints)
- 中窗 accel 窗口扩展(从 4 → 6,让 pos_ratio 更连续)如果 forward 数据量化太严
- mode 切换 hysteresis(Round 3 v0.1 标注的 feature → v0.2 决定是否平滑)

---

## §9. Phase 3 入口准备

Phase 3 决策引擎(SPEC §Phase 3 acceptance)读 signals 表的 contract:

| 字段 | Phase 3 用法 |
|---|---|
| `decision='entered_full' / 'entered_half'` | 选择是否生成 TradeOrder + sizing(满仓 vs 半仓)|
| `oi_data` (JSONB) | 决策溯源 / 风控审计(commit `1fe1f8e` schema 段定义 7 字段)|
| `square_data` (JSONB) | 同上 |
| `rejection_reason` (TEXT) | rejected 信号溯源(per §6.1 RCA 勘误,排查算法 vs 数据问题)|

**Phase 3 不依赖 v0.2 forward**(算法层 + 信号引擎已稳定,Phase 3 只读 signals 不再算)。**Phase 4 实盘下单依赖 v0.2 forward**(部署外网 + 7 天 forward 评估通过后才允许真下单)。

**Phase 2 v0.1 PARTIAL PASS**(算法 + 引擎完整,数学对账零偏差;长跑留外网),**Phase 3 数据 contract 满足,可进**。
