# Phase 5.2 Admin Web UI v1.1 — 公开读 + admin 写 设计文档

**编写日期:** 2026-05-13 BJT
**编写人:** Claude Code (claude-sonnet-4-6) + mu
**基于:** Phase 5.1 v1.0 acceptance + Round R.1/R.2/3.y 部署 + mu 真盘 forward catch

---

## mu Phase 5.2 入口决策备忘

| 决策项 | 选项 | mu 选择 |
|---|---|---|
| 实施时机 | 并行/顺序/暂停 | **BC 顺序**（Phase 5.2 设计 → Round 4 WS）|
| 真盘评估 | 并行/暂停 | **A 并行**（forward 自动跑） |
| 读权限 | 维持 basic auth / 完全公开 / 用户分级 | **A1 完全公开**（无 auth） |
| 写权限 | 维持 basic auth / 单独 admin / 多 admin | **B1 单独 admin auth** |
| 飞书告警 | Phase 5.2 内 / Phase 5.3 拆分 | 留 mu 决策时机（§4） |

---

## §1 总览

### 1.1 Phase 5.1 现状回顾

```
v1.0 read-only admin Web UI:
├── tag                  phase-5.1-admin-web-v1.0 (commit efa537b)
├── 实施周期             Round 0-8 (~3 周)
├── 部署                 https://trader.letsagent.net/admin
├── auth                 Caddy basic auth (全部 endpoint 都需 auth)
├── DB 隔离              admin-api 独立连接池 + read_only=on (max_conns=5)
├── 7 page
│   ├── Dashboard        账户余额 + 5 项熔断 + collector 状态
│   ├── 当前持仓 Open    open trades + Algo 状态
│   ├── 历史仓位 History 所有 closed trades + 胜率 + PnL
│   ├── PnL 分析         累计曲线 + by_symbol + by_exit_reason + stats
│   ├── Square 热点      Binance Square 24h mention top
│   ├── 市场扫描 Market  全市场 OI + 价格 + Square + watchlist 标记
│   └── Trade detail     单笔决策链 (signal → decision → execution)
└── 12 GET endpoints
```

### 1.2 Phase 5.1.x + Round R.1/R.2/3.y 已实施（v1.1 范围 sub-features）

```
Phase 5.1.x 已实施 (10 commits, ~2 weeks):
├── Dashboard collector 中文名 + 状态色彩
├── 当前持仓价值 (current value vs entry)
├── Square 数据源 + 列头排序
├── Square 24h 增长率 + 趋势图侧边栏
├── OI/价格 24h 侧边栏
├── Trade detail 全中文 + 整数显示
├── Trade detail 两列布局 (A 左 / B+C+D 右)
└── 绿涨红跌色彩惯例 (币圈)

3 bug fix:
├── 出场 fees 补偿 (commit 46ada16, real commission from GetUserTrades)
├── trade_exits 重复行 dedup (ON CONFLICT idempotent)
└── consecutive_losses 双倍累计 (idempotent skip)

Round R.1/R.2/3.y 已实施 (admin v1.1 first write op + bug fixes):
├── manual halt reset 按钮 (POST /api/admin/circuit-breaker/reset)
├── circuit_breaker_events audit log 表 (migration 0013)
├── R.2 fix: 完整 5 项 reset (daily_pnl + consec + last_btc_crash_ts + last_loss_at)
└── R.1 后 sizing leverage bug fix (cfg.Position.Leverage 传 sizing)
```

### 1.3 Phase 5.2 范围

```
Phase 5.2 admin v1.1 (~40-58h, ~6 周):
├── §2 权限分级 (重大改动: A1 公开读 + B1 单独 admin 写)
├── §3 写操作完整 (daily_pnl reset / 手工平仓 / 调阈值 / watchlist 管理)
├── §4 飞书告警 (mu 决策 Phase 5.2 内 vs 5.3 拆)
├── §5 UX 剩余 (Square 情绪分析 + Page 6 Section B 5 step + 5 项熔断曲线)
├── §6 移动端响应式 (mu 真盘 owner 手机端)
├── §7 audit log + admin 用户管理 (B1 单 admin)
└── §8 Round 0-7 拆分

工时估算: ~40-58h Claude Code, ~3-5 周 wall-clock
不在范围:
  · ❌ 多 admin / RBAC / OAuth (Phase 5.3+ if needed)
  · ❌ 复杂监控 (Grafana 已有, admin Web UI 不重复)
  · ❌ trader 内核改动 (Phase 5.2 仅 admin Web UI)
```

### 1.4 跟其他 Phase 关系

| Phase | 范围 | 依赖 | 状态 |
|---|---|---|---|
| Phase 5.1 v1.0 | 公网 read-only admin | trader v0.1 | ✅ tag |
| Phase 5.1.x | UX 改进 + bug fix | 5.1 | ✅ 在 main |
| **Phase 5.2 v1.1** | **公开读 + admin 写** | 5.1.x | **规划中** |
| Round R.1/R.2/3.y | trader bug fixes + 第一个写操作 | trader v0.2 | ✅ 部署 |
| v0.2 Round 4 WS | trader 实时 WS (跟 Phase 5.2 并行) | v0.2 | 待启动 |
| trader v1.0 | production-ready | 全部 | ~6 月底 |

