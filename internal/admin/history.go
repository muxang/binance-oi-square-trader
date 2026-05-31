package admin

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

type HistoryItem struct {
	TradeID        int64   `json:"trade_id"`
	Symbol         string  `json:"symbol"`
	Direction      string  `json:"direction"`
	EntryTsMs      int64   `json:"entry_ts_ms"`
	ExitTsMs       int64   `json:"exit_ts_ms"`
	HoldDurationMs int64   `json:"hold_duration_ms"`
	EntryPrice     float64 `json:"entry_price"`
	ExitPrice      float64 `json:"exit_price"`
	Qty            float64 `json:"qty"`
	RealizedPnl    float64 `json:"realized_pnl"`
	ExitReason     string  `json:"exit_reason"`
	Fees           float64 `json:"fees"`
	Status         string  `json:"status"`
}

type HistoryResponse struct {
	Total int           `json:"total"`
	Page  int           `json:"page"`
	Items []HistoryItem `json:"items"`
}

func (s *Server) handlePositionsHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	symbol     := q.Get("symbol")
	exitReason := q.Get("exit_reason")
	sinceStr   := q.Get("since") // unix ms
	untilStr   := q.Get("until") // unix ms
	pnlDir     := q.Get("pnl_dir") // profit | loss

	page, _     := strconv.Atoi(q.Get("page"))
	pageSize, _ := strconv.Atoi(q.Get("page_size"))
	if page < 1 {
		page = 1
	}
	if pageSize != 50 {
		pageSize = 20
	}

	dataSource := q.Get("data_source") // mainnet | testnet | all (default: testnet, R.18 D2)
	if dataSource == "" { dataSource = "testnet" }

	conds := []string{"t.status IN ('closed', 'failed')"}
	args  := []any{}
	n     := 1

	switch dataSource {
	case "mainnet":
		conds = append(conds, "t.data_source = 'mainnet'")
	case "all":
		// no filter
	default:
		conds = append(conds, "t.data_source = 'testnet'")
	}

	if symbol != "" {
		conds = append(conds, fmt.Sprintf("t.symbol = $%d", n))
		args  = append(args, strings.ToUpper(symbol))
		n++
	}
	if exitReason != "" {
		conds = append(conds, fmt.Sprintf("t.exit_reason = $%d", n))
		args  = append(args, exitReason)
		n++
	}
	if sinceStr != "" {
		if ms, err := strconv.ParseInt(sinceStr, 10, 64); err == nil {
			conds = append(conds, fmt.Sprintf("t.exit_ts >= $%d", n))
			args  = append(args, time.UnixMilli(ms).UTC())
			n++
		}
	}
	if untilStr != "" {
		if ms, err := strconv.ParseInt(untilStr, 10, 64); err == nil {
			conds = append(conds, fmt.Sprintf("t.exit_ts <= $%d", n))
			args  = append(args, time.UnixMilli(ms).UTC())
			n++
		}
	}
	switch pnlDir {
	case "profit":
		conds = append(conds, "t.realized_pnl > 0")
	case "loss":
		conds = append(conds, "t.realized_pnl <= 0")
	}

	where  := strings.Join(conds, " AND ")
	args    = append(args, pageSize, (page-1)*pageSize)
	limitN  := n
	offsetN := n + 1

	sql := fmt.Sprintf(`
		SELECT COUNT(*) OVER() AS total,
		       t.id, t.symbol, t.direction,
		       t.entry_ts, t.exit_ts,
		       t.entry_price, t.exit_price,
		       t.notional / NULLIF(t.entry_price, 0) AS qty,
		       t.realized_pnl, t.exit_reason, t.fees, t.status
		FROM trades t
		WHERE %s
		ORDER BY t.exit_ts DESC NULLS LAST
		LIMIT $%d OFFSET $%d
	`, where, limitN, offsetN)

	ctx  := r.Context()
	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		s.log.Error().Err(err).Msg("query history")
		s.writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	var total int
	items := make([]HistoryItem, 0, pageSize)

	for rows.Next() {
		var (
			cnt        int
			id         int64
			sym        string
			dir        string
			entryTs    pgtype.Timestamptz
			exitTs     pgtype.Timestamptz
			entryPrice pgtype.Numeric
			exitPrice  pgtype.Numeric
			qty        pgtype.Numeric
			realPnl    pgtype.Numeric
			reason     pgtype.Text
			fees       pgtype.Numeric
			status     string
		)
		if err := rows.Scan(&cnt, &id, &sym, &dir,
			&entryTs, &exitTs, &entryPrice, &exitPrice,
			&qty, &realPnl, &reason, &fees, &status); err != nil {
			s.log.Error().Err(err).Msg("scan history row")
			continue
		}
		total = cnt

		var entryTsMs, exitTsMs, holdMs int64
		if entryTs.Valid {
			entryTsMs = entryTs.Time.UnixMilli()
		}
		if exitTs.Valid {
			exitTsMs = exitTs.Time.UnixMilli()
			if entryTs.Valid {
				holdMs = exitTs.Time.Sub(entryTs.Time).Milliseconds()
			}
		}

		items = append(items, HistoryItem{
			TradeID:        id,
			Symbol:         sym,
			Direction:      dir,
			EntryTsMs:      entryTsMs,
			ExitTsMs:       exitTsMs,
			HoldDurationMs: holdMs,
			EntryPrice:     numericToFloat64(entryPrice),
			ExitPrice:      numericToFloat64(exitPrice),
			Qty:            numericToFloat64(qty),
			RealizedPnl:    numericToFloat64(realPnl),
			ExitReason:     reason.String,
			Fees:           numericToFloat64(fees),
			Status:         status,
		})
	}

	s.writeJSON(w, http.StatusOK, HistoryResponse{Total: total, Page: page, Items: items})
}
