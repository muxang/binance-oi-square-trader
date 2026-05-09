# Phase 0 Acceptance Report

> Status: **✅ PASS**. All 11 SPEC §「Phase 0 Acceptance」 items green.
> Last verified: 2026-05-09 via `bash scripts/e2e-phase0.sh` (WSL Ubuntu),
> end-to-end 21/21 PASS.

---

## Acceptance Matrix (SPEC §Phase 0 Acceptance)

| # | Item | Status | Evidence |
|---|------|--------|----------|
| 1 | go.mod / Makefile / 目录结构创建 | ✅ | `phase-0.1` (go.mod 1.25 + 9 deps) + `phase-0.2` (18 doc.go scaffolding 13 leaf packages + 5 pkg/) |
| 2 | docker-compose 起 PG/Redis/Prom/Grafana/Loki | ✅ | `deploy/docker-compose.yml` defines 5 services; e2e step 4 brings up postgres+redis (the deps trader needs at Phase 0) — Prom/Grafana/Loki are orchestration-ready, no app code consumes them yet |
| 3 | `make migrate` 跑完表结构正确 | ✅ | `phase-0.8` `db-test-migration.sh` 12→0→12 idempotency PASS + e2e steps 6-7 PASS (12 business tables, schema_migrations excluded) |
| 4 | `make dev` 启动 + `/health` ok | ✅ | `phase-0.10` (cmd/trader/main.go wires config→logger→proxy→client→pg→redis→Echo) + e2e steps 8-13 PASS (status=ok, deps.pg=ok, deps.redis=ok) |
| 5 | `make typecheck` (vet + lint) 全绿 | ✅ | e2e step 1 `go vet` PASS + `golangci-lint run ./cmd/... ./internal/...` exit 0, no findings |
| 6 | zerolog 正常输出 | ✅ | `phase-0.5` (Init + StartupBanner) + e2e step 12 banner 5 fields all match (mode/proxy_mode/timezone/utc_now/bjt_now) |
| 7 | `.env.example` 完整 + README 写明启动步骤 | ✅ | scaffold预置 + `phase-0.10` 行内注释修复(viper-safe)+ README "Quick Start" section |
| 8 | `TRADER_MODE=testnet` 默认 + 启动 banner 显示 | ✅ | `phase-0.3` (default via SetDefault) + `phase-0.5` banner + e2e step 10 (`/health` mode=testnet) + step 12 (banner contains `mode=testnet`) |
| 9 | Proxy 三种模式 + 单测覆盖 | ✅ | `phase-0.6` proxy.go (none/single/pool, round_robin/random) + 17 unit cases incl. `TestPool_PassiveRecovery`、`TestPool_Concurrent_HTTPClient_Race` PASS |
| 10 | 时区生效: 内部 UTC, 显示 BJT, daily reset 走 BJT | ✅ | `phase-0.4` timez 4 cases PASS (`TestTodayStartBJT` 4 边界) + `scripts/check-time-now.sh` CI 守卫 + banner 同时打印 `utc_now` 和 `bjt_now` 两时间戳 |
| 11 | `binance.Client` 写请求 hard-block + listenKey 白名单 | ✅ | `phase-0.7` doRequest 单一 egress + `TestDoWrite_HardBlock_Exhaustive` 8 paths(7 普通 + listenKey)+ `TestIsListenKeyPath` 防止 prefix 绕过(/fapi/v1/listenKeyAdmin 必失败) |

**11/11 ✅**.

---

## A. 已知遗留(进 Phase 1+)

- **速率限制**:`binance.NewNoopRateLimiter()` 是 Phase 0 占位,业务调用约定的 `RateLimiter.Acquire(ctx, weight)` 接口已定;Phase 1 切真 token bucket 不改业务代码。
- **Sentry**:`SENTRY_DSN` 配置已加载,`sentry.Init()` 未调用 — 等 Phase 1+ 真有错要上报再接。
- **WS user data stream**:`ProxyManager.WSDialer()` 已支持 http(s)/socks5 代理,但消费者(user data stream → ORDER_TRADE_UPDATE 等)留 Phase 4。
- **`go mod tidy`**:跨整个 Phase 0 未跑过 — 所有依赖通过 `go get @latest` 渐进添加。Phase 1 收尾时跑一次 tidy,确认依赖图无未使用项。
- **lint 配置 `.golangci.yml`**:启用 14 个 linter,Phase 0 全过;Phase 1 业务代码扩张时若新 linter 噪声大,再调白名单。

