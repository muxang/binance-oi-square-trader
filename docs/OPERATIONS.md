# OPERATIONS — 日常运维 (deployment v0.1)

> Status: deployment v0.1 Round 4 文档草稿
> 配套: `scripts/status.sh` / `scripts/update.sh` / `scripts/healthcheck.sh` / Grafana

---

## §1. 状态检查 — `status.sh` 5 段输出解读

```bash
bash scripts/status.sh
```

### 段 1: Container 状态
```
SERVICE      STATUS                   STATE
trader       Up 3 hours (healthy)     running
postgres     Up 3 hours (healthy)     running
redis        Up 3 hours (healthy)     running
caddy        Up 3 hours               running
prometheus   Up 3 hours               running
grafana      Up 3 hours               running
loki         Up 3 hours               running
promtail     Up 3 hours               running
```
**告警判定**:任一 service 不是 `Up + (healthy)` → 立刻 `docker compose logs <service>` 查根因。

### 段 2: Disk / Memory
```
Filesystem      Size  Used Avail Use% Mounted on
/dev/vda1        60G   12G   48G  20% /

              total        used        free      shared  buff/cache   available
Mem:           3.6G        1.2G        500M         12M        1.9G        2.4G
Swap:            0B          0B          0B

  deploy/data:  6.5G    (PG hypertable + Loki + Grafana 累积)
  backups/:     450M    (30 天 retention 后稳态)
```
**告警判定**:磁盘 > 80% → 触发 retention 缩短(SPEC §11 PG 30d / Loki 7d 调整)。内存 > 80% → 检查 Redis maxmemory(默认 256M)+ trader heap pprof。

### 段 3: Activity(last 5min × 9 collector)
```
  oi_history         1 ticks
  btc_regime         5 ticks
  klines             1 ticks
  square             0 ticks      ← 1h cron, 5min 窗看到 0 正常
  square_hashtag     0 ticks      ← 15min cron
  watchlist          0 ticks      ← 1h cron
  position_price     5 ticks
  signal_engine      1 ticks
  decision_engine    1 ticks
```
**告警判定**:5min cron 应 = 1 tick(允许 0 或 1,取决于 status.sh 启动时机)。1h cron 偶尔 0 正常。**连续 2 次** status.sh 跑都看到 0 → 真异常。

### 段 4: Metrics 摘要
```
trader_collector_runs_total{collector="oi_history",result="ok"} 8
trader_collector_runs_total{collector="oi_history",result="error"} 0
trader_decision_evaluations_total{outcome="trade_entering"} 0
trader_decision_evaluations_total{outcome="no_entered_signals"} 142
trader_decision_evaluations_total{outcome="rejected_position_limit"} 3
trader_panic_total 0
trader_circuit_breaker_state 0
```
**告警判定**:`trader_panic_total > 0` → 立即查 logs。`circuit_breaker_state = 1`(BTC 5m 暴跌触发熔断)→ 30min 后自动 reset 否则手动检查 ARCH §11.5。

### 段 5: Errors(last 1h)
```
  ✓ 无 ERROR / FATAL / panic
```
有 ERROR 时直接列最近 10 条,mu 决定:吞掉(已恢复)/ 修复 / 临时降级。

---

## §2. 代码更新流程

```bash
# 本机
git push origin master

# SSH 到 VPS, 增量更新
ssh vps
cd ~/trader
bash scripts/update.sh
# 流程: git pull + rebuild trader image + restart trader container + 跑迁移 + healthcheck
# 期间 trader ~30s 不可用 (其它 8 service 不停, Caddy 自动 503 → 200)
```

**update.sh 不会**:
- 改 .env(配置变更需 mu 手动 nano + restart)
- 重启基础设施(只重启 trader,PG/Redis/Caddy 等不动)
- 升级 docker image(用现有 base image rebuild)

---

## §3. 监控告警 — Grafana Dashboard 关键 metric

`https://trader.letsagent.net/grafana`(公网开放,走 Grafana admin 密码认证)

**4 组核心 panel**(Round 4 文档列出推荐,真 dashboard JSON 待 v0.2 部署后 export):

