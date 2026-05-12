package admin

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type TradeDetailResponse struct {
	TradeID    int64    `json:"trade_id"`
	Symbol     string   `json:"symbol"`
	Direction  string   `json:"direction"`
	Status     string   `json:"status"`
	DataSource string   `json:"data_source"`
	Margin     float64  `json:"margin"`
	Notional   float64  `json:"notional"`
	Leverage   int      `json:"leverage"`
	EntryTsMs  *int64   `json:"entry_ts_ms"`
	EntryPrice *float64 `json:"entry_price"`
	InitialATR *float64 `json:"initial_atr"`
	InitialSL  *float64 `json:"initial_stop_loss"`
	InitialTP1 *float64 `json:"initial_take_profit_1"`
	InitialTP2 *float64 `json:"initial_take_profit_2"`
	ExitTsMs   *int64   `json:"exit_ts_ms"`
	ExitPrice  *float64 `json:"exit_price"`
	ExitReason *string  `json:"exit_reason"`
	RealPnl    *float64 `json:"realized_pnl"`
	Fees       *float64 `json:"fees"`
	Signal    *TradeSignal    `json:"signal"`
	Position  *TradePosition  `json:"position"`
	Exits     []TradeExit     `json:"exits"`
	ApiErrors []TradeApiError `json:"api_errors"`
}

type TradeSignal struct {
	SignalID        int64   `json:"signal_id"`
	TsMs            int64   `json:"ts_ms"`
	OiTriggered     bool    `json:"oi_triggered"`
	OiData          any     `json:"oi_data"`
	SquareHot       bool    `json:"square_hot"`
	SquareData      any     `json:"square_data"`
	Decision        string  `json:"decision"`
	RejectionReason *string `json:"rejection_reason"`
}

type TradePosition struct {
	CurrentQty     float64  `json:"current_qty"`
	HighestPrice   *float64 `json:"highest_price"`
	TrailingActive bool     `json:"trailing_stop_active"`
	TrailingPrice  *float64 `json:"trailing_stop_price"`
	TpStage1Done   bool     `json:"tp_stage1_done"`
	TpStage2Done   bool     `json:"tp_stage2_done"`
	EntryOi        *float64 `json:"entry_oi"`
	LastCheckTsMs  *int64   `json:"last_check_ts_ms"`
}

type TradeExit struct {
	TsMs  int64   `json:"ts_ms"`
	Type  string  `json:"type"`
	Qty   float64 `json:"qty"`
	Price float64 `json:"price"`
	Pnl   float64 `json:"pnl"`
}

type TradeApiError struct {
	TsMs      int64  `json:"ts_ms"`
	Source    string `json:"source"`
	Endpoint  string `json:"endpoint"`
	HttpCode  int    `json:"http_code"`
	ErrorCode int    `json:"error_code"`
	Message   string `json:"message"`
}

func nptr(n pgtype.Numeric) *float64 {
	if !n.Valid || n.Int == nil { return nil }
	v := numericToFloat64(n)
	return &v
}

func tsptr(t pgtype.Timestamptz) *int64 {
	if !t.Valid { return nil }
	v := t.Time.UnixMilli()
	return &v
}

