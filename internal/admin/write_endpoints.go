// Phase 5.2 Round 2: 7 write endpoints (1 deferred to Round 2.x).
//
// All endpoints share: Caddy basic auth (already applied) + CSRF middleware
// + transaction (BEGIN/UPDATE/INSERT audit/COMMIT) + audit log to admin_audit_log.
//
// Endpoint matrix:
//   ✅ POST /api/admin/circuit-breaker/daily-pnl-reset   (a) daily_pnl=0
//   ✅ POST /api/admin/circuit-breaker/consec-reset      (b) consecutive_losses=0
//   ✅ POST /api/admin/circuit-breaker/halt              (i) manual halt
//   ✅ PUT  /api/admin/config/circuit-breaker-thresholds (e) write admin_overrides
//   ✅ PUT  /api/admin/config/signal-thresholds          (g) write admin_overrides
//   ✅ PUT  /api/admin/watchlist/include/:symbol         (f1) write watchlist_overrides
//   ✅ PUT  /api/admin/watchlist/exclude/:symbol         (f2) write watchlist_overrides
//   ⚠️ POST /api/admin/trades/:id/close                  (d) DEFERRED — Round 2.x
//                                                            (needs migration 0017 + exit_manager
//                                                            integration to pre-set exit_reason
//                                                            on 'closing' status)
//   ⚠️ POST /api/admin/halt-rca/:event_id/ack             (h) DEFERRED — Round 4 (跟飞书一起)
//
// Trader-side reload of admin_overrides + watchlist_overrides: Round 2.x
// (config_reloader collector 1min cron; until then, changes require trader restart).
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// --- a. daily_pnl reset ---

type pnlResetRequest struct {
	Confirm bool   `json:"confirm"`
	Note    string `json:"note"`
}

func (s *Server) handleDailyPnlReset(w http.ResponseWriter, r *http.Request) {
	var req pnlResetRequest
	if !s.decodeAndConfirm(w, r, &req.Confirm, &req, "confirm=true required") {
		return
	}
	s.adminMutate(w, r, "daily_pnl_reset", "circuit_breaker", "1", req.Note,
		`SELECT json_build_object('daily_pnl', daily_pnl::text, 'daily_pnl_date', daily_pnl_date)::jsonb FROM circuit_breaker_state WHERE id=1`,
		`UPDATE circuit_breaker_state SET daily_pnl=0, daily_pnl_date=CURRENT_DATE WHERE id=1`,
		[]byte(`{"daily_pnl": 0}`),
	)
}

// --- b. consec_losses reset ---

func (s *Server) handleConsecReset(w http.ResponseWriter, r *http.Request) {
	var req pnlResetRequest
	if !s.decodeAndConfirm(w, r, &req.Confirm, &req, "confirm=true required") {
		return
	}
	s.adminMutate(w, r, "consec_reset", "circuit_breaker", "1", req.Note,
		`SELECT json_build_object('consecutive_losses', consecutive_losses, 'last_loss_at', last_loss_at)::jsonb FROM circuit_breaker_state WHERE id=1`,
		`UPDATE circuit_breaker_state SET consecutive_losses=0, last_loss_at=NULL WHERE id=1`,
		[]byte(`{"consecutive_losses": 0}`),
	)
}

// --- i. manual halt (mu 主动 halt) ---

type manualHaltRequest struct {
	Confirm       bool   `json:"confirm"`
	DurationHours int    `json:"duration_hours"` // 1-168 (1week max)
	Note          string `json:"note"`
}

