package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
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

type CollectorStatusItem struct {
	Name            string  `json:"name"`
	LastTickSeconds float64 `json:"last_tick_seconds"` // Unix ts; 0 if never ran
	SuccessRate5min float64 `json:"success_rate_5min"` // 0-1; -1 if no data yet
	Status          string  `json:"status"`            // "active" | "stale" | "unknown"
}

type DashboardResponse struct {
	BalanceUSDT       float64               `json:"balance_usdt"`
	DailyPnL          float64               `json:"daily_pnl"`
	OpenPositions     int                   `json:"open_positions"`
	ConsecutiveLosses int32                 `json:"consecutive_losses"`
	BTC30mDropPct     float64               `json:"btc_30m_drop_pct"`
	HaltStatus        string                `json:"halt_status"`
	HaltReason        *string               `json:"halt_reason"`
	Collectors        []CollectorStatusItem `json:"collectors"`
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
	collectors, _ := s.collectorsStatus(ctx)

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
		Collectors:        collectors,
	})
}

// collectorsStatus queries Prometheus for each collector's last-tick time and
// 5min success rate. Returns empty slice (not error) if Prometheus unreachable.
func (s *Server) collectorsStatus(ctx context.Context) ([]CollectorStatusItem, error) {
	lastTicks, _ := s.promQueryVector(ctx, "trader_collector_last_tick_seconds", "collector")
	successCounts, _ := s.promQueryVector(ctx,
		`sum by (collector) (increase(trader_collector_runs_total{outcome="success"}[5m]))`, "collector")
	totalCounts, _ := s.promQueryVector(ctx,
		`sum by (collector) (increase(trader_collector_runs_total[5m]))`, "collector")

	names := make(map[string]struct{})
	for k := range lastTicks {
		names[k] = struct{}{}
	}
	for k := range successCounts {
		names[k] = struct{}{}
	}

	now := float64(time.Now().Unix())
	items := make([]CollectorStatusItem, 0, len(names))
	for name := range names {
		last := lastTicks[name]
		total := totalCounts[name]
		rate := -1.0
		if total > 0 {
			rate = successCounts[name] / total
		}
		status := "unknown"
		if last > 0 {
			if now-last < 3*60 {
				status = "active"
			} else {
				status = "stale"
			}
		}
		items = append(items, CollectorStatusItem{
			Name:            name,
			LastTickSeconds: last,
			SuccessRate5min: rate,
			Status:          status,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, nil
}

// promQueryFloat queries a no-label instant-vector metric and returns its value.
func (s *Server) promQueryFloat(ctx context.Context, metric string) (float64, error) {
	results, err := s.promQueryVector(ctx, metric, "")
	if err != nil {
		return 0, err
	}
	// No-label metric: result has one entry with an empty label key.
	for _, v := range results {
		return v, nil
	}
	return 0, fmt.Errorf("no result for %s", metric)
}

// promQueryVector queries Prometheus and returns a map of labelName value → float64.
// For metrics with no labels, pass labelName="" and read the single map entry.
func (s *Server) promQueryVector(ctx context.Context, query, labelName string) (map[string]float64, error) {
	u := s.prometheusURL + "/api/v1/query?query=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Data struct {
			Result []struct {
				Metric map[string]string  `json:"metric"`
				Value  [2]json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &result); err != nil || len(result.Data.Result) == 0 {
		return nil, fmt.Errorf("parse prom response for %q", query)
	}

	out := make(map[string]float64, len(result.Data.Result))
	for _, r := range result.Data.Result {
		key := r.Metric[labelName] // "" for no-label metrics
		var valStr string
		if err := json.Unmarshal(r.Value[1], &valStr); err != nil {
			continue
		}
		f, err := strconv.ParseFloat(valStr, 64)
		if err != nil {
			continue
		}
		out[key] = f
	}
	return out, nil
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
