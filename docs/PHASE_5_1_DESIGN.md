# Phase 5.1 Design — Admin Web UI v1.0 (只读版)

**编写日期:** 2026-05-12 BJT  
**编写人:** Claude Code (claude-sonnet-4-6) + mu  
**基于:** Phase 4 v0.3 mainnet 上线后 mu Phase 5.1 入口决策

---

## mu Phase 5.1 入口决策备忘

| 决策项 | mu 选择 |
|--------|---------|
| 版本范围 | v1.0 只读版 (mu 90% 真实需求) |
| 启动时机 | 立即，与 trader 真盘并行 |
| 部署位置 | 同 VPS (43.133.173.17) |
| 是否影响真盘 | 不影响 (独立进程，read-only DB) |
| 预算 | ~25-35h Claude Code，1-2 周 wall-clock |

---

## §1 总览

### 1.1 v1.0 只读版范围

Phase 5.1 admin Web UI v1.0 是一个面向 mu 个人使用的只读管理界面，运行在与 trader 同一台 VPS 上，通过 PostgreSQL + Redis 的 SELECT-only 访问获取数据，**不修改任何 trader 代码，不影响真盘运行**。

v1.0 覆盖 mu 90% 真实监控需求：
- 实时账户状态（余额 / PnL / halt / 熔断）
- 当前持仓 + 历史仓位
- PnL 分析（走势 / symbol 排名 / 胜率）
- Square 热点浏览
- 候选池 OI 排序
- **开仓决策原因追溯（mu 最关键需求）**

### 1.2 工程独立性保障

```
trader-app (mainnet 真盘)          admin-api (只读, 独立进程)
    │                                   │
    ├── PostgreSQL ────── SELECT ────────┤
    ├── Redis      ────── GET ───────────┤
    └── Prometheus  (Grafana 继续用)    │
                                        ▼
                                   admin-web (nginx 静态)
                                        │
                                   浏览器 (mu)
```

**保障：**
- admin-api crash → trader 不受影响
- admin-api DB 连接异常 → trader 不受影响
- admin-api 部署 → 零停机（不重启 trader）
- admin-api 只用 SELECT，不持有写锁

### 1.3 整体规模

| 维度 | 估算 |
|------|------|
| 页面数 | 6 page + 1 dashboard 主页 |
| API endpoints | ~12 个 |
| 后端代码 | ~1000-1500 行 Go |
| 前端代码 | ~3000-5000 行 TypeScript/TSX |
| Claude Code 工时 | ~25-35h |
| Wall-clock | 1-2 周 |

---

## §2 6 + 1 Page 详细设计

### Page 0: Dashboard 主页（中国 trader 习惯）

**URL:** `/admin/`  
**刷新:** 5s 自动 polling

**布局：**

```
┌─────────────────────────────────────────────────────────────┐
│  🟢 NORMAL  |  余额: 999.80 USDT  |  今日 PnL: +1.20 USDT  │  ← 顶栏大字
├───────────┬───────────┬──────────┬──────────┬──────────────┤
│  账户余额  │  今日PnL  │ 持仓数量 │ 连败次数 │  BTC 30m跌幅 │  ← 5 指标卡片
│  999.80U  │  +1.20U  │   2个    │   0次    │   0.03%      │
├───────────┴───────────┴──────────┴──────────┴──────────────┤
│  当前持仓快览 (当前持仓的 symbol + unrealized PnL 摘要)       │  ← 持仓缩略
├─────────────────────────────────────────────────────────────┤
│  12 collectors 状态 (最后 tick 时间 + 最近 5min 成功率)       │  ← 系统健康
└─────────────────────────────────────────────────────────────┘
```

**关键指标卡片（5-6 个）：**
- 账户余额 USDT（大字号）
- 今日 PnL（红跌绿涨，中国习惯）
- 当前持仓数
- 连续亏损次数
- BTC 30min 跌幅
- halt 状态（正常 / 已触发熔断 + 原因）

