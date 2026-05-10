# DEPLOYMENT — VPS 一键部署 (deployment v0.1)

> Status: deployment v0.1 Round 4 文档草稿
> 目标 VPS: 腾讯云东京轻量 2C4G / Ubuntu 24.04 LTS / IP `43.133.173.17`
> 域名: `trader.letsagent.net` (mu letsagent.net 注册商配 DNS)

---

## §1. 总览 — 架构 + 9 服务 + 3 层防护

```
┌──────────────────── VPS (43.133.173.17) ────────────────────┐
│                                                              │
│  Internet ──443/80──→ Caddy (Let's Encrypt auto cert)       │
│                       │                                      │
│                       ├─ /health  → trader:8080 (公开)       │
│                       ├─ /api/*   → trader:8080 (公开)       │
│                       ├─ /metrics → trader:2112 (公开)       │
│                       └─ /grafana → grafana:3000 (公开)      │
│                                                              │
│  trader (Go)  ─→ postgres-timescale + redis (内部 only)      │
│               ─→ prometheus + loki (内部 only)               │
│               ─→ binance.com (出网经 PROXY=pool)             │
│                                                              │
│  ufw 防火墙: 22/80/443 allow, 5432/6379/2112/3001/9090/3100 deny │
│                                                              │
└──────────────────────────────────────────────────────────────┘
```

