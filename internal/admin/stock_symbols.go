package admin

import (
	"encoding/json"
	"net/http"
)

const stockSymbolsCacheKey = "admin:stock:symbols:v1"

// StockSymbolsResponse — list of USDⓈ-M perp symbols whose underlyingType is
// EQUITY (i.e. stock-backed perpetuals like TSLAUSDT / NVDAUSDT / COINUSDT).
// Populated by trader-side StockSymbolsCollector hourly. Frontend builds a Set
// and shows a "📈" badge wherever those symbols appear.
type StockSymbolsResponse struct {
	Symbols []string `json:"symbols"`
}

func (s *Server) handleStockSymbols(w http.ResponseWriter, r *http.Request) {
	if s.rdb == nil {
		s.writeJSON(w, http.StatusOK, StockSymbolsResponse{Symbols: []string{}})
		return
	}
	b, err := s.rdb.Get(r.Context(), stockSymbolsCacheKey).Bytes()
	if err != nil {
		// Cache miss (scan not yet run) — return empty, not an error.
		s.writeJSON(w, http.StatusOK, StockSymbolsResponse{Symbols: []string{}})
		return
	}
	var symbols []string
	if err := json.Unmarshal(b, &symbols); err != nil {
		s.log.Warn().Err(err).Msg("stock_symbols cache unmarshal")
		s.writeError(w, http.StatusInternalServerError, "stock_symbols cache parse")
		return
	}
	s.writeJSON(w, http.StatusOK, StockSymbolsResponse{Symbols: symbols})
}
