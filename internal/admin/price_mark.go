// Round R.14 (mu 2026-05-27): price-mark CRUD. mu marks a target price for a
// symbol (from the Market row 🔔 or the 价格标记 page); the price_mark collector
// watches mark price and flips status→triggered + sends Feishu. This file is the
// admin-api surface: list (read pool) + create/ack/delete (write pool, CSRF).
//
// direction is decided by the frontend (target vs the Market row's current
// price) and sent explicitly — admin-api makes no binance call. current_price in
// the list is best-effort from redis latest_price:{symbol} (present for
// open-position symbols; "" otherwise).

package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

type PriceMarkRow struct {
	ID             int64  `json:"id"`
	Symbol         string `json:"symbol"`
	TargetPrice    string `json:"target_price"`
	Direction      string `json:"direction"`
	Note           string `json:"note"`
	Status         string `json:"status"`
	Acknowledged   bool   `json:"acknowledged"`
	CurrentPrice   string `json:"current_price"`   // redis latest_price; "" if unavailable
	TriggeredPrice string `json:"triggered_price"` // "" until triggered
	TriggeredAtMs  int64  `json:"triggered_at_ms"` // 0 until triggered
	CreatedAtMs    int64  `json:"created_at_ms"`
}

type PriceMarkListResponse struct {
	Total            int            `json:"total"`
	UnackedTriggered int            `json:"unacked_triggered"` // drives the UI banner
	Items            []PriceMarkRow `json:"items"`
}

func (s *Server) handlePriceMarksList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := s.db.Query(ctx, `
		SELECT id, symbol, target_price::text, direction, note, status, acknowledged,
		       COALESCE(triggered_price::text, ''),
		       COALESCE(EXTRACT(EPOCH FROM triggered_at)::bigint * 1000, 0),
		       EXTRACT(EPOCH FROM created_at)::bigint * 1000
		FROM price_marks
		ORDER BY (status = 'triggered' AND NOT acknowledged) DESC, created_at DESC`)
	if err != nil {
		s.log.Error().Err(err).Msg("price_marks list query")
		s.writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	items := make([]PriceMarkRow, 0, 32)
	symbolSet := map[string]struct{}{}
	unacked := 0
	for rows.Next() {
		var m PriceMarkRow
		if err := rows.Scan(&m.ID, &m.Symbol, &m.TargetPrice, &m.Direction, &m.Note,
			&m.Status, &m.Acknowledged, &m.TriggeredPrice, &m.TriggeredAtMs, &m.CreatedAtMs); err != nil {
			s.log.Error().Err(err).Msg("scan price_mark row")
			continue
		}
		if m.Status == "triggered" && !m.Acknowledged {
			unacked++
		}
		symbolSet[m.Symbol] = struct{}{}
		items = append(items, m)
	}

	// Best-effort current price from redis (open-position symbols have it free).
	if len(symbolSet) > 0 {
		keys := make([]string, 0, len(symbolSet))
		for sym := range symbolSet {
			keys = append(keys, "latest_price:"+sym)
		}
		if vals, err := s.rdb.MGet(ctx, keys...).Result(); err == nil {
			priceBy := make(map[string]string, len(keys))
			for i, k := range keys {
				if str, ok := vals[i].(string); ok {
					priceBy[strings.TrimPrefix(k, "latest_price:")] = str
				}
			}
			for i := range items {
				items[i].CurrentPrice = priceBy[items[i].Symbol]
			}
		}
	}

	s.writeJSON(w, http.StatusOK, PriceMarkListResponse{Total: len(items), UnackedTriggered: unacked, Items: items})
}

type priceMarkCreateReq struct {
	Symbol       string `json:"symbol"`
	TargetPrice  string `json:"target_price"`
	Direction    string `json:"direction"`
	Note         string `json:"note"`
	CurrentPrice string `json:"current_price"` // optional → stored as created_price
}

// handlePriceMarkCreate inserts an active mark. CSRF-guarded by route.
func (s *Server) handlePriceMarkCreate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2048))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "body too large")
		return
	}
	var req priceMarkCreateReq
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	symbol := strings.ToUpper(strings.TrimSpace(req.Symbol))
	if symbol == "" {
		s.writeError(w, http.StatusBadRequest, "symbol required")
		return
	}
	if req.Direction != "above" && req.Direction != "below" {
		s.writeError(w, http.StatusBadRequest, "direction must be 'above' or 'below'")
		return
	}
	target, err := decimal.NewFromString(strings.TrimSpace(req.TargetPrice))
	if err != nil || !target.IsPositive() {
		s.writeError(w, http.StatusBadRequest, "target_price must be a positive number")
		return
	}
	note := strings.TrimSpace(req.Note)
	if len(note) > 200 {
		note = note[:200]
	}
	var createdPrice any // NULL when frontend omits it
	if cp, err := decimal.NewFromString(strings.TrimSpace(req.CurrentPrice)); err == nil && cp.IsPositive() {
		createdPrice = cp.String()
	}

	ctx := r.Context()
	var id int64
	if err := s.writeDB.QueryRow(ctx, `
		INSERT INTO price_marks (symbol, target_price, direction, note, created_price)
		VALUES ($1, $2::numeric, $3, $4, $5::numeric)
		RETURNING id`,
		symbol, target.String(), req.Direction, note, createdPrice).Scan(&id); err != nil {
		s.log.Error().Err(err).Str("symbol", symbol).Msg("price_mark insert")
		s.writeError(w, http.StatusInternalServerError, "insert failed")
		return
	}

	auditNew, _ := json.Marshal(map[string]string{
		"symbol": symbol, "target_price": target.String(), "direction": req.Direction,
	})
	if _, err := s.writeDB.Exec(ctx, `
		INSERT INTO admin_audit_log (operator, action_type, resource_type, resource_id, new_state, note)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		"mu", "price_mark_create", "price_marks", strconv.FormatInt(id, 10), auditNew, note); err != nil {
		s.log.Warn().Err(err).Int64("id", id).Msg("price_mark audit log failed (non-fatal)")
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "id": id})
}

// handlePriceMarkAck clears the banner for one triggered mark. CSRF-guarded.
func (s *Server) handlePriceMarkAck(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	if _, err := s.writeDB.Exec(r.Context(),
		`UPDATE price_marks SET acknowledged = TRUE, updated_at = NOW() WHERE id = $1`, id); err != nil {
		s.log.Error().Err(err).Int64("id", id).Msg("price_mark ack")
		s.writeError(w, http.StatusInternalServerError, "ack failed")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "id": id})
}

// handlePriceMarkDelete removes a mark entirely. CSRF-guarded.
func (s *Server) handlePriceMarkDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	if _, err := s.writeDB.Exec(r.Context(), `DELETE FROM price_marks WHERE id = $1`, id); err != nil {
		s.log.Error().Err(err).Int64("id", id).Msg("price_mark delete")
		s.writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "id": id})
}

func (s *Server) pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.writeError(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}
