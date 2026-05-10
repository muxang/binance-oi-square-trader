# ARCHITECTURE.md — 技术架构

> 所有"用什么、怎么部署"问题的最终答案。

---

## 1. 设计目标

- **稳定性优先**:7×24 跑实盘,任何一个采集失败不能拖垮其它
- **可观测**:每笔交易从信号到出场可完整追溯,Prometheus 指标 + 结构化日志
- **可重启**:进程随时可挂可重启,启动后从币安和 DB 恢复完整状态
- **零金额计算用 float**:所有金额用 `numeric(36,18)` + Go 的 `decimal` 库
- **币安接口变更要平滑**:2025-12-09 算法单迁移,接口切换由抽象层吸收

## 2. 非目标

- 多策略并行(v0.2 再说)
- 多交易所(只对币安)
- 高可用主备(单 VPS 已经够)
- 微秒级延迟(目标 ≤ 5min 决策周期,Go + REST 完全够)
- 量化回测引擎(v0.1 不做,后续视需要再说)

---

## 3. 技术栈选型

| 层 | 选型 | 理由 |
|---|---|---|
| 语言 | Go 1.25+ | 静态编译、协程并发干净、长跑稳定 |
| 架构 | 模块化单体 | 单进程,内部 channel + 接口隔离 |
| HTTP | Echo v4 | Dashboard API,中间件生态稳 |
| 币安 SDK | 自封装 (resty + gorilla/websocket) | adshao/go-binance 滞后,自封装可控 |
| 主 DB | PostgreSQL 16 + TimescaleDB | 时序 + 关系型一站式 |
| 缓存 | Redis 7 | 实时状态、监控池、价格 |
| ORM | sqlc | 类型安全 + 纯 SQL,适合复杂时序查询 |
| 迁移 | golang-migrate | 标准方案 |
| 日志 | zerolog | 结构化、零分配 |
| 错误追踪 | Sentry | |
| 监控 | Prometheus + Grafana + Loki | 一套搞定 metric/log/dashboard |
| TG Bot | go-telegram-bot-api/v5 | |
| 配置 | viper(yaml + env) | |
| 测试 | testing + testify + dockertest | 集成测试用真 PG/Redis |
| 部署 | Docker Compose | 单 VPS,所有服务容器化 |
| 反向代理 | Caddy | 自动 HTTPS |
| Dashboard | React 18 + Vite + TS + shadcn/ui + TanStack Query | 复用用户前端栈 |

---

## 4. 系统架构图

```
┌──────────────────────────────────────────────────────────────┐
│                  数据采集层(Collectors)                     │
│  T1 OI 全量 5min      T2 Square Feed 1h    T3 Hashtag 5min  │
│  T4 监控池 1h          T5 持仓价格 60s      T6 BTC 1min     │
│  T7 K线/ATR 5min                                              │
└──────────────────────────┬───────────────────────────────────┘
                           ↓ 落库 + 缓存
                  PostgreSQL(主) + Redis(实时)
                           ↑ 读
┌──────────────────────────┴───────────────────────────────────┐
│                  信号引擎(Signal Engine)                     │
│  · 每 5min 评估池中所有币种                                   │
│  · OI Surge + Square Hot                                     │
│  · 输出 CompoundSignal → events.signal channel               │
└──────────────────────────┬───────────────────────────────────┘
                           ↓
┌──────────────────────────┴───────────────────────────────────┐
│              决策引擎(Decision Engine)                       │
│  · 全局过滤(熔断 / 持仓数 / 大盘)                            │
│  · 仓位 sizing                                                │
│  · 输出 TradeOrder → events.order channel                    │
└──────────────────────────┬───────────────────────────────────┘
                           ↓
┌──────────────────────────┴───────────────────────────────────┐
│              执行引擎(Execution Engine)                      │
│  · 币安下单(setLeverage / setMarginType / market order)     │
│  · 挂灾难止损条件单(STOP_MARKET / Algo Order 按日期切换)   │
│  · 写入 trades + position_states                             │
└──────────────────────────┬───────────────────────────────────┘
                           ↓
┌──────────────────────────┴───────────────────────────────────┐
│            持仓管理(Position Manager)                        │
│  · 每 1min 扫描所有持仓                                       │
│  · 三层止损 / 分批止盈 / 移动止损 / 超时                      │
│  · 程序重启时从币安恢复状态                                   │
└──────────────────────────┬───────────────────────────────────┘
                           ↓
┌──────────────────────────┴───────────────────────────────────┐
│             风控 + 监控(Risk + Monitoring)                  │
│  · 熔断状态机(daily loss / consecutive / btc crash 等)      │
│  · TG Bot(命令 + 告警)                                       │
│  · Web Dashboard(状态展示)                                  │
│  · Prometheus 指标暴露 /metrics                              │
└──────────────────────────────────────────────────────────────┘
                           ↑
┌──────────────────────────┴───────────────────────────────────┐
│         WebSocket(币安 User Data Stream)                     │
│  · ORDER_TRADE_UPDATE / ACCOUNT_UPDATE / MARGIN_CALL         │
│  · 旁路推送给 Position Manager 同步状态                       │
└──────────────────────────────────────────────────────────────┘
```

