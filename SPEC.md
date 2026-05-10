# SPEC.md — 业务需求规格

> 所有"做什么"问题的最终答案。Claude Code 不修改本文件。

---

## 项目元信息

```
项目名:    binance-oi-square-trader
目标:      币安 USDT 永续合约自动化交易系统(仅做多)
本金:      1000 USDT
策略核心:  OI 暴涨 + Square 热度上升做多
模式:      10x 逐仓
持仓周期:  目标 ≤ 24h
```

---

## 业务模型 v0.3

### 监控池(动态,每小时刷新)

无固定主流币池。监控池完全由信号自然涌入。

**来源**:
- A. **Square 推荐流提取的热议币种**(`feed-recommend/list`,1h)
- B. **OI 5min 涨幅 Top 30**
- C. **24h 价格涨幅 Top 20**
- D. **当前持仓币种**(无条件)

**过滤**:
- 币安 USDT 永续上线 ≥ 7 天
- 24h `quoteVolume` ≥ 1000 万 USDT
- 不在黑名单(稳定币 / 杠杆代币 UP/DOWN/BULL/BEAR / 已知下架风险)

**上限**:150 个币种(配合代理 pool + 并发 + 重试,详见 ARCHITECTURE §9.5)

每个 symbol 入池时打 source 标签 `['square','oi','price','position']`,可同时多源。

> **WatchlistConfig env 映射**:实现层 cfg 字段名(`MinQuoteVolume` / `MinListingDays`)与 env 命名(`WATCHLIST_MIN_VOLUME_USD` / `WATCHLIST_MIN_LIST_DAYS`)通过 `cmd/trader/main.go` wire-up 显式映射,双命名共存。

---

### 信号(每 5 min 评估池中所有币种)

#### 主信号:OI 暴涨

> 算法 1:1 锚定 `references/user-snippets/contract-monitor.js` 的 `checkOpenInterestSurge`。

```
周期: 5min (openInterestHist period=5m)
触发(全部满足):
  · 从最近 10 周期最低点的涨幅 ≥ 5%
  · 最近 6 周期总体涨幅 ≥ 3%  
  · 最近 6 周期至少一半相邻递增
  · 当前 5min 收盘价 > 60min 前价格 (不接顶保护, SPEC 追加)
```

##### oi_data JSONB schema(v0.1 落地)

> ARCH §7 仅声明 `signals.oi_data JSONB`,未规定内部结构。
> Phase 2 v0.1 由 `internal/signal/OISurgeResult` 定义(commit `516755b`),snake_case 7 字段:

| 字段 | 类型 | 含义 | 来源 |
|---|---|---|---|
| `triggered` | bool | 4 条规则全过 → true,任一 fail → false | `OISurge` 主返值 |
| `growth_from_min` | numeric (JSON string) | rule 1 实际涨幅 `(c[N]-min) / min` | min over last `LookbackPeriods=10` |
| `recent_growth` | numeric | rule 2 实际涨幅 `(c[N]-c[N-6]) / c[N-6]` | 最近 `RecentPeriods=6` 周期 |
| `growing_periods` | int | rule 3 相邻递增对数 | `count(c[i] > c[i-1])` for last 5 pairs |
| `recent_periods_count` | int | recent 窗口对数(默认 5)| `RecentPeriods - 1` 算法常量 |
| `price_moved_up` | bool | rule 4 SPEC 追加 `closeNow > closePrior` | klines 60min ago bar |
| `failed_reason` | string (optional) | `triggered=false` 时填首失败原因 | 见下方枚举 |

`failed_reason` 枚举(7 值):
- `insufficient_oi_history: have N need M`(N < RecentPeriods,数据不足)
- `zero_min_oi`(rule 1 边界 — min == 0,防除零)
- `zero_recent_start_oi`(rule 2 边界 — recent_start == 0)
- `low_growth_from_min`(rule 1 fail)
- `recent_flat`(rule 2 fail)
- `no_uptrend`(rule 3 fail)
- `price_not_moved_up`(rule 4 fail)

