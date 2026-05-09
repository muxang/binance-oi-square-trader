#!/usr/bin/env bash
# =============================================================================
# db-backup.sh — PG 全量 dump
#
# 输出: backups/trader-YYYYMMDD-HHMMSS.sql.gz (BJT 时间戳)
# 保留: 默认保留最近 30 天, 旧的自动删
#
# Usage:
#   bash scripts/db-backup.sh
#
# Cron(每天 BJT 3:00):
#   0 3 * * * cd /home/user/trader && bash scripts/db-backup.sh >> /var/log/trader-backup.log 2>&1
# =============================================================================

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

BACKUP_DIR="$REPO_ROOT/backups"
RETENTION_DAYS="${BACKUP_RETENTION_DAYS:-30}"

mkdir -p "$BACKUP_DIR"

# BJT 时间戳
TS="$(TZ=Asia/Shanghai date +%Y%m%d-%H%M%S)"
OUT="$BACKUP_DIR/trader-$TS.sql.gz"

echo "▶ 备份 PG → $OUT"
docker compose -f deploy/docker-compose.yml exec -T postgres \
    pg_dump -U trader -d trader \
    | gzip > "$OUT"

SIZE="$(du -h "$OUT" | cut -f1)"
echo "✓ 备份完成: $OUT ($SIZE)"

# 清理旧备份
echo "▶ 清理 ${RETENTION_DAYS} 天前的备份"
find "$BACKUP_DIR" -name 'trader-*.sql.gz' -mtime +$RETENTION_DAYS -print -delete
echo "✓ 清理完成"

# 列出当前所有备份
echo
echo "当前备份:"
ls -lh "$BACKUP_DIR"/trader-*.sql.gz 2>/dev/null | tail -10 || echo "  (none)"