> **注(Phase 2/3 v0.1 实施)**:`signals → trades` 走 DB 读写(`signals_ts_desc_idx` + `trades_symbol_status_idx` 索引高效查询 + `trades` 表 24h lookup 兜底防重),**不走 channel-based eventbus**(§5)。Phase 4 上线后视性能决定是否切 channel + ARCH 同步。

---

## 5. 进程内事件总线

单进程模块化单体,模块间用 Go channel 通信:

```go
// internal/eventbus/bus.go
type Bus struct {
    Signals chan domain.CompoundSignal   // collector → signal → decision
    Orders  chan domain.TradeOrder       // decision  → execution
    Fills   chan domain.OrderFill        // ws        → position
    Halts   chan domain.HaltEvent        // risk      → all
}
```

设计原则:
- channel 都是有缓冲(size=100),避免发送方阻塞
- 每个事件都有 `Ts time.Time` + `TraceID string`,方便链路追溯
- 消费者 panic 必须 recover + 告警,不能让事件丢失

---

## 6. 目录结构

```
binance-oi-square-trader/
├── cmd/
│   └── trader/
│       └── main.go              # 入口, 装配所有模块
├── internal/                    # Go 惯例: internal 防止外部 import
│   ├── config/                  # 配置加载(viper)
│   ├── domain/                  # 核心业务类型(Signal/Trade/Position/...)
│   ├── binance/                 # 币安 API 封装(REST + WS)
│   │   ├── client.go            # HTTP 客户端 + 签名 + 速率限制
│   │   ├── futures.go           # /fapi/v1/* endpoint
│   │   ├── algo.go              # /fapi/v1/algo/* (12-09 后启用)
│   │   ├── conditional.go       # 条件单抽象(STOP_MARKET / algo 按日期切)
│   │   ├── square.go            # 非官方 BAPI
│   │   ├── ws_user_data.go      # User Data Stream
│   │   └── error.go             # 错误码分类(ClassifyError)
│   ├── storage/
│   │   ├── postgres/
│   │   │   ├── queries/         # sqlc 输入 .sql
│   │   │   ├── migrations/      # golang-migrate
│   │   │   └── gen/             # sqlc 生成
│   │   └── redis/
│   ├── collector/               # 数据采集任务
│   │   ├── oi.go                # T1
│   │   ├── square_feed.go       # T2
│   │   ├── square_hashtag.go    # T3
│   │   ├── watchlist.go         # T4
│   │   ├── price_tracker.go     # T5
│   │   ├── btc_regime.go        # T6
│   │   └── kline_atr.go         # T7
│   ├── signal/
│   │   ├── oi_surge.go          # 锚定 contract-monitor.js
│   │   ├── square_hot.go
│   │   └── compound.go
│   ├── decision/
│   │   ├── filters.go
│   │   ├── sizing.go
│   │   └── engine.go
│   ├── execution/
│   │   ├── trader.go            # 入场流程
│   │   ├── exit.go              # 出场触发
│   │   └── recovery.go          # 重启恢复
│   ├── position/
│   │   ├── state_machine.go
│   │   └── manager.go           # 主循环
│   ├── risk/
│   │   ├── circuit_breaker.go
│   │   └── monitor.go
│   ├── eventbus/
│   │   └── bus.go
│   ├── scheduler/
│   │   └── scheduler.go         # robfig/cron/v3
│   ├── tg/
│   │   └── bot.go
│   ├── api/                     # Dashboard HTTP API
│   │   ├── server.go
│   │   ├── handlers/
│   │   └── middleware/
│   └── pkg/
│       ├── logger/              # zerolog 包装
│       ├── retry/
│       ├── ratelimit/           # token bucket
│       └── metrics/             # prometheus 指标
├── web/                         # Dashboard 前端
│   ├── src/
│   ├── package.json
│   └── vite.config.ts
├── deploy/
│   ├── docker-compose.yml
│   ├── docker-compose.prod.yml
│   ├── Dockerfile
│   ├── Caddyfile
│   ├── prometheus.yml
│   ├── loki-config.yml
│   └── grafana/
│       └── dashboards/
├── scripts/
│   ├── bootstrap.sh
│   ├── deploy.sh
│   └── db-backup.sh
├── test/
│   └── e2e/
├── .github/workflows/
│   └── ci.yml
├── references/                  # 唯一信息源
├── SPEC.md
├── ARCHITECTURE.md
├── CLAUDE.md
├── RUNBOOK.md                   # 后续补
├── README.md
├── Makefile
├── go.mod
└── .env.example
```