**与 `signals.decision` / `signals.rejection_reason` 的关系**:
- `triggered=true` + `square_data.hot=true` → `decision='entered_full'`,`rejection_reason` NULL
- `triggered=true` + `square_data.hot=false` → `decision='entered_half'`,`rejection_reason` NULL
- `triggered=false` → `decision='rejected'`,`rejection_reason` = `oi_data.failed_reason`(同值)

#### 辅助信号:Square 热度上升(自适应曲率判定)

> v0.1 算法 — Phase 2 设计阶段重构:hot 判定从"单条件幅度阈值"升级为"形态识别"。
> 区分爆发型(病毒式扩散,行情起步)/ 线性型(持续讨论)/ 衰减型(过顶反向),仅爆发型 → hot=true。
> 所有阈值参数为 v0.1 经验值,Phase 2 内 forward 跑数据后 v0.2 校准。

##### 数据来源

T3 SquareHashtagCollector **全采集 ~530 USDT 永续 symbols**(15min cron,非池内),
写入 `square_hashtag_history(symbol, ts, content_count, view_count)`。
全采集让池子动态进出时新入池 symbol 立即拿到完整历史,不再因数据不足永远 fallback。

##### 数学定义

某 symbol 评估时刻有 N 个 15min 采样,序列 `c[1..N]` 为 content_count:

```
60min 滑窗增量:  Δᵢ = c[i] − c[i−4]   (i ≥ 5,4 个 15min 拼 1 个 60min)
15min 增量:      δᵢ = c[i] − c[i−1]    (短窗用)
一阶差分:        Δ'ⱼ = Δⱼ − Δⱼ₋₁
二阶差分:        Δ''ₖ = Δ'ₖ − Δ'ₖ₋₁
二阶差分正数比例: pos_ratio = count(Δ'' > 0) / len(Δ'')
近远比:          ratio = recent_avg / baseline_median
```

`baseline_median = 0` 时 ratio 降级为 0(走 fallback)。

##### 自适应窗口与判定条件

| 模式 | 数据跨度 | 采样数 | 增量序列 | recent K | baseline | ratio 阈值 | 二阶差分窗口 | accel 阈值 | hot 条件 |
|---|---|---|---|---|---|---|---|---|---|
| **标准** | ≥ 24h | ≥ 96 | 60min Δ | 3 | 24h 内除最近 K 个 Δ 中位数 | 2.0 | 最近 6 Δ | 0.6 | ratio AND accel |
| **中窗** | 6–24h | 24–96 | 60min Δ | 3 | 全部除最近 K 个 Δ 中位数 | 2.0 | 最近 4 Δ | 0.6 | ratio AND accel |
| **短窗** | 2–6h | 8–24 | 15min δ | 6 | 全部除最近 K 个 δ 中位数 | 2.5 | (跳过)| — | ratio only |
| **fallback** | < 2h | < 8 | — | — | — | — | — | — | hot = **false** |

##### 阈值参数(v0.1,配置化,待真数据校准)

```
SIGNAL_HOT_STANDARD_RATIO_THRESHOLD=2.0
SIGNAL_HOT_STANDARD_ACCELERATION_THRESHOLD=0.6
SIGNAL_HOT_MEDIUM_RATIO_THRESHOLD=2.0
SIGNAL_HOT_MEDIUM_ACCELERATION_THRESHOLD=0.6
SIGNAL_HOT_SHORT_RATIO_THRESHOLD=2.5
SIGNAL_HOT_MIN_DATA_POINTS=8           # 短窗下限,= 2h × 4/h
```

> 注:上述 `#` 行内注释仅 SPEC 文档说明,实际 `.env` / `.env.example` 行尾禁加注释(Phase 0 viper `825d5d3` 修复约定)。

> v0.1 模式切换无 hysteresis:从短窗→中窗会加二阶差分要求,瞬间 hot 可能从 true→false。
> 这是 feature(算法置信度提升)非 bug,Phase 2 forward 跑数据后 v0.2 决定是否平滑切换。

##### 接口调用

接口调用方式锚定 `references/user-snippets/square-discussion.py`;时序统计 + 自适应判定由本项目实现。

##### square_data JSONB schema(v0.1 落地)

> ARCH §7 仅声明 `signals.square_data JSONB`,未规定内部结构。
> Phase 2 v0.1 由 `internal/signal/SquareHotResult` 定义(commit `563717a`),snake_case 7 字段:

| 字段 | 类型 | 含义 |
|---|---|---|
| `hot` | bool | ratio + acceleration 双条件全过 → true,短窗 ratio 单条件 |
| `mode` | string enum | `standard` / `medium` / `short` / `fallback`(per 自适应窗口表)|
| `ratio` | numeric (JSON string) | `recent_avg / baseline_median`,`baseline_median=0` 时降级 0 |
| `acceleration` | numeric | 二阶差分正数比例 `count(Δ''>0) / len(Δ'')`,短窗 mode 此字段 = 0 |
| `sample_count` | int | `len(contentCounts)` 入参长度 |
| `data_span_hours` | numeric | `(N-1) × SamplePeriod` 跨度小时数 |
| `failed_reason` | string (optional) | `hot=false` 时填首失败原因 |

`failed_reason` 枚举(5 值):
- `insufficient_samples: have N need M`(N < `MinDataPoints=8`,fallback mode)
- `insufficient_deltas: have N need >K`(模式选定后 deltas 不足 recentK,降级 mode 后仍不足)
- `zero_baseline_median`(baseline median == 0,SPEC L85 规定降级)
- `low_ratio`(ratio < 模式阈值)
- `low_acceleration`(标准/中窗 ratio 过但 accel < 阈值;短窗模式不会出现)

> **中窗 acceleration 量化特性**(Round 3 实施时识别,v0.1 设计选择):
> 中窗模式 二阶差分窗口仅 4 Δ → 3 Δ' → 2 Δ'' → `pos_ratio` 仅 3 档(0/0.5/1)。
> 阈值 0.6 实际等价 `pos_ratio = 1`(2/2 个 Δ'' 都正,即 v0.1 短样本严格性)。
> v0.2 看真数据决定是否调阈值或扩 accel 窗口。

> mode 降级:若 `len(deltas)` 不足 `accelWindow`(标准 6 / 中窗 4),mode 降级一档(诚实反映实际算法选择,非缩窗保持 mode)。

#### 入场决策

```
OI 触发 + hot=true   → 标准仓位
OI 触发 + hot=false  → 半仓
OI 不触发            → 不交易
```

---

### 仓位规则

```
保证金/笔: 50 USDT (满仓) / 25 USDT (半仓)
名义仓位:  500 USDT (满仓) / 250 USDT (半仓)
杠杆:      10x 逐仓
持仓上限:  5 个不同币种
单币种当日不二次入场:  是 (避免 stop loss → 重开 → stop loss 死循环)
```

最大保证金占用 = 250 USDT(25%)
最大名义敞口 = 2500 USDT(总资金 2.5x)

---

### 全局过滤(任一不满足跳过)

按顺序检查:
1. 熔断状态:`trading_halted=false`
2. 当日累计亏损 < 5%
3. 持仓数 < 5
4. 该 symbol 24h 内未持仓过
5. BTC 过去 5min 跌幅 ≤ 3%
6. 连续亏损暂停期外
7. 持仓总浮亏 < 8%
8. API 错误率正常
9. symbol 在监控池
10. symbol 支持 10x 逐仓 + minNotional 满足

---

### 出场逻辑(三层结构)

#### 第 1 层 — 灾难性止损(币安挂条件单)

```
距离: entry × 0.94 (-6%)
类型: STOP_MARKET, closePosition=true
作用: 兜底, 防爆仓 + 防程序挂掉
```

> ⚠️ 2025-12-09 起此用法迁移到 Algo Service。
> 实现时必须封装为 `binance.PlaceConditionalOrder()`,内部按日期切换。

#### 第 2 层 — 信号失效止损(本地判断)

```
每 1min 检查持仓, 任一触发即市价平仓:
  a) OI 从入场时下降 > 8%
  b) 价格跌破 "入场时 5min 低点 × 0.97"
  c) 连续 3 根 15min K 线收盘 < EMA20
```

#### 第 3 层 — 移动止损(浮盈 ≥ +3% 后激活)

```
跟踪: max(历史最高价) - 2 × ATR(14, 15min)
仅上移, 不下移
触及即市价平仓
```

#### 止盈分批

```
+5%   → 平 30% 仓位
+12%  → 再平 30% 仓位
剩余 40% 由移动止损 / 信号失效止损管理
```

