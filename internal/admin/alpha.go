package admin

import (
	"encoding/json"
	"net/http"
)

const alphaCacheKey = "admin:alpha:symbols:v1"

// AlphaSymbolsResponse — list of USDT perp symbols whose base asset is on
// Binance Alpha. Populated by trader-side AlphaCollector hourly. Frontend
// builds a Set and shows an "α" badge wherever those symbols appear.
type AlphaSymbolsResponse struct {
	Symbols []string `json:"symbols"`
}

func (s *Server) handleAlphaSymbols(w http.ResponseWriter, r *http.Request) {
	if s.rdb == nil {
		s.writeJSON(w, http.StatusOK, AlphaSymbolsResponse{Symbols: []string{}})
		return
	}
	b, err := s.rdb.Get(r.Context(), alphaCacheKey).Bytes()
	if err != nil {
		// Cache miss (scan not yet run) — return empty, not an error.
		s.writeJSON(w, http.StatusOK, AlphaSymbolsResponse{Symbols: []string{}})
		return
	}
	var symbols []string
	if err := json.Unmarshal(b, &symbols); err != nil {
		s.log.Warn().Err(err).Msg("alpha cache unmarshal")
		s.writeError(w, http.StatusInternalServerError, "alpha cache parse")
		return
	}
	s.writeJSON(w, http.StatusOK, AlphaSymbolsResponse{Symbols: symbols})
}