---

## 7. 数据库 Schema

### 时序表(TimescaleDB hypertable)

```sql
-- OI 历史(每 5min 全量)
CREATE TABLE oi_history (
  symbol      TEXT NOT NULL,
  ts          TIMESTAMPTZ NOT NULL,
  oi          NUMERIC(36, 18) NOT NULL,
  oi_value_usd NUMERIC(36, 18) NOT NULL,
  PRIMARY KEY (symbol, ts)
);
SELECT create_hypertable('oi_history', 'ts', chunk_time_interval => INTERVAL '1 day');
CREATE INDEX ON oi_history (symbol, ts DESC);

-- K 线
CREATE TABLE klines (
  symbol      TEXT NOT NULL,
  timeframe   TEXT NOT NULL,
  open_time   TIMESTAMPTZ NOT NULL,
  open        NUMERIC(36, 18) NOT NULL,
  high        NUMERIC(36, 18) NOT NULL,
  low         NUMERIC(36, 18) NOT NULL,
  close       NUMERIC(36, 18) NOT NULL,
  volume      NUMERIC(36, 18) NOT NULL,
  quote_volume NUMERIC(36, 18) NOT NULL,
  PRIMARY KEY (symbol, timeframe, open_time)
);
SELECT create_hypertable('klines', 'open_time', chunk_time_interval => INTERVAL '1 day');

-- Square hashtag 时序
CREATE TABLE square_hashtag_history (
  symbol        TEXT NOT NULL,
  ts            TIMESTAMPTZ NOT NULL,
  content_count BIGINT NOT NULL,
  view_count    BIGINT NOT NULL,
  PRIMARY KEY (symbol, ts)
);
SELECT create_hypertable('square_hashtag_history', 'ts', chunk_time_interval => INTERVAL '1 day');
```

### 关系表(普通 Postgres 表)

