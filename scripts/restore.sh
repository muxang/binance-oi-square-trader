#!/usr/bin/env bash
# =============================================================================
# restore.sh — 从 backups/*.sql.gz 恢复 PostgreSQL
#
# 流程:
#   1. 校验 backup 文件 + 格式 (.sql.gz)
#   2. mu 必须 type "yes-i-want-to-restore" 确认 (防误操作)
#   3. 停 trader (保留 PG/Redis/Prometheus/Grafana/Loki 基础设施)
#   4. drop + recreate trader DB
#   5. gunzip + psql restore from backup
#   6. 启 trader
#   7. healthcheck 验证
#
# 失败说明:
#   pg_restore 失败 → DB 处于不完整状态, 无法自动回滚 (PG state 已被覆盖).
#   解决: 重新跑 restore.sh 用更早一个备份, 或手工 drop + 跑迁移重建.
#
# v0.2 增强 (Phase 4 后真有价值数据时):
#   drop 前自动 pg_dump 当前 DB 到 /tmp/pre_restore_backup.sql.gz,
#   restore 失败自动 restore pre_restore_backup (cross-Round dependency 提示).
#
# Usage: bash scripts/restore.sh <backup-file>
#   backup-file: backups/trader-YYYYMMDD-HHMMSS.sql.gz (db-backup.sh 输出)
# =============================================================================

set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

red()    { printf "\033[31m%s\033[0m\n" "$*"; }
green()  { printf "\033[32m%s\033[0m\n" "$*"; }
yellow() { printf "\033[33m%s\033[0m\n" "$*"; }
blue()   { printf "\033[34m%s\033[0m\n" "$*"; }
step() { blue "▶ $*"; }
ok()   { green "  ✓ $*"; }
err()  { red "  ✗ $*"; exit 1; }

if [[ $# -lt 1 ]]; then
    err "Usage: bash scripts/restore.sh <backup-file>"
fi

BACKUP_FILE="$1"
[[ -f "$BACKUP_FILE" ]] || err "备份文件不存在: $BACKUP_FILE"
[[ "$BACKUP_FILE" =~ \.sql\.gz$ ]] || err "仅支持 .sql.gz 格式 (db-backup.sh 输出)"

SIZE=$(du -h "$BACKUP_FILE" | cut -f1)
MTIME=$(date -r "$BACKUP_FILE" '+%Y-%m-%d %H:%M:%S' 2>/dev/null || stat -c %y "$BACKUP_FILE" 2>/dev/null | cut -d. -f1)

yellow "============================================================"
yellow "⚠️ 即将从备份恢复 PostgreSQL"
yellow "  备份文件:   $BACKUP_FILE"
yellow "  备份大小:   $SIZE"
yellow "  备份时间:   $MTIME"
yellow "  当前 DB:    覆盖 (无法自动回退)"
yellow "  影响范围:   trader 容器 ~30s 不可用, PG/Redis 不停"
yellow "============================================================"
echo
read -r -p '请输入 "yes-i-want-to-restore" 确认: ' CONFIRM
if [[ "$CONFIRM" != "yes-i-want-to-restore" ]]; then
    err "确认串不匹配, 取消"
fi

COMPOSE="docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.prod.yml"

step "停 trader 容器 (保留基础设施)"
$COMPOSE stop trader || err "停 trader 失败"
ok "trader 已停"

step "drop + recreate trader DB"
$COMPOSE exec -T postgres psql -U trader -d postgres -c \
    "DROP DATABASE IF EXISTS trader;" >/dev/null \
    || err "drop DB 失败"
$COMPOSE exec -T postgres psql -U trader -d postgres -c \
    "CREATE DATABASE trader OWNER trader;" >/dev/null \
    || err "create DB 失败"
ok "DB 重建"

step "psql restore from $BACKUP_FILE"
gunzip -c "$BACKUP_FILE" | $COMPOSE exec -T postgres psql -U trader -d trader >/dev/null \
    || err "psql restore 失败 — DB 不完整, 用更早备份重跑或手工修复"
ok "数据恢复完成"

step "启 trader"
$COMPOSE up -d trader || err "启 trader 失败"
ok "trader 已启"

step "等待 trader 就绪 (10s) + 健康检查"
sleep 10
bash "$REPO_ROOT/scripts/healthcheck.sh" || err "healthcheck 失败 — 检查 trader 日志"

echo
green "✅ Restore 完成 ($BACKUP_FILE)"
echo "查看日志: $COMPOSE logs -f --tail=100 trader"