**颜色规范（中国习惯，与 Grafana 默认相反）：**
- 上涨 / 正 PnL → **红色** (`#f04864`)
- 下跌 / 负 PnL → **绿色** (`#30bf78`)
- 正常状态 → `#52c41a`
- 告警 → `#faad14`
- 熔断 → `#ff4d4f`

---

### Page 1: 当前持仓

**URL:** `/admin/positions`  
**刷新:** 5s polling  
**数据源:** `trades` WHERE status='open' + Binance positionRisk (实时价格)

**表格列：**

| 列 | 说明 |
|----|------|
| Symbol | BTCUSDT 等，点击跳 Page 6 |
| 方向 | LONG (v1.0 只做多) |
| 开仓时间 | BJT 格式，如 05-12 13:45 |
| 开仓价 | entry_price USDT |
| 当前价 | 实时（5s 刷新）|
| 持仓时长 | 如 "2h 15m" |
| Unrealized PnL | 金额 + 百分比，红跌绿涨 |
| Margin Ratio | 爆仓预警，超 80% 标红 |
| 操作 | → 查看详情（跳 Page 6）|

**行点击 → Page 6（开仓决策原因 detail）**

**空状态：** "当前无持仓" + 过去 24h 最近 3 笔历史摘要

---

### Page 2: 历史仓位（mu 关键需求）

**URL:** `/admin/history`  
**数据源:** `trades` WHERE status IN ('closed', 'failed')

**表格列：**

| 列 | 说明 |
|----|------|
| Symbol | 点击跳 Page 6 |
| 方向 | LONG |
| 开仓时间 | BJT |
| 平仓时间 | BJT |
| 持仓时长 | "2h 15m" |
| 开仓价 | entry_price |
| 平仓价 | exit_price |
| 数量 | notional / entry_price |
| Realized PnL | 金额 + 百分比，红跌绿涨 |
| 平仓原因 | Chip 标签（见下） |
| Fees | fees USDT |

**平仓原因 Chip（颜色区分）：**
- `disaster` → 红底（止损触发）
- `soft_timeout` → 橙底（24h 超时）
- `hard_timeout` → 深橙底（72h 强制）
- `margin_call` → 红闪（爆仓边界）
- `manual` → 灰底

**排序：** 平仓时间倒序（最新在上）

**过滤器：**
- Symbol（多选下拉）
- 平仓原因（多选 chip）
- 时间范围（今日 / 本周 / 本月 / 自定义）
- PnL 方向（全部 / 盈利 / 亏损）
- 持仓时长（< 1h / 1-24h / > 24h）

**分页：** 20 / 50 行每页可选

**行点击 → Page 6**

---

### Page 3: PnL 分析

**URL:** `/admin/pnl`

**时间维度切换（顶部 Tab）：** 今日 / 本周 / 本月 / 累计

**Tab 1: 累计 PnL 曲线**
- X轴：时间（每笔平仓时刻）
- Y轴：累计 realized PnL（USDT）
- 折线图（红色上行，绿色下行）
- 标注重大 PnL 节点（最大单笔盈利 / 亏损）
- 叠加账户余额走势（浅色参考线）

**Tab 2: Symbol PnL 排名**
- 横向条形图：symbol 按 realized PnL 排序
- Top 10 盈利 symbol（红色）/ Top 10 亏损 symbol（绿色）
- 显示笔数 + 总 PnL + 平均 PnL/笔

**Tab 3: 平仓原因 PnL 分布**
- 饼图 + 列表
- 各平仓原因对应的笔数 + 总 PnL + 平均 PnL

**Tab 4: 胜率统计**

| 指标 | 计算方式 |
|------|---------|
| 胜率 | 盈利笔数 / 总笔数 |
| 平均盈利 | 盈利笔 realized_pnl 均值 |
| 平均亏损 | 亏损笔 realized_pnl 均值 |
| 盈亏比 | \|平均盈利\| / \|平均亏损\| |
| 最大回撤 | 账户余额历史最高 → 最低 |
| 总 fees | 累计手续费 |

