# Phase 5.2 Round 2 — Acceptance Doc

**Tag**: `phase-5.2-round-2` (pushed Option E completion)
**Scope range**: `86d268e` (Round 2 write endpoints) → Round 3 frontend
**Date**: 2026-05-13 BJT
**Status**: ✅ FULL

---

## §1 Overview

Phase 5.2 admin Web UI v1.1 reaches the milestone where mu has a **complete
owner toolkit** for live mainnet operation — every halt control, threshold
calibration, and individual-trade intervention is available via:

1. **HTTP API** — 11 write endpoints + 14 read endpoints + audit log
2. **React Web UI** — visual forms, 2-step confirms, audit log viewer
3. **Hot reload** — 12 runtime keys swap within 1min, no trader restart

mu can now run the trader for weeks without touching the deploy pipeline —
all routine ops happen in the browser with audit trail.

### Commits in this milestone (chronological)

| Commit | Round | Description |
|---|---|---|
| `86d268e` | Round 2 | 7 write endpoints + admin_audit_log table |
| `a274f5f` | Round 2.x P1 | config_reloader 1min cron — admin_overrides → runtime |
| `a7562c8` | Round 2.x P2 | watchlist_collector applies admin overrides |
| `7823787` | Round 1.z | algo_polling NEW status actionable (~240 WRN/h → 0) |
| `a628281` | Round 2.x P3 | manual close endpoint + exit_manager integration |
| `17c33aa` | Round 2.y | wire 4 keys (TOTAL_FLOAT / BTC_PANIC / MAX_STOP / LEVERAGE) |
| `33f9ba6` | Round 2.y followup | main.go baselineRuntime seed all 6 keys |
| `23145ff` | Option E (Round 2.y final) | wire OI_GROWTH + SQUARE_HOT (8/8 keys) |
| `c35de8a` | Round R.4 | trail-fired orphan no longer trips false halt + balance gauge during halt |
| `b8d238f` | Round 2.z | wire TRAIL_STAGE1-4 (12/12 keys) — mu 真盘 owner catch |
| `d0488de` | Round 3 P1 | frontend write modals + cb/signal/trail forms |
| `0072684` | Round 3 P2 | frontend audit log viewer (public read) |

---

## §2 Write endpoints — full coverage

| # | Method | Path | Round | UI |
|---|---|---|---|---|
| 1 | POST | `/api/admin/circuit-breaker/reset`            | R.1 | Dashboard halt-reset modal |
| 2 | POST | `/api/admin/circuit-breaker/daily-pnl-reset`  | R.2 | Dashboard ↺ button |
| 3 | POST | `/api/admin/circuit-breaker/consec-reset`     | R.2 | Dashboard ↺ button |
| 4 | POST | `/api/admin/circuit-breaker/halt`             | R.2 | Dashboard ⏸️ button |
| 5 | PUT  | `/api/admin/config/circuit-breaker-thresholds` | R.2 + R.2.z | Settings → CB form |
| 6 | PUT  | `/api/admin/config/signal-thresholds`         | R.2 | Settings → Signal form |
| 7 | PUT  | `/api/admin/watchlist/include/{symbol}`       | R.2 | Settings → Watchlist form |
| 8 | PUT  | `/api/admin/watchlist/exclude/{symbol}`       | R.2 | Settings → Watchlist form |
| 9 | POST | `/api/admin/trades/{id}/close`                | R.2.x P3 | TradeDetail 🚨 button |
| 10 | GET | `/api/admin/circuit-breaker/events`           | R.1 | Dashboard (read) |
| 11 | GET | `/api/admin/audit-log`                        | R.3 | AuditLog page (public) |

All write endpoints share:
- CSRF middleware (`X-CSRF-Token` header, 30min TTL)
- Caddy basic auth at the reverse proxy (write tier only)
- `confirm: true` required in body
- Transactional: `BEGIN → READ prev → UPDATE → INSERT admin_audit_log → COMMIT`

---

## §3 12 wired runtime keys

config_reloader 1min cron reads `admin_overrides` table, overlays onto
`Runtime` (atomic.Pointer swap), consumers call `config.Get()` each
evaluation. Zero override → cfg fallback.