#### 超时

```
24h 软超时:
  · 浮亏 → 立即市价平
  · 浮盈 < +5% → 立即市价平
  · 浮盈 ≥ +5% → 转入 72h 硬超时窗口, 移动止损接管

72h 硬超时: 无条件市价平
```

#### 执行约束

- 第 1 层用币安条件单,程序断网/重启时由币安自动触发
- 第 2/3 层 + 止盈分批 + 超时由本地市价平仓触发
- **本地触发的平仓必须先撤掉对应的币安条件单**(避免重复)

---

### 风控熔断(硬规则)

| 触发条件 | 动作 |
|---|---|
| 日内累计亏损 ≥ 5% | 当日停机至 UTC 0 点 |
| 连续 5 笔亏损 | 暂停新开仓 24h |
| BTC 5min 跌幅 > 3% | 暂停新开仓 30min |
| 持仓总浮亏 > 8% | 暂停新开仓直到回稳 |
| API 错误率 > 3 次/分钟 | 进入只读模式 |
| MARGIN_CALL 事件 | 高优 TG 告警 + 切只读 |
| 紧急停机 `/halt` | 暂停新开仓 |
| `/close_all` | 立即市价平所有仓位 |

**熔断动作仅暂停新开仓,不主动平已有仓位**(防误伤,已有仓位由止损/止盈/超时正常退出),除非用户主动 `/close_all`。

---

### 数据采集任务

| 任务 | 频率 | 范围 | 说明 |
|---|---|---|---|
| T1 OI 全量扫描 | 5min | 全部 USDT 永续 | period=5m, limit=15 |
| T2 Square 推荐流 | 1h | feed-recommend, 8 次分页 ≤100 帖 | cashtag 发现 |
| T3 Square 热度跟踪 | 15min | queryByHashtag, **全 USDT 永续** | 时序入库 — Phase 2 v0.1 改全采集 + 15min,服务于 hot 自适应判定(§辅助信号);走代理(`SQUARE_USE_PROXY=true`),并发 10,单币重试 2 次,整轮 4min 硬超时,失败的币本轮跳过下轮补 |
| T4 监控池刷新 | 1h | 合并 A/B/C/D + 过滤 | 上限 150 |
| T5 持仓价格追踪 | 60s | 已开仓币种 ticker/price | 30s 实现待 robfig/cron SecondOptional 启用,Phase 2/3 决定 |
| T6 BTC regime | 1min | BTCUSDT 5min K 线跌幅 | 黑天鹅熔断 |
| T7 K 线 + ATR 缓存 | 5min | 池内每币 15min K 线 | ATR(14) + EMA(20) |

---

### Telegram Bot 命令

| 命令 | 说明 |
|---|---|
| `/status` | 当前持仓 + 今日 PnL + 熔断状态 |
| `/halt` | 立即暂停新开仓 |
| `/resume` | 解除 `/halt` |
| `/close_all` | 立即市价平所有仓位 |
| `/close <SYMBOL>` | 平指定币种 |
| `/positions` | 详细持仓 |
| `/pnl 7d` | 近 7 天 PnL |
| `/watchlist` | 当前监控池前 30 + 各 symbol 来源 |
| `/signals 24` | 近 24h 信号触发记录 |
| `/help` | 命令列表 |

**安全**:必须校验 `from.id == TG_CHAT_ID`,其它人发命令一律忽略 + 记录。

---

## Phase 划分(Claude Code 必须严格按序)

| Phase | 内容 | 预期工期 |
|---|---|---|
| Phase 0 | 项目骨架 + 配置 + DB Schema + Docker Compose | 1-2 天 |
| Phase 1 | 数据采集层 T1-T7,纯采集不交易 | 2-3 天 |
| Phase 2 | 信号引擎(纯计算,只落 signals 表) | 1-2 天 |
| Phase 3 | 决策引擎 + 风控状态机 | 1-2 天 |
| Phase 4 | 执行引擎 + 持仓管理 + WS user data stream | 2-3 天 |
| Phase 5 | TG Bot + Web Dashboard | 1-2 天 |
| Phase 6 | Testnet 集成测试 + 实盘小仓位 | 持续 |

