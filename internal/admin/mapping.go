package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// MappingRow combines coingecko_symbol_map + coingecko_market_cache for the
// UI table. NULL cache means CoinGecko returned no data for that mapping (or
// mapping was just added by mu manually and cache hasn't refreshed yet).
type MappingRow struct {
	BinanceSymbol     string   `json:"binance_symbol"`
	CoingeckoID       string   `json:"coingecko_id"`
	MarketCapUsdM     float64  `json:"market_cap_usd_m"`     // 0 if cache miss
	CirculatingSupply float64  `json:"circulating_supply"`   // 0 if cache miss
	InWatchlist       bool     `json:"in_watchlist"`
	InOpenPosition    bool     `json:"in_open_position"`
	LastRefreshedMs   int64    `json:"last_refreshed_ms"`    // 0 if cache miss
}

type MappingResponse struct {
	Total int          `json:"total"`
	Items []MappingRow `json:"items"`
}

func (s *Server) handleMappingList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := s.db.Query(ctx, `
		WITH wl AS (
			SELECT snap->>'symbol' AS sym
			FROM watchlist_snapshots, jsonb_array_elements(symbols) snap
			WHERE ts = (SELECT MAX(ts) FROM watchlist_snapshots)
		),
		op AS (SELECT DISTINCT symbol FROM trades WHERE status IN ('open','partial'))
		SELECT m.binance_symbol, m.coingecko_id,
		       COALESCE((c.market_cap_usd/1e6)::float8, 0),
		       COALESCE(c.circulating_supply::float8, 0),
		       (wl.sym IS NOT NULL),
		       (op.symbol IS NOT NULL),
		       COALESCE(EXTRACT(EPOCH FROM c.fetched_at)::bigint * 1000, 0)
		FROM coingecko_symbol_map m
		LEFT JOIN coingecko_market_cache c ON c.binance_symbol = m.binance_symbol
		LEFT JOIN wl ON wl.sym = m.binance_symbol
		LEFT JOIN op ON op.symbol = m.binance_symbol
		ORDER BY (c.market_cap_usd > 0) DESC, c.market_cap_usd DESC NULLS LAST, m.binance_symbol
	`)
	if err != nil {
		s.log.Error().Err(err).Msg("mapping list query")
		s.writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	items := make([]MappingRow, 0, 600)
	for rows.Next() {
		var r MappingRow
		if err := rows.Scan(&r.BinanceSymbol, &r.CoingeckoID, &r.MarketCapUsdM,
			&r.CirculatingSupply, &r.InWatchlist, &r.InOpenPosition, &r.LastRefreshedMs); err != nil {
			s.log.Error().Err(err).Msg("scan mapping row")
			continue
		}
		items = append(items, r)
	}
	s.writeJSON(w, http.StatusOK, MappingResponse{Total: len(items), Items: items})
}

type mappingUpdateReq struct {
	CoingeckoID string `json:"coingecko_id"`
}

// handleMappingUpdate hand-corrects a binance_symbol → coingecko_id mapping
// when the auto-heuristic picked the wrong canonical (e.g. mu spots BB =
// bitboard but knows it should be BounceBit). After update, next 6h supply
// tick refreshes cache with correct market_cap.
//
// Symbol path param is the binance symbol (e.g. "BBUSDT").
// Body: {"coingecko_id": "bouncebit"}
//
// Audit-logged. CSRF-guarded by route registration.
func (s *Server) handleMappingUpdate(w http.ResponseWriter, r *http.Request) {
	symbol := strings.ToUpper(strings.TrimSpace(r.PathValue("symbol")))
	if symbol == "" {
		s.writeError(w, http.StatusBadRequest, "symbol required")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "body too large")
		return
	}
	var req mappingUpdateReq
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	cgID := strings.ToLower(strings.TrimSpace(req.CoingeckoID))
	if cgID == "" {
		s.writeError(w, http.StatusBadRequest, "coingecko_id required")
		return
	}

	ctx := r.Context()
	// UPSERT preserves the (binance_symbol, coingecko_id) invariant. Also
	// nuke the cache row for this symbol so the next supply tick re-fetches
	// against the new id (otherwise stale wrong-token mcap lingers).
	tx, err := s.writeDB.Begin(ctx)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "tx begin")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO coingecko_symbol_map (binance_symbol, coingecko_id, last_refreshed)
		VALUES ($1, $2, NOW())
		ON CONFLICT (binance_symbol) DO UPDATE SET
			coingecko_id = EXCLUDED.coingecko_id,
			last_refreshed = NOW()
	`, symbol, cgID); err != nil {
		s.log.Error().Err(err).Str("symbol", symbol).Msg("mapping upsert")
		s.writeError(w, http.StatusInternalServerError, "upsert failed")
		return
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM coingecko_market_cache WHERE binance_symbol = $1`, symbol); err != nil {
		s.log.Error().Err(err).Str("symbol", symbol).Msg("mapping cache delete")
		s.writeError(w, http.StatusInternalServerError, "cache clear failed")
		return
	}
	// Audit log entry via admin_audit_log (migration 0014 schema).
	auditNew, _ := json.Marshal(map[string]string{"coingecko_id": cgID})
	if _, err := tx.Exec(ctx, `
		INSERT INTO admin_audit_log (operator, action_type, resource_type, resource_id, new_state, note)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, "mu", "coingecko_mapping_override", "coingecko_symbol_map", symbol,
		auditNew, "manual mapping fix via Web UI"); err != nil {
		// Don't fail the operation on audit log write error — but log warn.
		s.log.Warn().Err(err).Str("symbol", symbol).Msg("mapping audit log failed (non-fatal)")
	}
	if err := tx.Commit(ctx); err != nil {
		s.writeError(w, http.StatusInternalServerError, "commit failed")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{
		"status":         "ok",
		"binance_symbol": symbol,
		"coingecko_id":   cgID,
	})
}