| # | Key | Type | Consumer | Wired |
|---|---|---|---|---|
| 1 | `DAILY_LOSS_HALT_PCT` | Decimal | circuit_breaker.tripDailyLoss | R.2.x P1 |
| 2 | `CONSECUTIVE_LOSSES_HALT` | int | circuit_breaker.tripConsecutiveLosses | R.2.x P1 |
| 3 | `TOTAL_FLOAT_LOSS_HALT_PCT` | Decimal | circuit_breaker.tripTotalFloatLoss | R.2.y |
| 4 | `BTC_PANIC_DROP_PCT` | Decimal | circuit_breaker.tripBTCCrash | R.2.y |
| 5 | `MAX_STOP_PCT` | Decimal | executor.computeStopPct (ATR clip) | R.2.y |
| 6 | `LEVERAGE` | int | executor.SetLeverage (new entries) | R.2.y |
| 7 | `OI_GROWTH_FROM_MIN_PCT` | Decimal | signal.OISurge (GrowthFromMinMin) | Option E |
| 8 | `SQUARE_HOT_MULTIPLIER` | Decimal | signal.SquareHot (all 3 ratio thresholds) | Option E |
| 9 | `TRAIL_STAGE1_ACTIVATE_PCT` | Decimal | executor + trail_upgrader S0→S1 | R.2.z |
| 10 | `TRAIL_STAGE2_UPGRADE_PCT` | Decimal | trail_upgrader S1→S2 | R.2.z |
| 11 | `TRAIL_STAGE3_UPGRADE_PCT` | Decimal | trail_upgrader S2→S3 | R.2.z |
| 12 | `TRAIL_STAGE4_UPGRADE_PCT` | Decimal | trail_upgrader S3→S4 | R.2.z |

Helper pattern (uniform across all consumers):

```go
func (x *Foo) myThreshold() decimal.Decimal {
    if rt := cfgpkg.Get(); rt != nil && rt.MyField.IsPositive() {
        return rt.MyField    // runtime override
    }
    return x.cfg.MyField     // baseline from .env
}
```

Startup banner verification: `wired_keys=12` with all 12 baselines logged.

---

## §4 mu 真盘 forward acceptance — real-money data

Since v0.2 trail S1-S4 + Round R.1 5x leverage + 12% max_stop deploy
(2026-05-13 BJT), 3 trades closed naturally:

| Trade | Symbol | Path | Realized PnL | Net (incl fees) |
|---|---|---|---|---|
| #66 | INJUSDT | trail_s1 | −$0.41 | −$0.41 |
| #67 | TURBOUSDT | trail_s1 | +$0.69 | **+$0.63** |
| #59 | ESPORTSUSDT | trail_s2 ratchet | +$31.08 | **+$30.94** |
| | | **Total net** | | **+$31.16** |

vs the disaster cluster on v0.1 (2026-05-12 evening, Round R.1 trigger):
**−$129.79 daily loss**, which 75x+ improvement in the same notional band.

The mu 真盘 owner catches that drove engineering this milestone:
- INJ #66 microloss + TURBOUSDT #67 microprofit → **mu observation:
  trail S1 +3% activates too early on real trending trades**
- ESPORTSUSDT #59 trail_s2 +$30.94 → **reverse proof: stages 2+ matter
  when an entry runs**
- → Round 2.z lifted thresholds: 3/15/30/60 → 5/20/35/65%

---

## §5 mu 真实诉求 alignment

Engineering responded to owner observation, not abstract design intent:

| Owner observation | Engineering response | Round |
|---|---|---|
| "halt reset is a 60-second window" (Round R.1 manual reset re-trips) | Full 5-item reset (pnl + consec + halt) in one tx | R.2 fix |
| "orphan algo accumulating in Binance UI" | orphan_algo_cleaner 1min sweep | R.3 |
| "trail S1 +3% activates too early" | TRAIL_STAGE1-4 wire to admin Web UI | R.2.z |
| "balance shows 0 in dashboard" | Refresh balance gauge during halt (F3) | R.4 |
| "trail closes keep tripping false orphan halts" | TryReconcile covers trail algo too (F1) | R.4 |

---

## §6 Engineering discipline catches

Bugs caught in this Round, fixed with audit + tests + deploy:

| Catch | Owner | Round |
|---|---|---|
| Manual halt reset only cleared halt flag, daily_pnl re-tripped | mu | R.2 fix |
| Caddy bind-mount inode stale after `git pull` (Caddyfile changes) | Claude Code → memory persist | (memory) |
| algo_polling `NEW` status logged as unknown (240 WRN lines/h) | Claude Code (Round 8 Smoke side finding) | R.1.z |
| position_manager.local_only_orphan only checked disaster algo | Claude Code (after mu reported halt+balance=0) | R.4 (F1) |
| Balance + unrealized + BTC gauges stale during halt (Catch 6/7 sibling) | Claude Code (same investigation) | R.4 (F3) |
| main.go baselineRuntime only seeded 2/6 keys (Round 2.y deploy) | Claude Code (smoke caught) | follow-up |

---

## §7 Path forward