---

## §2 权限分级（mu A1 + B1 决策，本设计核心）

### 2.1 权限模型

```
┌─────────────────────────────────────────────────────────────┐
│  Public (无 auth)                                            │
│  ├── /admin SPA              (前端静态文件)                  │
│  ├── GET /api/admin/*        (所有读 endpoints)              │
│  └── 任何人通过 URL 直接访问                                  │
├─────────────────────────────────────────────────────────────┤
│  Admin (basic auth: admin_user + admin_password)            │
│  ├── POST   /api/admin/*     (所有写 endpoints)              │
│  ├── PUT    /api/admin/*                                     │
│  ├── DELETE /api/admin/*                                     │
│  └── CSRF token required (per session)                       │
└─────────────────────────────────────────────────────────────┘
```

### 2.2 公开读的工程含义（A1 mu informed）

**公开范围**:
- 账户余额 (Binance 总资产)
- 持仓详情 (symbol / qty / entry / current PnL / algo IDs)
- 历史交易 (所有 mainnet trades + 真实 P&L 曲线)
- 信号决策链 (OI growth / Square ratio / square_hot / decision_outcome)
- 市场扫描 (全市场 OI + 价格 + Square mention)
- 熔断状态 (halt_reason / daily_pnl / consec_losses)
- audit log (mu 操作历史，公开)

**隐藏范围**（即使公开读也不暴露）:
- ❌ Binance API key / secret (一直在 .env，不通过 admin Web UI 透出)
- ❌ Proxy 配置 (BINANCE_PROXY_URL 等)
- ❌ 数据库密码、Redis 密码
- ❌ TG_BOT_TOKEN / 飞书 webhook URL
- ❌ admin_password hash

**mu 隐私接受度**:
- ✅ 真盘策略公开（OI growth / Square 阈值）— 别人复制可能赚也可能亏
- ✅ 真实 P&L 公开（mu 真盘亏损 -129.79 USDT 历史可见）— 透明度 > 隐私
- ✅ 持仓策略公开（leverage / margin / trail callback）— 教育价值
- ⚠️ 实时 entry 推送公开（飞书也涉及）— Phase 5.2 飞书内容跟 admin Web UI 一致

**为什么不分级用户**（mu 拒绝复杂权限）:
- mu 是唯一 admin
- 真盘是 mu 一个人的资金
- 多用户/RBAC 是 over-engineering
- 复杂权限会拖慢迭代

### 2.3 写权限实施（B1 单 admin auth）

**admin 用户**:
- 单一 admin user (e.g. `mu`)
- bcrypt hashed password 存 `.env`：
  ```
  ADMIN_USER=mu
  ADMIN_PASSWORD_BCRYPT=$2a$10$...
  ```
- 与 Phase 5.1 Caddy basic auth 复用同一对（迁移成本零）

**Auth 实施层次**:
```
┌─ 浏览器                                                    
│   └─ /admin SPA (公开)                                     
│        ├─ GET /api/admin/* (公开)                          
│        └─ POST /api/admin/* (调用前调 admin login modal)   
│                                                            
├─ Caddy reverse proxy                                       
│   ├─ /admin/* → static (public)                           
│   ├─ GET /api/admin/* → admin-api (no auth)               
│   └─ POST /api/admin/* → require_basic_auth + admin-api    
│                                                            
└─ admin-api (Go)                                            
    └─ 中间件: 检查 method                                   
        ├─ GET → 跳过 auth                                   
        └─ POST/PUT/DELETE → 验证 Authorization header       
```

**Caddy 配置变更**:
```caddyfile
trader.letsagent.net {
    # 公开 - SPA 静态文件
    handle_path /admin/* {
        root * /var/www/admin-web-dist
        try_files {path} /index.html
        file_server
    }
    
    # 公开 - GET endpoints
    @read_admin {
        method GET
        path /api/admin/*
    }
    handle @read_admin {
        reverse_proxy admin-api:3002
    }
    
    # 受保护 - 写 endpoints
    @write_admin {
        method POST PUT DELETE PATCH
        path /api/admin/*
    }
    handle @write_admin {
        basicauth {
            mu $2a$10$...
        }
        reverse_proxy admin-api:3002
    }
}
```

**CSRF token 防御**:
- 写操作前先 `GET /api/admin/csrf-token` 拿 token
- POST/PUT/DELETE 必带 `X-CSRF-Token` header
- token 与 admin session 绑定（30min 过期）
- 缺/错 token → 403

**前端流程**:
```
用户点击 "🚨 手工平仓"
  ↓
检查 sessionStorage 是否有 admin token
  ├── 有：直接调 API
  └── 无：弹出 admin login modal
        ├── user/pass input
        ├── POST /api/admin/login (单独 endpoint)
        └── 返回 csrf_token + session 标记
              ↓
              发起带 X-CSRF-Token 的写请求
              ↓
              成功：执行操作 + audit log + close modal
              失败：显示错误
```