```sql
-- Square 推荐流帖子原始数据
CREATE TABLE square_posts (
  id          TEXT PRIMARY KEY,
  fetched_at  TIMESTAMPTZ NOT NULL,
  author_id   TEXT,
  author_type TEXT,
  author_name TEXT,
  title       TEXT,
  content_text TEXT,
  view_count  BIGINT,
  like_count  BIGINT,
  comment_count BIGINT,
  raw_json    JSONB
);
CREATE INDEX ON square_posts (fetched_at DESC);

-- 提取出的币种提及
CREATE TABLE square_mentions (
  post_id  TEXT REFERENCES square_posts(id),
  symbol   TEXT NOT NULL,
  weight   NUMERIC(8, 4) NOT NULL DEFAULT 1.0,
  ts       TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (post_id, symbol)
);
CREATE INDEX ON square_mentions (symbol, ts DESC);

-- 监控池快照
CREATE TABLE watchlist_snapshots (
  id      BIGSERIAL PRIMARY KEY,
  ts      TIMESTAMPTZ NOT NULL,
  symbols JSONB NOT NULL  -- [{symbol, sources: ['square','oi','price','position'], score}]
);

-- 信号事件
CREATE TABLE signals (
  id              BIGSERIAL PRIMARY KEY,
  ts              TIMESTAMPTZ NOT NULL,
  symbol          TEXT NOT NULL,
  oi_triggered    BOOL NOT NULL,
  oi_data         JSONB,
  square_hot      BOOL NOT NULL,
  square_data     JSONB,
  decision        TEXT NOT NULL,  -- entered_full / entered_half / rejected
  rejection_reason TEXT
);
CREATE INDEX ON signals (ts DESC);
CREATE INDEX ON signals (symbol, ts DESC);

-- 交易记录
CREATE TABLE trades (
  id                              BIGSERIAL PRIMARY KEY,
  signal_id                       BIGINT REFERENCES signals(id),
  symbol                          TEXT NOT NULL,
  direction                       TEXT NOT NULL DEFAULT 'LONG',
  entry_ts                        TIMESTAMPTZ,
  entry_price                     NUMERIC(36, 18),
  margin                          NUMERIC(36, 18) NOT NULL,
  notional                        NUMERIC(36, 18) NOT NULL,
  leverage                        SMALLINT NOT NULL DEFAULT 10,
  initial_atr                     NUMERIC(36, 18),
  initial_stop_loss               NUMERIC(36, 18),
  initial_take_profit_1           NUMERIC(36, 18),
  initial_take_profit_2           NUMERIC(36, 18),
  binance_position_id             TEXT,
  binance_disaster_stop_order_id  TEXT,
  status                          TEXT NOT NULL,  -- entering / open / partial / closed / orphan
  exit_ts                         TIMESTAMPTZ,
  exit_price                      NUMERIC(36, 18),
  exit_reason                     TEXT,
  realized_pnl                    NUMERIC(36, 18),
  fees                            NUMERIC(36, 18),
  raw_events                      JSONB
);
CREATE INDEX ON trades (status, entry_ts DESC);
CREATE INDEX ON trades (symbol, status);

-- 部分平仓事件
CREATE TABLE trade_exits (
  id        BIGSERIAL PRIMARY KEY,
  trade_id  BIGINT REFERENCES trades(id),
  ts        TIMESTAMPTZ NOT NULL,
  type      TEXT NOT NULL,  -- tp_stage1 / tp_stage2 / trailing / signal_fail / disaster / soft_timeout / hard_timeout / manual / rollback
  qty       NUMERIC(36, 18) NOT NULL,
  price     NUMERIC(36, 18) NOT NULL,
  pnl       NUMERIC(36, 18) NOT NULL
);

-- 进行中持仓的实时状态(供状态机)
CREATE TABLE position_states (
  trade_id              BIGINT PRIMARY KEY REFERENCES trades(id),
  current_qty           NUMERIC(36, 18) NOT NULL,
  highest_price         NUMERIC(36, 18),
  trailing_stop_active  BOOL NOT NULL DEFAULT FALSE,
  trailing_stop_price   NUMERIC(36, 18),
  tp_stage1_done        BOOL NOT NULL DEFAULT FALSE,
  tp_stage2_done        BOOL NOT NULL DEFAULT FALSE,
  entry_oi              NUMERIC(36, 18),
  last_check_ts         TIMESTAMPTZ
);

-- 熔断状态(单行)
CREATE TABLE circuit_breaker_state (
  id                       SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  trading_halted           BOOL NOT NULL DEFAULT FALSE,
  halt_reason              TEXT,
  halt_until               TIMESTAMPTZ,
  daily_pnl                NUMERIC(36, 18) NOT NULL DEFAULT 0,
  daily_pnl_date           DATE NOT NULL DEFAULT (NOW() AT TIME ZONE 'UTC')::DATE,
  consecutive_losses       SMALLINT NOT NULL DEFAULT 0,
  last_btc_crash_ts        TIMESTAMPTZ
);
INSERT INTO circuit_breaker_state (id) VALUES (1);

-- API 错误日志(滑动窗口风控用)
CREATE TABLE api_errors (
  id          BIGSERIAL PRIMARY KEY,
  ts          TIMESTAMPTZ NOT NULL,
  source      TEXT NOT NULL,    -- binance_rest / binance_ws / square
  endpoint    TEXT,
  http_code   INT,
  error_code  INT,
  message     TEXT
);
CREATE INDEX ON api_errors (ts DESC);
```

### Redis Key 约定

| Key | 类型 | TTL | 用途 |
|---|---|---|---|
| `symbol_info:{symbol}` | hash | 1h | 精度、最小下单等 |
| `latest_price:{symbol}` | string | 5min | 实时价格 |
| `atr:{symbol}` | string | 30min | ATR(14, 15min) |
| `ema20:{symbol}` | string | 30min | EMA(20, 15min) |
| `watchlist:current` | json string | 永久(每 1h 覆盖) | 当前监控池 |
| `btc_5m_change` | string | 5min | BTC regime |
| `bnc_uuid` | string | 永久 | Square 匿名 UUID(启动时生成) |

---

## 8. 关键流程

### 8.1 入场流程(执行引擎)

