// One-shot rescue tool: re-attach Binance algo IDs to a trade whose algo_id
// columns were nulled by the R.26 -2013 false positive (before R.27 fixed it).
//
// What it does:
//   1. Reads trades.symbol for the given trade_id from PG.
//   2. Calls /fapi/v1/openAlgoOrders, filters to that symbol's SELL reduceOnly
//      algos.
//   3. Maps types → columns:
//        STOP_MARKET            → binance_disaster_stop_order_id
//        TRAILING_STOP_MARKET   → binance_trail_algo_id
//        TAKE_PROFIT_MARKET × 2 → tp1 (lower stopPrice) + tp2 (higher stopPrice)
//   4. With --dry-run: prints the planned UPDATE SQL and exits.
//      Without:        executes the UPDATE inside a tx, prints final state.
//
// Usage (inside the trader container so env + proxy reach Binance + PG):
//   /app/algo-rescue-ids [--dry-run] <trade_id>
//
// Note: Go's flag package needs flags BEFORE positional args.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"trader/internal/binance"
	"trader/internal/config"
	"trader/internal/pkg/ratelimit"
)

// rawAlgo includes StopPrice which binance.AlgoOpenOrder drops — we need it
// to distinguish TP1 (lower trigger) from TP2 (higher trigger).
type rawAlgo struct {
	AlgoID     int64  `json:"algoId"`
	Symbol     string `json:"symbol"`
	Side       string `json:"side"`
	Type       string `json:"type"`
	Status     string `json:"algoStatus"`
	Quantity   string `json:"quantity"`
	StopPrice  string `json:"stopPrice"`  // STOP_MARKET / TP_MARKET / TRAIL all carry this
	ReduceOnly bool   `json:"reduceOnly"`
}