### 2.4 Round R.1 + R.2 兼容（已实施 endpoint 自动 fit）

| Endpoint | Phase 5.1 状态 | Phase 5.2 目标 | 改动 |
|---|---|---|---|
| `POST /api/admin/circuit-breaker/reset` | Caddy basic auth | Caddy POST → basic auth | 无（已 fit）|
| `GET /api/admin/circuit-breaker/events` | Caddy basic auth | Caddy GET → 公开 | 移除 auth（公开 audit log）|
| 所有现有 `GET /api/admin/*` | Caddy basic auth | Caddy GET → 公开 | 移除 auth |
| 新写 endpoints (§3) | — | Caddy POST → basic auth | 新加 |

Round R.1/R.2 已实施 `POST /api/admin/circuit-breaker/reset` + writeDB 双 pool + circuit_breaker_events audit。Phase 5.2 Round 2 复用同一架构添加更多写 endpoint。

---

## §3 写操作完整范围

### 3.1 已实施（Round R.1/R.2，跟 v1.1 衔接）

```
✅ POST /api/admin/circuit-breaker/reset
   · 完整 5 项 reset (halt_flag + halt_until + daily_pnl + consec_losses + last_btc_crash_ts + last_loss_at)
   · body: { confirm: true, actor: 'mu', note: 'reason' }
   · 写 circuit_breaker_events (event_type='manual_full_reset')
```

### 3.2 Phase 5.2 新增写操作（按优先级）

#### a. daily_pnl reset（fine control，跟 halt reset 拆分）

```
POST /api/admin/circuit-breaker/daily-pnl-reset
body: { confirm: true, note: 'BJT 新一天开始 fresh start' }
behavior:
  · 仅 UPDATE daily_pnl=0 + daily_pnl_date=今日 BJT
  · 不动 trading_halted / halt_until / consec_losses
  · 写 admin_audit_log
use case:
  · mu 早上重新 baseline，daily_pnl=-50 但不想 halt
```

#### b. consecutive_losses reset

```
POST /api/admin/circuit-breaker/consec-reset
body: { confirm: true, note: '冷静后重启' }
behavior:
  · 仅 UPDATE consecutive_losses=0
  · 不动其他
  · 写 audit
use case:
  · 连亏 6 笔（halt 阈值 8），mu 决定"重新计数"
```

#### c. 手工平仓 ⭐（mu 真盘 owner 紧急工具）

```
POST /api/admin/trades/:id/close
body: { confirm: true, reason: 'manual' | 'rca', note: 'mu RCA 决定' }
behavior:
  1. 验证 trade.status='open'
  2. cancel 所有 binance algos (disaster + trail + tp1 + tp2)
  3. place MARKET SELL reduceOnly=true qty=current_qty
  4. waitFill (10s)
  5. persistClose (exit_reason='manual', InsertTradeExit + UpdateTradeClosed)
  6. circuit_breaker_state 不动（手工平仓不算 trip）
  7. 写 admin_audit_log

UI:
  · Trade detail page Section D 加 "🚨 手工平仓" 按钮 (only when status=open)
  · 二次确认 modal:
    - 显示 symbol + qty + entry_price + current PnL
    - 风险提示
    - 备注输入
```

#### d. 5 项熔断阈值调整 ⭐（mu RCA 后真实诉求）

```
PUT /api/admin/config/circuit-breaker-thresholds
body: {
  daily_loss_halt_pct?: 0.08 → 0.05,
  consecutive_losses_halt?: 8 → 6,
  total_float_loss_halt_pct?: 0.12 → 0.10,
  btc_panic_drop_pct?: 0.03,
  oi_imbalance_ratio?: 0.7,
  note: 'RCA 后收紧'
}
behavior:
  · 验证每个阈值在合理范围
  · UPDATE 一个新表 admin_overrides (migration 0014)
  · trader collector 读 admin_overrides 优先于 .env (next tick effective)
  · 写 admin_audit_log

工程实施:
  · 新表 admin_overrides (key TEXT PK, value JSONB, updated_at, updated_by)
  · trader 启动时 + 1min cron 读 admin_overrides
  · 优先级: admin_overrides > .env > sizing.go defaults

UI:
  · 新 page "⚙️ 阈值配置"
  · 5 项阈值表单 + 当前值 + 默认值 + 历史曲线
  · 调整 → 二次确认 → POST
```

#### e. watchlist 管理（mu RCA 后真实诉求）

```
PUT  /api/admin/watchlist/include/:symbol  body: { confirm: true }
PUT  /api/admin/watchlist/exclude/:symbol  body: { confirm: true, reason: 'SAPIEN 反复 -4168' }
GET  /api/admin/watchlist/blocklist        (公开 watchlist 排除清单)
behavior:
  · 写 watchlist_overrides 表 (migration 0015)
  · WatchlistCollector 每 1h tick 时读 overrides + 应用
  · audit log

UI:
  · Market scan page 每行 symbol 右键菜单 / 按钮:
    - "⛔ 排除" (POST exclude)
    - "✅ 添加" (POST include) — only when not in watchlist
  · 单独 "Watchlist 配置" page 显示 overrides
```

