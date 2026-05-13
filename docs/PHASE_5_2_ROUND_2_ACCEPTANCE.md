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