---

### Page 4: Square 热点

**URL:** `/admin/square`  
**数据源:** `square_hashtags` + `square_feeds`（Phase 1 collector 写入）

**主体：热点 hashtag 排行**

| 列 | 说明 |
|----|------|
| Hashtag | #bitcoin 等 |
| 提及数 | 最新 count |
| 24h 增长率 | 红色上涨 / 绿色下跌 |
| 相关 Symbols | 自动关联的合约 symbol |
| 最新帖子时间 | BJT |

**排序切换：** 增长率 / 绝对热度 / 最新时间

**点击 hashtag →** 右侧展开：
- 关联 symbols 列表（可跳 Page 5）
- 最近 10 条相关 square 帖子摘要（时间 + 内容前 100 字）

**市场情绪指示器：** 基于 top hashtag 与 trader watchlist 的重叠度，显示"高关注 / 普通 / 低关注"

---

### Page 5: 候选池 + OI 排序

**URL:** `/admin/watchlist`  
**数据源:** `open_interests`（最新 5min bar）+ `klines`（最新价格）+ `square_hashtags`

**主体：候选池 symbol 列表**

| 列 | 说明 |
|----|------|
| Symbol | BTCUSDT 等 |
| OI ($M) | 最新 open interest（百万 USDT）|
| OI 1h 增长率 | 相对 1h 前 OI 变化率 |
| OI 24h 增长率 | 相对 24h 前 OI 变化率 |
| 当前价 | 最新 kline close |
| 24h 涨跌幅 | 价格变化 % |
| Square 热度 | hashtag 关联数量（🔥 图标）|
| 在持仓 | 当前是否在 trades 表中 open |

**排序切换：** OI 1h 增长率 / OI 24h 增长率 / OI 绝对值 / 价格涨跌幅

**搜索：** symbol 关键字过滤

**行点击 → Symbol Detail 侧边栏：**
- OI 走势图（最近 6h，5min bar）
- 价格走势图（最近 6h）
- Square 相关帖子时序列表
- 是否进入过 trades（历史）

---

### Page 6: 开仓决策原因（mu 最关键需求）

**URL:** `/admin/trade/:trade_id`  
**入口：** Page 1 / Page 2 行点击，或直接 URL

**Section A: 信号触发时刻**

```
信号 #1234  |  BTCUSDT  |  2026-05-12 13:45:02 BJT
─────────────────────────────────────────────────
OI 数据
  最新 OI:      $892.3M   (+12.4% vs 1h 前)
  OI 阈值:      +8%       ← 已超阈值 ✓
  OI 触发:      是

Square 数据
  热点 hashtag: #bitcoin (+340%), #crypto (+180%)
  相关 mentions: 47 条帖子 / 15min

决策结果:    entered_full (全仓入场)
```

**Section B: decision_engine 评估链**

```
Step 1 BTC Panic Check      ✓ PASS  (BTC 30min 跌幅 0.03% < 3%)
Step 2 Market Filter        ✓ PASS  (drop_pct OK, regime normal)
Step 3 Recent Trade Check   ✓ PASS  (距上次 BTCUSDT > 24h)
Step 4 Position Limit       ✓ PASS  (当前持仓 1 < 5 上限)
Step 5 Sizing               → full  (OI 涨幅 > 10%, 全仓)

sizing: margin=50U, notional=500U, leverage=10x
```

**Section C: executor 执行路径**

```
13:45:02.001  SetMarginType(ISOLATED)     ✓ 200ms
13:45:02.201  SetLeverage(10)             ✓ 150ms
13:45:02.351  PlaceMarketOrder            ✓ 280ms
              BUY BTCUSDT 0.006 @ 83,500
              orderId: 10000072454567
13:45:02.631  PlaceAlgoConditionalStop   ✓ 190ms
              STOP_MARKET @ 78,490 (entry × 0.94)
              algoId: 1000000072454568
13:45:02.820  trade #53 状态 → open ✓
```

