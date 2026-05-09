#!/usr/bin/env bash
# db-test-migration.sh — verify 0001_init.up.sql / .down.sql idempotency.
#
# Spins a temporary timescaledb container, runs `up → down → up`, and checks
# the public-schema table count at each step (expect 12 → 0 → 12).
# Requires: docker, golang-migrate CLI (`make bootstrap` installs it).
set -euo pipefail

CONTAINER=trader-migrate-test
PORT=15432
DB_URL="postgres://trader:trader@localhost:${PORT}/trader?sslmode=disable"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
MIG_DIR="${ROOT}/internal/storage/postgres/migrations"

cleanup() {
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

require() {
    command -v "$1" >/dev/null 2>&1 || { echo "missing dependency: $1"; exit 2; }
}
require docker
require migrate

echo "==> Starting timescaledb container on :${PORT}"
docker run -d --rm --name "$CONTAINER" \
    -e POSTGRES_USER=trader \
    -e POSTGRES_PASSWORD=trader \
    -e POSTGRES_DB=trader \
    -p "${PORT}:5432" \
    timescale/timescaledb:latest-pg16 >/dev/null

echo "==> Waiting for postgres to accept connections"
for i in $(seq 1 30); do
    if docker exec "$CONTAINER" pg_isready -U trader >/dev/null 2>&1; then
        break
    fi
    sleep 1
    if [ "$i" -eq 30 ]; then
        echo "postgres did not become ready in 30s"
        exit 1
    fi
done

count_tables() {
    # Exclude golang-migrate's own schema_migrations bookkeeping table —
    # we only count the application-domain tables defined by 0001_init.up.sql.
    docker exec "$CONTAINER" psql -U trader -d trader -tAc \
        "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE' AND table_name <> 'schema_migrations';" \
        | tr -d '[:space:]'
}

run_up()   { migrate -path "$MIG_DIR" -database "$DB_URL" up; }
run_down() { migrate -path "$MIG_DIR" -database "$DB_URL" down 1; }

echo "==> migrate up #1"
run_up
n=$(count_tables)
echo "    tables: $n (expected 12)"
[ "$n" = "12" ] || { echo "FAIL: expected 12 tables, got $n"; exit 1; }

echo "==> migrate down"
run_down
n=$(count_tables)
echo "    tables: $n (expected 0)"
[ "$n" = "0" ] || { echo "FAIL: expected 0 tables after down, got $n"; exit 1; }

echo "==> migrate up #2 (re-apply)"
run_up
n=$(count_tables)
echo "    tables: $n (expected 12)"
[ "$n" = "12" ] || { echo "FAIL: re-apply produced $n tables"; exit 1; }

echo "==> All checks PASS"
