#!/usr/bin/env bash
# =============================================================================
# bootstrap.sh — VPS 首次部署
#
# 1. 装 Docker + Docker Compose plugin (若未装)
# 2. 校验 .env 必需项
# 3. 拉镜像 + 启动所有服务
# 4. 等待 PG 就绪 + 跑迁移
# 5. 健康检查
#
# Usage: sudo bash scripts/bootstrap.sh
# =============================================================================

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# Color helpers
red()    { printf "\033[31m%s\033[0m\n" "$*"; }
green()  { printf "\033[32m%s\033[0m\n" "$*"; }
yellow() { printf "\033[33m%s\033[0m\n" "$*"; }
blue()   { printf "\033[34m%s\033[0m\n" "$*"; }

step() { blue "▶ $*"; }
ok()   { green "  ✓ $*"; }
err()  { red "  ✗ $*"; exit 1; }

# -----------------------------------------------------------------------------
# 0. 校验环境
# -----------------------------------------------------------------------------
step "检查运行环境"

if [[ "$EUID" -ne 0 ]]; then
    err "请用 sudo 运行: sudo bash scripts/bootstrap.sh"
fi

if ! grep -qi 'ubuntu\|debian' /etc/os-release 2>/dev/null; then
    yellow "  ⚠ 仅在 Ubuntu / Debian 测试过, 其它系统可能需要调整"
fi

ok "环境检查通过"

# -----------------------------------------------------------------------------
# 1. 校验 .env
# -----------------------------------------------------------------------------
step "校验 .env"

if [[ ! -f "$REPO_ROOT/.env" ]]; then
    err ".env 不存在, 请: cp .env.example .env && nano .env"
fi

# 锁定 .env 权限 (VPS 公网部署防其他用户读 secret)
chmod 600 "$REPO_ROOT/.env"
ok ".env 权限锁定 600"

# 必需项
REQUIRED_VARS=(
    BINANCE_API_KEY
    BINANCE_API_SECRET
    TG_BOT_TOKEN
    TG_CHAT_ID
    DATABASE_URL
    REDIS_URL
    GF_SECURITY_ADMIN_PASSWORD
    DOMAIN
    ACME_EMAIL
)

set -a; source "$REPO_ROOT/.env"; set +a

for var in "${REQUIRED_VARS[@]}"; do
    if [[ -z "${!var:-}" ]]; then
        err ".env 缺少 $var"
    fi
done

# Mainnet 二次确认
if [[ "${TRADER_MODE:-}" == "mainnet" ]]; then
    if [[ "${TRADER_MAINNET_CONFIRM:-}" != "I_UNDERSTAND" ]]; then
        err "TRADER_MODE=mainnet 必须配合 TRADER_MAINNET_CONFIRM=I_UNDERSTAND"
    fi
    yellow "  ⚠ TRADER_MODE=mainnet — 真实资金即将启动"
    sleep 3
fi

ok ".env 校验通过 (TRADER_MODE=$TRADER_MODE)"

# pool 模式: 校验 proxies.txt 存在且非空 (避免 trader 启动即崩溃)
if [[ "${BINANCE_PROXY_MODE:-}" == "pool" ]]; then
    _pfile="${BINANCE_PROXY_POOL_FILE:-}"
    if [[ -z "$_pfile" ]]; then
        err "BINANCE_PROXY_MODE=pool 但 BINANCE_PROXY_POOL_FILE 未设置"
    fi
    [[ -f "$_pfile" ]] || err "BINANCE_PROXY_POOL_FILE=$_pfile 不存在, 请创建并填入代理 URL (每行一个)"
    _pcnt=$(grep -cE '^[^#[:space:]]' "$_pfile" 2>/dev/null || echo 0)
    [[ "$_pcnt" -gt 0 ]] || err "BINANCE_PROXY_POOL_FILE=$_pfile 无有效代理 URL"
    ok "代理池 $_pfile: $_pcnt 个 URL"
fi

# -----------------------------------------------------------------------------
# 1.5 DNS 校验 (DOMAIN 应解析到当前 VPS 公网 IP)
# -----------------------------------------------------------------------------
step "DNS 校验"
if ! command -v dig >/dev/null 2>&1; then
    yellow "  dig 未安装, 安装 dnsutils..."
    apt-get install -y dnsutils
fi

VPS_IP="$(curl -fs --max-time 5 https://api.ipify.org 2>/dev/null || curl -fs --max-time 5 https://icanhazip.com 2>/dev/null || echo '')"
[[ -n "$VPS_IP" ]] || err "无法获取 VPS 公网 IP (api.ipify.org / icanhazip.com 都 fail)"

DNS_IP="$(dig +short "$DOMAIN" @8.8.8.8 2>/dev/null | tail -1)"
[[ -n "$DNS_IP" ]] || err "DOMAIN=$DOMAIN 未解析 (DNS 未配 / 未生效, 等 5-10min 重试)"

if [[ "$DNS_IP" != "$VPS_IP" ]]; then
    err "DOMAIN=$DOMAIN 解析到 $DNS_IP, 不匹配 VPS 公网 IP $VPS_IP — 请检查 DNS A 记录"
