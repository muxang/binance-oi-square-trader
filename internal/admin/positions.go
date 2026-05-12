package admin

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// OpenPosition is one row in /api/admin/positions/open.
// margin_ratio = max(0, -unrealized_pnl) / margin  (>0.8 triggers MARGIN_CALL).
type OpenPosition struct {
	TradeID          int64   `json:"trade_id"`
	Symbol           string  `json:"symbol"`
	Direction        string  `json:"direction"`
	EntryTsMs        int64   `json:"entry_ts_ms"`
	EntryPrice       float64 `json:"entry_price"`
	CurrentPrice     float64 `json:"current_price"`      // 0 if Redis miss
	CurrentQty       float64 `json:"current_qty"`
	Margin           float64 `json:"margin"`
	HoldDurationMs   int64   `json:"hold_duration_ms"`
	UnrealizedPnl    float64 `json:"unrealized_pnl"`
	UnrealizedPnlPct float64 `json:"unrealized_pnl_pct"` // % of margin
	MarginRatio      float64 `json:"margin_ratio"`        // 0-1; >0.8 = danger
}

// RecentClosedTrade appears in the empty-state footer when open positions = 0.
type RecentClosedTrade struct {
	TradeID     int64   `json:"trade_id"`
	Symbol      string  `json:"symbol"`
	ExitTsMs    int64   `json:"exit_ts_ms"`
	EntryPrice  float64 `json:"entry_price"`
	ExitPrice   float64 `json:"exit_price"`
	RealizedPnl float64 `json:"realized_pnl"`
	ExitReason  string  `json:"exit_reason"`
}

// PositionsOpenResponse wraps positions + recent-closed so the frontend can
// show the empty state without a second round trip.
type PositionsOpenResponse struct {
	Positions []OpenPosition      `json:"positions"`
	Recent    []RecentClosedTrade `json:"recent"` // last 3 closed in 24h
}

func (s *Server) handlePositionsOpen(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	rows, err := s.db.Query(ctx, `
		SELECT
			t.id, t.symbol, t.direction, t.entry_ts, t.entry_price,
			t.margin, t.notional,
			COALESCE(ps.current_qty, t.notional / NULLIF(t.entry_price, 0)) AS current_qty
		FROM trades t
		LEFT JOIN position_states ps ON ps.trade_id = t.id
		WHERE t.status = 'open'
		ORDER BY t.entry_ts ASC
	`)
	if err != nil {
		s.log.Error().Err(err).Msg("query open positions")
		s.writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	now := time.Now()
	var positions []OpenPosition
	for rows.Next() {
		var (
			id         int64
			symbol     string
			direction  string
			entryTs    pgtype.Timestamptz
			entryPrice pgtype.Numeric
			margin     pgtype.Numeric
			notional   pgtype.Numeric
			currentQty pgtype.Numeric
		)
		if err := rows.Scan(&id, &symbol, &direction, &entryTs,
			&entryPrice, &margin, &notional, &currentQty); err != nil {
			s.log.Error().Err(err).Msg("scan open position")
			continue
		}

		ep := numericToFloat64(entryPrice)
		mg := numericToFloat64(margin)
		qty := numericToFloat64(currentQty)

		// Redis latest price — fall back to entry price on miss (conservative).
		cp := ep
		if s.rdb != nil {
			if val, rerr := s.rdb.Get(ctx, "latest_price:"+symbol).Result(); rerr == nil {
				if p, perr := strconv.ParseFloat(val, 64); perr == nil {
					cp = p
				}
			}
		}

		var entryTsMs, holdMs int64
		if entryTs.Valid {
			entryTsMs = entryTs.Time.UnixMilli()
			holdMs = now.Sub(entryTs.Time).Milliseconds()
		}

		unrealizedPnl := (cp - ep) * qty
		var marginRatio, pnlPct float64
		if mg > 0 {
			marginRatio = math.Max(0, -unrealizedPnl) / mg
			pnlPct = unrealizedPnl / mg * 100
		}

		positions = append(positions, OpenPosition{
			TradeID:          id,
			Symbol:           symbol,
			Direction:        direction,
			EntryTsMs:        entryTsMs,
			EntryPrice:       ep,
			CurrentPrice:     cp,
			CurrentQty:       qty,
			Margin:           mg,
			HoldDurationMs:   holdMs,
			UnrealizedPnl:    unrealizedPnl,
			UnrealizedPnlPct: pnlPct,
			MarginRatio:      marginRatio,
		})
	}
	rows.Close()

	var recent []RecentClosedTrade
	if len(positions) == 0 {
		recent = s.recentClosed(ctx)
	}
	if positions == nil {
		positions = []OpenPosition{} // never return null
	}

	s.writeJSON(w, http.StatusOK, PositionsOpenResponse{
		Positions: positions,
		Recent:    recent,
	})
}

func (s *Server) recentClosed(ctx context.Context) []RecentClosedTrade {
	rows, err := s.db.Query(ctx, `
		SELECT id, symbol, exit_ts, entry_price, exit_price, realized_pnl, exit_reason
		FROM trades
		WHERE status IN ('closed', 'failed')
		  AND exit_ts > NOW() - INTERVAL '24 hours'
		ORDER BY exit_ts DESC
		LIMIT 3
	`)
	if err != nil {
		s.log.Warn().Err(err).Msg("query recent closed")
		return nil
	}
	defer rows.Close()

	var result []RecentClosedTrade
	for rows.Next() {
		var (
			id          int64
			symbol      string
			exitTs      pgtype.Timestamptz
			entryPrice  pgtype.Numeric
			exitPrice   pgtype.Numeric
			realizedPnl pgtype.Numeric
			exitReason  pgtype.Text
		)
		if err := rows.Scan(&id, &symbol, &exitTs, &entryPrice,
			&exitPrice, &realizedPnl, &exitReason); err != nil {
			continue
		}
		var exitTsMs int64
		if exitTs.Valid {
			exitTsMs = exitTs.Time.UnixMilli()
		}
		result = append(result, RecentClosedTrade{
			TradeID:     id,
			Symbol:      symbol,
			ExitTsMs:    exitTsMs,
			EntryPrice:  numericToFloat64(entryPrice),
			ExitPrice:   numericToFloat64(exitPrice),
			RealizedPnl: numericToFloat64(realizedPnl),
			ExitReason:  exitReason.String,
		})
	}
	return result
}
