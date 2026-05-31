package admin

import (
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgtype"
)

type OiPoint struct {
	TsMs  int64   `json:"ts_ms"`
	OiUsd float64 `json:"oi_usd_m"` // millions
}

type PricePoint struct {
	TsMs  int64   `json:"ts_ms"`
	Close float64 `json:"close"`
}

type SquareMentionPoint struct {
	TsMs     int64 `json:"ts_ms"`
	Mentions int64 `json:"mentions"`
}

type SymbolSquarePost struct {
	TsMs    int64  `json:"ts_ms"`
	Title   string `json:"title"`
	Content string `json:"content"`
	Views   int64  `json:"views"`
	Likes   int64  `json:"likes"`
}

type SymbolTrade struct {
	TradeID     int64   `json:"trade_id"`
	EntryTsMs   int64   `json:"entry_ts_ms"`
	ExitTsMs    int64   `json:"exit_ts_ms"`
	EntryPrice  float64 `json:"entry_price"`
	ExitPrice   float64 `json:"exit_price"`
	RealizedPnl float64 `json:"realized_pnl"`
	ExitReason  string  `json:"exit_reason"`
	Status      string  `json:"status"`
	DataSource  string  `json:"data_source"`
}

type SymbolDetailResponse struct {
	Symbol        string                `json:"symbol"`
	CurrentPrice  float64               `json:"current_price"`
	Price24hPct   float64               `json:"price_24h_pct"`
	OiSeries      []OiPoint             `json:"oi_series"`
	PriceSeries   []PricePoint          `json:"price_series"`
	SquareSeries  []SquareMentionPoint  `json:"square_series"`
	SquarePosts   []SymbolSquarePost    `json:"square_posts"`
	Trades        []SymbolTrade         `json:"trades"`
	// R.11.B3 — large_holder_ratios trends within the same hours window.
	RatiosSeries  []RatioPoint          `json:"ratios_series"`
}

// RatioPoint: one large_holder_ratios row in time-series shape. Any field can
// be 0 — frontend chart skips 0 points (collector miss / market_cap NULL).
type RatioPoint struct {
	TsMs        int64   `json:"ts_ms"`
	AcctRatio   float64 `json:"acct_ratio"`
	PosRatio    float64 `json:"pos_ratio"`
	McapPct     float64 `json:"mcap_pct"`
}