func (s *Server) handleManualHalt(w http.ResponseWriter, r *http.Request) {
	var req manualHaltRequest
	if !s.decodeAndConfirm(w, r, &req.Confirm, &req, "confirm=true required") {
		return
	}
	if req.DurationHours < 1 || req.DurationHours > 168 {
		s.writeError(w, http.StatusBadRequest, "duration_hours must be 1..168")
		return
	}
	haltUntil := time.Now().UTC().Add(time.Duration(req.DurationHours) * time.Hour)
	newState, _ := json.Marshal(map[string]any{
		"trading_halted": true,
		"halt_reason":    "manual_admin",
		"halt_until":     haltUntil.Format(time.RFC3339),
	})
	s.adminMutate(w, r, "manual_halt", "circuit_breaker", "1", req.Note,
		`SELECT json_build_object('trading_halted', trading_halted, 'halt_reason', halt_reason, 'halt_until', halt_until)::jsonb FROM circuit_breaker_state WHERE id=1`,
		fmt.Sprintf(`UPDATE circuit_breaker_state SET trading_halted=true, halt_reason='manual_admin', halt_until='%s' WHERE id=1`, haltUntil.Format("2006-01-02 15:04:05.000-07:00")),
		newState,
	)
}

// --- e. circuit_breaker thresholds (write admin_overrides) ---

type cbThresholdsRequest struct {
	Confirm                bool    `json:"confirm"`
	DailyLossHaltPct       *string `json:"daily_loss_halt_pct"`        // decimal as string
	ConsecutiveLossesHalt  *int    `json:"consecutive_losses_halt"`
	TotalFloatLossHaltPct  *string `json:"total_float_loss_halt_pct"`
	BtcPanicDropPct        *string `json:"btc_panic_drop_pct"`
	OiImbalanceRatio       *string `json:"oi_imbalance_ratio_threshold"`
	Note                   string  `json:"note"`
}

func (s *Server) handleCBThresholds(w http.ResponseWriter, r *http.Request) {
	var req cbThresholdsRequest
	if !s.decodeAndConfirm(w, r, &req.Confirm, &req, "confirm=true required") {
		return
	}
	updates := map[string]any{}
	if req.DailyLossHaltPct != nil {
		updates["DAILY_LOSS_HALT_PCT"] = *req.DailyLossHaltPct
	}
	if req.ConsecutiveLossesHalt != nil {
		updates["CONSECUTIVE_LOSSES_HALT"] = *req.ConsecutiveLossesHalt
	}
	if req.TotalFloatLossHaltPct != nil {
		updates["TOTAL_FLOAT_LOSS_HALT_PCT"] = *req.TotalFloatLossHaltPct
	}
	if req.BtcPanicDropPct != nil {
		updates["BTC_PANIC_DROP_PCT"] = *req.BtcPanicDropPct
	}
	if req.OiImbalanceRatio != nil {
		updates["OI_IMBALANCE_RATIO_THRESHOLD"] = *req.OiImbalanceRatio
	}
	if len(updates) == 0 {
		s.writeError(w, http.StatusBadRequest, "no threshold provided")
		return
	}
	s.writeOverridesAndAudit(w, r, "cb_thresholds_update", updates, req.Note)
}

// --- g. signal_engine thresholds ---

type signalThresholdsRequest struct {
	Confirm                  bool    `json:"confirm"`
	OIGrowthFromMinPct       *string `json:"oi_growth_from_min_pct"`
	OISurgeRecentPeriods     *int    `json:"oi_surge_recent_periods"`
	SquareRatioThreshold     *string `json:"square_ratio_threshold"`
	SquareAccelThreshold     *string `json:"square_hot_acceleration_threshold"`
	Note                     string  `json:"note"`
}

func (s *Server) handleSignalThresholds(w http.ResponseWriter, r *http.Request) {
	var req signalThresholdsRequest
	if !s.decodeAndConfirm(w, r, &req.Confirm, &req, "confirm=true required") {
		return
	}
	updates := map[string]any{}
	if req.OIGrowthFromMinPct != nil {
		updates["OI_GROWTH_FROM_MIN_PCT"] = *req.OIGrowthFromMinPct
	}
	if req.OISurgeRecentPeriods != nil {
		updates["OI_SURGE_RECENT_PERIODS"] = *req.OISurgeRecentPeriods
	}
	if req.SquareRatioThreshold != nil {
		updates["SQUARE_HOT_MULTIPLIER"] = *req.SquareRatioThreshold
	}
	if req.SquareAccelThreshold != nil {
		updates["SQUARE_HOT_ACCEL_THRESHOLD"] = *req.SquareAccelThreshold
	}
	if len(updates) == 0 {
		s.writeError(w, http.StatusBadRequest, "no threshold provided")
		return
	}
	s.writeOverridesAndAudit(w, r, "signal_thresholds_update", updates, req.Note)
}

