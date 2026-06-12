// R.28: uptrend favorites — user-curated watchlist for long-term tracking.
// Persisted as a Redis SET (admin:uptrend:favorites:v1). Writes are CSRF-guarded.
package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
)

const uptrendFavoritesKey = "admin:uptrend:favorites:v1"

type UptrendFavoritesResponse struct {
	Symbols []string `json:"symbols"`
}

type uptrendFavoriteRequest struct {
	Symbol string `json:"symbol"`
}

func (s *Server) handleUptrendFavoritesList(w http.ResponseWriter, r *http.Request) {
	if s.rdb == nil {
		s.writeJSON(w, http.StatusOK, UptrendFavoritesResponse{Symbols: []string{}})
		return
	}
	members, err := s.rdb.SMembers(r.Context(), uptrendFavoritesKey).Result()
	if err != nil {
		s.log.Warn().Err(err).Msg("uptrend.favorites: SMembers failed")
		s.writeJSON(w, http.StatusOK, UptrendFavoritesResponse{Symbols: []string{}})
		return
	}
	// Stable order for UI rendering.
	sort.Strings(members)
	s.writeJSON(w, http.StatusOK, UptrendFavoritesResponse{Symbols: members})
}

func (s *Server) handleUptrendFavoriteAdd(w http.ResponseWriter, r *http.Request) {
	sym, ok := s.parseSymbolBody(w, r)
	if !ok {
		return
	}
	if s.rdb == nil {
		s.writeError(w, http.StatusServiceUnavailable, "redis unavailable")
		return
	}
	if err := s.rdb.SAdd(r.Context(), uptrendFavoritesKey, sym).Err(); err != nil {
		s.log.Error().Err(err).Str("symbol", sym).Msg("uptrend.favorites: SAdd failed")
		s.writeError(w, http.StatusInternalServerError, "redis write failed")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "symbol": sym})
}

func (s *Server) handleUptrendFavoriteRemove(w http.ResponseWriter, r *http.Request) {
	sym := strings.ToUpper(strings.TrimSpace(r.PathValue("symbol")))
	if sym == "" {
		s.writeError(w, http.StatusBadRequest, "symbol required")
		return
	}
	if s.rdb == nil {
		s.writeError(w, http.StatusServiceUnavailable, "redis unavailable")
		return
	}
	if err := s.rdb.SRem(r.Context(), uptrendFavoritesKey, sym).Err(); err != nil {
		s.log.Error().Err(err).Str("symbol", sym).Msg("uptrend.favorites: SRem failed")
		s.writeError(w, http.StatusInternalServerError, "redis write failed")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "symbol": sym})
}

// parseSymbolBody reads {"symbol": "..."} from the request body, normalizes to
// uppercase, and validates non-empty + alnum-ish. Writes the error response on
// failure and returns ok=false.
func (s *Server) parseSymbolBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1024))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "read body failed")
		return "", false
	}
	var req uptrendFavoriteRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "json parse")
		return "", false
	}
	sym := strings.ToUpper(strings.TrimSpace(req.Symbol))
	if sym == "" {
		s.writeError(w, http.StatusBadRequest, "symbol required")
		return "", false
	}
	if !isPlainSymbol(sym) {
		s.writeError(w, http.StatusBadRequest, "invalid symbol chars")
		return "", false
	}
	return sym, true
}

// isPlainSymbol guards against shenanigans in the SET key. Real Binance perp
// symbols are uppercase letters + digits only (e.g. BTCUSDT, 1000PEPEUSDT).
func isPlainSymbol(s string) bool {
	if len(s) < 3 || len(s) > 32 {
		return false
	}
	for _, c := range s {
		if !((c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}
