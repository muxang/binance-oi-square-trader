package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"trader/internal/market"
)

const uptrendCacheKey = "admin:market:uptrend:v1"

type UptrendResponse struct {
	Total   int                  `json:"total"`   // total symbols evaluated
	Passing int                  `json:"passing"` // count satisfying all 6 conditions
	Items   []market.UptrendItem `json:"items"`
}

// handleUptrend serves the cached uptrend scan (written by the trader-side
// UptrendCollector every ~2min). Query params:
//
//	passing=1 (default)   only return symbols satisfying all 6 conditions
//	passing=0             return all evaluated symbols (debugging / transparency)
//	search=…              case-insensitive substring filter on symbol
//	limit=N (default 50)  cap items in response (max 500)
func (s *Server) handleUptrend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.rdb == nil {
		s.writeJSON(w, http.StatusOK, UptrendResponse{Items: []market.UptrendItem{}})
		return
	}
	q := r.URL.Query()
	passingOnly := q.Get("passing") != "0"
	search := strings.ToUpper(strings.TrimSpace(q.Get("search")))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 50
	}

	b, err := s.rdb.Get(ctx, uptrendCacheKey).Bytes()
	if err != nil {
		// Cache miss is a non-error from the user's POV — return empty.
		// Trader collector populates this key; if absent, scan hasn't run yet.
		s.writeJSON(w, http.StatusOK, UptrendResponse{Items: []market.UptrendItem{}})
		return
	}
	var all []market.UptrendItem
	if err := json.Unmarshal(b, &all); err != nil {
		s.log.Warn().Err(err).Msg("uptrend cache unmarshal")
		s.writeError(w, http.StatusInternalServerError, "cache parse")
		return
	}
	passing := 0
	out := make([]market.UptrendItem, 0, len(all))
	for _, it := range all {
		if it.Pass {
			passing++
		}
		if passingOnly && !it.Pass {
			continue
		}
		if search != "" && !strings.Contains(it.Symbol, search) {
			continue
		}
		out = append(out, it)
	}
	if len(out) > limit {
		out = out[:limit]
	}
	s.writeJSON(w, http.StatusOK, UptrendResponse{Total: len(all), Passing: passing, Items: out})
}
