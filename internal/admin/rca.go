// Phase 5.2 Round 4 Part 2: halt RCA ack workflow.
//
// GET  /api/admin/halt-rca/unacknowledged  (public — read tier)
// POST /api/admin/halt-rca/{id}/ack         (write — CSRF + audit log)
//
// mu's mobile flow:
//   1. Feishu 🔴 critical 通知 with deep link /admin/audit?halt_event={id}
//   2. mu opens admin Web UI on phone
//   3. RCA panel on Dashboard shows pending halt with full context_json
//   4. mu reviews + clicks "ack as resolved" / "ack as investigating"
//   5. POST /halt-rca/{id}/ack writes mu_acknowledged + admin_audit_log row
//
// halt_rca rows persist after ack — only the panel's pending list filters
// them out. The audit-log page surfaces all rca_ack action_type entries
// for review history.

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

type HaltRCAEntry struct {
	ID           int64           `json:"id"`
	HaltType     string          `json:"halt_type"`
	TriggeredAt  time.Time       `json:"triggered_at"`
	ContextJSON  json.RawMessage `json:"context_json"`
	Acknowledged bool            `json:"mu_acknowledged"`
	Action       string          `json:"mu_action,omitempty"`
	AckedAt      *time.Time      `json:"mu_acknowledged_at,omitempty"`
	ResolvedAt   *time.Time      `json:"resolved_at,omitempty"`
}

type HaltRCAUnackResponse struct {
	Total int            `json:"total"`
	Items []HaltRCAEntry `json:"items"`
}

func (s *Server) handleHaltRCAUnacknowledged(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	rows, err := s.db.Query(ctx, `
		SELECT id, halt_type, triggered_at, context_json
		FROM halt_rca
		WHERE NOT mu_acknowledged
		ORDER BY triggered_at DESC
		LIMIT 50
	`)
	if err != nil {
		s.log.Error().Err(err).Msg("rca_unack: query failed")
		s.writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	items := make([]HaltRCAEntry, 0)
	for rows.Next() {
		var e HaltRCAEntry
		var ctxJSON []byte
		if err := rows.Scan(&e.ID, &e.HaltType, &e.TriggeredAt, &ctxJSON); err != nil {
			s.log.Error().Err(err).Msg("rca_unack: scan failed")
			continue
		}
		if len(ctxJSON) > 0 {
			e.ContextJSON = ctxJSON
		}
		items = append(items, e)
	}
	s.writeJSON(w, http.StatusOK, HaltRCAUnackResponse{Total: len(items), Items: items})
}

type HaltRCAAckRequest struct {
	Confirm bool   `json:"confirm"`
	Action  string `json:"action"` // "resolved" | "investigating" | "ignored"
	Note    string `json:"note"`
}

func (s *Server) handleHaltRCAAck(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	rcaID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid rca id")
		return
	}
	var req HaltRCAAckRequest
	if !s.decodeAndConfirm(w, r, &req.Confirm, &req, "confirm=true required") {
		return
	}
	switch req.Action {
	case "resolved", "investigating", "ignored":
		// ok
	default:
		s.writeError(w, http.StatusBadRequest, "action must be one of: resolved | investigating | ignored")
		return
	}

	ctx := r.Context()
	tx, err := s.writeDB.Begin(ctx)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "db tx error")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Read prev state for audit + 404/409 gating.
	var (
		haltType string
		acked    bool
		ctxJSON  []byte
	)
	if err := tx.QueryRow(ctx, `SELECT halt_type, mu_acknowledged, context_json FROM halt_rca WHERE id=$1`, rcaID).
		Scan(&haltType, &acked, &ctxJSON); err != nil {
		s.writeError(w, http.StatusNotFound, "rca not found")
		return
	}
	if acked {
		s.writeError(w, http.StatusConflict, "rca already acknowledged")
		return
	}

	if _, err := tx.Exec(ctx, `
		UPDATE halt_rca
		SET mu_acknowledged = TRUE,
		    mu_action = $2,
		    mu_acknowledged_at = NOW(),
		    resolved_at = CASE WHEN $2 = 'resolved' THEN NOW() ELSE NULL END
		WHERE id = $1
		  AND NOT mu_acknowledged
	`, rcaID, req.Action); err != nil {
		s.writeError(w, http.StatusInternalServerError, "update error")
		return
	}

	prev, _ := json.Marshal(map[string]any{
		"halt_type": haltType,
		"acked":     false,
	})
	newSt, _ := json.Marshal(map[string]any{
		"action": req.Action,
		"acked":  true,
	})
	if err := s.insertAuditLogTx(ctx, tx, r, "rca_ack", "halt_rca", idStr, prev, newSt, req.Note); err != nil {
		s.writeError(w, http.StatusInternalServerError, "audit error")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		s.writeError(w, http.StatusInternalServerError, "commit error")
		return
	}
	s.log.Info().Int64("rca_id", rcaID).Str("action", req.Action).Str("halt_type", haltType).
		Msg("rca.acked")
	s.writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"rca_id": rcaID,
		"action": req.Action,
	})
}

// R.15: batch-ack every unacknowledged halt_rca as "ignored".
// One tx: UPDATE NOT acked → ignored, then one admin_audit_log row per
// affected id (per-row audit keeps single-id history queries working).
type HaltRCAAckAllRequest struct {
	Confirm bool   `json:"confirm"`
	Note    string `json:"note"`
}

func (s *Server) handleHaltRCAAckAll(w http.ResponseWriter, r *http.Request) {
	var req HaltRCAAckAllRequest
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

	rows, err := tx.Query(ctx, `
		UPDATE halt_rca
		SET mu_acknowledged = TRUE,
		    mu_action = 'ignored',
		    mu_acknowledged_at = NOW()
		WHERE NOT mu_acknowledged
		RETURNING id, halt_type
	`)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "update error")
		return
	}
	type affected struct {
		id       int64
		haltType string
	}
	var hits []affected
	for rows.Next() {
		var a affected
		if err := rows.Scan(&a.id, &a.haltType); err != nil {
			rows.Close()
			s.writeError(w, http.StatusInternalServerError, "scan error")
			return
		}
		hits = append(hits, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		s.writeError(w, http.StatusInternalServerError, "rows error")
		return
	}

	ids := make([]int64, 0, len(hits))
	for _, a := range hits {
		prev, _ := json.Marshal(map[string]any{"halt_type": a.haltType, "acked": false})
		newSt, _ := json.Marshal(map[string]any{"action": "ignored", "acked": true, "batch_ack": true})
		if err := s.insertAuditLogTx(ctx, tx, r, "rca_ack", "halt_rca", strconv.FormatInt(a.id, 10), prev, newSt, req.Note); err != nil {
			s.writeError(w, http.StatusInternalServerError, "audit error")
			return
		}
		ids = append(ids, a.id)
	}

	if err := tx.Commit(ctx); err != nil {
		s.writeError(w, http.StatusInternalServerError, "commit error")
		return
	}
	s.log.Info().Int("count", len(ids)).Ints64("ids", ids).Msg("rca.ack_all")
	s.writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"count": len(ids),
		"ids":   ids,
	})
}