#### f. 信号阈值调整（mu RCA 后真实诉求）

```
PUT /api/admin/config/signal-thresholds
body: {
  oi_growth_from_min_pct?: 0.05 → 0.08,
  square_ratio_threshold?: 1.0 → 1.5,
  square_hot_acceleration_threshold?: 0.5,
  note: 'OI 5% 假阳性多, 收到 8%'
}
behavior: 同 d (admin_overrides 表)
```

#### g. RCA ack（halt trip 自动报告）

```
GET  /api/admin/halt-rca/:event_id   公开 RCA 报告 + 触发链
POST /api/admin/halt-rca/:event_id/ack  body: { ack_note: 'understood, will adjust' }
behavior:
  · RCA report = 自动生成 (触发那笔 trade + 信号 + 市场环境)
  · ack 写 admin_audit_log
  · 跟 reset 不同: ack 是 informed, reset 是操作
```

#### h. trader 主动 halt（mu 紧急停机）

```
POST /api/admin/circuit-breaker/halt
body: { halt_reason: 'mu_manual', duration_hours: 24, note: 'going out' }
behavior:
  · UPDATE trading_halted=true, halt_reason='manual_admin', halt_until=NOW()+N hour
  · 写 admin_audit_log
  · trader cron 看到 trading_halted=true → 跳过 entry
use case:
  · mu 出门 / 周末 / 重大新闻前主动停
```

#### i. 现有持仓 leverage 改动（工程不可行，mu informed）

```
❌ NOT IMPLEMENTED
原因: binance 持仓时 setLeverage 拒绝（-4045 Margin is insufficient）
替代:
  · trade detail 显示 "leverage_change_blocked" 提示
  · 解决方案: 手工平仓 → 重新 entry（带新 leverage）
```

### 3.3 写操作工程纪律

```
✅ FULL standards:
  · 二次确认 modal (跟 Round R.1/R.2 一致)
  · admin auth required (basic auth)
  · CSRF token required
  · audit log 完整 (operator + timestamp + previous_state + new_state + note)
  · 失败 rollback (DB transaction)
  · 输入验证 (阈值范围、symbol 格式)
  · 日志 + Prometheus metric

⚠️ PARTIAL allowed:
  · admin user 单 mu (B1, 复杂用户管理留 Phase 5.3+)
  · session timeout 简版 (30min, JWT 复杂版留 后续)
```

---

## §4 飞书告警（Phase 5.2 内 vs 5.3 拆分，mu 决策时机）

### 4.1 告警分级

| Level | 触发 | 飞书行为 | 频率限制 |
|---|---|---|---|
| 🔴 critical | halt trip / disaster_stop_placement_failed / 单笔 ≥-$50 | @ 群 + 加急 | 5min cooldown |
| 🟡 warning | SIGFAIL / margin_call / 灾难止损触发 / trail S2→S3 升级 | @ 群 | 1min cooldown |
| 🟢 info | mainnet entry / TP1/TP2 触发 / trail 激活 | 普通消息 | 不限 |
| ⚪ daily | BJT 00:00 日报 (PnL + 持仓 + 累计胜率) | 普通消息 | 1/day |

### 4.2 告警消息模板

```
🔴 [critical] 熔断 trip - daily_loss
  当前 daily_pnl: -85.40 USDT (-8.5% of $1000)
  阈值: -80 USDT (8%)
  连亏笔数: 6
  最后一笔: BTCUSDT -$15.50 (disaster, 10min 前)
  halt_until: 2026-05-14 12:00 BJT
  操作: https://trader.letsagent.net/admin

🟢 [info] 真盘 entry - SOLUSDT
  signal_id: 1234, decision: entered_half ($25 margin × 5x)
  entry_price: $145.32, qty: 0.86
  OI growth_from_min: 7.5%, Square ratio: 2.3
  algos: disaster ✓ trail_s1 ✓ tp1 ✓ tp2 ✓
  trade: https://trader.letsagent.net/admin/trade/123

⚪ [daily] BJT 00:00 日报
  daily PnL: +$15.20 (+1.5%)
  open positions: 2
  closed today: 5 (win 3 / loss 2, 60% win rate)
  累计 (forward 启动起): -$95.50 (-9.5%, 14 trades, 21% win rate)
```

### 4.3 工程实施

```
internal/notify/feishu.go:
  · FeishuClient (webhook URL + secret signing)
  · Alert{Level, Title, Body, RelatedLinks}
  · 同步 send + retry 3 次 (exponential 1s/2s/4s)
  · rate limiting (per Level cooldown)

集成点:
  · circuit_breaker.go 各 trip → critical/warning
  · executor.PlaceEntry → info
  · algo_reconciler.autoClose → info/warning
  · cron daily_report → daily

配置:
  FEISHU_WEBHOOK_URL=https://open.feishu.cn/open-apis/bot/v2/hook/xxx
  FEISHU_WEBHOOK_SECRET=xxx                  (签名 anti-replay)
  FEISHU_ENABLED=true
```

