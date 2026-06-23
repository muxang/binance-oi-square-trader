// R.35: 7-day pass-count leaderboard + distribution histogram. Aggregates the
// per-symbol ZSETs that R.29's UptrendCollector populates
// (admin:uptrend:pass_hours:<symbol>, score = hour-bucket Unix seconds).
//
// Read-only; no CSRF. Cost per call: 1 SCAN + N ZCOUNT (pipelined). N is
// bounded by the uptrend collector's TopN (≤200 typical) so a single tick is
// well under 50ms even with cache-miss.
package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type UptrendLeaderboardItem struct {
	Symbol string `json:"symbol"`
	Count  int    `json:"count"`
}

type UptrendHistogramBucket struct {
	Label string `json:"label"`
	Min   int    `json:"min"`
	Max   int    `json:"max"` // 0 = no upper bound
	Count int    `json:"count"`
}

type UptrendLeaderboardResponse struct {
	WindowDays   int                      `json:"window_days"`
	TotalSymbols int                      `json:"total_symbols"` // symbols with ≥1 pass in window (pre-cap)
	TotalPasses  int                      `json:"total_passes"`  // sum of counts across all symbols
	Leaderboard  []UptrendLeaderboardItem `json:"leaderboard"`   // capped at 100
	Histogram    []UptrendHistogramBucket `json:"histogram"`
}

// handleUptrendLeaderboard scans admin:uptrend:pass_hours:* and counts entries
// inside the last 7d window. Query params:
//
//	limit=N (default 100)         cap leaderboard items in response (max 200)
//	exclude_stocks=1              filter out symbols in admin:stock:symbols:v1
//	                              (Binance underlyingType=EQUITY); applied BEFORE
//	                              the top-N cap and histogram so both reflect
//	                              the filter consistently.
func (s *Server) handleUptrendLeaderboard(w http.ResponseWriter, r *http.Request) {
	empty := UptrendLeaderboardResponse{
		WindowDays:  7,
		Leaderboard: []UptrendLeaderboardItem{},
		Histogram:   defaultHistogramBuckets(),
	}
	if s.rdb == nil {
		s.writeJSON(w, http.StatusOK, empty)
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	excludeStocks := q.Get("exclude_stocks") == "1"

	ctx := r.Context()

	// Stock filter set — read once when requested. Failure is non-fatal:
	// log + serve unfiltered, since the alternative (blocking the endpoint)
	// is worse UX. R.31 stores this as a STRING containing a JSON array
	// (not a Redis SET) — must match stock_symbols.go's Get + json.Unmarshal.
	var stockSet map[string]struct{}
	if excludeStocks {
		b, err := s.rdb.Get(ctx, stockSymbolsCacheKey).Bytes()
		if err != nil {
			s.log.Warn().Err(err).Msg("uptrend.leaderboard: stock list fetch failed; serving unfiltered")
		} else {
			var symbols []string
			if jerr := json.Unmarshal(b, &symbols); jerr != nil {
				s.log.Warn().Err(jerr).Msg("uptrend.leaderboard: stock list parse failed; serving unfiltered")
			} else {
				stockSet = make(map[string]struct{}, len(symbols))
				for _, sym := range symbols {
					stockSet[sym] = struct{}{}
				}
			}
		}
	}

	symbols, err := scanUptrendPassHourKeys(ctx, s.rdb)
	if err != nil {
		s.log.Warn().Err(err).Msg("uptrend.leaderboard: SCAN failed")
		s.writeJSON(w, http.StatusOK, empty)
		return
	}
	if len(symbols) == 0 {
		s.writeJSON(w, http.StatusOK, empty)
		return
	}

	// 7d window — match R.29's UI convention (frontend currently shows
	// `7d×N`; the collector retains 14d but UI surfaces only the last 7).
	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour).Unix()
	cutoffStr := strconv.FormatInt(cutoff, 10)

	pipe := s.rdb.Pipeline()
	cmds := make([]*redis.IntCmd, len(symbols))
	for i, sym := range symbols {
		cmds[i] = pipe.ZCount(ctx, uptrendPassHoursKeyPrefix+sym, cutoffStr, "+inf")
	}
	if _, err := pipe.Exec(ctx); err != nil {
		// Partial results are still useful (per-cmd .Val() returns 0 on err).
		s.log.Warn().Err(err).Msg("uptrend.leaderboard: pipeline ZCount partial failure")
	}

	items := make([]UptrendLeaderboardItem, 0, len(symbols))
	totalPasses := 0
	for i, sym := range symbols {
		c := int(cmds[i].Val())
		if c <= 0 {
			continue
		}
		if stockSet != nil {
			if _, isStock := stockSet[sym]; isStock {
				continue
			}
		}
		items = append(items, UptrendLeaderboardItem{Symbol: sym, Count: c})
		totalPasses += c
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count != items[j].Count {
			return items[i].Count > items[j].Count
		}
		return items[i].Symbol < items[j].Symbol
	})

	buckets := defaultHistogramBuckets()
	for _, it := range items {
		for i := range buckets {
			b := &buckets[i]
			if it.Count >= b.Min && (b.Max == 0 || it.Count <= b.Max) {
				b.Count++
				break
			}
		}
	}

	totalSymbols := len(items)
	if len(items) > limit {
		items = items[:limit]
	}

	s.writeJSON(w, http.StatusOK, UptrendLeaderboardResponse{
		WindowDays:   7,
		TotalSymbols: totalSymbols,
		TotalPasses:  totalPasses,
		Leaderboard:  items,
		Histogram:    buckets,
	})
}

// defaultHistogramBuckets returns the bucket layout. Tuned for the practical
// pass-count distribution: most symbols 1-7 over a week, with a long tail.
// Max 168 (7d × 24h, the theoretical ceiling of distinct hours).
func defaultHistogramBuckets() []UptrendHistogramBucket {
	return []UptrendHistogramBucket{
		{Label: "1", Min: 1, Max: 1},
		{Label: "2-3", Min: 2, Max: 3},
		{Label: "4-7", Min: 4, Max: 7},
		{Label: "8-14", Min: 8, Max: 14},
		{Label: "15-30", Min: 15, Max: 30},
		{Label: "31-70", Min: 31, Max: 70},
		{Label: "70+", Min: 71, Max: 0},
	}
}

// scanUptrendPassHourKeys walks all admin:uptrend:pass_hours:<symbol> keys via
// SCAN (non-blocking; production-safe vs KEYS). Returns the extracted symbol
// list (prefix stripped). Empty list on no matches.
func scanUptrendPassHourKeys(ctx context.Context, rdb *redis.Client) ([]string, error) {
	var symbols []string
	var cursor uint64
	for {
		keys, next, err := rdb.Scan(ctx, cursor, uptrendPassHoursKeyPrefix+"*", 200).Result()
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			sym := strings.TrimPrefix(k, uptrendPassHoursKeyPrefix)
			if sym != "" {
				symbols = append(symbols, sym)
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return symbols, nil
}

// Re-declared here (handler-local) so the admin package doesn't depend on the
// internal/collector package. Must stay in sync with the prefix in
// internal/collector/uptrend.go.
const uptrendPassHoursKeyPrefix = "admin:uptrend:pass_hours:"