```
1. setMarginType(symbol, ISOLATED)         # -4046 当成功
2. setLeverage(symbol, 10)
3. INSERT trades (status='entering')
4. marketBuy(symbol, quantity, clientOrderId='boss-entry-{tradeId}')
   → 等待 ORDER_TRADE_UPDATE 的 FILLED 事件确认成交价
   → 若 -1006/-1007 → 用 client id 查询订单状态
5. UPDATE trades SET entry_price=..., status='open'
6. PlaceConditionalOrder(STOP_MARKET, stopPrice=entry*0.94, closePosition=true,
   clientOrderId='boss-disaster-{tradeId}')
7. UPDATE trades SET binance_disaster_stop_order_id=...
8. INSERT position_states (...)
9. TG 告警: 已开仓

回滚:
若步骤 4 成功但步骤 6 失败 → 立即市价平仓 (clientOrderId='boss-rollback-{tradeId}')
INSERT trade_exits type='rollback'
TG 高优告警(裸奔仓位短暂存在过)
```

### 8.2 持仓管理 tick(每 1min)

详见 SPEC.md "出场逻辑"。状态机要点:

```
state {entering, open, partial, closed} 转换:
  entering → open      入场单 FILLED
  open     → partial   tp_stage1 触发
  partial  → partial   tp_stage2 触发
  partial  → closed    最后一份仓位平掉
  open     → closed    全平(灾难/信号失效/超时/手动)
```

### 8.3 重启恢复(启动时)

```
1. binance.GetAllPositions()
2. 对每个币安持仓:
   - 查 trades WHERE symbol=? AND status IN ('open','partial')
   - 找到: 校验 quantity, 不一致告警
   - 没找到: 孤儿仓位 → 标记 + TG 告警, 不自动平
3. 对每个本地 status='open' 但币安没仓位的:
   - 标记 status='orphan_local', TG 告警
4. binance.GetOpenOrders() (普通 + algo)
5. 对每个本地有 disaster_stop_order_id 但币安没单的:
   - 重新挂止损单
```

### 8.4 灾难止损接口切换(2025-12-09)

```go
// internal/binance/conditional.go
func (c *Client) PlaceConditionalOrder(ctx, params) (*Order, error) {
    if c.now().After(c.cfg.AlgoMigrationDate) {
        return c.placeAlgoOrder(ctx, params)        // POST /fapi/v1/algo/order
    }
    return c.placeRegularConditional(ctx, params)   // POST /fapi/v1/order type=STOP_MARKET
}
```

业务代码只调 `PlaceConditionalOrder`,不感知差异。

---

## 9. 速率限制 + 重试策略

### 速率限制(token bucket,IP 维度)

```
binance_rest_weight: 2400 / 1min       (实际只用 80%, 留 20% 余量)
binance_rest_orders: 1200 / 1min       (实际只用 80%)
binance_ws:          连接数 ≤ 1
square_feed:         8 次 / 1h
square_hashtag:      60 次 / 5min, 并发 ≤ 5
```

### 重试策略

```go
type RetryPolicy struct {
    MaxAttempts    int           // 默认 3
    InitialBackoff time.Duration // 默认 1s
    MaxBackoff     time.Duration // 默认 30s
    Multiplier     float64       // 默认 2.0
    Jitter         float64       // 默认 0.2
}
```

错误分类决定是否重试,详见 `references/binance/urls.md` "特殊错误处理"。

---

## 9.5 代理 (Binance Proxy)

批量采集币安全部合约时,单 IP 容易触发限流(-1003)或地区封锁(451)。
本项目在 `BinanceClient` 内部实现代理层,业务代码不感知。

### 三种模式

| 模式 | 用途 |
|---|---|
| `none` | 直连(开发期 / VPS 在已开放区域) |
| `single` | 所有出向请求走单一代理 |
| `pool` | 多代理轮换 + 自动摘除恢复 |

### 配置(`.env`)

```env
BINANCE_PROXY_MODE=pool
BINANCE_PROXY_POOL_URLS=http://user:pass@p1.example.com:8080,http://user:pass@p2.example.com:8080,socks5://p3.example.com:1080
BINANCE_PROXY_POOL_STRATEGY=round_robin
BINANCE_PROXY_FAILURE_THRESHOLD=5
BINANCE_PROXY_RECOVERY_MINUTES=5

SQUARE_USE_PROXY=true   # Square BAPI 也是 binance.com 域, 通常需要走代理
```

### 实现要点

```go
// internal/binance/proxy.go
type ProxyManager interface {
    HTTPClient(ctx context.Context) (*http.Client, error) // REST 用
    WSDialer(ctx context.Context) (*websocket.Dialer, error) // WS 用
    ReportFailure(proxyURL string, err error)
    ReportSuccess(proxyURL string)
    Stats() ProxyStats
}
```