**Section D: 持仓期间（动态，若 open 中）**

```
margin_ratio 走势图（时序折线，最近更新时刻标注）
unrealized PnL 走势图
持仓时长: 2h 15m
最高价: 84,200 USDT（entry +0.84%）
```

**Section E: 平仓记录（若已平仓）**

```
平仓时间:   2026-05-12 17:23:44 BJT
平仓原因:   soft_timeout (持仓 > 24h)
平仓价:     83,200 USDT
Realized PnL: -$18.00 (-3.6%)
Fees:        $0.42
```

---

## §3 技术架构

### 3.1 前端技术栈

| 技术 | 选型理由 |
|------|---------|
| React 18 + Vite | 开发体验好，build 快，HMR |
| TypeScript | 类型安全，减少 runtime 错误 |
| Tailwind CSS | 快速 UI 开发，暗色主题易配 |
| Recharts | React 原生图表库，OI/PnL 走势 |
| React Router v6 | 6+1 page SPA 路由 |
| TanStack Query | API polling + 缓存 + 5s 刷新 |
| dayjs | 时间格式化（BJT 显示）|

**目录结构：**
```
web/admin/
  src/
    pages/
      Dashboard.tsx
      Positions.tsx
      History.tsx
      PnlAnalysis.tsx
      Square.tsx
      Watchlist.tsx
      TradeDetail.tsx
    components/
      MetricCard.tsx
      PositionTable.tsx
      PnlChart.tsx
      CollectorStatus.tsx
    api/
      client.ts        ← axios, base /api/admin/
    hooks/
      usePolling.ts    ← 5s polling wrapper
    theme/
      colors.ts        ← 中国红涨绿跌配色
  index.html
  vite.config.ts
  tailwind.config.js
```

**部署产物：** `web/admin/dist/` → nginx 静态托管

### 3.2 后端技术栈

| 技术 | 说明 |
|------|------|
| Go 1.25+ | 与 trader 同语言，共享 DB schema |
| pgx/v5 | PostgreSQL read-only 连接 |
| go-redis/v9 | Redis GET（余额 / halt 状态缓存）|
| net/http | 标准库 HTTP，轻量 REST |
| zerolog | 与 trader 同日志格式 |
| sqlc | 与 trader 共享 gen/ 查询（SELECT only）|

**目录结构：**
```
cmd/admin-api/
  main.go           ← HTTP server + 路由注册
internal/admin/
  handler/
    dashboard.go
    positions.go
    history.go
    pnl.go
    square.go
    watchlist.go
    trade_detail.go
  query/
    positions.sql   ← SELECT only
    pnl.sql
    square.sql
    watchlist.sql
  server.go         ← Server struct + middleware
```

**关键设计约束：**
- admin-api 使用独立 DB 连接，max_conns=5（trader 用 max_conns=10）
- 所有查询加 `SET default_transaction_read_only = on`
- Redis 只用 GET，不 SET/DEL

### 3.3 REST API Endpoints（12 个）

