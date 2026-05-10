#!/usr/bin/env bash
# =============================================================================
# setup-ufw.sh — VPS firewall 配置 (Ubuntu ufw, deployment v0.1 Round 3)
#
# 默认策略: deny incoming + allow outgoing
# 开放: 22 (SSH) / 80 / 443 (Caddy)
# 显式 deny: 5432 (PG) / 6379 (Redis) / 2112 (metrics) / 3001 (Grafana) /
#           9090 (Prometheus) / 3100 (Loki)
#           — docker-compose.yml 已绑 127.0.0.1, ufw deny 是双重保险防 docker 漂移
#
# Idempotent: 重复跑不破坏现有规则 (ufw 自动去重)
#
# Usage: sudo bash scripts/setup-ufw.sh
# =============================================================================

set -euo pipefail

if [[ "$EUID" -ne 0 ]]; then
    echo "请用 sudo 运行: sudo bash scripts/setup-ufw.sh" >&2
    exit 1
fi

if ! command -v ufw >/dev/null 2>&1; then
    echo "▶ ufw 未安装, 安装中..."
    apt-get update -qq
    apt-get install -y ufw
fi

echo "▶ 默认策略: deny incoming + allow outgoing"
ufw --force default deny incoming
ufw --force default allow outgoing

echo "▶ 开放端口"
ufw allow 22/tcp comment 'SSH'
ufw allow 80/tcp comment 'Caddy HTTP (auto redirect to 443)'
ufw allow 443/tcp comment 'Caddy HTTPS'

echo "▶ 显式 deny 内部服务端口 (双重保险)"
ufw deny 5432/tcp comment 'Postgres internal-only'
ufw deny 6379/tcp comment 'Redis internal-only'
ufw deny 2112/tcp comment 'trader metrics — via Caddy /metrics'
ufw deny 3001/tcp comment 'Grafana — via Caddy /grafana'
ufw deny 9090/tcp comment 'Prometheus internal-only'
ufw deny 3100/tcp comment 'Loki internal-only'

echo "▶ 启用 ufw"
ufw --force enable

echo
echo "▶ 当前 ufw 状态:"
ufw status numbered