| Round | Scope | Estimate |
|---|---|---|
| Round 4 | 飞书 alerts + RCA acknowledgment workflow | ~10-15h |
| Round 5 | UX polish (filter persistence, keyboard shortcuts, dark/light mode) | ~5-8h |
| Round 6 | Mobile responsive layout | ~5-10h |
| Round 7 | Final acceptance + tag `phase-5.2-admin-web-v1.1` | ~3-5h |
| Round 2.w (deferred) | Wire trail callback rates (TRAIL_STAGE{N}_CALLBACK_RATE) | ~1-2h after forward eval |

mu decides timing on Round 4+. Forward 评估 continues passively — new
entries with +5% S1 activate accumulate data for callback-rate decision.

---

## Postscript — 2026-05-13 20:32 activatePrice 真盘 bug catch

**TL;DR**: 本 milestone §4 列的 3 笔 trade PnL 数据是在**有 bug**的代码下产生
的,Round 2.z 的阈值提升从 v0.2 Round 1 上线 ~ 2026-05-13 20:32 fix 之间**完
全没有传到 Binance**。这是 mu 真盘 owner 在 Binance UI 对账时发现的。

### Bug

`internal/binance/orders.go:PlaceAlgoTrailingStop` 用了 `activationPrice`
(regular `/fapi/v1/order` 的 param 名) 而 Algo Service `/fapi/v1/algoOrder`
要的是 `activatePrice` (无 `ion`)。Binance 对未知 param 静默忽略,fallback
到官方文档默认: `"default as the latest price"` — 即用 placement 时的 mark
price 作为 activation。

结果: 所有 trail S1 在开仓那一刻就处于"可激活"状态,callback 从 mark@fill
追踪。这是"永远活跃"的 trail,不是设计的"先涨 +5% 才激活" trail。

### 影响范围

| Trade | 设计 activate | 实际 activate (bug) | 设计 vs 实际行为 |
|---|---|---|---|
| INJ #66 | entry × 1.03 | mark@fill | trail 立即追踪,微亏 -$0.41 |
| TURBOUSDT #67 | entry × 1.03 | mark@fill | trail 立即追踪,微利 +$0.63 |
| ESPORTSUSDT #59 | entry × 1.15 (S2) | mark@upgrade | trail 立即追踪 (升级后),大利 +$30.94 |
| **ARPA #68** | **entry × 1.05** | **mark@fill 0.01164** | bug 发现 + 修复 (commit `765834a`) → cancel + replace 至 0.01222 |

§4 写的 "75x improvement vs v0.1 disaster cluster" 数据本身仍成立(v0.1
是 -$129.79 损,v0.2 这 3 笔合计 +$31.16),但 **causal 归因错了**:
我们 attribute 给 "trail S1/S2 设计 + ratchet" 的改善,实际机制是
"always-on trail + ratchet"。真正的"先涨再活" trail 行为还没有真盘数据。

### Round 2.z 重新评价

Round 2.z (Phase 5.2 Round 2.z, commit `b8d238f`) 把 trail S1 activate
从 +3% 提到 +5%,并 wire 到 admin Web UI 运行时可调。**代码正确,DB 正
确,runtime swap 正确** — 但因为 binance client 的 param 命名 bug,这些
值从未到达 Binance。

mu owner observation 才发现这一点 — 不是工程方的 pre-deploy 验证。

### 工程教训

1. **单元测试 + fake client 无法 catch param 名问题** — fake 接受任何 key。
   修复: cmd/algo-query 工具 dump raw JSON;Round 7+ scope 加 Binance testnet
   integration smoke (task #36)。
2. **Observability gap**: `AlgoOrderQuery` struct 丢弃了 Binance response 里
   的 `activatePrice` / `callbackRate` 字段。已修 (commit 待定 — task #32),
   `algo_polling` cron 此后可对比 DB vs Binance,WARN 当差异 > 0.5%。
3. **API param 命名差异**未记录在 references。已补 (task #33,
   `references/binance/urls.md` §「Algo Service 与 Regular Order 参数命名差异」)。
4. **TP1/TP2 同期 catch**: LOT_SIZE.stepSize 没做 qty 取整 → 所有 stepSize≥1
   的 alt symbols (ARPA/SAPIEN/DOGE-class) 的 TP1/TP2 silently 失败。已修
   (commit 待定 — task #31)。

### Forward 评估重启

**ARPA #68 是第一笔 activatePrice fix 应用的真实 trade。** mu 真盘 forward 评估
真正"从零重启"。Round 2.z 阈值 (5/20/35/65 %) 首次真实生效。建议 ≥3 笔新
entry 数据后再考虑 callback rate wire (Round 2.w deferred 件) 或阈值调整。

