// v0.2 Round R.1 Part 2: admin manual halt reset endpoint.
//
// POST /api/admin/circuit-breaker/reset
//
//	body: {"confirm": true}
//	auth: Caddy basic-auth at reverse proxy (no in-process auth — trusting infra)
//
// Idempotent semantics:
//   - if trading_halted=false: 409 (nothing to reset)
//   - if confirm != true:      400
//   - on success: clears halt_reason/halt_until + sets manual_reset_at/by
//   - INSERT circuit_breaker_events row (event_type='manual_reset' with snapshots)
//
// GET /api/admin/circuit-breaker/events
//
//	returns last 50 events (newest first) for audit history panel.
package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type CBResetRequest struct {
	Confirm bool   `json:"confirm"`
	Actor   string `json:"actor"` // optional override; defaults to "mu"
	Note    string `json:"note"`
}

type CBResetResponse struct {
	OK              bool      `json:"ok"`
	PreviousReason  string    `json:"previous_halt_reason"`
	PreviousUntil   time.Time `json:"previous_halt_until"`
	ManualResetAt   time.Time `json:"manual_reset_at"`
	ManualResetBy   string    `json:"manual_reset_by"`
}

type CBEvent struct {
	ID                int64     `json:"id"`
	Ts                time.Time `json:"ts"`
	EventType         string    `json:"event_type"`
	HaltReason        string    `json:"halt_reason"`
	HaltUntilBefore   *time.Time `json:"halt_until_before"`
	Actor             string    `json:"actor"`
	DailyPnlSnapshot  string    `json:"daily_pnl_snapshot"`
	ConsecLossesSnap  int16     `json:"consecutive_losses_snapshot"`
	Note              string    `json:"note"`
}

type CBEventsResponse struct {
	Events []CBEvent `json:"events"`
}

func (s *Server) handleCircuitBreakerReset(w http.ResponseWriter, r *http.Request) {
	var req CBResetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !req.Confirm {
		s.writeError(w, http.StatusBadRequest, "confirm=true required")
		return
	}
	actor := req.Actor
	if actor == "" {
		actor = "mu"
	}

	ctx := r.Context()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		s.log.Error().Err(err).Msg("cb_reset: begin tx failed")
		s.writeError(w, http.StatusInternalServerError, "db tx error")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback-on-error is fine; commit clears it

	// Snapshot current state (inside tx, with FOR UPDATE to serialize concurrent resets).
	var (
		halted        bool
		haltReason    *string
		haltUntil     *time.Time
		dailyPnl      string
		consecLosses  int16
	)
	if err := tx.QueryRow(ctx, `
		SELECT trading_halted, halt_reason, halt_until, daily_pnl::text, consecutive_losses
		FROM circuit_breaker_state WHERE id = 1 FOR UPDATE
	`).Scan(&halted, &haltReason, &haltUntil, &dailyPnl, &consecLosses); err != nil {
		s.log.Error().Err(err).Msg("cb_reset: read state failed")
		s.writeError(w, http.StatusInternalServerError, "read state error")
		return
	}
	if !halted {
		s.writeError(w, http.StatusConflict, "no halt to reset (trading_halted=false)")
		return
	}

	now := time.Now().UTC()
	if _, err := tx.Exec(ctx, `
		UPDATE circuit_breaker_state
		SET trading_halted   = false,
		    halt_reason      = NULL,
		    halt_until       = NULL,
		    manual_reset_at  = $1,
		    manual_reset_by  = $2
		WHERE id = 1
	`, now, actor); err != nil {
		s.log.Error().Err(err).Msg("cb_reset: update state failed")
		s.writeError(w, http.StatusInternalServerError, "update state error")
		return
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO circuit_breaker_events
			(ts, event_type, halt_reason, halt_until_before, actor, daily_pnl_snapshot, consec_losses_snapshot, note)
		VALUES ($1, 'manual_reset', $2, $3, $4, $5::numeric, $6, $7)
	`, now, derefOrEmpty(haltReason), haltUntil, actor, dailyPnl, consecLosses, req.Note); err != nil {
		s.log.Error().Err(err).Msg("cb_reset: insert event failed")
		s.writeError(w, http.StatusInternalServerError, "audit log error")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		s.log.Error().Err(err).Msg("cb_reset: commit failed")
		s.writeError(w, http.StatusInternalServerError, "commit error")
		return
	}

	s.log.Warn().
		Str("actor", actor).
		Str("previous_reason", derefOrEmpty(haltReason)).
		Str("note", req.Note).
		Msg("admin.cb_reset: trader halt manually cleared")

	resp := CBResetResponse{
		OK:            true,
		PreviousReason: derefOrEmpty(haltReason),
		ManualResetAt:  now,
		ManualResetBy:  actor,
	}
	if haltUntil != nil {
		resp.PreviousUntil = *haltUntil
	}
	s.writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleCircuitBreakerEvents(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	rows, err := s.db.Query(ctx, `
		SELECT id, ts, event_type, COALESCE(halt_reason, ''), halt_until_before,
		       COALESCE(actor, ''), COALESCE(daily_pnl_snapshot::text, '0'),
		       COALESCE(consec_losses_snapshot, 0), COALESCE(note, '')
		FROM circuit_breaker_events
		ORDER BY ts DESC
		LIMIT 50
	`)
	if err != nil {
		s.log.Error().Err(err).Msg("cb_events: query failed")
		s.writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	out := make([]CBEvent, 0, 32)
	for rows.Next() {
		var e CBEvent
		var haltUntil *time.Time
		if err := rows.Scan(&e.ID, &e.Ts, &e.EventType, &e.HaltReason, &haltUntil,
			&e.Actor, &e.DailyPnlSnapshot, &e.ConsecLossesSnap, &e.Note); err != nil {
			s.log.Error().Err(err).Msg("cb_events: scan failed")
			continue
		}
		e.HaltUntilBefore = haltUntil
		out = append(out, e)
	}
	s.writeJSON(w, http.StatusOK, CBEventsResponse{Events: out})
}

func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