### Panel 1: 9 Collector 健康度
```promql
rate(trader_collector_runs_total{result="ok"}[5m])
  / rate(trader_collector_runs_total[5m]) * 100
```
**阈值**:成功率 < 95% 触发 warning,< 80% 触发 critical(对照 Phase 1 §8 + Phase 2 v0.1 §8 forward 5 项)。

### Panel 2: 16 Decision Outcome 分布
```promql
sum by (outcome) (increase(trader_decision_evaluations_total[1h]))
```
**判定**:`trade_entering` / `rejected_*` / `sizing_*` 比例,Phase 3 §Phase 3 acceptance L387 "拒绝原因分布合理"标准 — v0.2 forward 跑 7-14 天后 mu 校准 FilterConfig 阈值。

### Panel 3: Sizing Deviation Histogram
```promql
histogram_quantile(0.95, sum by (le, symbol_class)
  (rate(trader_decision_sizing_deviation_pct_bucket[1h])))
```
**阈值**:p95 < 5% 接受,5-10% 警告,> 10% reject(Round 3 #3 决策)。

### Panel 4: Panic / Circuit Breaker
```promql
trader_panic_total
trader_circuit_breaker_state
```
**告警**:panic > 0 或 circuit_breaker_state = 1 立即 TG 通知(Phase 5 实施)。

---

## §4. 7 Collector 成功率 — Phase 3 §8 forward 5 项量化

| 项 | 目标 | 测量 query | 来源 |
|---|---|---|---|
| **1. T1/T7 全采集** | ≥ 95% | `rate(trader_collector_runs_total{collector="oi_history",result="ok"}[1h]) / rate(trader_collector_runs_total{collector="oi_history"}[1h])` | Phase 1 §8 baseline |
| **2. T3 hashtag 全采集** | ≥ 80% | `rate(trader_collector_runs_total{collector="square_hashtag",result="ok"}[1h]) / rate(...)` | Phase 2 §6.1 RCA |
| **3. signal_engine 真生 entered_*** | > 0 / day | `increase(trader_signals_total{decision=~"entered_.*"}[24h])` | Phase 2 v0.2 forward 关键标志 |
| **4. decision_engine trade_entering** | 视情况 | `increase(trader_decision_evaluations_total{outcome="trade_entering"}[24h])` | Phase 3 §8 #5 (拒绝分布合理) |
| **5. 7-14 天 forward 评估** | mu 周报 | grafana export → mu 阈值校准 | v0.2 准入条件 |

---

## §5. v0.2 调阈值流程(决策树)

```
forward 跑 7 天后 (mu 周报)
│
├─ 7 collector 成功率 < 80%
│  ├─ 代理 IP ban 高频 → 切付费代理 (DISASTER_RECOVERY §3)
│  └─ 单代理慢 → 加代理源 (deploy/proxies.txt 追加)
│
├─ trade_entering = 0 (持续 7 天)
│  ├─ 真没信号 (币市低波动) → 等
│  ├─ FilterConfig 太严 → 调:
│  │   - BTCDropThreshold 0.03 → 0.04
│  │   - PositionLimit 5 → 7
│  │   - Recent24hWindow 24h → 12h
│  └─ Sizing 偏差 > 10% → 调 step round 容忍度 (warning vs reject)
│
├─ trade_entering 频率过高 (> 5/day)
│  └─ FilterConfig 太松 → 收紧 (反向操作)
│
└─ panic / circuit_breaker 频发
   └─ 立即 stop, 跟 mu RCA, 不靠调阈值掩盖 bug
```

**v0.2 调阈值改 .env → restart trader**(`docker compose restart trader`),不需 rebuild。

---

## §6. 日志查看

### 实时 trader 日志
```bash
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml \
    logs -f --tail=100 trader
```

### Loki / Grafana 日志查询
`https://trader.letsagent.net/grafana` → Explore → Loki datasource:
```logql
{container_name="trader-app"} |= "ERROR" | json | level="error"
{container_name="trader-app"} |= "tick complete" | json | line_format "{{.collector}} {{.symbol}}"
```

### 单 collector 日志过滤
```bash
docker compose logs --tail=2000 trader | grep -E '"collector":"square_hashtag"' | tail -20
```

### Caddy access log
```bash
docker compose exec caddy tail -f /var/log/caddy/access.log
```
