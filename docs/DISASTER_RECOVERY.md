# DISASTER_RECOVERY — 灾难恢复 (deployment v0.1)

> Status: deployment v0.1 Round 4 文档草稿
> 配套: `scripts/db-backup.sh` / `scripts/restore.sh`

---

## §1. 备份策略

**db-backup.sh**(Phase deployment v0.1 已实施):
- 全量 `pg_dump -U trader -d trader | gzip` → `backups/trader-YYYYMMDD-HHMMSS.sql.gz`(BJT 时间戳)
- 30 天本地 retention(`-mtime +30 -delete`)
- 不自动上云(mu 自配 rclone / aws cli 推 S3 / COS)

**Cron 配置**(VPS 部署后 mu 加):
```bash
crontab -e
# 加: 每天 BJT 3:00 备份
0 3 * * * cd /home/<user>/trader && bash scripts/db-backup.sh >> /var/log/trader-backup.log 2>&1
```

**备份大小预估**:
- Phase 3 v0.1(228 signals + 0 trades):~2-5MB / 备份
- Phase 4 真下单 7-14 天:预估 ~50-100MB / 备份(取决于 OI/klines hypertable retention)
- 30 天 × 100MB = ~3GB(60GB 磁盘的 5%,可接受)

---

## §2. PG 故障恢复 — restore.sh 完整流程

```bash
# 1. 找最近一个备份
ls -lh backups/

# 2. 恢复 (必须 type "yes-i-want-to-restore" 确认)
bash scripts/restore.sh backups/trader-YYYYMMDD-HHMMSS.sql.gz

# 流程 (file header + step 输出):
#   1. 校验 backup 文件 + 格式 (.sql.gz)
#   2. 显示备份大小 + 时间, 等 mu type 确认串
#   3. docker compose stop trader (保留 PG/Redis/Caddy 不停)
#   4. drop + recreate trader DB
#   5. gunzip + psql restore
#   6. 启 trader (rebuild 不需要)
#   7. 等 10s + healthcheck.sh 验证
```

**v0.1 失败说明**(restore.sh file header L13-15 标注):
- pg_restore 失败 → DB 不完整,无 auto rollback
- 解决方案:重跑 restore.sh 用更早一个备份,或手工 drop + 跑 migrate 重建

**v0.2 增强**(Phase 4 后真有价值数据时):
- drop 前自动 `pg_dump` 当前 DB 到 `/tmp/pre_restore_backup.sql.gz`
- restore 失败自动恢复 pre_restore snapshot
- "snapshot 模式"灾难恢复(per restore.sh L17-19 cross-Round dependency 提示)

---

## §3. 代理全死 — Phase 1 §8 + Phase 2 §6.1 RCA

**症状**:7 collector 成功率断崖 → 90% → 30%(`trader_collector_runs_total{result="ok"}` 比例)。

**RCA 决策树**:

```
代理失败 > 50%
│
├─ trader_proxy_active_count = 0 (deploy/proxies.txt 全死)
│  ├─ 公共代理来源整批 ban (币安官方反爬升级) → 必切付费方案
│  └─ deploy/proxies.txt 误改空 → ls -la deploy/proxies.txt + .env BINANCE_PROXY_POOL_FILE
│
├─ 单代理失败率 > 90% 持续 1h
│  └─ deploy/proxies.txt 删除该行 → restart trader (热重载 v0.2 实施)
│
└─ 全代理慢 (响应 > 5s)
   ├─ 临时切 single 高质量 → BINANCE_PROXY_MODE=single + BINANCE_PROXY_URL=<付费>
   └─ 长期方案: 付费 Tokyo 代理池(同 VPS region 减低延迟)
```

**付费代理切换 SOP**(Phase 4 前 mu 决策):
1. 选定付费方案(如 Bright Data / Smartproxy Tokyo region)
2. 改 .env `BINANCE_PROXY_POOL_FILE=./deploy/proxies-paid.txt`
3. 上传新 proxies-paid.txt(.gitignore 同模式)
4. `docker compose restart trader`
5. 跑 healthcheck.sh + status.sh 验证

---

## §4. VPS 重启 / 迁移

### 4.1 VPS 重启(腾讯云控制台 / `sudo reboot`)

```bash
# Docker compose restart=unless-stopped → 全部容器自动重启 + healthcheck 自动恢复
# 验证:
ssh vps
bash scripts/status.sh
# 期望: 8 service 全 healthy 内 5min 恢复
```

**注意**:
- `deploy/data/` 持久化(volume mount),数据不丢
- `caddy/data/` 持久化,Let's Encrypt cert 不重新签发
- redis AOF 持久化,close-out signals 不丢

### 4.2 新 VPS 迁移(从备份恢复)

mu 选择切新 VPS / 区域(如东京 → 新加坡):

```bash
# 老 VPS
ssh old-vps
bash scripts/db-backup.sh                      # 触发立即备份
scp backups/trader-LATEST.sql.gz mu-laptop:/tmp/

# 新 VPS (按 DEPLOYMENT.md §4 SOP 一键部署)
ssh new-vps
git clone ... ~/trader && cd ~/trader
cp .env.example .env && nano .env              # 改 DOMAIN 指向新 VPS
sudo bash scripts/bootstrap.sh                  # 起空 DB

# 上传备份 + 恢复
scp mu-laptop:/tmp/trader-LATEST.sql.gz ~/trader/backups/
bash scripts/restore.sh backups/trader-LATEST.sql.gz

# DNS 切换
# letsagent.net 改 A 记录: trader.letsagent.net → 新 VPS IP
# 等 5-10min DNS 生效

# 老 VPS 退役 (1-2 天观察期后)
ssh old-vps && docker compose down -v
```

---

## §5. 网络故障排查

### 5.1 Caddy / Let's Encrypt 证书未签发

```bash
docker compose logs caddy | grep -E "tls|cert|acme"
```
**常见原因**:
- DNS 未生效(等 5-10min,或 letsagent.net TTL 过长)
- 80 端口被占(`ss -tlnp | grep :80`,确认仅 caddy 监听)
- Let's Encrypt rate limit(单域名 5 cert/week — bootstrap 重跑触发)

**解决**:`docker compose restart caddy` + 等 60s。

### 5.2 ufw 屏蔽合法流量

```bash
sudo ufw status numbered                       # 查规则
sudo ufw delete <NUM>                          # 删错误规则
sudo bash scripts/setup-ufw.sh                 # 重置标准规则 (idempotent)
```

### 5.3 币安 API 全 fail

```bash
# 走应用层探针
curl -fsSL https://trader.letsagent.net/health/binance

# 手动测代理
docker compose exec trader curl -fs --proxy <one-proxy-url> https://fapi.binance.com/fapi/v1/exchangeInfo
```
**根因**:见 §3 代理全死 RCA。

### 5.4 VPS 厂商网络 / 控制台 / 安全组

- 腾讯云东京轻量:控制台 → 防火墙 → 确保 22/80/443 开放(VPS-level firewall,在 ufw 之前)
- 腾讯云"流量包"用尽 → 限速 / 计费切换:控制台查看
- BGP / AS132203 路由抖动:`mtr fapi.binance.com`,持续 30min RTT 跳变 > 50ms 联系厂商

---

**总结**:v0.1 灾难恢复**手动驱动**(mu 决策每步),v0.2 forward 后部分自动化(snapshot rollback / proxy auto-rotate / Caddy auto-reload)。