### 4.4 mu 决策时机

```
选项 A (本 Phase): 飞书告警 ~10-15h 实施完成在 v1.1 内
  · 优点: 一次性交付完整工具
  · 缺点: 总工时 +10-15h, 拖 acceptance

选项 B (拆 Phase 5.3): 飞书告警单独 phase
  · 优点: v1.1 更聚焦权限分级 + 写操作
  · 缺点: forward 评估期间 mu 无飞书 push, 需主动看 admin

Claude Code 推荐: 选项 A
  · forward 评估 6 周内, 飞书 push 价值高
  · mu 真盘 owner 不可能 24h 看 admin
  · 10-15h 在 §8 总 ~40-58h 中占比小
```

---

## §5 UX 改进剩余范围（Phase 5.1.x 后）

### 5.1 Phase 5.1.x 已交付（10 commits）

详见 §1.2 已实施清单。

### 5.2 Phase 5.2 待实施 UX

#### a. Square 情绪分析（mu GitHub Binance-Square-Analysis 引用）

```
Option A (轻量): keyword sentiment
  · 关键词字典 (positive: "to the moon", "bull", "rally"; negative: "rug", "dump", "bear")
  · 每个 post 计 +1 / -1 / 0
  · symbol sentiment = sum / count
  · 工时 ~3-5h
  · 准确度 ~60%

Option B (NLP): transformers / LLM API
  · 调 OpenAI / Anthropic API 或本地 model
  · 工时 ~8-12h
  · 准确度 ~85%
  · 成本: API 调用费

Option C (推迟): forward 评估完成后 mu 决策
  · 当前 Square mention count 已经有信号
  · 情绪分析是增量价值, 不是关键路径

推荐: Option C → 必要时 Phase 5.3
```

#### b. Page 6 Trade detail Section B 5 step（decision_engine 评估链）

```
当前: signal 数据 (oi_data + square_data) 一团 JSON 显示
目标: 拆 5 step
  Step 1: signal_engine 触发 (OI 5 periods growing + growth_from_min)
  Step 2: square_engine 评估 (hot? ratio? acceleration?)
  Step 3: decision_engine filter (BTC regime + circuit_breaker + 24h dedup)
  Step 4: decision_engine sizing (entered_full vs entered_half)
  Step 5: executor (margin + leverage + actual fill)
  · 每 step 显示绿色 ✓ / 红色 ✗
  · 工时 ~3-5h
```

#### c. 5 项熔断历史曲线（Grafana 风格内嵌）

```
新 page "📊 风控历史"
  · daily_pnl 时序曲线 (BJT day)
  · consec_losses 时序
  · halt 触发标记
  · halt manual reset 标记
  · 工时 ~5-7h (Recharts 已用)
```

#### d. Margin Ratio gauge 实时

```
当前: dashboard 5s polling refresh
目标: WebSocket push (跟 Round 4 v0.2 WS 衔接)
  · 工时 ~3-5h (跟 Round 4 一起做)
```

#### e. 公开访问后 mu 真盘真值显示（vs 5.1 私有）

```
当前: 5.1 假设全是 mu 看, 直接显示
公开后: 同样显示 (mu A1 决策无隐私)
  · 不需要改动
  · UX 文案略调 (e.g. "你的真盘" → "真盘")
  · 工时 ~1-2h
```

---

## §6 移动端响应式（mu 真盘 owner 手机端）

### 6.1 需求场景

```
mu 真盘 owner 移动端使用:
  · 出门时收到飞书告警 → 手机点开 admin Web UI
  · 紧急 halt reset (大按钮 + 触屏)
  · 持仓简要 check (顶部状态栏 + 持仓列表)
  · 累计 PnL 一眼 (单数字大字体)
  · 飞书内嵌打开 admin (微信 / 飞书 in-app browser)
```

### 6.2 实施范围

```
✅ FULL:
  · Dashboard mobile-first (单列布局 + 大数字)
  · 当前持仓 简要列表 (隐藏次要列 e.g. algo_id)
  · 手工平仓 / halt reset 触屏大按钮 (≥48dp)
  · 告警链接深链 (https://trader.letsagent.net/admin/dashboard?halt_event=123)

⚠️ PARTIAL:
  · 市场扫描 page 仍 desktop-first (信息密度高, 不强制移动友好)
  · Trade detail 两列布局 自适应 (mobile 改单列)
  · Chart 渲染 mobile 触屏 (Recharts 默认支持)

⚪ OUT-OF-SCOPE:
  · 原生 APP (Phase 5.3+ if needed)
  · PWA install (后续考虑)
```

### 6.3 工程实施

```
Tailwind CSS breakpoints (已用):
  sm: 640px   (small mobile)
  md: 768px   (tablet)
  lg: 1024px  (desktop, default current)

实施:
  · 全部 grid-cols-N 加 sm:grid-cols-1
  · 全部 text-2xl 在 mobile 改 text-3xl (大字体)
  · 隐藏次要信息 class="hidden md:table-cell"
  · 工时 ~5-8h
```

