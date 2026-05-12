package admin

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

// Server holds shared dependencies for all admin-api handlers.
type Server struct {
	db            *pgxpool.Pool
	rdb           *redis.Client
	prometheusURL string
	log           zerolog.Logger
	startTime     time.Time
}

func NewServer(db *pgxpool.Pool, rdb *redis.Client, prometheusURL string, log zerolog.Logger) *Server {
	return &Server{
		db:            db,
		rdb:           rdb,
		prometheusURL: prometheusURL,
		log:           log,
		startTime:     time.Now(),
	}
}

// Routes registers all 12 admin endpoints on a new ServeMux.
// Go 1.22+ method+path syntax; path values extracted via r.PathValue().
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/admin/health", s.handleHealth)
	mux.HandleFunc("GET /api/admin/dashboard", s.handleDashboard)
	mux.HandleFunc("GET /api/admin/positions/open", s.handlePositionsOpen)
	mux.HandleFunc("GET /api/admin/positions/history", s.handlePositionsHistory)
	mux.HandleFunc("GET /api/admin/pnl/cumulative", s.handlePnlCumulative)
	mux.HandleFunc("GET /api/admin/pnl/by_symbol", s.handlePnlBySymbol)
	mux.HandleFunc("GET /api/admin/pnl/by_exit_reason", s.handlePnlByExitReason)
	mux.HandleFunc("GET /api/admin/pnl/stats", s.handlePnlStats)
	mux.HandleFunc("GET /api/admin/square/trending", s.handleSquareTrending)
	mux.HandleFunc("GET /api/admin/watchlist", s.handleWatchlist)
	mux.HandleFunc("GET /api/admin/symbol/{symbol}", s.handleSymbolDetail)
	mux.HandleFunc("GET /api/admin/trade/{trade_id}", s.handleTradeDetail)
	return s.cors(mux)
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.log.Error().Err(err).Msg("encode response")
	}
}

func (s *Server) writeError(w http.ResponseWriter, code int, msg string) {
	s.writeJSON(w, code, map[string]string{"error": msg})
}

// --- Stub handlers (Round 2-6 will fill these in) ---

func (s *Server) handlePositionsOpen(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, []any{})
}

func (s *Server) handlePositionsHistory(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{"total": 0, "page": 1, "items": []any{}})
}

func (s *Server) handlePnlCumulative(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, []any{})
}

func (s *Server) handlePnlBySymbol(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, []any{})
}

func (s *Server) handlePnlByExitReason(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, []any{})
}

func (s *Server) handlePnlStats(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) handleSquareTrending(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, []any{})
}

func (s *Server) handleWatchlist(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, []any{})
}

func (s *Server) handleSymbolDetail(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{
		"symbol": r.PathValue("symbol"),
	})
}

func (s *Server) handleTradeDetail(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{
		"trade_id": r.PathValue("trade_id"),
	})
}
