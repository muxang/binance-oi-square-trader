# RUNBOOK — 日常运维命令速查

> VPS: 43.133.173.17  域名: trader.letsagent.net  所有命令在 ~/trader 执行

---

## 一、每日巡检（30 秒）

```bash
bash scripts/status.sh
```

正常输出：8 services Up、5min collector ticks 非全零、无 ERROR/panic。

快速健康检查（不看 metrics）：

```bash
bash scripts/healthcheck.sh
```

期望：Pass: 7  Fail: 0

---

## 二、容器状态

```bash
# 所有服务状态
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml ps

# 只看 trader 是否 healthy
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml ps trader
```

**状态解读**

| STATUS | 含义 |
|---|---|
| Up N hours (healthy) | 正常 |
| Up N hours | 运行但无 healthcheck（Prometheus/Loki 正常） |
| Restarting | 崩溃重启 → 立即看 logs |
| Exited | 已退出 → 立即看 logs |

---

## 三、日志查看

### 3.1 trader 实时日志

```bash
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml \
    logs -f --tail=100 trader
```

### 3.2 按 collector 过滤

```bash
# 查看某个 collector 最近 20 条（把 oi_history 换成任意 collector 名）
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml \
    logs --tail=2000 trader \
    | grep '"collector":"oi_history"' | tail -20

# 9 个 collector 名称:
# oi_history  btc_regime  klines  square  square_hashtag
# watchlist  position_price  signal_engine  decision_engine
```

### 3.3 只看错误

```bash
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml \
    logs --since=1h trader \
    | grep -E '"level":"error"|"level":"fatal"|panic:'
```

### 3.4 查看 tick complete（确认 collector 在跑）

```bash
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml \
    logs --tail=500 trader | grep "tick complete"
```

### 3.5 Caddy 访问日志

```bash
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml \
    exec caddy tail -f /var/log/caddy/access.log
```

### 3.6 其他服务日志

```bash
# 把 trader 替换为: postgres / redis / prometheus / grafana / loki / caddy
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml \
    logs --tail=50 prometheus
```

---

## 四、Grafana 使用

访问：**https://trader.letsagent.net/grafana/**  
登录：`admin` / `$GF_SECURITY_ADMIN_PASSWORD`（.env 里设置的值）

### 4.1 Explore — Prometheus（指标）

左侧菜单 → Explore → 顶部 datasource 选 **Prometheus**

**必看 4 个查询：**

```promql
# 1. 9 collector 5min 成功率（正常应接近 100%）
rate(trader_collector_runs_total{result="ok"}[5m])
  / rate(trader_collector_runs_total[5m]) * 100

# 2. 1h 决策分布（看 trade_entering / rejected_* 比例）
sum by (outcome) (increase(trader_decision_evaluations_total[1h]))

# 3. panic 计数（必须为 0）
trader_panic_total

# 4. 熔断状态（0=正常 1=已触发）
trader_circuit_breaker_state
```

**collector 级别查询：**

```promql
# 某个 collector 1h 内成功次数
increase(trader_collector_runs_total{collector="oi_history",result="ok"}[1h])

# 全部 collector error 次数
increase(trader_collector_runs_total{result="error"}[1h])
```

### 4.2 Explore — Loki（日志）

左侧菜单 → Explore → 顶部 datasource 选 **Loki**

```logql
# 全部 trader 日志（实时）
{container_name="trader-app"}

# 只看 error
{container_name="trader-app"} | json | level="error"

# 只看 tick complete（确认 collector 运行）
{container_name="trader-app"} |= "tick complete"

# 某个 collector 的日志
{container_name="trader-app"} |= "oi_history"

# 决策引擎进场日志
{container_name="trader-app"} |= "trade_entering"
```

### 4.3 创建 Dashboard

1. 左侧 → Dashboards → New → New Dashboard
2. Add visualization → 选 Prometheus
3. 在 PromQL 输入框粘贴上面的查询
4. 右上角保存

预建 dashboard 在 Phase 5+ 会作为 JSON 文件加入 `deploy/grafana/dashboards/`。

---

## 五、服务管理

### 5.1 重启单个服务

```bash
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml \
    restart trader   # 把 trader 换成任意服务名

# 重启后看日志确认
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml \
    logs -f --tail=50 trader
```

### 5.2 重启全部服务

```bash
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml \
    restart
```

### 5.3 停止 / 启动全部

```bash
# 停止（保留数据）
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml down

# 启动
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml up -d
```

---

## 六、代码更新

```bash
# 本机 push 后，SSH 到 VPS：
bash scripts/update.sh

# 流程：git pull → rebuild trader → restart trader → migrate → healthcheck
# 约 60-90s，期间 trader 不可用（其余服务不停）
```

---

## 七、本机连 VPS 数据库（SSH tunnel）

```bash
# 本机启动 tunnel（后台运行）
bash scripts/ssh-tunnel.sh up vps

# 连接
psql postgres://trader:trader@localhost:15432/trader
redis-cli -p 16379

# 查 tunnel 状态 / 关闭
bash scripts/ssh-tunnel.sh status
bash scripts/ssh-tunnel.sh down
```

---

## 八、Metrics 直接查看（不用 Grafana）

```bash
# 在 VPS 上查看所有 trader 指标
curl -s http://localhost:2112/metrics | grep '^trader_'

# 只看关键几个
curl -s http://localhost:2112/metrics \
    | grep -E '^trader_(collector_runs|decision_eval|panic|circuit_breaker)'
```

---

## 九、常见问题排查

### trader 停了 / Restarting

```bash
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml \
    logs --tail=50 trader
# 看最后几行报错原因
```

常见原因：

| 报错 | 修法 |
|---|---|
| `load pool file ... no such file` | proxies.txt 路径错或文件不存在 |
| `pq: ... connection refused` | postgres 未就绪，等 30s 重试 |
| `migrate: Dirty database` | `docker compose exec postgres psql -U trader -c "DELETE FROM schema_migrations"` |

### Caddy 证书失败

```bash
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml \
    logs --tail=30 caddy | grep -i "error\|cert\|obtain"

# 清缓存重试
sudo rm -rf deploy/data/caddy/* deploy/data/caddy-config/*
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml \
    restart caddy
```

### Prometheus / Grafana / Loki 崩溃

```bash
# 99% 是 data 目录权限问题（sudo mkdir 建的）
sudo chmod -R 777 deploy/data/prometheus deploy/data/grafana deploy/data/loki
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml \
    restart prometheus grafana loki
```

### 代理 IP 被封（collector error 率高）

```bash
# 查 error 率
curl -s http://localhost:2112/metrics \
    | grep 'trader_collector_runs_total{.*result="error"'

# 换代理：编辑 deploy/proxies.txt，加新代理 URL
nano deploy/proxies.txt
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml \
    restart trader
```

---

## 十、备份（手动 / cron）

```bash
# 手动备份
bash scripts/db-backup.sh

# 加 cron（每天 3:00 AM BJT 备份，保留 30 天）
crontab -e
# 加入：
# 0 3 * * * cd ~/trader && bash scripts/db-backup.sh >> ~/trader/backups/cron.log 2>&1
```
