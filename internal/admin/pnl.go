package admin

import (
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// timeRangeQuery parses ?range=today|week|month and ?data_source=mainnet|testnet|all.
// Returns (args, cond) for WHERE clauses filtering on exit_ts + data_source.
func timeRangeQuery(r *http.Request) (args []any, cond string) {
	until := time.Now().UTC()
	var since time.Time
	switch r.URL.Query().Get("range") {
	case "today":
		since = time.Date(until.Year(), until.Month(), until.Day(), 0, 0, 0, 0, time.UTC)
	case "week":
		since = until.AddDate(0, 0, -7)
	case "month":
		since = until.AddDate(0, -1, 0)
	}

	ds := r.URL.Query().Get("data_source") // mainnet | testnet | all (default: testnet, R.18 D2)
	if ds == "" { ds = "testnet" }

	var dsCond string
	switch ds {
	case "mainnet":
		dsCond = "data_source = 'mainnet'"
	case "all":
		dsCond = "TRUE"
	default:
		dsCond = "data_source = 'testnet'"
	}

	if since.IsZero() {
		return []any{until}, fmt.Sprintf("exit_ts <= $1 AND %s", dsCond)
	}
	return []any{since, until}, fmt.Sprintf("exit_ts >= $1 AND exit_ts <= $2 AND %s", dsCond)
}

// --- /api/admin/pnl/cumulative ---

type CumulativePoint struct {
	Date       string  `json:"date"`
	DailyPnl   float64 `json:"daily_pnl"`
	Cumulative float64 `json:"cumulative"`
}

func (s *Server) handlePnlCumulative(w http.ResponseWriter, r *http.Request) {
	args, cond := timeRangeQuery(r)
	ctx := r.Context()
	rows, err := s.db.Query(ctx, `
		SELECT DATE(exit_ts AT TIME ZONE 'UTC'), COALESCE(SUM(realized_pnl), 0)::float8
		FROM trades
		WHERE status = 'closed' AND `+cond+`
		GROUP BY DATE(exit_ts AT TIME ZONE 'UTC')
		ORDER BY 1 ASC
	`, args...)
	if err != nil {
		s.log.Error().Err(err).Msg("pnl cumulative")
		s.writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	points := make([]CumulativePoint, 0)
	var cum float64
	for rows.Next() {
		var d pgtype.Date
		var v float64
		if err := rows.Scan(&d, &v); err != nil {
			continue
		}
		cum += v
		date := ""
		if d.Valid {
			date = d.Time.Format("2006-01-02")
		}
		points = append(points, CumulativePoint{Date: date, DailyPnl: v, Cumulative: cum})
	}
	s.writeJSON(w, http.StatusOK, points)
}

// --- /api/admin/pnl/by_symbol ---

type SymbolPnl struct {
	Symbol      string  `json:"symbol"`
	RealizedPnl float64 `json:"realized_pnl"`
	TradeCount  int     `json:"trade_count"`
	WinCount    int     `json:"win_count"`
}

func (s *Server) handlePnlBySymbol(w http.ResponseWriter, r *http.Request) {
	args, cond := timeRangeQuery(r)
	ctx := r.Context()
	rows, err := s.db.Query(ctx, `
		SELECT symbol,
		       COALESCE(SUM(realized_pnl), 0)::float8,
		       COUNT(*)::int,
		       COUNT(*) FILTER (WHERE realized_pnl > 0)::int
		FROM trades
		WHERE status = 'closed' AND `+cond+`
		GROUP BY symbol
		ORDER BY SUM(realized_pnl) DESC
	`, args...)
	if err != nil {
		s.log.Error().Err(err).Msg("pnl by symbol")
		s.writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	result := make([]SymbolPnl, 0)
	for rows.Next() {
		var sym string
		var pnl float64
		var cnt, wins int
		if err := rows.Scan(&sym, &pnl, &cnt, &wins); err != nil {
			continue
		}
		result = append(result, SymbolPnl{Symbol: sym, RealizedPnl: pnl, TradeCount: cnt, WinCount: wins})
	}
	s.writeJSON(w, http.StatusOK, result)
}

// --- /api/admin/pnl/by_exit_reason ---

type ExitReasonPnl struct {
	ExitReason  string  `json:"exit_reason"`
	Count       int     `json:"count"`
	RealizedPnl float64 `json:"realized_pnl"`
}

func (s *Server) handlePnlByExitReason(w http.ResponseWriter, r *http.Request) {
	args, cond := timeRangeQuery(r)
	ctx := r.Context()
	rows, err := s.db.Query(ctx, `
		SELECT COALESCE(exit_reason, 'unknown'),
		       COUNT(*)::int,
		       COALESCE(SUM(realized_pnl), 0)::float8
		FROM trades
		WHERE status IN ('closed', 'failed') AND `+cond+`
		GROUP BY exit_reason
		ORDER BY COUNT(*) DESC
	`, args...)
	if err != nil {
		s.log.Error().Err(err).Msg("pnl by exit reason")
		s.writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	result := make([]ExitReasonPnl, 0)
	for rows.Next() {
		var reason string
		var cnt int
		var pnl float64
		if err := rows.Scan(&reason, &cnt, &pnl); err != nil {
			continue
		}
		result = append(result, ExitReasonPnl{ExitReason: reason, Count: cnt, RealizedPnl: pnl})
	}
	s.writeJSON(w, http.StatusOK, result)
}

// --- /api/admin/pnl/stats ---

type PnlStats struct {
	TotalTrades  int     `json:"total_trades"`
	WinCount     int     `json:"win_count"`
	LossCount    int     `json:"loss_count"`
	WinRate      float64 `json:"win_rate"`
	TotalPnl     float64 `json:"total_pnl"`
	AvgPnl       float64 `json:"avg_pnl"`
	AvgWinPnl    float64 `json:"avg_win_pnl"`
	AvgLossPnl   float64 `json:"avg_loss_pnl"`
	AvgHoldMs    float64 `json:"avg_hold_ms"`
	ProfitFactor float64 `json:"profit_factor"`
}

func (s *Server) handlePnlStats(w http.ResponseWriter, r *http.Request) {
	args, cond := timeRangeQuery(r)
	ctx := r.Context()

	var total, wins, losses int64
	var totalPnl, avgPnl, avgWin, avgLoss, avgHoldMs, sumWins, sumLosses float64

	err := s.db.QueryRow(ctx, `
		SELECT
			COUNT(*),
			COUNT(*) FILTER (WHERE realized_pnl > 0),
			COUNT(*) FILTER (WHERE realized_pnl <= 0),
			COALESCE(SUM(realized_pnl), 0)::float8,
			COALESCE(AVG(realized_pnl), 0)::float8,
			COALESCE(AVG(realized_pnl) FILTER (WHERE realized_pnl > 0), 0)::float8,
			COALESCE(AVG(realized_pnl) FILTER (WHERE realized_pnl <= 0), 0)::float8,
			COALESCE(AVG(EXTRACT(EPOCH FROM (exit_ts - entry_ts)) * 1000.0), 0),
			COALESCE(SUM(realized_pnl) FILTER (WHERE realized_pnl > 0), 0)::float8,
			COALESCE(ABS(SUM(realized_pnl) FILTER (WHERE realized_pnl <= 0)), 0)::float8
		FROM trades
		WHERE status = 'closed' AND `+cond,
		args...,
	).Scan(&total, &wins, &losses, &totalPnl, &avgPnl, &avgWin, &avgLoss,
		&avgHoldMs, &sumWins, &sumLosses)
	if err != nil {
		s.log.Error().Err(err).Msg("pnl stats")
		s.writeError(w, http.StatusInternalServerError, "db error")
		return
	}

	pf := 0.0
	if sumLosses > 0 {
		pf = sumWins / sumLosses
	}
	wr := 0.0
	if total > 0 {
		wr = float64(wins) / float64(total) * 100
	}

	s.writeJSON(w, http.StatusOK, PnlStats{
		TotalTrades:  int(total),
		WinCount:     int(wins),
		LossCount:    int(losses),
		WinRate:      wr,
		TotalPnl:     totalPnl,
		AvgPnl:       avgPnl,
		AvgWinPnl:    avgWin,
		AvgLossPnl:   avgLoss,
		AvgHoldMs:    avgHoldMs,
		ProfitFactor: pf,
	})
}