### 6.4 mu 决策

```
选项 A: Phase 5.2 内做
  · forward 6 周内 mu 手机端价值高
  · 跟飞书告警一起 ✓

选项 B: Phase 5.3 拆
  · v1.1 更聚焦权限 + 写操作

Claude Code 推荐: 选项 A (跟飞书一起, 移动 push 工程链条完整)
```

---

## §7 audit log + 权限管理

### 7.1 audit log 设计（扩展 Round R.2 circuit_breaker_events）

```sql
-- migration 0014_admin_audit_log.up.sql
CREATE TABLE admin_audit_log (
    id              BIGSERIAL PRIMARY KEY,
    ts              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    operator        TEXT NOT NULL,         -- 'mu' or future admin names
    action_type     TEXT NOT NULL,         -- 'halt_reset' | 'manual_close' | 'threshold_update' | etc.
    resource_type   TEXT,                  -- 'trade' | 'circuit_breaker' | 'config' | 'watchlist'
    resource_id     TEXT,                  -- trade_id / config_key / symbol
    previous_state  JSONB,
    new_state       JSONB,
    note            TEXT,
    ip_address      INET,
    user_agent      TEXT
);
CREATE INDEX admin_audit_log_ts_desc_idx ON admin_audit_log (ts DESC);
CREATE INDEX admin_audit_log_operator_idx ON admin_audit_log (operator, ts DESC);
CREATE INDEX admin_audit_log_action_idx ON admin_audit_log (action_type, ts DESC);
```

**兼容 Round R.2 circuit_breaker_events**：
- 双表并存（不迁移历史数据）
- 新写操作只记录 admin_audit_log
- audit 查询页 union 两表

**公开 audit log 查看页**（mu A1 决策）：
```
新 page "📋 操作历史" (公开):
  · 最近 100 条 admin_audit_log + circuit_breaker_events
  · 按时间倒序
  · filter: operator / action_type / date
  · 任何人可看 mu 真盘操作历史 (透明度)
```

### 7.2 admin 用户管理（B1 单 admin）

```
简化版:
  · 1 个 admin user (mu)
  · ADMIN_USER + ADMIN_PASSWORD_BCRYPT 在 .env
  · Caddy basic auth + admin-api 中间件
  · session: bcrypt verify per request (无 server-side session)

跟 Phase 5.1 兼容:
  · Phase 5.1 Caddy basic auth 复用同一对
  · admin-web 前端 session 仅缓存 csrf_token (sessionStorage)

后续扩展可能 (Phase 5.3+):
  · 多 admin (mu + assistant)
  · JWT / session 中心化
  · RBAC (read / halt / config / watchlist 等细分权限)
```

### 7.3 CSRF 防御

```
admin-api 新 endpoint:
  GET /api/admin/csrf-token
    auth: required
    return: { token: "...", expires_at: "..." }
    server: 内存 map {token: {expires_at, user}}, 30min TTL

写 endpoint 中间件:
  1. require admin auth (basic auth)
  2. require X-CSRF-Token header
  3. verify token in memory map + not expired
  4. 处理请求

前端流程:
  1. 用户点写按钮 → 调用 ensureCsrfToken()
  2. ensureCsrfToken: 若 sessionStorage 无 token 或过期 → GET csrf-token → 缓存
  3. POST/PUT/DELETE 时 axios interceptor 自动加 X-CSRF-Token
```

---

## §8 Round 拆分（Phase 5.2 实施，~40-58h）

### Round 0: 设计文档（本次，~2-3h）
- docs/PHASE_5_2_ADMIN_V1_1_DESIGN.md (~1000-1500 行)
- mu 审 + 决策（§4 飞书 / §6 移动端 / 飞书优先级）
- 不打 tag

### Round 1: 权限分级 + 公开读 + admin auth（~6-8h）

```
acceptance:
  ✅ Caddy config path-based auth (read=public, write=admin)
  ✅ admin-api: 中间件区分 GET vs POST/PUT/DELETE
  ✅ admin-web: 全部公开访问 (无 login redirect)
  ✅ admin login modal (写按钮前弹)
  ✅ csrf token endpoint + 中间件
  ✅ 单元测试: 公开 GET / 受保护 POST / csrf 验证
  ✅ 部署 verify (浏览器无 auth 直接看)
```

### Round 2: 写操作 endpoint 后端（~6-8h）

```
acceptance:
  ✅ migration 0014 admin_audit_log
  ✅ migration 0015 admin_overrides
  ✅ migration 0016 watchlist_overrides
  ✅ POST /api/admin/circuit-breaker/daily-pnl-reset
  ✅ POST /api/admin/circuit-breaker/consec-reset
  ✅ POST /api/admin/circuit-breaker/halt (manual halt)
  ✅ POST /api/admin/trades/:id/close (手工平仓)
  ✅ PUT  /api/admin/config/circuit-breaker-thresholds
  ✅ PUT  /api/admin/config/signal-thresholds
  ✅ PUT  /api/admin/watchlist/include/:symbol
  ✅ PUT  /api/admin/watchlist/exclude/:symbol
  ✅ GET  /api/admin/audit-log (公开)
  ✅ 单元测试 ≥ 30 tests
  ✅ trader 读 admin_overrides 优先逻辑 (1min cron)
```