```
GET /api/admin/health
    → {"status":"ok","db":"ok","redis":"ok","uptime":"2h15m"}

GET /api/admin/dashboard
    → 余额 + 今日PnL + halt + 持仓数 + 连败 + BTC跌幅 + collector最后tick

GET /api/admin/positions/open
    → [{trade_id, symbol, entry_ts, entry_price, current_price,
        unrealized_pnl, margin_ratio, hold_duration_ms}]

GET /api/admin/positions/history?page=1&size=20&symbol=&exit_reason=&
       from=&to=&pnl_sign=&hold_duration=
    → {total, page, items:[{...}]}

GET /api/admin/pnl/cumulative?period=week
    → [{ts, cumulative_pnl, balance}]

GET /api/admin/pnl/by_symbol?period=month
    → [{symbol, count, total_pnl, avg_pnl, win_rate}]

GET /api/admin/pnl/by_exit_reason?period=all
    → [{exit_reason, count, total_pnl, avg_pnl}]

GET /api/admin/pnl/stats?period=month
    → {win_rate, avg_win, avg_loss, payoff_ratio, max_drawdown, total_fees}

GET /api/admin/square/trending
    → [{hashtag, count, growth_24h, related_symbols, latest_ts}]

GET /api/admin/watchlist?sort=oi_1h_pct&search=
    → [{symbol, oi_usd, oi_1h_pct, oi_24h_pct, price, price_24h_pct,
        square_mentions, in_open_position}]

GET /api/admin/symbol/:symbol
    → {symbol, oi_series:[{ts,oi}], price_series:[{ts,price}],
        square_posts:[{ts,content}], trade_history:[...]}

GET /api/admin/trade/:trade_id
    → {
        signal: {signal_id, ts, symbol, oi_data, square_data, decision},
        engine_steps: [{step, result, detail}],
        executor: [{ts, action, result, latency_ms}],
        position: {margin_ratio_series, pnl_series, hold_ms, highest_price},
        exit: {ts, reason, price, realized_pnl, fees}  // nullable
      }
```

### 3.4 部署架构

**当前 VPS 容器（不变）：**
```
trader-app        :8080 (health) + :2112 (metrics)
trader-postgres   :5432
trader-redis      :6379
trader-prometheus :9090
trader-grafana    :3001
trader-loki       :3100
```

**新增容器：**
```
trader-admin-api  :3002   ← Go admin-api 进程
trader-admin-web  内嵌 nginx ← 静态文件托管
```

**Caddy 新增路由（追加到 Caddyfile）：**
```
{$DOMAIN:-localhost} {
    # 现有路由 (不变)
    ...

    # 新增: admin API — basicauth 保护
    handle /api/admin/* {
        basicauth {
            mu <hashed_password>   # caddy hash-password 生成, mu 本机操作, 不入 git
        }
        reverse_proxy localhost:3002
    }

    # 新增: admin 前端静态 — basicauth 保护
    handle /admin/* {
        basicauth {
            mu <hashed_password>
        }
        reverse_proxy localhost:3003
    }
}
```

**Caddy basicauth 部署流程（Round 7 mu 操作，password 不入 git）：**
```bash
# mu 本机生成 hashed password:
docker run --rm caddy:latest caddy hash-password
# 输入 mu 想要的 password → 输出 $2a$14$xyz... 形式 hash
# mu 手工编辑 VPS /home/ubuntu/trader/deploy/Caddyfile
# 替换 <hashed_password> 为实际 hash
# 触发 Caddy reload:
docker exec trader-caddy caddy reload --config /etc/caddy/Caddyfile
```

跟 Grafana admin password 同纪律：mu 个人保管，不通过任何 chat 中转。

**访问 URL：** `https://trader.letsagent.net/admin`

**docker-compose.prod.yml 新增（追加）：**
```yaml
admin-api:
  image: trader-admin-api:latest
  container_name: trader-admin-api
  restart: unless-stopped
  environment:
    - DATABASE_URL=${DATABASE_URL}
    - REDIS_URL=${REDIS_URL}
  networks:
    - default
  ports:
    - "127.0.0.1:3002:3002"

admin-web:
  image: trader-admin-web:latest
  container_name: trader-admin-web
  restart: unless-stopped
  networks:
    - default
  ports:
    - "127.0.0.1:3003:80"
```

---

## §4 开发阶段

### Round 拆分 v2（8 Rounds，~30-40h）— vertical slice 模式

