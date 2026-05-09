# Deployment Scripts

VPS 一键部署脚本。设计目标:Ubuntu 22.04+ 全新 VPS,run 完即跑通。

## 环境前提

```
OS:        Ubuntu 22.04 / 24.04 LTS (推荐) 或 Debian 12+
权限:      首次部署需 sudo (装 docker)
配置:      .env 已填入 BINANCE_API_KEY / TG_BOT_TOKEN 等
代理:      .env 中 BINANCE_PROXY_MODE 配置完毕
```

## 脚本清单

| 脚本 | 用途 | 何时运行 |
|---|---|---|
| `bootstrap.sh` | 首次部署:装 Docker / Docker Compose / 起所有服务 / 跑迁移 | 新 VPS 第一次 |
| `deploy.sh` | 增量部署:`git pull` + 重 build + 重启 | 后续每次代码更新 |
| `db-backup.sh` | PG 全量 dump 到本地 `backups/` | 定时任务(cron) |
| `healthcheck.sh` | 全链路健康检查(应用 / DB / 币安连通) | 出问题排查时 |

## 标准工作流

### 第一次部署(VPS)

```bash
# 1. SSH 到 VPS
ssh user@vps

# 2. clone 代码
git clone <repo> ~/trader
cd ~/trader

# 3. 配置 .env
cp .env.example .env
nano .env
#   - 填 BINANCE_API_KEY / SECRET
#   - 填 TG_BOT_TOKEN / TG_CHAT_ID
#   - 填 BINANCE_PROXY_POOL_URLS (如需要)
#   - 默认 TRADER_MODE=testnet, 不要改成 mainnet

# 4. 一键部署
sudo bash scripts/bootstrap.sh

# 5. 验证
bash scripts/healthcheck.sh
```

### 后续更新

```bash
cd ~/trader
git pull
bash scripts/deploy.sh
bash scripts/healthcheck.sh
```

### 备份(放进 cron)

```bash
crontab -e
# 加: 每天 BJT 3:00 备份
0 3 * * * cd /home/user/trader && bash scripts/db-backup.sh >> /var/log/trader-backup.log 2>&1
```

### 切换到 mainnet

```bash
nano .env
# 改两行:
# TRADER_MODE=mainnet
# TRADER_MAINNET_CONFIRM=I_UNDERSTAND

bash scripts/deploy.sh
# 启动日志会有 ⚠️ 5 行 + sleep 5s, 给你最后反应时间
```

## 安全约束

- bootstrap.sh **只在首次**运行。后续重复运行会跳过已装的依赖
- deploy.sh **不会**自动改 .env,需要更新配置先手动 nano 再 deploy
- db-backup.sh 备份在本地 `backups/` 目录,**不**自动上传云端 — 自己配 rclone 或 aws cli
- 所有脚本 **set -euo pipefail**,任何一步失败立即终止
