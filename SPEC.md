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

#### 辅助信号:Square 热度上升

```
对监控池每个 symbol 调 queryByHashtag, 5min 一次
跟踪 contentCount 时间序列

hot 判定:
  · 取该 symbol 最近 24h 的 contentCount 时序
  · 计算每个 60min 滑窗的增量
  · 若当前 60min 增量 > 该 symbol 24h 增量中位数 × 2 → hot=true
  · 数据不足 24h(刚入池) → hot=false (fallback)
```

> 算法锚定 `references/user-snippets/square-discussion.py` 的接口调用方式;
> 时序统计逻辑由本项目实现。

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
| T3 Square 热度跟踪 | 5min | queryByHashtag, 池内每币 | 时序入库;走代理(`SQUARE_USE_PROXY=true`),并发 10,单币重试 2 次,整轮 4min 硬超时,失败的币本轮跳过下轮补 |
| T4 监控池刷新 | 1h | 合并 A/B/C/D + 过滤 | 上限 150 |
| T5 持仓价格追踪 | 30s | 已开仓币种 ticker/price | |
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