> **修订说明 (2026-05-12):** 原 Round 1「全部 12 endpoints」设计改为 vertical slice 模式。
> 每 Round 前后端配对实施，Round 结束时该 slice 100% real（不留 stub 欠债）。
> 工程纪律参照 Phase 4 v0.1：每 Round 报告必须 explicit 标 FULL / PARTIAL / STUB / OUT-OF-SCOPE。

#### Round 完成度标注规范

| 标记 | 含义 |
|------|------|
| ✅ FULL | 真实施 + 真测，无 placeholder |
| ⚠️ PARTIAL | 部分实施，剩余需后续 Round 补 |
| ⚪ STUB | 函数签名存在，返 placeholder JSON，待后续 Round 实施 |
| ❌ OUT-OF-SCOPE | 有意推后，已明确说明 |

#### Round 清单

| Round | 内容 | Endpoints 实施清单 | 预估工时 | 关键产出 |
|-------|------|--------------------|---------|---------|
| Round 0 | 设计文档 | — | ~2h | PHASE_5_1_DESIGN.md |
| Round 1 ✅ | framework + health + dashboard 基础 | ✅ health, ✅ dashboard (基础)<br>⚪ 其余 10/12 STUB | ~2-3h | `cmd/admin-api/` 可编译运行 |
| Round 2 | Dashboard 前端 + dashboard endpoint 完善 | ✅ dashboard (加 collector status) | ~3-4h | Page 0 100% real |
| Round 3 | Page 1 当前持仓 前端 + endpoint 实施 | ✅ positions/open | ~3-4h | Page 1 100% real |
| Round 4 | Page 2 历史仓位 + Page 3 PnL 分析 | ✅ positions/history<br>✅ pnl/cumulative<br>✅ pnl/by_symbol<br>✅ pnl/by_exit_reason<br>✅ pnl/stats | ~6-7h | Page 2+3 100% real |
| Round 5 | Page 4 Square + Page 5 候选池 | ✅ square/trending<br>✅ watchlist<br>✅ symbol/:symbol | ~5-6h | Page 4+5 100% real |
| Round 6 | Page 6 开仓决策原因（完整 Section A-E）| ✅ trade/:trade_id | ~4-6h | Page 6 100% real，12/12 all FULL |
| Round 7 | Caddy basicauth + Docker + VPS 部署 | — | ~3-4h | https://trader.letsagent.net/admin |
| Round 8 | Acceptance + commit + tag | — | ~1-2h | `phase-5.1-admin-web-v1.0` |

**总计：** ~30-40h Claude Code，1-2 周 wall-clock（总量不变，slice 模式更可验证）

#### Round 1 完成度记录（已完成）

| 产出 | 完成度 | 说明 |
|------|--------|------|
| `cmd/admin-api/main.go` | ✅ FULL | DB read-only pool, Redis, graceful shutdown |
| `internal/admin/server.go` | ✅ FULL | 12 路由注册 + CORS + helpers |
| `GET /api/admin/health` | ✅ FULL | DB + Redis ping，VPS curl 真测通过 |
| `GET /api/admin/dashboard` | ⚠️ PARTIAL | circuit_breaker_state + trade count + Prom metrics；collector status 待 Round 2 补 |
| 其余 10/12 endpoints | ⚪ STUB | 返回正确 JSON 结构（空数组/空对象），待 Round 2-6 逐步实施 |

### Round 内纪律（每 Round）

1. 写代码前描述思路，等 mu 确认
2. 完成后 mu review（贴关键 diff）
3. VPS 真测（curl API / 浏览器截图）
4. commit（每 Round 一个 commit）
5. 报告按完成度标注规范 explicit 标每项（不接受「全部 OK」笼统声明）
6. 等 mu 明确同意后进入下一 Round

---

## §5 工程纪律

### 5.1 核心约束

```
admin Web UI 不修改 trader 代码        ← 铁律
admin-api 只 SELECT，不 INSERT/UPDATE  ← 铁律
admin-api crash 不影响 trader          ← 铁律
DB connection 独立（不竞争 trader 连接）← 铁律
```

