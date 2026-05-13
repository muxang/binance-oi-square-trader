// One-shot tool: rebuild trail S1 for a single trade with correct activatePrice.
// Used after the 2026-05-13 catch where the Algo Service param name was wrong
// (sent activationPrice, Binance Algo Service expects activatePrice) — existing
// trail algos on Binance have wrong activation. This tool cancels the old algo
// and places a new one with the corrected client code path.
//
// Usage (inside trader container):
//
//	/app/algo-rebuild-trail <trade_id>
//
// Flow:
//  1. Load trade from DB (entry_price, qty, current trail_algo_id, symbol).
//  2. Cancel old trail algo on Binance.
//  3. Compute new activatePrice = entry × (1 + TrailStage1ActivatePct), tick-aligned.
//  4. Place new trail (uses fixed binance.PlaceAlgoTrailingStop param).
//  5. UPDATE trades SET binance_trail_algo_id + trail_activation_price.
//
// Safety: between steps 2 and 4 the position has no trail. Disaster stop algo
// remains armed throughout. Window is typically <2s. Tool prints each step.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"

	"trader/internal/binance"
	"trader/internal/config"
	"trader/internal/pkg/ratelimit"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: algo-rebuild-trail <trade_id>")
		os.Exit(2)
	}
	tradeID, err := strconv.ParseInt(os.Args[1], 10, 64)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid trade_id:", err)
		os.Exit(2)
	}

	log := zerolog.New(os.Stderr).With().Timestamp().Logger()
	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("config load")
	}
	pm, err := binance.NewProxyManager(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("proxy")
	}
	limiter := ratelimit.NewTokenBucket(1920, 32)
	bc, err := binance.New(cfg, pm, limiter, log)
	if err != nil {
		log.Fatal().Err(err).Msg("binance client")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 1. Load trade + current qty from DB.
	pool, err := pgxpool.New(ctx, cfg.DB.PostgresURL)
	if err != nil {
		log.Fatal().Err(err).Msg("db connect")
	}
	defer pool.Close()

	var (
		symbol         string
		status         string
		entryPrice     pgtype.Numeric
		oldAlgoIDStr   pgtype.Text
		oldActivation  pgtype.Numeric
	)
	if err := pool.QueryRow(ctx, `
		SELECT symbol, status, entry_price, binance_trail_algo_id, trail_activation_price
		FROM trades WHERE id=$1
	`, tradeID).Scan(&symbol, &status, &entryPrice, &oldAlgoIDStr, &oldActivation); err != nil {
		log.Fatal().Err(err).Int64("trade_id", tradeID).Msg("trade not found")
	}
	if status != "open" && status != "partial" {
		log.Fatal().Str("status", status).Msg("trade not open/partial; nothing to rebuild")
	}
	if !oldAlgoIDStr.Valid || oldAlgoIDStr.String == "" {
		log.Fatal().Msg("trade has no current trail algo (trail_stage probably 0)")
	}
	var qty pgtype.Numeric
	if err := pool.QueryRow(ctx, `SELECT current_qty FROM position_states WHERE trade_id=$1`, tradeID).Scan(&qty); err != nil {
		log.Fatal().Err(err).Msg("position_states current_qty fetch failed")
	}

	entryDec := decimalFromPgNumeric(entryPrice)
	qtyDec := decimalFromPgNumeric(qty)
	oldActDec := decimalFromPgNumeric(oldActivation)

	fmt.Printf("=== trade #%d %s ===\n", tradeID, symbol)
	fmt.Printf("  entry_price        = %s\n", entryDec.String())
	fmt.Printf("  current_qty        = %s\n", qtyDec.String())
	fmt.Printf("  old_algo_id        = %s\n", oldAlgoIDStr.String)
	fmt.Printf("  old_activation_db  = %s  (what we *thought* was placed; Binance had different)\n", oldActDec.String())

	// 2. Cancel old algo.
	oldAlgoID, err := strconv.ParseInt(oldAlgoIDStr.String, 10, 64)
	if err != nil {
		log.Fatal().Err(err).Msg("parse old algo id")
	}
	if err := bc.CancelAlgoOrder(ctx, symbol, oldAlgoID); err != nil {
		log.Fatal().Err(err).Msg("cancel old algo failed — ABORT, no changes made")
	}
	fmt.Printf("  ✓ cancelled old algo %d\n", oldAlgoID)

	// 3. Compute new activatePrice.
	rt := config.Get()
	activatePct := cfg.Exit.TrailStage1ActivatePct
	if rt != nil && rt.TrailStage1ActivatePct.IsPositive() {
		activatePct = rt.TrailStage1ActivatePct
	}
	callbackPct := cfg.Exit.TrailStage1CallbackRate
	if activatePct.IsZero() || callbackPct.IsZero() {
		log.Fatal().Msg("activate or callback pct zero — config issue")
	}

	symSvc := binance.NewSymbolService(bc, log)
	filters, err := symSvc.GetTradingFilters(ctx, symbol)
	if err != nil {
		log.Fatal().Err(err).Msg("get trading filters failed")
	}
	newActivate := entryDec.Mul(decimal.NewFromInt(1).Add(activatePct))
	if !filters.TickSize.IsZero() {
		newActivate = newActivate.Div(filters.TickSize).Truncate(0).Mul(filters.TickSize)
	}
	cbBinance, _ := callbackPct.Mul(decimal.NewFromInt(100)).Float64()

	fmt.Printf("  new_activation     = %s  (entry × 1+%s, tick-aligned)\n", newActivate.String(), activatePct.String())
	fmt.Printf("  callback_pct       = %.2f%%\n", cbBinance)

	// 4. Place new trail with FIXED param name.
	res, err := bc.PlaceAlgoTrailingStop(ctx, symbol, qtyDec.String(), newActivate.String(), cbBinance)
	if err != nil {
		log.Fatal().Err(err).Msg("place new trail FAILED — position has no trail right now! Run again to retry")
	}
	fmt.Printf("  ✓ new algo placed: %d (status: %s)\n", res.AlgoID, res.Status)

	// 5. UPDATE DB.
	var newActPg pgtype.Numeric
	_ = newActPg.Scan(newActivate.String())
	if _, err := pool.Exec(ctx, `
		UPDATE trades
		SET binance_trail_algo_id = $2,
		    trail_activation_price = $3
		WHERE id = $1
	`, tradeID, strconv.FormatInt(res.AlgoID, 10), newActPg); err != nil {
		log.Error().Err(err).Msg("DB update failed — algo ON BINANCE but DB stale; run UPDATE manually")
		os.Exit(1)
	}
	fmt.Printf("  ✓ DB updated\n")
	fmt.Printf("\n✅ trade #%d trail rebuilt with correct activatePrice\n", tradeID)
}

// decimalFromPgNumeric converts pgx Numeric to decimal.Decimal. Empty/NaN → zero.
func decimalFromPgNumeric(n pgtype.Numeric) decimal.Decimal {
	if !n.Valid {
		return decimal.Zero
	}
	v, err := n.Value()
	if err != nil || v == nil {
		return decimal.Zero
	}
	s, ok := v.(string)
	if !ok {
		return decimal.Zero
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero
	}
	return d
}