func (s *Server) handleSymbolDetail(w http.ResponseWriter, r *http.Request) {
	symbol     := r.PathValue("symbol")
	hours, _   := strconv.Atoi(r.URL.Query().Get("hours"))
	dataSource := r.URL.Query().Get("data_source") // mainnet | testnet | all
	if hours <= 0 || hours > 168 { hours = 6 }
	if dataSource == "" { dataSource = "testnet" } // R.18 D2: default aligned with DB DEFAULT

	ctx := r.Context()

	// OI time series
	oiRows, err := s.db.Query(ctx, `
		SELECT ts, (oi_value_usd / 1e6)::float8
		FROM oi_history
		WHERE symbol = $1 AND ts >= NOW() - ($2 || ' hours')::INTERVAL
		ORDER BY ts ASC
	`, symbol, strconv.Itoa(hours))
	if err != nil {
		s.log.Error().Err(err).Str("symbol", symbol).Msg("oi series")
		s.writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer oiRows.Close()

	oiSeries := make([]OiPoint, 0)
	for oiRows.Next() {
		var ts pgtype.Timestamptz
		var v float64
		if err := oiRows.Scan(&ts, &v); err != nil { continue }
		if ts.Valid { oiSeries = append(oiSeries, OiPoint{TsMs: ts.Time.UnixMilli(), OiUsd: v}) }
	}
	oiRows.Close()

	// Price time series (15m klines)
	priceRows, err := s.db.Query(ctx, `
		SELECT open_time, close::float8
		FROM klines
		WHERE symbol = $1 AND timeframe='15m' AND open_time >= NOW() - ($2 || ' hours')::INTERVAL
		ORDER BY open_time ASC
	`, symbol, strconv.Itoa(hours))
	if err != nil {
		s.log.Error().Err(err).Msg("price series")
		// non-fatal: continue without price series
	}

	priceSeries := make([]PricePoint, 0)
	if priceRows != nil {
		defer priceRows.Close()
		for priceRows.Next() {
			var ts pgtype.Timestamptz
			var c float64
			if err := priceRows.Scan(&ts, &c); err != nil { continue }
			if ts.Valid { priceSeries = append(priceSeries, PricePoint{TsMs: ts.Time.UnixMilli(), Close: c}) }
		}
		priceRows.Close()
	}

	// Current price + 24h pct from klines
	var currentPrice, price24hPct float64
	if len(priceSeries) > 0 {
		currentPrice = priceSeries[len(priceSeries)-1].Close
	}
	var prev24h float64
	_ = s.db.QueryRow(ctx, `
		SELECT close::float8 FROM klines
		WHERE symbol=$1 AND timeframe='15m' AND open_time <= NOW()-INTERVAL '24 hours'
		ORDER BY open_time DESC LIMIT 1
	`, symbol).Scan(&prev24h)
	if prev24h > 0 && currentPrice > 0 {
		price24hPct = (currentPrice - prev24h) / prev24h * 100
	}

	// Square hashtag trend: cumulative new posts relative to window start.
	// Baseline-subtracted so curve starts near 0; slope shows activity rate.
	sqSeriesRows, err := s.db.Query(ctx, `
		WITH baseline AS (
			SELECT COALESCE(MIN(content_count), 0) AS base
			FROM square_hashtag_history
			WHERE symbol = $1 AND ts >= NOW() - ($2 || ' hours')::INTERVAL
		)
		SELECT date_trunc('hour', ts) AS h,
		       (MAX(content_count) - (SELECT base FROM baseline))::bigint AS rel
		FROM square_hashtag_history
		WHERE symbol = $1 AND ts >= NOW() - ($2 || ' hours')::INTERVAL
		GROUP BY h ORDER BY h ASC
	`, symbol, strconv.Itoa(hours))

	squareSeries := make([]SquareMentionPoint, 0)
	if err == nil {
		defer sqSeriesRows.Close()
		for sqSeriesRows.Next() {
			var ts pgtype.Timestamptz
			var cnt int64
			if err := sqSeriesRows.Scan(&ts, &cnt); err != nil { continue }
			if ts.Valid {
				squareSeries = append(squareSeries, SquareMentionPoint{TsMs: ts.Time.UnixMilli(), Mentions: cnt})
			}
		}
		sqSeriesRows.Close()
	}

	// Square posts (last 24h for this symbol via square_mentions)
	postRows, err := s.db.Query(ctx, `
		SELECT p.fetched_at, p.title, p.content_text, p.view_count, p.like_count
		FROM square_posts p
		JOIN square_mentions m ON m.post_id = p.id
		WHERE m.symbol = $1 AND p.fetched_at >= NOW() - INTERVAL '24 hours'
		ORDER BY p.fetched_at DESC
		LIMIT 20
	`, symbol)

	squarePosts := make([]SymbolSquarePost, 0)
	if err == nil {
		defer postRows.Close()
		for postRows.Next() {
			var ts pgtype.Timestamptz
			var title, content pgtype.Text
			var views, likes pgtype.Int8
			if err := postRows.Scan(&ts, &title, &content, &views, &likes); err != nil { continue }
			var tsMs int64
			if ts.Valid { tsMs = ts.Time.UnixMilli() }
			squarePosts = append(squarePosts, SymbolSquarePost{
				TsMs:    tsMs,
				Title:   title.String,
				Content: content.String,
				Views:   views.Int64,
				Likes:   likes.Int64,
			})
		}
	}

	// Trade history (R.18 D2: default testnet branch handled by upstream "" → "testnet" assignment)
	dsCond := "t.data_source = 'testnet'"
	if dataSource == "all" { dsCond = "TRUE" }
	if dataSource == "mainnet" { dsCond = "t.data_source = 'mainnet'" }

	tradeRows, err := s.db.Query(ctx, `
		SELECT t.id, t.entry_ts, t.exit_ts, t.entry_price, t.exit_price,
		       t.realized_pnl, t.exit_reason, t.status, t.data_source
		FROM trades t
		WHERE t.symbol = $1 AND t.status IN ('open','partial','closed','failed')
		  AND `+dsCond+`
		ORDER BY t.entry_ts DESC NULLS LAST
		LIMIT 20
	`, symbol)

	trades := make([]SymbolTrade, 0)
	if err == nil {
		defer tradeRows.Close()
		for tradeRows.Next() {
			var (
				id         int64
				entryTs    pgtype.Timestamptz
				exitTs     pgtype.Timestamptz
				entryPrice pgtype.Numeric
				exitPrice  pgtype.Numeric
				realPnl    pgtype.Numeric
				reason     pgtype.Text
				status     string
				ds         pgtype.Text
			)
			if err := tradeRows.Scan(&id, &entryTs, &exitTs, &entryPrice, &exitPrice,
				&realPnl, &reason, &status, &ds); err != nil { continue }
			var entryMs, exitMs int64
			if entryTs.Valid { entryMs = entryTs.Time.UnixMilli() }
			if exitTs.Valid  { exitMs  = exitTs.Time.UnixMilli() }
			trades = append(trades, SymbolTrade{
				TradeID:    id,
				EntryTsMs:  entryMs, ExitTsMs: exitMs,
				EntryPrice:  numericToFloat64(entryPrice),
				ExitPrice:   numericToFloat64(exitPrice),
				RealizedPnl: numericToFloat64(realPnl),
				ExitReason:  reason.String,
				Status:      status,
				DataSource:  ds.String,
			})
		}
	}

	// R.11.B3: large_holder_ratios trends within same hours window. Non-fatal —
	// pre-R.11 symbols / empty table just yields empty series.
	ratiosSeries := make([]RatioPoint, 0)
	if rhRows, err := s.db.Query(ctx, `
		SELECT ts,
		       COALESCE(account_long_short_ratio::float8, 0),
		       COALESCE(position_long_short_ratio::float8, 0),
		       COALESCE(market_cap_ratio_pct::float8, 0)
		FROM large_holder_ratios
		WHERE symbol = $1 AND ts >= NOW() - ($2 || ' hours')::INTERVAL
		ORDER BY ts ASC
	`, symbol, strconv.Itoa(hours)); err == nil {
		defer rhRows.Close()
		for rhRows.Next() {
			var ts pgtype.Timestamptz
			var acct, pos, mcap float64
			if err := rhRows.Scan(&ts, &acct, &pos, &mcap); err != nil { continue }
			if ts.Valid {
				ratiosSeries = append(ratiosSeries, RatioPoint{
					TsMs: ts.Time.UnixMilli(),
					AcctRatio: acct, PosRatio: pos, McapPct: mcap,
				})
			}
		}
	}

	s.writeJSON(w, http.StatusOK, SymbolDetailResponse{
		Symbol: symbol, CurrentPrice: currentPrice, Price24hPct: price24hPct,
		OiSeries: oiSeries, PriceSeries: priceSeries,
		SquareSeries: squareSeries, SquarePosts: squarePosts, Trades: trades,
		RatiosSeries: ratiosSeries,
	})
}