**9 服务**(全 Docker Compose 编排):
1. trader(Go binary container,7+2 collector + decision_engine)
2. postgres-timescale(PG 16 + TimescaleDB hypertable)
3. redis(7-alpine + AOF + LRU 256mb)
4. prometheus(:9090 内部,30d retention)
5. grafana(:3000 内部,via Caddy /grafana)
6. loki(:3100 内部,日志聚合)
7. promtail(scrape docker logs → loki)
8. caddy(:80/:443,反向代理 + Let's Encrypt 自动证书)
9. (Phase 5+ dashboard 静态文件,Caddy `handle` 默认路由)

**3 层防护**(B 简化安全模式):
- **DNS 层**:`trader.letsagent.net` A 记录 → VPS IP,bootstrap.sh 启动时强校验(不匹配立即 abort)
- **Caddy 层**:HTTPS 强制(Let's Encrypt 自动证书),所有路由公网开放走各自认证(/grafana 走 Grafana admin 密码)
- **ufw 层**:默认 deny incoming,显式 allow 22/80/443,内部端口(5432/6379/2112/3001/9090/3100)deny — 公网仅 Caddy 入口,内部隔离

---

## §2. VPS Prerequisites

| 项 | 要求 | 实测验证 |
|---|---|---|
| OS | Ubuntu 22.04 / 24.04 LTS | mu 实测 24.04 ✓ |
| CPU / RAM | 2 核 / 4G+ | 2C4G 519M used / 3.6G ✓ |
| 磁盘 | 60GB+ SSD | 5.3GB / 60GB used ✓ |
| 公网 IP | 固定 | 43.133.173.17(腾讯云东京)|
| 网络 | 币安 API 直连 | curl fapi.binance.com 200 OK,2.4ms ✓ |
| SSH | key-based,sudoer 用户 | mu 自配 |

---

## §3. mu 部署前准备(3 项)

### 3.1 DNS A 记录配置

在 letsagent.net 注册商管理界面:
```
Name:   trader
Type:   A
Value:  43.133.173.17
TTL:    300 (5min, 首次部署可短)
```

部署前用 `dig +short trader.letsagent.net @8.8.8.8` 验证返回 `43.133.173.17`(可能等 5-10min 生效)。

### 3.2 .env 12 字段填值

| 字段 | 性质 | 必填 | 示例 / 说明 |
|---|---|---|---|
| `BINANCE_API_KEY` | secret | Phase 4 必填 | testnet 也要配置 |
| `BINANCE_API_SECRET` | secret | Phase 4 必填 | |
| `TG_BOT_TOKEN` | secret | 推荐 | Phase 5 告警通道 |
| `TG_CHAT_ID` | config | 推荐 | mu 个人 chat id |
| `BINANCE_PROXY_MODE` | config | 必填 | `pool`(VPS 强制建议) |
| `BINANCE_PROXY_POOL_FILE` | config | 必填 | `./deploy/proxies.txt` |
| `DATABASE_URL` | config | 必填 | `postgres://trader:trader@postgres:5432/trader?sslmode=disable` |
| `REDIS_URL` | config | 必填 | `redis://redis:6379/0` |
| `GF_SECURITY_ADMIN_PASSWORD` | secret | 必填 | ≥16 字符随机串 |
| `DOMAIN` | config | 必填 | `trader.letsagent.net` |
| `ACME_EMAIL` | config | 必填 | mu 真实邮箱(Let's Encrypt 通知) |

### 3.3 mu 本机 ~/.ssh/config 加 vps 别名

```
Host vps
    HostName 43.133.173.17
    User <mu-vps-user>
    IdentityFile ~/.ssh/id_ed25519
    ServerAliveInterval 60
```

之后 `ssh vps` / `bash scripts/ssh-tunnel.sh up vps` 直接生效。

---

## §4. 一键部署 SOP(4 步)

```bash
# Step 1: SSH 到 VPS
ssh vps

# Step 2: clone 代码
git clone https://github.com/<mu>/binance-oi-square-trader.git ~/trader
cd ~/trader

# Step 3: 配置 .env
cp .env.example .env
nano .env
# 填 §3.2 表格 12 字段
# 默认 TRADER_MODE=testnet, 不要改 mainnet (Phase 4 才切)

# Step 4: 一键部署 (sudo 必需, 装 Docker + ufw + 起服务 + 跑迁移 + Caddy cert)
sudo bash scripts/bootstrap.sh
```

**bootstrap.sh 8 段流程**:
1. 校验环境(sudo + Ubuntu)
2. 校验 .env(REQUIRED_VARS 10 字段 + chmod 600)
3. **DNS 校验**(`dig $DOMAIN` 应等于 VPS 公网 IP,不匹配 abort)
4. 装 Docker + Docker Compose plugin
5. 设 VPS 时区 Asia/Shanghai
6. 准备 deploy/data/{postgres,redis,...} 目录
7. **配置 ufw**(allow 22/80/443,deny 内部端口)
8. 起服务(docker compose up -d --build)
9. 等 PG 就绪 + 跑 migrate
10. **等 Caddy + Let's Encrypt 证书**(poll `https://$DOMAIN/health`,最多 90s)
11. healthcheck.sh 8 项 check

---

## §5. 部署后验证(5 项)

```bash
# 1. HTTPS + cert
curl -fsSL https://trader.letsagent.net/health
# 期望: {"status":"ok",...}

# 2. 9 collector 状态摘要
bash scripts/status.sh
# 期望: 5 段输出, 9 collector last 5min ticks > 0

# 3. Container 全 healthy
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml ps
# 期望: 8 services 全 Up + healthy (trader / postgres / redis / prometheus / grafana / loki / promtail / caddy)

# 4. Grafana 登录 (公网开放, 走 Grafana 自己的 admin 密码认证)
# 浏览器: https://trader.letsagent.net/grafana
# admin / $GF_SECURITY_ADMIN_PASSWORD
# 期望: 看到 datasources prometheus + loki, dashboards 待 OPERATIONS.md §3 加

# 5. 9 collector 真数据 (前 30min)
docker compose logs -f --tail=200 trader | grep "tick complete"
# 期望: oi_history / btc_regime / klines / square / square_hashtag / watchlist / position_price / signal_engine / decision_engine 全有 tick complete
```

---

## §6. mu 本机连 VPS 数据库

per Round 0 决策 5,SSH tunnel 方案(scripts/ssh-tunnel.sh,不暴露 PG/Redis 公网):

```bash
# 本机启动 tunnel (后台 + PID 文件管理)
bash scripts/ssh-tunnel.sh up vps
# ✓ tunnel up (pid=12345)
#   本机连 PG:    psql postgres://trader:trader@localhost:15432/trader
#   本机连 Redis: redis-cli -p 16379

# 本机 .env.dev 配置
DATABASE_URL=postgres://trader:trader@localhost:15432/trader?sslmode=disable
REDIS_URL=redis://localhost:16379/0

# 状态 / 停止
bash scripts/ssh-tunnel.sh status
bash scripts/ssh-tunnel.sh down
```

**典型 mu 开发 Phase 4 工作流**:本机 IDE → ssh-tunnel → VPS PG/Redis 真数据 → 跑 mock binance API 测试 → push code → ssh vps → `bash scripts/update.sh`。

---

## §7. 安全模式说明(B 简化,覆盖所有阶段)

**trader 应用层**(mu 决策,不变):
- `/grafana` + `/metrics` 公网开放
- `GF_SECURITY_ADMIN_PASSWORD` 强密码(≥16 字符,Grafana 自己认证)
- 不引入 IP 限制 / basicauth / fail2ban / VPN
- 操作便利性优先,mu 接受公网暴露

下面 3 项是 **VPS / 币安账户层标准做法**(跟 trader 应用层防护无关,不是"升级"),Phase 4 真盘前 mu 必做:

1. `GF_SECURITY_ADMIN_PASSWORD` ≥16 字符随机串(Grafana 自己的认证,已包含在 trader 应用层决策内)
2. 币安 API key IP 白名单(币安账户保护,防 API key 泄露被滥用)
3. SSH 禁用密码登录(VPS 自身保护,`/etc/ssh/sshd_config` 加 `PasswordAuthentication no`)
