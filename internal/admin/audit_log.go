// Phase 5.2 Round 3: public audit log viewer.
//
// GET /api/admin/audit-log
//
//	query params:
//	  page=N       (default 1, 1-indexed)
//	  page_size=N  (default 20, max 100)
//	  action=...   (optional filter on action_type)
//
// No auth required (mu A1 decision — read tier is public so anyone can
// inspect mu's owner operations). Returns the most recent N rows from
// admin_audit_log, newest first, with pagination.
package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

type AuditLogEntry struct {
	ID            int64           `json:"id"`
	Ts            time.Time       `json:"ts"`
	Operator      string          `json:"operator"`
	ActionType    string          `json:"action_type"`
	ResourceType  string          `json:"resource_type"`
	ResourceID    string          `json:"resource_id"`
	PreviousState json.RawMessage `json:"previous_state,omitempty"`
	NewState      json.RawMessage `json:"new_state,omitempty"`
	Note          string          `json:"note"`
	IPAddress     string          `json:"ip_address"`
	UserAgent     string          `json:"user_agent"`
}

type AuditLogResponse struct {
	Total int             `json:"total"`
	Page  int             `json:"page"`
	Items []AuditLogEntry `json:"items"`
}

func (s *Server) handleAuditLog(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	page := 1
	if v := r.URL.Query().Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			page = n
		}
	}
	pageSize := 20
	if v := r.URL.Query().Get("page_size"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			pageSize = n
		}
	}
	action := r.URL.Query().Get("action")

	whereClause := ""
	args := []any{}
	if action != "" {
		whereClause = "WHERE action_type = $1"
		args = append(args, action)
	}

	var total int
	countSQL := "SELECT COUNT(*) FROM admin_audit_log " + whereClause
	if err := s.db.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		s.log.Error().Err(err).Msg("audit_log: count failed")
		s.writeError(w, http.StatusInternalServerError, "db error")
		return
	}

	offset := (page - 1) * pageSize
	listSQL := `
		SELECT id, ts, operator, action_type,
		       COALESCE(resource_type, ''), COALESCE(resource_id, ''),
		       previous_state, new_state,
		       COALESCE(note, ''), COALESCE(host(ip_address), ''), COALESCE(user_agent, '')
		FROM admin_audit_log ` + whereClause + `
		ORDER BY ts DESC
		LIMIT $` + strconv.Itoa(len(args)+1) + ` OFFSET $` + strconv.Itoa(len(args)+2)
	args = append(args, pageSize, offset)

	rows, err := s.db.Query(ctx, listSQL, args...)
	if err != nil {
		s.log.Error().Err(err).Msg("audit_log: query failed")
		s.writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	items := make([]AuditLogEntry, 0, pageSize)
	for rows.Next() {
		var e AuditLogEntry
		var prev, newSt []byte
		if err := rows.Scan(&e.ID, &e.Ts, &e.Operator, &e.ActionType,
			&e.ResourceType, &e.ResourceID, &prev, &newSt,
			&e.Note, &e.IPAddress, &e.UserAgent); err != nil {
			s.log.Error().Err(err).Msg("audit_log: scan failed")
			continue
		}
		if len(prev) > 0 {
			e.PreviousState = prev
		}
		if len(newSt) > 0 {
			e.NewState = newSt
		}
		items = append(items, e)
	}

	s.writeJSON(w, http.StatusOK, AuditLogResponse{Total: total, Page: page, Items: items})
}