每个 Phase 完成后必须停下,输出 Acceptance 对照,等用户确认后再进下一阶段。

---

## Phase 0 Acceptance

```
□ go.mod / Makefile / 目录结构创建
□ docker-compose up 起来 PG + Redis + Prometheus + Grafana + Loki
□ make migrate 跑完所有迁移, 表结构正确(详见 ARCHITECTURE.md DB Schema 章节)
□ make dev 启动 trader binary, /health 返回 ok
□ make typecheck (vet + lint) 全绿
□ zerolog 正常输出
□ .env.example 完整, README 写明启动步骤
□ TRADER_MODE=testnet 默认生效, 启动日志显示当前模式
□ Proxy 配置层加载 (none/single/pool 三种模式, 单测覆盖)
□ 时区配置生效: 内部 UTC, 显示 BJT, daily reset cron 在 BJT 0 点
□ binance.Client 在 mode!=mainnet 时, 写请求(POST/DELETE)被 hard-block,
  仅放行 listenKey 相关接口
```

## Phase 1 Acceptance

```
□ 系统连续运行 6 小时, 各采集任务无 unhandled exception
□ oi_history 表数据正常增长 (5min 一批, ~400 行/批)
□ square_posts 表 1h 一批新数据
□ square_mentions 抽样 50 个 cashtag 的正确率 ≥ 90%
□ watchlist_snapshots 每小时新快照, 池中 20-150 个币
□ Redis 各缓存 key 都有数据且 TTL 正确
□ api_errors 表错误率 < 1%
□ 没有任何决策/下单代码被运行
```

## Phase 2 Acceptance

```
□ 单元测试覆盖 oi-surge 和 square-hot 的触发/不触发边界
□ 跑在已运行 ≥ 24h 的数据上, signals 表有合理数量记录
   (预期 5-30 条 shouldEnter=true / 天)
□ 抽查 10 条 signals 记录, 人工核对 OI 数据是否真的暴涨
□ 仍未触发任何下单代码
```

## Phase 3 Acceptance

```
□ 单元测试覆盖每个过滤器的 pass/fail 路径
□ sizing 函数对 BTC/ETH/PEPE 等不同价格量级币都能算合法 quantity
□ 决策引擎在 signal 触发时正确产生 TradeOrder 或 RejectionRecord
□ TradeOrder 不真正下单, 只打日志
□ 跑 1 天, 看决策日志, 拒绝原因分布合理
□ 风控状态机各 trip 函数手动触发测试通过
```

## Phase 4 Acceptance

```
□ Binance Testnet 跑通完整入场→止损/止盈/超时全流程
□ 故意 kill 进程, 重启后能正确恢复持仓状态
□ 故意挂止损失败, 验证回滚逻辑(立即平仓)
□ 部分平仓后剩余仓位的移动止损正确跟踪
□ 单元测试覆盖 position-manager 各分支
□ 灾难止损被触发时本地状态正确同步(通过 user stream)
```

## Phase 5 Acceptance

```
□ TG bot 各命令测试通过, 非授权用户被忽略
□ /halt 后系统真的不再开新仓
□ /close_all 真的平了所有仓
□ Dashboard 数据和 DB 一致
□ Dashboard 在手机浏览器上可用 (响应式)
```

## Phase 6 上线检查表

```
□ 跑过 Phase 1-5 全部 acceptance
□ Binance Testnet 跑过完整入场→出场 ≥ 20 笔
□ 主网 API key:
  · 关闭提币
  · 仅开"启用合约"
  · 绑定 VPS IP
□ 子账户隔离, 仅充入 1500 USDT
□ TG bot 收到测试告警
□ /halt /close_all 在主网手动测试通过(测试时仓位 = 1U)
□ systemd service 配置 auto-restart
□ 日志 rotate 配置
□ DB 备份脚本配置(每日 dump)
□ Sentry 接入
□ 24h 干运行(实盘配置但 max_concurrent=0)
```

实盘启动节奏:
```
Day 1:    系统启动, max_concurrent=1, margin=10U/笔
Day 2-3:  观察 ≥ 5 笔后, max_concurrent=2, margin=25U
Day 4-7:  margin=50U, max_concurrent=3
Day 8+:   max_concurrent=5(满配)
```