### Round 3: 写操作前端 + modal（~5-7h）

```
acceptance:
  ✅ Dashboard 加 daily_pnl reset / consec reset / 主动 halt 按钮
  ✅ Trade detail 加手工平仓按钮 + 确认 modal
  ✅ 新 page "⚙️ 阈值配置" (CB + signal 阈值表单)
  ✅ Market scan 加 watchlist include/exclude 按钮
  ✅ 新 page "📋 操作历史" (公开 audit log)
  ✅ 每个写操作 二次确认 modal + 备注 input + 风险提示
  ✅ 写后自动 invalidate + reload
```

### Round 4: 飞书告警（~10-15h, mu 决策是否拆 Phase 5.3）

```
acceptance:
  ✅ internal/notify/feishu.go
  ✅ FeishuClient + 签名 + retry
  ✅ Alert 分级 (critical/warning/info/daily)
  ✅ Rate limit per level
  ✅ 集成点: circuit_breaker / executor / algo_reconciler / cron daily
  ✅ FEISHU_* 配置
  ✅ 单元测试 (mock webhook)
  ✅ admin Web UI 加 "飞书通知" 设置页 (start/stop 各告警类型)
```

### Round 5: UX 改进剩余（~5-8h）

```
acceptance:
  ✅ Page 6 Section B 5 step 拆分显示
  ✅ 5 项熔断历史曲线 page
  ✅ Square 情绪分析 (Option C 推迟 / Option A 轻量实施)
  ✅ Margin Ratio 实时 (跟 Round 4 WS 衔接)
```

### Round 6: 移动端响应式（~5-8h）

```
acceptance:
  ✅ Dashboard mobile single-column
  ✅ 持仓列表 触屏友好 (大按钮 + 卡片式 vs 表格)
  ✅ 手工平仓 / halt reset 大按钮 + 风险提示
  ✅ Trade detail mobile 单列
  ✅ Chart Recharts mobile 触屏
  ✅ 测试: iPhone Safari / Android Chrome / 飞书内嵌
```

### Round 7: 集成测试 + acceptance + tag（~3-5h）

```
acceptance:
  ✅ 端到端: 公开读 + admin login + 写操作 + audit 全链
  ✅ rollback 测试: 写失败 DB 不脏
  ✅ 跟 v0.2 trader 集成测试 (Round 4 WS 完成后)
  ✅ docs/PHASE_5_2_ACCEPTANCE.md
  ✅ tag phase-5.2-admin-web-v1.1
```

### 工时总结

| Round | 工时 | 备注 |
|---|---|---|
| 0 | 2-3h | 设计 (本次) |
| 1 | 6-8h | 权限分级 |
| 2 | 6-8h | 写 endpoint |
| 3 | 5-7h | 写前端 |
| 4 | 10-15h | 飞书 |
| 5 | 5-8h | UX |
| 6 | 5-8h | 移动端 |
| 7 | 3-5h | acceptance |
| **总** | **~42-62h** | ~3-5 周 wall-clock |

---

## §9 兼容性 v1.0 → v1.1

### 9.1 API 兼容

```
GET endpoints:
  · Phase 5.1 全部保留 (12 个 + Round R.1 events)
  · 唯一改动: 移除 Caddy basic auth (变公开)
  · 数据返回结构不变 (前端可继续用)

POST/PUT/DELETE endpoints:
  · 新加 (~10 个 §3 写操作)
  · 不影响 Phase 5.1 read-only
```

### 9.2 数据 schema 兼容

```
新表 (migration 0014/0015/0016):
  · admin_audit_log     (新, 不影响现有)
  · admin_overrides     (新, trader 读优先于 .env)
  · watchlist_overrides (新, WatchlistCollector 读 + 应用)

修改表:
  · 无 (Round R.2 已加 manual_reset_at/by 列 + events 表)

数据迁移:
  · circuit_breaker_events 保留 (Round R.2 历史数据)
  · 新写操作记录 admin_audit_log (新表)
  · audit 查看 union 两表
```

### 9.3 前端兼容

```
现有 7 page:
  · Dashboard / Open / History / PnL / Square / Market / Trade detail
  · 保留, 公开访问 (无 login redirect)

新 page (Phase 5.2):
  · "⚙️ 阈值配置"
  · "📋 操作历史"
  · "🔔 飞书通知" (写设置, admin auth)
```

### 9.4 部署兼容

```
mu 现有持仓 (ESPORTSUSDT + INJUSDT):
  · 不影响 (Phase 5.2 admin Web UI 改动, 不动 trader)
  · 平仓 → 公开访问后 PnL 数据公开

forward 评估并行:
  · trader 跑 v0.2 + Round R.1/R.2/3.y bug fixes
  · admin Web UI v1.0 (基线 read-only) 同时 deploy
  · Phase 5.2 实施期间不影响 forward 评估
```