- **Pool 选择策略**:
  - `round_robin`(默认):简单轮转,均匀分摊请求
  - `random`:随机选择,避免某代理被识别为机器人
- **健康检查**:
  - 每个代理维护一个失败计数器,连续失败 ≥ `FAILURE_THRESHOLD` 临时摘除
  - 摘除后过 `RECOVERY_MINUTES` 分钟自动尝试一次,成功则重新启用
  - 全部代理摘除时 → TG 高优告警 + 进入只读模式(暂停下单)
- **WebSocket 走代理**:
  - `gorilla/websocket.Dialer.Proxy = http.ProxyURL(...)`
  - 注意 socks5 走 ws 需要 `golang.org/x/net/proxy` + 自定义 NetDial

### 失败处理

```
单代理失败 → 计数, 不重试本请求, 业务层走重试策略 (可能选到其它代理)
全部代理失败 → 告警 + 切只读模式
代理本身延迟 100-500ms → Phase 4 时验证不影响超时阈值 (recvWindow=5000ms 足够)
```

### Prometheus 指标

```
binance_proxy_requests_total{proxy_url, status}     counter
binance_proxy_failures_total{proxy_url, error_type} counter
binance_proxy_active_count                          gauge   # 当前可用代理数
binance_proxy_evicted_count                         gauge   # 被摘除的代理数
```

### 严禁的兜底策略

- ❌ 单代理失败时**不**自动 fallback 到直连(直连大概率也被封,且暴露真实 IP)
- ❌ 代理认证信息**不**记日志(包含密码)
- ❌ Pool 模式中**不**对失败代理立即重试,等下次轮转

### Square 跟踪并发约束

Square hashtag 跟踪(T3)走代理 pool 后,移除"上限 80"硬约束,但必须配合以下并发限制:

| 项 | 值 | 说明 |
|---|---|---|
| 单轮并发 | `SQUARE_HASHTAG_CONCURRENCY`(默认 10) | 高于此易被识别 |
| 单币种超时 | 8s | 包含代理握手时间 |
| 单币种重试 | 2 次,间隔 1s | 处理瞬时抖动 |
| 整轮硬超时 | 4 分钟 | 下次 cron 到来前必须结束 |
| 整轮失败处理 | 未完成的币本轮跳过,下轮 5min 后补 | 不堆积任务 |

---

## 9.6 时区铁律(Time Zone Discipline)

> ⚠️ 时区是交易系统最常见的 bug 来源。本节规则**不可商量**。

### 三层时区角色

| 层 | 时区 | 强制约束 |
|---|---|---|
| 币安 API 时间戳 | UTC ms | 币安固定,改不了 |
| **DB 存储** | **UTC**(`TIMESTAMPTZ`) | 工业标准 |
| **业务计算**(time.Time) | **UTC** | 全部用 `time.Now().UTC()`,严禁裸 `time.Now()` |
| **日志**(zerolog timestamp) | **UTC** | 与币安事件对齐,方便对账 |
| **Cron"日界"** | **BJT (UTC+8)** | "今天" = 北京时间 0 点开始 |
| **TG 告警渲染** | **BJT** | 用户体验 |
| **Dashboard 显示** | **BJT** | 用户体验 |
| **PnL 报表"今日"** | **BJT 0 点为分界** | 用户体验 |

### 实现细节

```go
// internal/pkg/timez/timez.go (新增包,封装时区操作)
var (
    UTC = time.UTC
    BJT *time.Location // = time.LoadLocation("Asia/Shanghai")
)

// 严禁 time.Now() 裸用, 一律用 NowUTC()
func NowUTC() time.Time { return time.Now().UTC() }

// "BJT 今天 0:00" 对应的 UTC 时刻
func TodayStartBJT(now time.Time) time.Time {
    bjt := now.In(BJT)
    return time.Date(bjt.Year(), bjt.Month(), bjt.Day(), 0, 0, 0, 0, BJT).UTC()
}

// 渲染给用户看的字符串(BJT)
func FormatBJT(t time.Time, layout string) string {
    return t.In(BJT).Format(layout)
}
```

### Cron 调度(robfig/cron/v3)

```go
import "github.com/robfig/cron/v3"

c := cron.New(cron.WithLocation(timez.BJT))
// "每天 BJT 0:00 重置 daily_pnl"
c.AddFunc("0 0 * * *", resetDailyPnL)
// "每天 BJT 8:00 发送日报到 TG"
c.AddFunc("0 8 * * *", sendDailyReport)
```

### Postgres 注意