// --- f. watchlist include/exclude ---

type watchlistRequest struct {
	Confirm bool   `json:"confirm"`
	Reason  string `json:"reason"`
}

func (s *Server) handleWatchlistInclude(w http.ResponseWriter, r *http.Request) {
	s.handleWatchlistAction(w, r, "include")
}
func (s *Server) handleWatchlistExclude(w http.ResponseWriter, r *http.Request) {
	s.handleWatchlistAction(w, r, "exclude")
}

func (s *Server) handleWatchlistAction(w http.ResponseWriter, r *http.Request, action string) {
	symbol := r.PathValue("symbol")
	if symbol == "" {
		s.writeError(w, http.StatusBadRequest, "symbol required")
		return
	}
	var req watchlistRequest
	if !s.decodeAndConfirm(w, r, &req.Confirm, &req, "confirm=true required") {
		return
	}

	ctx := r.Context()
	tx, err := s.writeDB.Begin(ctx)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "db tx error")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var prevAction, prevReason string
	err = tx.QueryRow(ctx, `SELECT COALESCE(action,''), COALESCE(reason,'') FROM watchlist_overrides WHERE symbol=$1`, symbol).
		Scan(&prevAction, &prevReason)
	if err != nil && err != pgx.ErrNoRows {
		s.log.Error().Err(err).Msg("watchlist: read prev failed")
		s.writeError(w, http.StatusInternalServerError, "db error")
		return
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO watchlist_overrides (symbol, action, reason, updated_by, updated_at)
		VALUES ($1, $2, $3, 'mu', NOW())
		ON CONFLICT (symbol) DO UPDATE
		SET action=EXCLUDED.action, reason=EXCLUDED.reason, updated_by='mu', updated_at=NOW()
	`, symbol, action, req.Reason); err != nil {
		s.log.Error().Err(err).Msg("watchlist: upsert failed")
		s.writeError(w, http.StatusInternalServerError, "upsert error")
		return
	}

	prev, _ := json.Marshal(map[string]any{"action": prevAction, "reason": prevReason})
	newSt, _ := json.Marshal(map[string]any{"action": action, "reason": req.Reason})
	if err := s.insertAuditLogTx(ctx, tx, r, "watchlist_"+action, "watchlist", symbol, prev, newSt, req.Reason); err != nil {
		s.writeError(w, http.StatusInternalServerError, "audit error")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		s.writeError(w, http.StatusInternalServerError, "commit error")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "symbol": symbol, "action": action})
}

// --- internal helpers ---

// decodeAndConfirm decodes JSON body + checks the confirm flag pointer.
// Returns false (and writes error response) on any failure.
func (s *Server) decodeAndConfirm(w http.ResponseWriter, r *http.Request, confirm *bool, dst any, confirmErrMsg string) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	if !*confirm {
		s.writeError(w, http.StatusBadRequest, confirmErrMsg)
		return false
	}
	return true
}

// adminMutate runs a generic single-row mutation inside a transaction:
//   1. Read previous state via prevQuery (must return a single JSONB column)
//   2. Apply mutation via updateStmt
//   3. INSERT admin_audit_log with previous_state + new_state
//   4. COMMIT
func (s *Server) adminMutate(w http.ResponseWriter, r *http.Request, actionType, resourceType, resourceID, note, prevQuery, updateStmt string, newState []byte) {
	ctx := r.Context()
	tx, err := s.writeDB.Begin(ctx)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "db tx error")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var prev []byte
	if err := tx.QueryRow(ctx, prevQuery).Scan(&prev); err != nil {
		s.log.Error().Err(err).Str("action", actionType).Msg("adminMutate: read prev failed")
		s.writeError(w, http.StatusInternalServerError, "read prev error")
		return
	}
	if _, err := tx.Exec(ctx, updateStmt); err != nil {
		s.log.Error().Err(err).Str("action", actionType).Msg("adminMutate: update failed")
		s.writeError(w, http.StatusInternalServerError, "update error")
		return
	}
	if err := s.insertAuditLogTx(ctx, tx, r, actionType, resourceType, resourceID, prev, newState, note); err != nil {
		s.writeError(w, http.StatusInternalServerError, "audit error")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		s.writeError(w, http.StatusInternalServerError, "commit error")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "action": actionType})
}

// writeOverridesAndAudit writes N config keys to admin_overrides + audit log.
// Each key gets its own row (key, value JSONB, previous_value JSONB).
func (s *Server) writeOverridesAndAudit(w http.ResponseWriter, r *http.Request, actionType string, updates map[string]any, note string) {
	ctx := r.Context()
	tx, err := s.writeDB.Begin(ctx)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "db tx error")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	prevAll := map[string]any{}
	for key, val := range updates {
		// Read previous value (if any).
		var prevBytes []byte
		err := tx.QueryRow(ctx, `SELECT value FROM admin_overrides WHERE key=$1`, key).Scan(&prevBytes)
		if err != nil && err != pgx.ErrNoRows {
			s.log.Error().Err(err).Str("key", key).Msg("override: read prev failed")
			s.writeError(w, http.StatusInternalServerError, "db error")
			return
		}
		if len(prevBytes) > 0 {
			var prevVal any
			_ = json.Unmarshal(prevBytes, &prevVal)
			prevAll[key] = prevVal
		}
		// Upsert.
		newJSON, _ := json.Marshal(map[string]any{"value": val})
		if _, err := tx.Exec(ctx, `
			INSERT INTO admin_overrides (key, value, previous_value, updated_by, updated_at, note)
			VALUES ($1, $2::jsonb, $3::jsonb, 'mu', NOW(), $4)
			ON CONFLICT (key) DO UPDATE
			SET previous_value=admin_overrides.value, value=EXCLUDED.value,
			    updated_by='mu', updated_at=NOW(), note=EXCLUDED.note
		`, key, newJSON, prevBytes, note); err != nil {
			s.log.Error().Err(err).Str("key", key).Msg("override: upsert failed")
			s.writeError(w, http.StatusInternalServerError, "upsert error")
			return
		}
	}

	prevJSON, _ := json.Marshal(prevAll)
	newJSON, _ := json.Marshal(updates)
	if err := s.insertAuditLogTx(ctx, tx, r, actionType, "config", actionType, prevJSON, newJSON, note); err != nil {
		s.writeError(w, http.StatusInternalServerError, "audit error")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		s.writeError(w, http.StatusInternalServerError, "commit error")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "updated_keys": len(updates)})
}

// insertAuditLogTx inserts one admin_audit_log row inside the given transaction.
// IP + UA captured from request (best-effort).
func (s *Server) insertAuditLogTx(ctx context.Context, tx pgx.Tx, r *http.Request, actionType, resourceType, resourceID string, prev, newSt []byte, note string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO admin_audit_log
			(ts, operator, action_type, resource_type, resource_id,
			 previous_state, new_state, note, ip_address, user_agent)
		VALUES ($1, 'mu', $2, $3, $4, $5::jsonb, $6::jsonb, $7, $8::inet, $9)
	`, time.Now().UTC(), actionType, resourceType, resourceID, prev, newSt, note, clientIPOrNull(r), r.UserAgent())
	return err
}

// clientIPOrNull extracts the client IP from X-Forwarded-For / RemoteAddr.
// Returns nil (NULL in PG) if neither yields a parseable IP. Strips port.
func clientIPOrNull(r *http.Request) any {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Caddy sets X-Forwarded-For to the original client IP.
		// May be comma-separated list — take the first.
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				return xff[:i]
			}
		}
		return xff
	}
	if r.RemoteAddr == "" {
		return nil
	}
	// RemoteAddr is "ip:port" — strip port.
	for i := len(r.RemoteAddr) - 1; i >= 0; i-- {
		if r.RemoteAddr[i] == ':' {
			return r.RemoteAddr[:i]
		}
	}
	return r.RemoteAddr
}

// strconv used in some endpoints (compile placeholder so file compiles in isolation).
var _ = strconv.Itoa