## B. 累计代码量(by `find + wc`)

| 类别 | 行数 |
|---|---|
| Go 源码(非测试) | 1,396 |
| Go 测试代码 | 1,483 |
| SQL(migrations) | 208 |
| Shell 脚本 | 653 |
| **Phase 0 项目代码总计** | **3,740** |

测试代码比源码多 6.2% — 反映金钱安全路径的 zero-tolerance 测试覆盖原则
(CLAUDE.md §2)。

## C. 累计测试 case(`go test -v` 计数)

| 包 | case 数(含 subtests) |
|---|---|
| `internal/binance` | 46 |
| `internal/config` | 21 |
| `internal/pkg/logger` | 8 |
| `internal/api`(server+handlers)| 5 |
| `internal/pkg/timez` | 4 |
| **总计** | **84** |

最大集中在 `binance` — proxy/client/sign/errors 四模块都涉及金钱安全路径,
每个分支必有 case。

## D. Phase 0 暴露并修复的真 bug

| # | bug | 暴露环节 | 修复 commit |
|---|-----|---------|-------------|
| 1 | viper `bindEnvFromTags` 反射递归把 leaf struct 当作容器 | `phase-0.3` 穷举测试 `TestLoad_AllFieldsBound` 抓到:`decimal.Decimal` 和 `time.Time` 是 struct 类型,函数原本只看 `Kind() == Struct` 就递归,导致 `MARGIN_PER_TRADE_FULL`、`WATCHLIST_MIN_VOLUME_USD` 等 decimal 字段 BindEnv 没被注册,Unmarshal 静默 zero — **若不被穷举测试抓住,实盘 sizing 会算出零金额、永远不下单**。 | `phase-0.3` |
| 2 | `.env.example` 行内注释污染配置值 | `phase-0.10` 真跑 `make dev` 才暴露:`LOG_FORMAT=pretty    # pretty (dev) / json (prod)` 这种行内注释 viper env-parser **不剥离**,`cfg.Log.Format` 实际是整段字符串,Format 比较失败 fallback 到 JSON 输出。`LOG_LEVEL` 同理被静默 fallback 到 InfoLevel(隐藏更深)。 | `phase-0.10`(.env.example 把注释移到值上方 + 顶部加 viper 行为说明) |

两个 bug 都是"不真跑就发现不了"的类型 — 印证 0.8 起立的"e2e 真跑"标准。

---

## Verification command

```bash
bash scripts/e2e-phase0.sh
```

Run from WSL/Linux (Windows cmd 不支持). Requires: `make`, `go 1.25+`, `docker`, `migrate` CLI, `curl`. Auto-creates `.env` with dummy values if missing. Idempotent (cleanup via trap on EXIT).

E2e 最近一次完整运行结果见 README / commit log 的 phase-0.11 batch 报告。

---

## Phase 0 git history

```
8803f49 chore: scope go test/vet to actual source dirs
a32655c feat(api): main wire-up + /health endpoint
7825d67 feat(storage): postgres + timescaledb migrations (12 tables)
e848d92 feat(binance): client core + sign + error classification
f89020f feat(binance): proxy manager (none/single/pool) + WS dialer
b4dce2f feat(logger): zerolog setup with sanitized startup banner
76372bb chore: tune watchlist + square hashtag concurrency params
168d310 chore: add test-race make target with WSL bridge
7838299 feat(timez): UTC/BJT time helpers + ban time.Now() in CI
cd75bad feat(config): viper-based config loader with mainnet gate
bfae736 chore: scaffold internal package directories with doc.go
825d5d3 chore: align go.mod/Makefile/Dockerfile to Go 1.25, CGO=0
3306a91 init
```

每个 commit 在它本身的 checkout 状态下可独立编译 + 测试 PASS(CLAUDE.md §22)。
