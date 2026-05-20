package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type MarketItem struct {
	Symbol          string  `json:"symbol"`
	// R.11.B1+ contract-monitor.js 维度. 流动市值 = circulating_supply × current_price
	// (USD millions). 0 = no CoinGecko supply data or no klines price.
	CMcapUsdM       float64 `json:"cmcap_usd_m"`
	OiUsdM          float64 `json:"oi_usd_m"`        // OI in USD millions
	Oi1hPct         float64 `json:"oi_1h_pct"`
	Oi24hPct        float64 `json:"oi_24h_pct"`
	CurrentPrice    float64 `json:"current_price"`   // 0 if no klines data
	Price24hPct     float64 `json:"price_24h_pct"`
	SquareMentions  int64   `json:"square_mentions"` // 24h mention count
	Square24hPct    float64 `json:"square_24h_pct"`  // vs prior 24h; 0 = no prior data
	// Round R.11.B1: contract-monitor.js 3 维度 — 0 means no data yet (collector
	// hasn't run or symbol not on CoinGecko). Frontend treats >0 as displayable.
	AcctLongShortRatio float64 `json:"acct_ls_ratio"`  // newest large_holder_ratios.account_long_short_ratio
	PosLongShortRatio  float64 `json:"pos_ls_ratio"`   // newest large_holder_ratios.position_long_short_ratio
	MarketCapRatioPct  float64 `json:"mcap_ratio_pct"` // OI_USD / market_cap × 100; 0 = supply unavailable
	InWatchlist     bool    `json:"in_watchlist"`
	InOpenPosition  bool    `json:"in_open_position"`
}

type MarketResponse struct {
	Total int          `json:"total"`
	Items []MarketItem `json:"items"`
}

const (
	// Cache key bumped to v3 in R.11.B1+ (added cmcap_usd_m column).
	// Old :v2 cached blobs lack the new fields and would deserialize to 0.
	marketCacheKey = "admin:market:full:v3"
	marketCacheTTL = 2 * time.Minute
)

func (s *Server) handleMarket(w http.ResponseWriter, r *http.Request) {
	q       := r.URL.Query()
	scope   := q.Get("scope")  // all | watchlist | positions
	sortBy  := q.Get("sort")   // oi_1h_pct | oi_24h_pct | oi_usd | price_24h_pct | square
	order   := q.Get("order")  // asc | desc (default desc; mu 2026-05-14 catch — UI 之前只能降序)
	search := strings.ToUpper(strings.TrimSpace(q.Get("search")))
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("size"))

	if scope == "" { scope = "all" }
	if sortBy == "" { sortBy = "oi_1h_pct" }
	if order != "asc" { order = "desc" }
	if page < 1 { page = 1 }
	if size != 100 { size = 50 }

	ctx := r.Context()

	// Redis cache for the full 529-symbol market view (2min TTL)
	var all []MarketItem
	if s.rdb != nil {
		if b, err := s.rdb.Get(ctx, marketCacheKey).Bytes(); err == nil {
			_ = json.Unmarshal(b, &all)
		}
	}
	if all == nil {
		var err error
		all, err = s.computeMarket(ctx)
		if err != nil {
			s.log.Error().Err(err).Msg("compute market")
			s.writeError(w, http.StatusInternalServerError, "db error")
			return
		}
		if s.rdb != nil {
			if b, err := json.Marshal(all); err == nil {
				_ = s.rdb.Set(ctx, marketCacheKey, b, marketCacheTTL).Err()
			}
		}
	}

	// Filter
	items := make([]MarketItem, 0, len(all))
	for i := range all {
		it := &all[i]
		switch scope {
		case "watchlist":
			if !it.InWatchlist { continue }
		case "positions":
			if !it.InOpenPosition { continue }
		}
		if search != "" && !strings.Contains(it.Symbol, search) { continue }
		items = append(items, *it)
	}

	// Sort. Comparator returns "i should come before j" — desc uses >, asc uses <.
	asc := order == "asc"
	sort.Slice(items, func(i, j int) bool {
		var iv, jv float64
		switch sortBy {
		case "oi_24h_pct":     iv, jv = items[i].Oi24hPct, items[j].Oi24hPct
		case "oi_usd":         iv, jv = items[i].OiUsdM, items[j].OiUsdM
		case "price_24h_pct":  iv, jv = items[i].Price24hPct, items[j].Price24hPct
		case "square":         iv, jv = float64(items[i].SquareMentions), float64(items[j].SquareMentions)
		case "square_24h_pct": iv, jv = items[i].Square24hPct, items[j].Square24hPct
		default:               iv, jv = items[i].Oi1hPct, items[j].Oi1hPct
		}
		if asc {
			return iv < jv
		}
		return iv > jv
	})

	// Paginate
	total := len(items)
	start := (page - 1) * size
	if start > total { start = total }
	end := start + size
	if end > total { end = total }

	s.writeJSON(w, http.StatusOK, MarketResponse{Total: total, Items: items[start:end]})
}