func main() {
	dryRun := flag.Bool("dry-run", false, "print planned SQL and exit")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: algo-rescue-ids <trade_id> [--dry-run]")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(2)
	}
	tradeID, err := strconv.ParseInt(flag.Arg(0), 10, 64)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid trade_id:", err)
		os.Exit(2)
	}

	log := zerolog.New(os.Stderr).With().Timestamp().Logger()
	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("config load")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, cfg.DB.PostgresURL)
	if err != nil {
		log.Fatal().Err(err).Msg("pg connect")
	}
	defer pool.Close()

	// 1. trade lookup
	var symbol, status string
	var tp1Cur, tp2Cur, trailCur, disasterCur pgtype.Text
	err = pool.QueryRow(ctx,
		`SELECT symbol, status, binance_tp1_algo_id, binance_tp2_algo_id,
		        binance_trail_algo_id, binance_disaster_stop_order_id
		 FROM trades WHERE id=$1`, tradeID).
		Scan(&symbol, &status, &tp1Cur, &tp2Cur, &trailCur, &disasterCur)
	if err != nil {
		log.Fatal().Err(err).Int64("trade_id", tradeID).Msg("trade lookup")
	}
	fmt.Printf("Trade #%d: symbol=%s status=%s\n", tradeID, symbol, status)
	fmt.Printf("  current DB: tp1=%q tp2=%q trail=%q disaster=%q\n",
		tp1Cur.String, tp2Cur.String, trailCur.String, disasterCur.String)

	// 2. binance client
	pm, err := binance.NewProxyManager(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("proxy")
	}
	limiter := ratelimit.NewTokenBucket(1920, 32)
	bc, err := binance.New(cfg, pm, limiter, log)
	if err != nil {
		log.Fatal().Err(err).Msg("binance client")
	}

	// 3. ListOpenAlgoOrders raw
	body, err := bc.DoReadAccount(ctx, "/fapi/v1/openAlgoOrders", url.Values{}, 1)
	if err != nil {
		log.Fatal().Err(err).Msg("openAlgoOrders fetch")
	}
	var envelope struct {
		Orders []rawAlgo `json:"orders"`
	}
	var orders []rawAlgo
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Orders != nil {
		orders = envelope.Orders
	} else {
		// Bare-array variant (matches binance.ListOpenAlgoOrders fallback).
		if err := json.Unmarshal(body, &orders); err != nil {
			log.Fatal().Err(err).Msg("parse bare array")
		}
	}
	envelope.Orders = orders
	fmt.Printf("\nParsed %d open algo orders total from Binance\n", len(envelope.Orders))

	// 4. filter to symbol + SELL + reduceOnly + NEW/WORKING (case-insensitive
	// to tolerate testnet returning lowercase variants).
	var (
		disasters, trails, tps []rawAlgo
	)
	for _, a := range envelope.Orders {
		if !strings.EqualFold(a.Symbol, symbol) ||
			!strings.EqualFold(a.Side, "SELL") || !a.ReduceOnly {
			continue
		}
		if !strings.EqualFold(a.Status, "NEW") &&
			!strings.EqualFold(a.Status, "WORKING") {
			continue
		}
		switch strings.ToUpper(a.Type) {
		case "STOP_MARKET":
			disasters = append(disasters, a)
		case "TRAILING_STOP_MARKET":
			trails = append(trails, a)
		case "TAKE_PROFIT_MARKET":
			tps = append(tps, a)
		}
	}

	fmt.Printf("\nFound on Binance for %s (SELL reduceOnly NEW/WORKING):\n", symbol)
	for _, a := range disasters {
		fmt.Printf("  disaster  algo_id=%d  stopPrice=%s  qty=%s\n", a.AlgoID, a.StopPrice, a.Quantity)
	}
	for _, a := range trails {
		fmt.Printf("  trail     algo_id=%d  stopPrice=%s  qty=%s\n", a.AlgoID, a.StopPrice, a.Quantity)
	}
	for _, a := range tps {
		fmt.Printf("  TP        algo_id=%d  stopPrice=%s  qty=%s\n", a.AlgoID, a.StopPrice, a.Quantity)
	}

	// 5. map to columns
	if len(disasters) > 1 || len(trails) > 1 || len(tps) > 2 {
		log.Fatal().
			Int("disasters", len(disasters)).Int("trails", len(trails)).Int("tps", len(tps)).
			Msg("unexpected counts (>1 disaster, >1 trail, or >2 TP) — manual review needed")
	}
	// Sort TPs by stopPrice ascending so TP1 (closer to entry) maps first.
	sort.Slice(tps, func(i, j int) bool {
		fi, _ := strconv.ParseFloat(tps[i].StopPrice, 64)
		fj, _ := strconv.ParseFloat(tps[j].StopPrice, 64)
		return fi < fj
	})

	var newDisaster, newTrail, newTP1, newTP2 *string
	if len(disasters) == 1 {
		s := strconv.FormatInt(disasters[0].AlgoID, 10)
		newDisaster = &s
	}
	if len(trails) == 1 {
		s := strconv.FormatInt(trails[0].AlgoID, 10)
		newTrail = &s
	}
	if len(tps) >= 1 {
		s := strconv.FormatInt(tps[0].AlgoID, 10)
		newTP1 = &s
	}
	if len(tps) >= 2 {
		s := strconv.FormatInt(tps[1].AlgoID, 10)
		newTP2 = &s
	}

	fmt.Printf("\nPlanned mapping for trade #%d:\n", tradeID)
	fmt.Printf("  disaster_stop_order_id = %s\n", deref(newDisaster, "(unchanged, none found)"))
	fmt.Printf("  trail_algo_id          = %s\n", deref(newTrail, "(unchanged, none found)"))
	fmt.Printf("  tp1_algo_id (lower)    = %s\n", deref(newTP1, "(unchanged, none found)"))
	fmt.Printf("  tp2_algo_id (higher)   = %s\n", deref(newTP2, "(unchanged, none found)"))

	if *dryRun {
		fmt.Println("\n[dry-run] no DB change applied.")
		return
	}

	// 6. apply UPDATE inside tx — only fields we actually recovered
	tx, err := pool.Begin(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("tx begin")
	}
	defer tx.Rollback(ctx)

	apply := func(col string, v *string) {
		if v == nil {
			return
		}
		if _, err := tx.Exec(ctx,
			fmt.Sprintf("UPDATE trades SET %s=$1 WHERE id=$2", col), *v, tradeID); err != nil {
			log.Fatal().Err(err).Str("col", col).Msg("update failed")
		}
		fmt.Printf("  ✓ updated %s = %s\n", col, *v)
	}
	apply("binance_disaster_stop_order_id", newDisaster)
	apply("binance_trail_algo_id", newTrail)
	apply("binance_tp1_algo_id", newTP1)
	apply("binance_tp2_algo_id", newTP2)

	if err := tx.Commit(ctx); err != nil {
		log.Fatal().Err(err).Msg("tx commit")
	}
	fmt.Printf("\n✅ trade #%d algo_ids re-attached.\n", tradeID)
}

func deref(p *string, fallback string) string {
	if p == nil {
		return fallback
	}
	return *p
}