func (s *Server) handleTradeDetail(w http.ResponseWriter, r *http.Request) {
	tradeID, err := strconv.ParseInt(r.PathValue("trade_id"), 10, 64)
	if err != nil || tradeID <= 0 {
		s.writeError(w, http.StatusBadRequest, "invalid trade_id")
		return
	}
	ctx := r.Context()

	var (
		resp                             TradeDetailResponse
		margin, notl                     pgtype.Numeric
		entryTs, exitTs                  pgtype.Timestamptz
		entryPr, iATR, iSL, iTP1, iTP2  pgtype.Numeric
		exitPr, realPnl, fees            pgtype.Numeric
		exitRsn                          pgtype.Text
		sigID                            pgtype.Int8
		sigTs                            pgtype.Timestamptz
		sigOiTr, sigSqHt                 pgtype.Bool
		sigOiD, sigSqD                   []byte
		sigDec, sigRej                   pgtype.Text
	)
	err = s.db.QueryRow(ctx, `
		SELECT t.id, t.symbol, t.direction, t.status,
		       COALESCE(t.data_source,'mainnet'),
		       t.margin, t.notional, t.leverage,
		       t.entry_ts, t.entry_price,
		       t.initial_atr, t.initial_stop_loss,
		       t.initial_take_profit_1, t.initial_take_profit_2,
		       t.exit_ts, t.exit_price, t.exit_reason,
		       t.realized_pnl, t.fees,
		       s.id, s.ts, s.oi_triggered, s.oi_data,
		       s.square_hot, s.square_data, s.decision, s.rejection_reason
		FROM trades t
		LEFT JOIN signals s ON s.id = t.signal_id
		WHERE t.id = $1
	`, tradeID).Scan(
		&resp.TradeID, &resp.Symbol, &resp.Direction, &resp.Status, &resp.DataSource,
		&margin, &notl, &resp.Leverage,
		&entryTs, &entryPr, &iATR, &iSL, &iTP1, &iTP2,
		&exitTs, &exitPr, &exitRsn, &realPnl, &fees,
		&sigID, &sigTs, &sigOiTr, &sigOiD, &sigSqHt, &sigSqD, &sigDec, &sigRej,
	)
	if err == pgx.ErrNoRows {
		s.writeError(w, http.StatusNotFound, "trade not found")
		return
	}
	if err != nil {
		s.log.Error().Err(err).Int64("trade_id", tradeID).Msg("tradedetail")
		s.writeError(w, http.StatusInternalServerError, "db error")
		return
	}

	resp.Margin = numericToFloat64(margin)
	resp.Notional = numericToFloat64(notl)
	resp.EntryTsMs = tsptr(entryTs)
	resp.ExitTsMs  = tsptr(exitTs)
	resp.EntryPrice = nptr(entryPr)
	resp.InitialATR = nptr(iATR)
	resp.InitialSL  = nptr(iSL)
	resp.InitialTP1 = nptr(iTP1)
	resp.InitialTP2 = nptr(iTP2)
	resp.ExitPrice  = nptr(exitPr)
	resp.RealPnl    = nptr(realPnl)
	resp.Fees       = nptr(fees)
	if exitRsn.Valid { resp.ExitReason = &exitRsn.String }

	if sigID.Valid {
		sig := &TradeSignal{
			SignalID:    sigID.Int64,
			TsMs:        sigTs.Time.UnixMilli(),
			OiTriggered: sigOiTr.Bool,
			SquareHot:   sigSqHt.Bool,
			Decision:    sigDec.String,
		}
		if sigRej.Valid { sig.RejectionReason = &sigRej.String }
		if len(sigOiD) > 0 { _ = json.Unmarshal(sigOiD, &sig.OiData) }
		if len(sigSqD) > 0 { _ = json.Unmarshal(sigSqD, &sig.SquareData) }
		resp.Signal = sig
	}

	// Position state (best-effort; not an error if absent)
	var curQty, hiPr, trlPr, entOi pgtype.Numeric
	var lcTs pgtype.Timestamptz
	var hasTrl, tp1, tp2 bool
	if posErr := s.db.QueryRow(ctx, `
		SELECT current_qty, highest_price, trailing_stop_active,
		       trailing_stop_price, tp_stage1_done, tp_stage2_done,
		       entry_oi, last_check_ts
		FROM position_states WHERE trade_id = $1
	`, tradeID).Scan(&curQty, &hiPr, &hasTrl, &trlPr, &tp1, &tp2, &entOi, &lcTs); posErr == nil {
		resp.Position = &TradePosition{
			CurrentQty:     numericToFloat64(curQty),
			HighestPrice:   nptr(hiPr),
			TrailingActive: hasTrl,
			TrailingPrice:  nptr(trlPr),
			TpStage1Done:   tp1,
			TpStage2Done:   tp2,
			EntryOi:        nptr(entOi),
			LastCheckTsMs:  tsptr(lcTs),
		}
	}

	// Trade exits
	resp.Exits = make([]TradeExit, 0)
	if eRows, _ := s.db.Query(ctx, `
		SELECT ts, type, qty, price, pnl FROM trade_exits
		WHERE trade_id = $1 ORDER BY ts ASC
	`, tradeID); eRows != nil {
		defer eRows.Close()
		for eRows.Next() {
			var ts pgtype.Timestamptz
			var typ string
			var qty, prc, pnl pgtype.Numeric
			if err := eRows.Scan(&ts, &typ, &qty, &prc, &pnl); err != nil { continue }
			resp.Exits = append(resp.Exits, TradeExit{
				TsMs: ts.Time.UnixMilli(), Type: typ,
				Qty: numericToFloat64(qty), Price: numericToFloat64(prc), Pnl: numericToFloat64(pnl),
			})
		}
	}

	// API errors near signal time (Section C: executor failure RCA)
	resp.ApiErrors = make([]TradeApiError, 0)
	if sigID.Valid {
		if aeRows, _ := s.db.Query(ctx, `
			SELECT ts, source,
			       COALESCE(endpoint,''), COALESCE(http_code,0),
			       COALESCE(error_code,0), COALESCE(message,'')
			FROM api_errors
			WHERE ts >= $1 - INTERVAL '1 minute'
			  AND ts <= $1 + INTERVAL '10 minutes'
			ORDER BY ts ASC LIMIT 20
		`, sigTs.Time); aeRows != nil {
			defer aeRows.Close()
			for aeRows.Next() {
				var ts pgtype.Timestamptz
				var src, ep, msg string
				var hc, ec int
				if err := aeRows.Scan(&ts, &src, &ep, &hc, &ec, &msg); err != nil { continue }
				resp.ApiErrors = append(resp.ApiErrors, TradeApiError{
					TsMs: ts.Time.UnixMilli(), Source: src,
					Endpoint: ep, HttpCode: hc, ErrorCode: ec, Message: msg,
				})
			}
		}
	}

	s.writeJSON(w, http.StatusOK, resp)
}