### 5.2 数据库 read-only 保障

每个 DB 连接初始化时执行：
```sql
SET default_transaction_read_only = on;
```

所有 SQL 文件后缀 `.admin.sql`（与 trader 的 `.sql` 区分），sqlc 单独 config。

### 5.3 文件变更预算

跟 Phase 4 同纪律：
- 每次响应最多 3 个文件
- 单文件单次改动不超过 200 行
- 超出预算 → 拆多次

### 5.4 中国 trader 审美规范

| 项目 | 规范 |
|------|------|
| 颜色 | 红涨（`#f04864`）绿跌（`#30bf78`），与 Grafana 默认相反 |
| 字号 | Dashboard 余额 32px，PnL 28px，指标值 24px |
| 主题 | 暗色（`#141414` 背景，`#1f1f1f` 卡片）|
| 时区 | 全部显示 BJT（UTC+8），后端存 UTC 前端转换 |
| 数字格式 | 余额保留 2 位小数，PnL 带 + / - 前缀 |
| 表格 | 币安持仓表格风格（紧凑，行高 40px）|

### 5.5 安全（v1.0 决策）

- v1.0 **Caddy basicauth**（mu 决策 B，工程纪律高水位）
- v1.0 不做 HTTPS 证书新申请（复用 trader.letsagent.net 现有证书）
- v1.0 不做 rate limiting（内网访问）
- v1.1 再考虑 auth + audit log

---

## §6 mu 决策点

### Round 0 设计阶段决策（mu 本次 review）

| 决策点 | 选项 | mu 选择 |
|--------|------|---------|
| Dashboard 主要指标卡片 | 5 个（余额/PnL/持仓数/连败/BTC跌幅）| ✅ 接受，顶栏 halt 状态 |
| 前端 UI 框架 | React + Tailwind | ✅ 确认 |
| admin API 端口 | :3002（VPS 已验证空闲）| ✅ 确认 |
| auth v1.0 | **Caddy basicauth**（mu 决策 B）| ✅ 确认 |
| Page 6 trace 数据粒度 | executor 每步 ts + action（完整 9 步）| ✅ 确认 |
| Page 6 margin_ratio 时序图 | 需要（读 position_states 历史）| ✅ 确认 |
| Caddy 路由方式 | 追加到现有 Caddyfile | ✅ 确认 |

### Round 1-8 开发期决策点

- Round 1: SQL 查询设计（mu 看是否覆盖需求）
- Round 2: Dashboard 颜色 / 字号（mu 给 reference 截图最好）
- Round 4: 历史仓位分页大小（20 / 50）
- Round 7: Caddy 路由方式（追加 vs 独立 subdomain）

---

## §7 未来扩展

### Phase 5.2 — admin Web UI v1.1（写操作版）

**估算：** ~15-20h Claude Code，1 周 wall-clock

**新增功能：**
- halt / resume 操作（带确认弹窗 + audit log）
- RCA ack（确认已知道熔断原因）
- 手工平仓（指定 trade_id → executor POST）
- 阈值热更新（adjustable config via Redis）
- auth（JWT / basic auth）
- 所有写操作记入 audit_log 表

### Phase 5.3 — 飞书告警

**估算：** ~10-15h Claude Code，独立部署

**功能：**
- 5 项熔断 trip → 飞书消息推送
- RCA pending 通知 → 飞书 react ack
- 日报（BJT 00:00）：今日 PnL / 持仓摘要 / collector 健康
- 飞书 bot 命令：`/status` / `/halt` / `/resume`

### Phase 5.4 — 移动端适配

**估算：** ~8-12h

- Admin Web UI 移动端响应式
- 关键指标 PWA（可添加到手机主屏）

---

*文档版本: Phase 5.1 Round 0 (设计) + Round 1 完成度记录 — 2026-05-12 BJT*  
*下次更新: Round 8 Acceptance 时补充实际 vs 设计对照表*