---

## §10 实施时机 + mu 决策点

### 10.1 时间轴

```
2026-05-13 (T+0):
  · Round R.1/R.2/3.y 部署完成 ✅
  · trader v0.2 完整 5 layer 出场系统 ✅
  · Phase 5.2 Round 0 设计文档 ✅ (本次)
  · forward 评估并行启动

2026-05-13 ~ 06-03 (T+1-3 周):
  · trader v0.2 Round 4 WS 实施 (与 Phase 5.2 Round 1-3 并行)
  · Phase 5.2 Round 1 (权限分级)
  · Phase 5.2 Round 2 (写 endpoint)
  · Phase 5.2 Round 3 (写前端)

2026-06-03 ~ 06-17 (T+3-5 周):
  · Phase 5.2 Round 4 (飞书) [if 选 A]
  · Phase 5.2 Round 5 (UX)
  · Phase 5.2 Round 6 (移动端)

2026-06-17 ~ 06-24 (T+5-6 周):
  · Phase 5.2 Round 7 (集成 + acceptance)
  · tag phase-5.2-admin-web-v1.1
  · trader v0.2 tag phase-trader-v0.2

2026-06-24+ (T+6 周):
  · trader v1.0 production-ready
  · admin Web UI v1.1 production-ready
  · 公开访问可分享 / mu 移动端 / 飞书 push 完整
```

### 10.2 mu 决策点（Round 0 完成后）

```
1. §4 飞书告警: Phase 5.2 内 (A) vs Phase 5.3 拆 (B)?
   推荐: A (forward push 价值高)

2. §5 Square 情绪分析: Option A 轻量 / B NLP / C 推迟?
   推荐: C 推迟 (forward 数据多了再决策)

3. §6 移动端: Phase 5.2 内 vs Phase 5.3?
   推荐: Phase 5.2 内 (跟飞书告警链路一致)

4. §7 admin 用户管理: 单 admin (B1) vs 多 admin?
   推荐: B1 单 admin (mu 唯一 owner, 复杂留 5.3+)

5. §8 Round 拆分: vertical slice OK 还是再细?
   推荐: 当前 8 Round 合理, 每 Round 工时 3-15h 适中

6. §10 实施时机: 跟 Round 4 WS 并行启动 OK?
   推荐: OK (Phase 5.2 不阻塞 trader, 反之亦然)
```

### 10.3 Phase 5.3+ 后续展望

```
Phase 5.3 候选 (mu 决策时):
  · 多 admin / RBAC
  · 飞书 (若 Phase 5.2 选 B)
  · Square 情绪 NLP (Option B)
  · Grafana 风格内嵌 dashboards
  · trader 实时 WS push 替换 5s polling (跟 Round 4 协同)
  · 移动 native app (PWA install)
  · backtest 系统 (替换 mu 真盘做实验)
```

---

## Appendix: 关键设计决策对比

### A.1 公开 vs auth 的权衡

| 维度 | 公开（mu A1） | 全 auth（Phase 5.1） |
|---|---|---|
| 工程复杂度 | 低 | 中 |
| 用户体验 | 优（链接直开） | 中（每次输密码）|
| 真盘隐私 | 牺牲（PnL 公开） | 保留 |
| 教育价值 | 高（社区可学）| 低（私有）|
| 安全风险 | 低（只读 + 写权限分开） | 极低 |
| mu 选择 | ✅ A1 | ❌ |

### A.2 单 admin vs 多 admin

| 维度 | 单 admin (B1) | 多 admin / RBAC |
|---|---|---|
| 工程复杂度 | 低 (basic auth + bcrypt) | 高 (用户管理 + JWT + RBAC) |
| 当前需求 | ✅ 满足 (mu 唯一) | over-engineering |
| 后续扩展 | ⏳ 可升级到 5.3+ | 一步到位 |
| mu 选择 | ✅ B1 | ❌ |

### A.3 飞书告警范围

| 维度 | Phase 5.2 内 (推荐 A) | Phase 5.3 拆 (B) |
|---|---|---|
| 工时 | +10-15h | 0 |
| Forward push | 6 周内即有 | 6 周后才有 |
| mu 真盘 6 周 push | ✓ | ✗ (主动查) |
| 推荐 | ✅ A | — |

---

**文档状态:**
- Round 0 ✅ FULL（设计文档完整 ~1300 行）
- §4 飞书 / §6 移动端 / §5 Square 情绪 ⚠️ PARTIAL（mu 决策时确认）
- §7 admin 用户管理 ✅ B1 简化版决策
- §8 Round 拆分 ✅ vertical slice 7 round

**等 mu review 后**:
- commit subject: `docs(admin): Phase 5.2 admin Web UI v1.1 规划设计 (A1 公开读 + B1 单独写权限)`
- 不打 tag (实施时打 phase-5.2-admin-web-v1.1)
- Round 1 启动等 mu 决策 (跟 Round 4 WS 并行)