fi
ok "DNS: $DOMAIN → $VPS_IP"

# -----------------------------------------------------------------------------
# 2. 装 Docker
# -----------------------------------------------------------------------------
step "检查 / 安装 Docker"

if ! command -v docker >/dev/null 2>&1; then
    yellow "  Docker 未安装, 开始安装..."
    apt-get update
    apt-get install -y ca-certificates curl gnupg
    install -m 0755 -d /etc/apt/keyrings
    curl -fsSL https://download.docker.com/linux/ubuntu/gpg \
        | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
    chmod a+r /etc/apt/keyrings/docker.gpg
    
    . /etc/os-release
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] \
https://download.docker.com/linux/${ID} ${VERSION_CODENAME} stable" \
        > /etc/apt/sources.list.d/docker.list
    
    apt-get update
    apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
    systemctl enable docker
    systemctl start docker
    ok "Docker 安装完成"
else
    ok "Docker 已安装: $(docker --version)"
fi

if ! docker compose version >/dev/null 2>&1; then
    err "Docker Compose plugin 缺失"
fi
ok "Docker Compose: $(docker compose version --short)"

# -----------------------------------------------------------------------------
# 3. 设置 VPS 时区(对齐应用层)
# -----------------------------------------------------------------------------
step "设置 VPS 时区为 Asia/Shanghai"
timedatectl set-timezone Asia/Shanghai
ok "VPS 时区: $(timedatectl show -p Timezone --value)"

# -----------------------------------------------------------------------------
# 4. 准备数据目录
# -----------------------------------------------------------------------------
step "准备数据目录"
mkdir -p deploy/data/{postgres,redis,prometheus,loki,grafana,caddy,caddy-config}
mkdir -p backups
ok "数据目录就绪"

# -----------------------------------------------------------------------------
# 4.5 配置 ufw 防火墙 (在起服务前)
# -----------------------------------------------------------------------------
step "配置 ufw 防火墙"
bash "$REPO_ROOT/scripts/setup-ufw.sh" || err "ufw 配置失败"
ok "ufw 已启用 (22/80/443 开放, 内部端口 deny)"

# -----------------------------------------------------------------------------
# 5. 启动基础设施
# -----------------------------------------------------------------------------
step "启动基础设施 (PG / Redis / Prometheus / Grafana / Loki)"
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml up -d --build
ok "服务已启动"

# -----------------------------------------------------------------------------
# 6. 等待 PG 就绪 + 跑迁移
# -----------------------------------------------------------------------------
step "等待 PostgreSQL 就绪"
for i in $(seq 1 30); do
    if docker compose -f deploy/docker-compose.yml exec -T postgres \
        pg_isready -U trader >/dev/null 2>&1; then
        ok "PG 就绪"
        break
    fi
    sleep 2
    if [[ $i -eq 30 ]]; then
        err "PG 60s 未就绪"
    fi
done

step "运行数据库迁移"
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml \
    run --rm --no-deps --entrypoint="" trader \
    sh -c '/app/migrate -path /app/migrations -database "$DATABASE_URL" up' \
    || err "迁移失败"
ok "迁移完成"

step "重启 trader (迁移完成后确保进程状态一致)"
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml \
    restart trader
ok "trader 已重启"

# -----------------------------------------------------------------------------
# 6.5 等待 Caddy + Let's Encrypt 证书 (30-90s)
# -----------------------------------------------------------------------------
step "等待 Caddy + Let's Encrypt 证书 (最多 90s)"
for i in $(seq 1 30); do
    if curl -fs --max-time 3 "https://$DOMAIN/health" >/dev/null 2>&1; then
        ok "HTTPS + cert OK ($DOMAIN)"
        break
    fi
    sleep 3
    if [[ $i -eq 30 ]]; then
        yellow "  ⚠ Caddy cert 90s 仍未就绪 (Let's Encrypt 偶尔较慢, 不阻塞 bootstrap)"
        yellow "    排查: docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml logs caddy"
        yellow "    常见原因: DNS 未生效 / 80 端口被占 / Let's Encrypt rate limit"
    fi
done

# -----------------------------------------------------------------------------
# 7. 健康检查
# -----------------------------------------------------------------------------
step "健康检查"
sleep 5
bash "$REPO_ROOT/scripts/healthcheck.sh" || err "健康检查失败"

# -----------------------------------------------------------------------------
# 完成
# -----------------------------------------------------------------------------
echo
green "╔════════════════════════════════════════╗"
green "║  ✅ Bootstrap 完成                      ║"
green "╚════════════════════════════════════════╝"
echo
echo "下一步:"
echo "  - 查看日志:         docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml logs -f trader"
echo "  - Health:          https://$DOMAIN/health"
echo "  - Grafana:         https://$DOMAIN/grafana (admin / \$GF_SECURITY_ADMIN_PASSWORD)"
echo "  - 状态摘要:         bash scripts/status.sh"
echo "  - 后续更新:         bash scripts/update.sh"
echo "  - 备份 (加 cron):   bash scripts/db-backup.sh"
echo
