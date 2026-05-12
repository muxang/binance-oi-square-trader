package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

type HealthResponse struct {
	Status string `json:"status"`
	DB     string `json:"db"`
	Redis  string `json:"redis"`
	Uptime string `json:"uptime"`
}

type DashboardResponse struct {
	BalanceUSDT       float64  `json:"balance_usdt"`
	DailyPnL          float64  `json:"daily_pnl"`
	OpenPositions     int      `json:"open_positions"`
	ConsecutiveLosses int32    `json:"consecutive_losses"`
	BTC30mDropPct     float64  `json:"btc_30m_drop_pct"`
	HaltStatus        string   `json:"halt_status"`
	HaltReason        *string  `json:"halt_reason"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	dbStatus := "ok"
	if err := s.db.Ping(r.Context()); err != nil {
		dbStatus = "error"
	}
	redisStatus := "ok"
	if s.rdb != nil {
		if err := s.rdb.Ping(r.Context()).Err(); err != nil {
			redisStatus = "error"
		}
	}
	status := "ok"
	code := http.StatusOK
	if dbStatus != "ok" {
		status = "degraded"
		code = http.StatusServiceUnavailable
	}
	uptime := time.Since(s.startTime).Truncate(time.Second).String()
	s.writeJSON(w, code, HealthResponse{
		Status: status,
		DB:     dbStatus,
		Redis:  redisStatus,
		Uptime: uptime,
	})
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var (
		tradingHalted     bool
		haltReason        pgtype.Text
		dailyPnL          pgtype.Numeric
		consecutiveLosses int32
	)
	row := s.db.QueryRow(ctx, `
		SELECT trading_halted, halt_reason, daily_pnl, consecutive_losses
		FROM circuit_breaker_state
		LIMIT 1
	`)
	if err := row.Scan(&tradingHalted, &haltReason, &dailyPnL, &consecutiveLosses); err != nil {
		s.log.Error().Err(err).Msg("query circuit_breaker_state")
		s.writeError(w, http.StatusInternalServerError, "db error")
		return
	}

	var openCount int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM trades WHERE status = 'open'`).Scan(&openCount); err != nil {
		s.log.Warn().Err(err).Msg("count open trades")
	}

	balanceUSDT, _ := s.promQueryFloat(ctx, "trader_account_balance_usdt")
	btcDrop, _ := s.promQueryFloat(ctx, "trader_btc_30min_drop_pct")

	haltStatus := "NORMAL"
	if tradingHalted {
		haltStatus = "HALTED"
	}
	var haltReasonPtr *string
	if haltReason.Valid {
		haltReasonPtr = &haltReason.String
	}

	s.writeJSON(w, http.StatusOK, DashboardResponse{
		BalanceUSDT:       balanceUSDT,
		DailyPnL:          numericToFloat64(dailyPnL),
		OpenPositions:     openCount,
		ConsecutiveLosses: consecutiveLosses,
		BTC30mDropPct:     btcDrop,
		HaltStatus:        haltStatus,
		HaltReason:        haltReasonPtr,
	})
}

// promQueryFloat queries a single instant-vector metric from Prometheus and
// returns its float64 value. Returns 0 if the metric has no result.
func (s *Server) promQueryFloat(ctx context.Context, metric string) (float64, error) {
	u := s.prometheusURL + "/api/v1/query?query=" + url.QueryEscape(metric)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	// {"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[<ts>,"<val>"]}]}}
	var result struct {
		Data struct {
			Result []struct {
				Value [2]json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &result); err != nil || len(result.Data.Result) == 0 {
		return 0, fmt.Errorf("no result for %s", metric)
	}
	var valStr string
	if err := json.Unmarshal(result.Data.Result[0].Value[1], &valStr); err != nil {
		return 0, err
	}
	return strconv.ParseFloat(valStr, 64)
}

// numericToFloat64 converts pgtype.Numeric to float64 for display purposes.
// Precision loss is acceptable here — dashboard shows 2 decimal places.
func numericToFloat64(n pgtype.Numeric) float64 {
	if !n.Valid || n.Int == nil {
		return 0
	}
	f, _ := n.Int.Float64()
	if n.Exp > 0 {
		for i := int32(0); i < n.Exp; i++ {
			f *= 10
		}
	} else {
		for i := n.Exp; i < 0; i++ {
			f /= 10
		}
	}
	if n.NaN {
		return 0
	}
	return f
}
