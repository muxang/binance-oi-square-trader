# DB Migrations

Migration tool: [`golang-migrate`](https://github.com/golang-migrate/migrate)
(installed by `make bootstrap`). The Makefile wraps it as `make migrate` /
`make migrate-down`.

## Layout

Files are versioned `NNNN_<name>.up.sql` / `NNNN_<name>.down.sql`. NNNN is a
4-digit sequence; `up` applies the change forward, `down` reverts it.

```
0001_init.up.sql       Initial schema (12 tables, anchored to ARCHITECTURE §7)
0001_init.down.sql     Reverse — drops every table in FK-safe order
0002_<feature>.up.sql  Future schema change
0002_<feature>.down.sql
...
```

## Running

```sh
# Apply all pending migrations.
make migrate

# Roll back the most recent migration.
make migrate-down

# Local idempotency check (Docker required, see scripts/db-test-migration.sh).
bash scripts/db-test-migration.sh
```

`DATABASE_URL` must be exported (sourced from `.env` or shell). Format:
`postgres://trader:trader@localhost:5432/trader?sslmode=disable`.

## Adding a new migration

1. Create both files with the next sequence number:
   ```
   internal/storage/postgres/migrations/0002_<short_name>.up.sql
   internal/storage/postgres/migrations/0002_<short_name>.down.sql
   ```
2. `up.sql` applies the change; `down.sql` reverses it cleanly (FK children
   dropped before parents; columns added in up are dropped in down; etc.).
3. **Already-released migrations are immutable.** If you need to fix something
   in `0001`, write `0002` that corrects it. Editing applied migration files
   breaks every deployed environment that already ran them — Postgres records
   the version in `schema_migrations` and won't re-run the same number.
4. Verify locally with `bash scripts/db-test-migration.sh` (up → down → up).

## Schema rules (enforced by review, not by tooling)

- **All time columns are `TIMESTAMPTZ`.** Never `TIMESTAMP` — that drops timezone
  info and silently misaligns with the UTC discipline (CLAUDE.md §17, time zone).
- **All money columns are `NUMERIC(36, 18)`.** Never `FLOAT` / `DOUBLE PRECISION`
  — float rounding propagates into PnL math (CLAUDE.md §19).
- **Time-series tables are hypertables.** `oi_history`, `klines`,
  `square_hashtag_history` get `SELECT create_hypertable(...)` immediately after
  `CREATE TABLE`, with a 1-day chunk interval.
- **FK-safe drop order in `down.sql`.** Children before parents. The current
  graph: `trade_exits` / `position_states` → `trades` → `signals`,
  `square_mentions` → `square_posts`.
- **Idempotent seeds.** `circuit_breaker_state` has `id = 1` as a single-row
  invariant; the seed `INSERT` uses `ON CONFLICT (id) DO NOTHING` so a re-up
  after a partial restore does not violate the CHECK.
- **No new tables outside `ARCHITECTURE.md §7`.** A new table requires updating
  the doc first; reviewers check that any added migration corresponds to a §7
  entry.
