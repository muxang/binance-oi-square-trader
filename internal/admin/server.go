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
//
// db      → read-only pool (max_conns=5, SET default_transaction_read_only=on).
//           Used by every GET endpoint.
// writeDB → writable pool (max_conns=2). Used only by Round R.1 Part 2+
//           write endpoints (manual halt reset; future: manual close, etc.).
//           Kept separate so write bugs can't starve reads.
type Server struct {
	db            *pgxpool.Pool
	writeDB       *pgxpool.Pool
	rdb           *redis.Client
	csrf          *csrfStore // Phase 5.2 Round 1: write-endpoint CSRF guard
	prometheusURL string
	log           zerolog.Logger
	startTime     time.Time
}

func NewServer(db, writeDB *pgxpool.Pool, rdb *redis.Client, prometheusURL string, log zerolog.Logger) *Server {
	return &Server{
		db:            db,
		writeDB:       writeDB,
		rdb:           rdb,
		csrf:          newCsrfStore(),
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
	mux.HandleFunc("GET /api/admin/market", s.handleMarket)
	mux.HandleFunc("GET /api/admin/square/trending", s.handleSquareTrending)
	mux.HandleFunc("GET /api/admin/watchlist", s.handleWatchlist)
	mux.HandleFunc("GET /api/admin/symbol/{symbol}", s.handleSymbolDetail)
	mux.HandleFunc("GET /api/admin/trade/{trade_id}", s.handleTradeDetail)
	// v0.2 Round R.1 Part 2: first admin WRITE endpoint (manual halt reset).
	// Phase 5.2 Round 1: wrapped with CSRF guard.
	mux.HandleFunc("POST /api/admin/circuit-breaker/reset", s.requireCsrf(s.handleCircuitBreakerReset))
	mux.HandleFunc("GET /api/admin/circuit-breaker/events", s.handleCircuitBreakerEvents)
	// Phase 5.2 Round 1: CSRF token endpoint. Caddy basic auth at path matcher
	// guards this in production; browser prompts on first call per session.
	mux.HandleFunc("GET /api/admin/csrf-token", s.handleCsrfToken)

	// Phase 5.2 Round 2: 7 write endpoints (manual close `d` deferred to Round 2.x,
	// RCA ack `h` to Round 4). All wrapped with CSRF; Caddy provides basic auth.
	mux.HandleFunc("POST /api/admin/circuit-breaker/daily-pnl-reset", s.requireCsrf(s.handleDailyPnlReset))
	mux.HandleFunc("POST /api/admin/circuit-breaker/consec-reset", s.requireCsrf(s.handleConsecReset))
	mux.HandleFunc("POST /api/admin/circuit-breaker/halt", s.requireCsrf(s.handleManualHalt))
	mux.HandleFunc("PUT /api/admin/config/circuit-breaker-thresholds", s.requireCsrf(s.handleCBThresholds))
	mux.HandleFunc("PUT /api/admin/config/signal-thresholds", s.requireCsrf(s.handleSignalThresholds))
	mux.HandleFunc("PUT /api/admin/watchlist/include/{symbol}", s.requireCsrf(s.handleWatchlistInclude))
	mux.HandleFunc("PUT /api/admin/watchlist/exclude/{symbol}", s.requireCsrf(s.handleWatchlistExclude))
	return s.cors(mux)
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
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

// handlePositionsOpen    implemented in positions.go
// handlePositionsHistory implemented in history.go
// handlePnl*             implemented in pnl.go
// handleMarket           implemented in market.go
// handleSquareTrending   implemented in square.go
// handleSymbolDetail     implemented in symbol.go
// handleTradeDetail      implemented in tradedetail.go

func (s *Server) handleWatchlist(w http.ResponseWriter, r *http.Request) {
	// legacy alias for /api/admin/market?scope=watchlist
	s.writeJSON(w, http.StatusOK, []any{})
}