```sql
-- 表全部用 TIMESTAMPTZ
CREATE TABLE foo (
  ts TIMESTAMPTZ NOT NULL  -- ✅
  -- 不要 TIMESTAMP, 那个不带时区会出错
);

-- 查询"今天"(BJT)
SELECT * FROM trades
WHERE entry_ts >= (CURRENT_DATE AT TIME ZONE 'Asia/Shanghai')
  AND entry_ts <  (CURRENT_DATE + 1 AT TIME ZONE 'Asia/Shanghai');

-- 但更推荐: 业务层算好 UTC 边界, 直接传参
```

### 容器时区

```dockerfile
# Dockerfile 必须设 TZ
ENV TZ=Asia/Shanghai
RUN apk add --no-cache tzdata
```

```yaml
# docker-compose.yml 各容器
environment:
  TZ: Asia/Shanghai
```

### 常见坑(Claude Code 必须警惕)

1. **`time.Now()` 不带时区**,等于本地时区,在不同 VPS 上行为不一致 → **永远用 `time.Now().UTC()` 或 timez.NowUTC()`**
2. **跨时区比较**:计算"持仓 24h"用 UTC 时间差,**不**用"BJT 今天和昨天"字符串
3. **JSON 序列化时间**:统一用 RFC3339(`2006-01-02T15:04:05Z`),含时区信息
4. **Cron 表达式**:`cron.WithLocation(BJT)` 必须显式指定,**默认是 server 时区**
5. **币安返回的时间戳**:都是 UTC ms,`time.UnixMilli(ts).UTC()` 转换,不要忘记 `.UTC()`

### CI 守卫

```yaml
# .github/workflows/ci.yml
- name: Forbid bare time.Now() in business code
  run: |
    if grep -RnE 'time\.Now\(\)' internal/ \
        --include='*.go' \
        | grep -v '_test.go' \
        | grep -v 'pkg/timez/'; then
      echo "::error::Bare time.Now() forbidden, use timez.NowUTC()"
      exit 1
    fi
```

---

## 10. Prometheus 指标

```
# 业务指标
trader_signals_total{symbol, decision}            counter
trader_trades_total{symbol, exit_reason}          counter
trader_trade_pnl_usdt{symbol}                     histogram
trader_open_positions                             gauge
trader_daily_pnl_usdt                             gauge
trader_circuit_breaker_active{reason}             gauge

# 数据采集
trader_collector_runs_total{name, status}         counter
trader_collector_duration_seconds{name}           histogram
trader_watchlist_size                             gauge

# 币安 API
binance_api_requests_total{endpoint, http_code}   counter
binance_api_request_duration_seconds{endpoint}    histogram
binance_api_weight_used                           gauge
binance_ws_connected                              gauge
binance_ws_events_total{event_type}               counter

# 代理(BINANCE_PROXY_MODE != none 时启用)
binance_proxy_requests_total{proxy_url, status}   counter
binance_proxy_failures_total{proxy_url, reason}   counter
binance_proxy_active_count                        gauge
binance_proxy_evicted_count                       gauge