// computeMarket runs the full market query (all USDⓈ-M symbols with OI data).
// Price columns (current_price, price_24h_pct) are only available for symbols
// that have klines data (typically watchlist symbols).
func (s *Server) computeMarket(ctx context.Context) ([]MarketItem, error) {
	rows, err := s.db.Query(ctx, `
		WITH
		lo AS (
			SELECT DISTINCT ON (symbol) symbol, oi_value_usd
			FROM oi_history ORDER BY symbol, ts DESC
		),
		h1 AS (
			SELECT DISTINCT ON (symbol) symbol, oi_value_usd AS v
			FROM oi_history WHERE ts <= NOW() - INTERVAL '1 hour'
			ORDER BY symbol, ts DESC
		),
		h24 AS (
			SELECT DISTINCT ON (symbol) symbol, oi_value_usd AS v
			FROM oi_history WHERE ts <= NOW() - INTERVAL '24 hours'
			ORDER BY symbol, ts DESC
		),
		sq_cur AS (
			SELECT DISTINCT ON (symbol) symbol, content_count
			FROM square_hashtag_history ORDER BY symbol, ts DESC
		),
		sq_prev AS (
			SELECT DISTINCT ON (symbol) symbol, content_count AS prev_count
			FROM square_hashtag_history
			WHERE ts <= NOW() - INTERVAL '24 hours'
			ORDER BY symbol, ts DESC
		),
		lp AS (
			SELECT DISTINCT ON (symbol) symbol, close AS price
			FROM klines WHERE timeframe='15m' ORDER BY symbol, open_time DESC
		),
		p24 AS (
			SELECT DISTINCT ON (symbol) symbol, close AS price
			FROM klines WHERE timeframe='15m' AND open_time <= NOW() - INTERVAL '24 hours'
			ORDER BY symbol, open_time DESC
		),
		wl AS (
			SELECT snap->>'symbol' AS sym
			FROM watchlist_snapshots, jsonb_array_elements(symbols) snap
			WHERE ts = (SELECT MAX(ts) FROM watchlist_snapshots)
		),
		op AS (SELECT DISTINCT symbol FROM trades WHERE status IN ('open','partial')),
		-- Round R.11.B1: newest large_holder_ratios row per symbol.
		lh AS (
			SELECT DISTINCT ON (symbol) symbol,
			       account_long_short_ratio, position_long_short_ratio, market_cap_ratio_pct
			FROM large_holder_ratios ORDER BY symbol, ts DESC
		)
		SELECT
			lo.symbol,
			-- R.11.B1+: 流动市值 USD millions = circulating_supply × current_price.
			-- Returns 0 if either piece is missing (mu sees "—" in UI).
			COALESCE((lh.circulating_supply * lp.price / 1e6)::float8, 0),
			(lo.oi_value_usd / 1e6)::float8,
			CASE WHEN h1.v>0 THEN ((lo.oi_value_usd-h1.v)/h1.v*100)::float8 ELSE 0 END,
			CASE WHEN h24.v>0 THEN ((lo.oi_value_usd-h24.v)/h24.v*100)::float8 ELSE 0 END,
			COALESCE(lp.price::float8, 0),
			CASE WHEN p24.price>0 AND lp.price>0
			     THEN ((lp.price-p24.price)/p24.price*100)::float8
			     ELSE 0 END,
			COALESCE(sq_cur.content_count, 0),
			CASE WHEN COALESCE(sq_prev.prev_count, 0) > 0
			     THEN ((COALESCE(sq_cur.content_count, 0) - sq_prev.prev_count)::float8 / sq_prev.prev_count * 100)
			     ELSE 0 END,
			COALESCE(lh.account_long_short_ratio::float8, 0),
			COALESCE(lh.position_long_short_ratio::float8, 0),
			COALESCE(lh.market_cap_ratio_pct::float8, 0),
			(wl.sym IS NOT NULL),
			(op.symbol IS NOT NULL)
		FROM lo
		LEFT JOIN h1      ON h1.symbol      = lo.symbol
		LEFT JOIN h24     ON h24.symbol     = lo.symbol
		LEFT JOIN sq_cur  ON sq_cur.symbol  = lo.symbol
		LEFT JOIN sq_prev ON sq_prev.symbol = lo.symbol
		LEFT JOIN lp      ON lp.symbol      = lo.symbol
		LEFT JOIN p24     ON p24.symbol     = lo.symbol
		LEFT JOIN lh      ON lh.symbol      = lo.symbol
		LEFT JOIN wl      ON wl.sym         = lo.symbol
		LEFT JOIN op      ON op.symbol      = lo.symbol
	`)
	if err != nil {
		return nil, fmt.Errorf("market query: %w", err)
	}
	defer rows.Close()

	items := make([]MarketItem, 0, 600)
	for rows.Next() {
		var (
			sym       string
			cmcapUsdM float64
			oiUsdM    float64
			oi1h      float64
			oi24h     float64
			price     float64
			p24pct    float64
			sqCnt     int64
			sqGrowth  float64
			acctLS    float64
			posLS     float64
			mcapPct   float64
			inWl      bool
			inPos     bool
		)
		if err := rows.Scan(&sym, &cmcapUsdM, &oiUsdM, &oi1h, &oi24h, &price, &p24pct, &sqCnt, &sqGrowth,
			&acctLS, &posLS, &mcapPct, &inWl, &inPos); err != nil {
			s.log.Error().Err(err).Str("sym", sym).Msg("scan market row")
			continue
		}
		items = append(items, MarketItem{
			Symbol: sym, CMcapUsdM: cmcapUsdM, OiUsdM: oiUsdM,
			Oi1hPct: oi1h, Oi24hPct: oi24h,
			CurrentPrice: price, Price24hPct: p24pct,
			SquareMentions: sqCnt, Square24hPct: sqGrowth,
			AcctLongShortRatio: acctLS, PosLongShortRatio: posLS,
			MarketCapRatioPct: mcapPct,
			InWatchlist: inWl, InOpenPosition: inPos,
		})
	}
	return items, nil
}