# 系统
process_resident_memory_bytes                     (默认)
process_cpu_seconds_total                         (默认)
go_goroutines                                     (默认)
```

---

## 11. 部署拓扑(Docker Compose)

```
┌────────────────── VPS (1 台) ──────────────────┐
│                                                  │
│  caddy (443/80) ──→ trader (8080)               │
│                  ├→ dashboard (3000) (静态文件) │
│                  └→ grafana (3001)              │
│                                                  │
│  trader (Go)    ── 写 ─→ postgres-timescale     │
│                  ── 写 ─→ redis                 │
│                  ── 推 ─→ prometheus            │
│                  ── 推 ─→ loki (via promtail)   │
│                  ── 拉 ─→ binance.com (出网)    │
│                                                  │
│  prometheus      ── 拉 ─→ trader/metrics        │
│  grafana         ── 拉 ─→ prometheus + loki     │
│                                                  │
└──────────────────────────────────────────────────┘
```

每个服务都是独立容器,host network 仅 caddy 用。

---

## 11.5 运行模式与写请求 hard-block(Trader Mode)

```
TRADER_MODE=testnet      连币安 testnet, 全部接口可用
TRADER_MODE=mainnet      连主网真实下单, 必须配合 TRADER_MAINNET_CONFIRM=I_UNDERSTAND
```

**默认值**:`testnet`。**绝不**默认 mainnet。

### Hard-block 机制

`testnet` 模式下,数据采集走 testnet 拿数据 — 但本项目我们要的是真实 OI / Square 数据,
所以 `BinanceClient` 在 `testnet` 模式下:

- 读接口(GET):**走主网 production**,拿真实数据
- 写接口(POST/DELETE):**走 testnet**,可以测试下单流程
- listenKey 接口:走当前所连 testnet/mainnet 对应的环境

**关键约束**:

```go
// internal/binance/client.go
func (c *Client) doWrite(method, path string, ...) error {
    // 防意外:即使代码 bug 误调写接口,也只会打到 testnet
    if c.mode == "testnet" && c.restBaseWrite != TestnetRESTBaseURL {
        return errors.New("safety: testnet mode but write base url is not testnet")
    }
    // ...
}
```

切到 mainnet 时:

```go
func main() {
    if cfg.Mode == "mainnet" {
        if os.Getenv("TRADER_MAINNET_CONFIRM") != "I_UNDERSTAND" {
            log.Fatal("MAINNET mode requires TRADER_MAINNET_CONFIRM=I_UNDERSTAND")
        }
        log.Warn().Msg("⚠️ ============================================")
        log.Warn().Msg("⚠️  RUNNING IN MAINNET MODE — REAL MONEY")
        log.Warn().Msg("⚠️  Pre-flight checklist verified?")
        log.Warn().Msg("⚠️  TG halt/close_all tested in mainnet?")
        log.Warn().Msg("⚠️ ============================================")
        time.Sleep(5 * time.Second)  // 给操作员反应时间
    }
}
```

### CI 守卫

```yaml
- name: Forbid TRADER_MODE=mainnet in test files
  run: |
    if grep -RE 'TRADER_MODE\s*=\s*"?mainnet"?' \
        --include='*_test.go' --include='*.yml' --include='*.yaml' .; then
      echo "::error::TRADER_MODE=mainnet must NOT appear in test/CI files"
      exit 1
    fi
```

---

## 12. 安全

- 币安 API key:
  - 关闭提币
  - 仅启用合约交易
  - 绑定 VPS IP 白名单
  - 走环境变量,不进 yaml,不进 git
- TG bot:
  - 校验 `from.id == TG_CHAT_ID`,其它人发命令一律忽略 + 记录
- Dashboard:
  - 暴露端口仅本地,通过 caddy + basic auth 对外
- DB:
  - 不暴露端口到公网
  - 独立用户,最小权限
- 日志:
  - 含 API key / 签名字段必须脱敏

---

## 13. ADR(Architecture Decision Records)

### ADR-001:Go 而不是 TypeScript 或 Rust
- 静态二进制部署、协程模型契合并发采集、long-running 稳定
- TS 异步 stack 调试痛、Rust 上手慢

### ADR-002:模块化单体而不是微服务
- 单机交易系统体量小,微服务过度设计
- 模块化保留拆分余地,后期可拆

### ADR-003:进程内 channel 而不是 NATS
- 单体内 channel 性能比任何 MQ 高 1000 倍,延迟纳秒级
- 后期拆服务再加 MQ

### ADR-004:sqlc 而不是 GORM
- 量化系统大量复杂时序查询,GORM 写不优雅
- sqlc 让你写纯 SQL + 自动生成 Go 类型

### ADR-005:TimescaleDB 而不是 InfluxDB / QuestDB
- PG 兼容,一个库搞定全部
- hypertable 处理本项目体量(亿级行)足够

### ADR-006:不引入消息队列
- 单进程内 channel 即可
- 后期拆模块再加 MQ

### ADR-007:灾难止损用条件单不用本地兜底
- 程序挂掉也能止损
- 12-09 后切 Algo Service 接口,业务代码不变(抽象层吸收)

### ADR-008:不引入 DuckDB / 离线分析数据库
- v0.1 只跑实盘观察,不做正式回测
- 引入 DuckDB 需要 CGO,Dockerfile 需 build-base,部署复杂度上升
- 后期需要分析时,直连 PG 跑 SQL 即可,本项目数据量级不至于慢到不能用

### ADR-009:Proxy 在 BinanceClient 内部实现,不外包给 squid
- 业务代码不感知代理细节
- 支持 single / pool 两种模式,pool 内置健康检查和摘除恢复
- 减少 VPS 上额外服务依赖

### ADR-010:时区铁律 — UTC 存储,BJT 显示和日界
- 内部所有 time.Time、DB 所有 TIMESTAMPTZ 都 UTC
- 仅在 cron 表达式("daily reset @ BJT 0:00")、TG 告警渲染、Dashboard 展示用 BJT
- 跨时区比较(如"24h 持仓超时")永远用 UTC 时间差
- 详见 §「时区铁律」
