// Binance USDⓈ-M Futures order execution methods.
//
// ref: references/binance/urls.md §「New Order」POST /fapi/v1/order
// ref: references/binance/urls.md §「Set Margin Type」POST /fapi/v1/marginType
// ref: references/binance/urls.md §「Change Initial Leverage」POST /fapi/v1/leverage
// ref: references/binance/urls.md §「New Algo Order」POST /fapi/v1/algoOrder
// fetched: 2026-05-11
package binance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/shopspring/decimal"

	"trader/internal/pkg/metrics"
)

// OrderResult holds the relevant fill fields from a MARKET order response.
// Populated by PlaceMarketOrder (RESULT mode) and GetOrder.
type OrderResult struct {
	OrderID       int64
	ClientOrderID string
	Symbol        string
	Status        string          // FILLED / PARTIALLY_FILLED / NEW / etc.
	AvgPrice      decimal.Decimal // 0 if not yet filled
	ExecutedQty   decimal.Decimal
	CumQuote      decimal.Decimal
	UpdateTime    time.Time
}

// AlgoOrderResult holds the response fields from a Conditional algo order.
type AlgoOrderResult struct {
	AlgoID       int64
	ClientAlgoID string
	Status       string
}

// AlgoOrderQuery holds the GET /fapi/v1/algoOrder response fields needed by
// the v0.2 Algo polling reconciler. algoStatus enum: WORKING / FINISHED /
// CANCELED / EXPIRED. Only FINISHED is the "Algo triggered + filled" state.
//
// ref: references/binance/urls.md §「Query Algo Order」GET /fapi/v1/algoOrder
// docs: https://developers.binance.com/docs/derivatives/usds-margined-futures/trade/rest-api/Query-Algo-Order
// fetched: 2026-05-12
type AlgoOrderQuery struct {
	AlgoID         int64
	Symbol         string
	AlgoStatus     string          // WORKING / FINISHED / CANCELED / EXPIRED
	ActualOrderID  string          // underlying market order id (FINISHED only)
	ActualPrice    decimal.Decimal // fill price (FINISHED only; 0 otherwise)
	Quantity       decimal.Decimal // original Algo qty
	TriggerPrice   decimal.Decimal
	UpdateTime     time.Time
	TriggerTime    time.Time // when Algo fired (FINISHED only)
}

// orderRESULTResp maps the RESULT-mode order response JSON.
type orderRESULTResp struct {
	OrderID       int64  `json:"orderId"`
	ClientOrderID string `json:"clientOrderId"`
	Symbol        string `json:"symbol"`
	Status        string `json:"status"`
	AvgPrice      string `json:"avgPrice"`
	ExecutedQty   string `json:"executedQty"`
	CumQuote      string `json:"cumQuote"`
	UpdateTime    int64  `json:"updateTime"`
}

// SetMarginType sets the margin type for a symbol (ISOLATED or CROSSED).
// -4046 "No need to change margin type." is idempotent → treated as success.
//
// ref: references/binance/urls.md §「Change Margin Type」
// docs: https://developers.binance.com/docs/derivatives/usds-margined-futures/trade/rest-api/Change-Margin-Type
// fetched: 2026-05-11
func (c *Client) SetMarginType(ctx context.Context, symbol, marginType string) error {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("marginType", marginType)
	_, err := c.doWrite(ctx, http.MethodPost, "/fapi/v1/marginType", params, 1)
	// v0.2 Gap 2: record before any error wrapping; recordAPIError filters
	// out treat-as-success internally (-4046 won't trip the rate counter).
	c.recordAPIError(ctx, "SetMarginType", "/fapi/v1/marginType", err)
	if err == nil {
		return nil
	}
	if apiErr, ok := err.(*APIError); ok && ClassifyError(apiErr.HTTPCode, apiErr.BizCode) == ActionTreatAsSuccess {
		return nil // -4046: already at desired state
	}
	return fmt.Errorf("set margin type %s %s: %w", symbol, marginType, err)
}

// SetLeverage sets the initial leverage for a symbol (1-125). Returns confirmed leverage.
// -4059 "No need to change leverage." is idempotent → treated as success.
//
// ref: references/binance/urls.md §「Change Initial Leverage」
// docs: https://developers.binance.com/docs/derivatives/usds-margined-futures/trade/rest-api/Change-Initial-Leverage
// fetched: 2026-05-11
func (c *Client) SetLeverage(ctx context.Context, symbol string, leverage int) (int, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("leverage", strconv.Itoa(leverage))
	body, err := c.doWrite(ctx, http.MethodPost, "/fapi/v1/leverage", params, 1)
	// v0.2 Gap 2: record (filter handles -4059 idempotent success).
	c.recordAPIError(ctx, "SetLeverage", "/fapi/v1/leverage", err)
	if err != nil {
		if apiErr, ok := err.(*APIError); ok && ClassifyError(apiErr.HTTPCode, apiErr.BizCode) == ActionTreatAsSuccess {
			return leverage, nil // -4059: already at desired state
		}
		return 0, fmt.Errorf("set leverage %s %d: %w", symbol, leverage, err)
	}
	var resp struct {
		Leverage int `json:"leverage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, fmt.Errorf("parse leverage resp: %w", err)
	}
	return resp.Leverage, nil
}

// PlaceMarketOrder places a MARKET order (BUY or SELL) and returns fill details.
// Uses newOrderRespType=RESULT so market fills return avgPrice immediately.
//
// ref: references/binance/urls.md §「New Order」
// docs: https://developers.binance.com/docs/derivatives/usds-margined-futures/trade/rest-api
// fetched: 2026-05-11
func (c *Client) PlaceMarketOrder(ctx context.Context, symbol, side, quantity, clientOrderID string) (OrderResult, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("side", side)
	params.Set("type", "MARKET")
	params.Set("quantity", quantity)
	params.Set("newOrderRespType", "RESULT")
	if clientOrderID != "" {
		params.Set("newClientOrderId", clientOrderID)
	}
	body, err := c.doWriteRetry(ctx, http.MethodPost, "/fapi/v1/order", params, 1)
	if err != nil {
		// -4116 duplicate clientOrderId: order may have already succeeded on a
		// prior attempt (e.g. network timeout that actually delivered). Look up
		// by clientOrderId and return the existing fill state — idempotent path.
		var apiErr *APIError
		if errors.As(err, &apiErr) && ClassifyError(apiErr.HTTPCode, apiErr.BizCode) == ActionTreatAsExisting {
			metrics.OrdersIdempotentHitTotal.WithLabelValues(symbol).Inc()
			existing, lookupErr := c.GetOrderByClientID(ctx, symbol, clientOrderID)
			if lookupErr != nil {
				return OrderResult{}, fmt.Errorf("place market -4116 + lookup %s %s: %w", symbol, clientOrderID, lookupErr)
			}
			return existing, nil
		}
		return OrderResult{}, fmt.Errorf("place market order %s %s: %w", symbol, side, err)
	}
	return parseOrderResult(body)
}

// GetOrderByClientID queries a single order by its clientOrderId.
// Used by Round 2 idempotent recovery path (-4116 + startup recovery).
//
// ref: references/binance/urls.md §「Query Order」GET /fapi/v1/order
// docs: https://developers.binance.com/docs/derivatives/usds-margined-futures/trade/rest-api/Query-Order
func (c *Client) GetOrderByClientID(ctx context.Context, symbol, clientOrderID string) (OrderResult, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("origClientOrderId", clientOrderID)
	// Account data — testnet API key requires testnet base (DoReadAccount).
	body, err := c.DoReadAccount(ctx, "/fapi/v1/order", params, 1)
	if err != nil {
		return OrderResult{}, fmt.Errorf("get order by client id %s %s: %w", symbol, clientOrderID, err)
	}
	return parseOrderResult(body)
}

// GetOrder queries a single order by its exchange order ID.
func (c *Client) GetOrder(ctx context.Context, symbol string, orderID int64) (OrderResult, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("orderId", strconv.FormatInt(orderID, 10))
	// Account data — testnet API key requires testnet base (DoReadAccount).
	body, err := c.DoReadAccount(ctx, "/fapi/v1/order", params, 1)
	if err != nil {
		return OrderResult{}, fmt.Errorf("get order %s %d: %w", symbol, orderID, err)
	}
	return parseOrderResult(body)
}

// CancelOrder cancels an open order by exchange order ID.
// -2011 / -2013 (order not found / already filled) treated as already cancelled.
func (c *Client) CancelOrder(ctx context.Context, symbol string, orderID int64) error {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("orderId", strconv.FormatInt(orderID, 10))
	_, err := c.doWrite(ctx, http.MethodDelete, "/fapi/v1/order", params, 1)
	// v0.2 Gap 2: record (filter handles -2011/-2013 cancel-already-gone).
	c.recordAPIError(ctx, "CancelOrder", "/fapi/v1/order", err)
	if err == nil {
		return nil
	}
	if apiErr, ok := err.(*APIError); ok && ClassifyError(apiErr.HTTPCode, apiErr.BizCode) == ActionTreatAsCanceled {
		return nil
	}
	return fmt.Errorf("cancel order %s %d: %w", symbol, orderID, err)
}

// QueryAlgoOrder fetches one Algo order by algoId (GET /fapi/v1/algoOrder,
// weight=1). Used by v0.2 algo_reconciler to detect FINISHED status (Algo
// triggered + market SELL filled) and auto-close the matching trade.
//
// ref: references/binance/urls.md §「Query Algo Order」GET /fapi/v1/algoOrder
// docs: https://developers.binance.com/docs/derivatives/usds-margined-futures/trade/rest-api/Query-Algo-Order
// fetched: 2026-05-12
func (c *Client) QueryAlgoOrder(ctx context.Context, algoID int64) (AlgoOrderQuery, error) {
	params := url.Values{}
	params.Set("algoId", strconv.FormatInt(algoID, 10))
	// Account data — testnet API key requires testnet base (DoReadAccount).
	body, err := c.DoReadAccount(ctx, "/fapi/v1/algoOrder", params, 1)
	if err != nil {
		return AlgoOrderQuery{}, fmt.Errorf("query algo order %d: %w", algoID, err)
	}
	var resp struct {
		AlgoID        int64  `json:"algoId"`
		Symbol        string `json:"symbol"`
		AlgoStatus    string `json:"algoStatus"`
		ActualOrderID string `json:"actualOrderId"`
		ActualPrice   string `json:"actualPrice"`
		Quantity      string `json:"quantity"`
		TriggerPrice  string `json:"triggerPrice"`
		UpdateTime    int64  `json:"updateTime"`
		TriggerTime   int64  `json:"triggerTime"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return AlgoOrderQuery{}, fmt.Errorf("parse algo order resp: %w", err)
	}
	q := AlgoOrderQuery{
		AlgoID:        resp.AlgoID,
		Symbol:        resp.Symbol,
		AlgoStatus:    resp.AlgoStatus,
		ActualOrderID: resp.ActualOrderID,
		ActualPrice:   parseDecimalOrZero(resp.ActualPrice),
		Quantity:      parseDecimalOrZero(resp.Quantity),
		TriggerPrice:  parseDecimalOrZero(resp.TriggerPrice),
	}
	if resp.UpdateTime > 0 {
		q.UpdateTime = time.UnixMilli(resp.UpdateTime).UTC()
	}
	if resp.TriggerTime > 0 {
		q.TriggerTime = time.UnixMilli(resp.TriggerTime).UTC()
	}
	return q, nil
}

// AlgoOpenOrder is the subset of /fapi/v1/openAlgoOrders rows used by Round R.3
// orphan_algo_cleaner. Includes the fields needed to identify exit-only algos
// (reduceOnly / closePosition / side / type / status) so the cleaner can skip
// non-exit algos and cancel only orphans.
//
// ref: GET /fapi/v1/openAlgoOrders (Algo Service, no symbol → all)
// docs: https://developers.binance.com/docs/derivatives/usds-margined-futures/trade/rest-api/Current-All-Algo-Open-Orders
// fetched: 2026-05-13
type AlgoOpenOrder struct {
	AlgoID         int64
	Symbol         string
	Side           string          // SELL (exit) / BUY (entry, rare for our flow)
	OrderType      string          // STOP_MARKET / TAKE_PROFIT_MARKET / TRAILING_STOP_MARKET
	Status         string          // NEW / WORKING / FINISHED / CANCELED / EXPIRED
	Quantity       decimal.Decimal
	ReduceOnly     bool
	ClosePosition  bool
}

// ListOpenAlgoOrders returns all currently-open Algo orders for the account.
// Used by Round R.3 orphan_algo_cleaner to find SELL reduceOnly algos whose
// position has already been closed (binance qty=0 but algo still NEW/WORKING).
func (c *Client) ListOpenAlgoOrders(ctx context.Context) ([]AlgoOpenOrder, error) {
	// Account-scoped read → direct (no proxy) to match API key IP whitelist.
	body, err := c.DoReadAccount(ctx, "/fapi/v1/openAlgoOrders", url.Values{}, 1)
	if err != nil {
		return nil, fmt.Errorf("list open algo orders: %w", err)
	}
	// Binance returns { "orders": [...] } envelope.
	var resp struct {
		Orders []struct {
			AlgoID        int64  `json:"algoId"`
			Symbol        string `json:"symbol"`
			Side          string `json:"side"`
			OrderType     string `json:"type"`
			Status        string `json:"algoStatus"`
			Quantity      string `json:"quantity"`
			ReduceOnly    bool   `json:"reduceOnly"`
			ClosePosition bool   `json:"closePosition"`
		} `json:"orders"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		// Some Binance variants return bare array; try that as fallback.
		var bare []struct {
			AlgoID        int64  `json:"algoId"`
			Symbol        string `json:"symbol"`
			Side          string `json:"side"`
			OrderType     string `json:"type"`
			Status        string `json:"algoStatus"`
			Quantity      string `json:"quantity"`
			ReduceOnly    bool   `json:"reduceOnly"`
			ClosePosition bool   `json:"closePosition"`
		}
		if err2 := json.Unmarshal(body, &bare); err2 != nil {
			return nil, fmt.Errorf("parse open algo orders (envelope=%v, bare=%v)", err, err2)
		}
		resp.Orders = bare
	}
	out := make([]AlgoOpenOrder, 0, len(resp.Orders))
	for _, o := range resp.Orders {
		out = append(out, AlgoOpenOrder{
			AlgoID:        o.AlgoID,
			Symbol:        o.Symbol,
			Side:          o.Side,
			OrderType:     o.OrderType,
			Status:        o.Status,
			Quantity:      parseDecimalOrZero(o.Quantity),
			ReduceOnly:    o.ReduceOnly,
			ClosePosition: o.ClosePosition,
		})
	}
	return out, nil
}

// CancelAlgoOrder cancels an Algo Service order by algoId.
// -2011 / -2013 (order not found / already canceled or triggered) → nil.
// Used by Round 5 close pipeline before MARKET SELL.
//
// ref: references/binance/urls.md §「Cancel Algo Order」DELETE /fapi/v1/algoOrder
// docs: https://developers.binance.com/docs/derivatives/usds-margined-futures/trade/rest-api/Cancel-Algo-Order
func (c *Client) CancelAlgoOrder(ctx context.Context, symbol string, algoID int64) error {
	params := url.Values{}
	params.Set("algoId", strconv.FormatInt(algoID, 10))
	_, err := c.doWriteRetry(ctx, http.MethodDelete, "/fapi/v1/algoOrder", params, 1)
	if err == nil {
		return nil
	}
	if apiErr, ok := err.(*APIError); ok && ClassifyError(apiErr.HTTPCode, apiErr.BizCode) == ActionTreatAsCanceled {
		return nil
	}
	return fmt.Errorf("cancel algo order %s %d: %w", symbol, algoID, err)
}

// PlaceAlgoConditionalStop places a CONDITIONAL STOP_MARKET via Algo Service.
// Required after 2025-12-09 — STOP_MARKET must use /fapi/v1/algoOrder.
// triggerPrice is the mark-price threshold; quantity is the full position size.
// reduceOnly=true ensures it only closes the existing LONG position.
//
// ref: references/binance/urls.md §「New Algo Order」POST /fapi/v1/algoOrder
// docs: https://developers.binance.com/docs/derivatives/usds-margined-futures/trade/rest-api/New-Algo-Order
// fetched: 2026-05-11
func (c *Client) PlaceAlgoConditionalStop(ctx context.Context, symbol, quantity, triggerPrice string) (AlgoOrderResult, error) {
	params := url.Values{}
	params.Set("algoType", "CONDITIONAL")
	params.Set("symbol", symbol)
	params.Set("side", "SELL")
	params.Set("positionSide", "BOTH") // one-way mode (Binance default)
	params.Set("type", "STOP_MARKET")
	params.Set("quantity", quantity)
	params.Set("triggerPrice", triggerPrice)
	params.Set("workingType", "MARK_PRICE")
	params.Set("reduceOnly", "true")
	body, err := c.doWriteRetry(ctx, http.MethodPost, "/fapi/v1/algoOrder", params, 1)
	if err != nil {
		return AlgoOrderResult{}, fmt.Errorf("place algo stop %s: %w", symbol, err)
	}
	var resp struct {
		AlgoID       int64  `json:"algoId"`
		ClientAlgoID string `json:"clientAlgoId"`
		AlgoStatus   string `json:"algoStatus"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return AlgoOrderResult{}, fmt.Errorf("parse algo order resp: %w", err)
	}
	return AlgoOrderResult{AlgoID: resp.AlgoID, ClientAlgoID: resp.ClientAlgoID, Status: resp.AlgoStatus}, nil
}

// PlaceAlgoTrailingStop places a TRAILING_STOP_MARKET via Algo Service.
// callbackRate is the Binance unit (%, range 0.1–5.0). activationPrice is the
// mark price that arms the trail (Binance starts tracking from there).
// Caller is responsible for rounding activationPrice to symbol tickSize.
//
// ref: references/binance/urls.md §「New Algo Order」POST /fapi/v1/algoOrder
// docs: https://developers.binance.com/docs/derivatives/usds-margined-futures/trade/rest-api/New-Algo-Order
// fetched: 2026-05-13
func (c *Client) PlaceAlgoTrailingStop(ctx context.Context, symbol, quantity, activationPrice string, callbackRate float64) (AlgoOrderResult, error) {
	params := url.Values{}
	params.Set("algoType", "CONDITIONAL")
	params.Set("symbol", symbol)
	params.Set("side", "SELL")
	params.Set("positionSide", "BOTH")
	params.Set("type", "TRAILING_STOP_MARKET")
	params.Set("quantity", quantity)
	params.Set("activationPrice", activationPrice)
	params.Set("callbackRate", strconv.FormatFloat(callbackRate, 'f', -1, 64))
	params.Set("workingType", "MARK_PRICE")
	params.Set("reduceOnly", "true")
	body, err := c.doWriteRetry(ctx, http.MethodPost, "/fapi/v1/algoOrder", params, 1)
	if err != nil {
		return AlgoOrderResult{}, fmt.Errorf("place algo trailing stop %s: %w", symbol, err)
	}
	var resp struct {
		AlgoID       int64  `json:"algoId"`
		ClientAlgoID string `json:"clientAlgoId"`
		AlgoStatus   string `json:"algoStatus"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return AlgoOrderResult{}, fmt.Errorf("parse algo trailing resp: %w", err)
	}
	return AlgoOrderResult{AlgoID: resp.AlgoID, ClientAlgoID: resp.ClientAlgoID, Status: resp.AlgoStatus}, nil
}

// GetOpenInterest returns the current openInterest (contract count) for a symbol.
// Used by v0.2 Round 3 Module C to snapshot initial_oi at entry time.
// weight=1, public endpoint (no API key required).
//
// ref: GET /fapi/v1/openInterest
// docs: https://developers.binance.com/docs/derivatives/usds-margined-futures/market-data/rest-api/Open-Interest
// fetched: 2026-05-13
func (c *Client) GetOpenInterest(ctx context.Context, symbol string) (decimal.Decimal, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	body, err := c.DoRead(ctx, "/fapi/v1/openInterest", params, 1)
	if err != nil {
		return decimal.Zero, fmt.Errorf("get open interest %s: %w", symbol, err)
	}
	var resp struct {
		OpenInterest string `json:"openInterest"`
		Symbol       string `json:"symbol"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return decimal.Zero, fmt.Errorf("parse open interest resp: %w", err)
	}
	return parseDecimalOrZero(resp.OpenInterest), nil
}

// PlaceAlgoTakeProfit places a TAKE_PROFIT_MARKET via Algo Service (Round 2 Module A).
// Fires SELL MARKET when mark price >= stopPrice. quantity is partial (e.g. 20% of
// position for TP1). reduceOnly=true keeps it position-bounded so multiple TPs +
// disaster/trail can coexist without over-selling.
// Caller is responsible for rounding stopPrice to symbol tickSize and quantity to stepSize.
//
// ref: references/binance/urls.md §「New Algo Order」POST /fapi/v1/algoOrder
// fetched: 2026-05-13
func (c *Client) PlaceAlgoTakeProfit(ctx context.Context, symbol, quantity, stopPrice string) (AlgoOrderResult, error) {
	params := url.Values{}
	params.Set("algoType", "CONDITIONAL")
	params.Set("symbol", symbol)
	params.Set("side", "SELL")
	params.Set("positionSide", "BOTH")
	params.Set("type", "TAKE_PROFIT_MARKET")
	params.Set("quantity", quantity)
	params.Set("triggerPrice", stopPrice)
	params.Set("workingType", "MARK_PRICE")
	params.Set("reduceOnly", "true")
	body, err := c.doWriteRetry(ctx, http.MethodPost, "/fapi/v1/algoOrder", params, 1)
	if err != nil {
		return AlgoOrderResult{}, fmt.Errorf("place algo take profit %s: %w", symbol, err)
	}
	var resp struct {
		AlgoID       int64  `json:"algoId"`
		ClientAlgoID string `json:"clientAlgoId"`
		AlgoStatus   string `json:"algoStatus"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return AlgoOrderResult{}, fmt.Errorf("parse algo take profit resp: %w", err)
	}
	return AlgoOrderResult{AlgoID: resp.AlgoID, ClientAlgoID: resp.ClientAlgoID, Status: resp.AlgoStatus}, nil
}

// UserTrade holds one fill from GET /fapi/v1/userTrades.
// Commission is always in commissionAsset (USDT for USDⓈ-M with BNB fee discount off).
//
// ref: GET /fapi/v1/userTrades (Account Trade List), weight=5
// docs: https://developers.binance.com/docs/derivatives/usds-margined-futures/trade/rest-api/Account-Trade-List
// fetched: 2026-05-12
type UserTrade struct {
	OrderID         int64
	Price           decimal.Decimal
	Qty             decimal.Decimal
	RealizedPnl     decimal.Decimal // position P&L for this fill (price-diff based, excl. commission)
	Commission      decimal.Decimal // fee charged for this fill
	CommissionAsset string
	Time            time.Time
}

// GetUserTrades fetches all fills for a given order (weight=5).
// Typically called after a close SELL fills to get the real commission paid.
func (c *Client) GetUserTrades(ctx context.Context, symbol string, orderID int64) ([]UserTrade, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("orderId", strconv.FormatInt(orderID, 10))
	body, err := c.DoReadAccount(ctx, "/fapi/v1/userTrades", params, 5)
	if err != nil {
		return nil, fmt.Errorf("get user trades %s %d: %w", symbol, orderID, err)
	}
	var raw []struct {
		OrderID         int64  `json:"orderId"`
		Price           string `json:"price"`
		Qty             string `json:"qty"`
		RealizedPnl     string `json:"realizedPnl"`
		Commission      string `json:"commission"`
		CommissionAsset string `json:"commissionAsset"`
		Time            int64  `json:"time"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse user trades resp: %w", err)
	}
	trades := make([]UserTrade, 0, len(raw))
	for _, r := range raw {
		trades = append(trades, UserTrade{
			OrderID:         r.OrderID,
			Price:           parseDecimalOrZero(r.Price),
			Qty:             parseDecimalOrZero(r.Qty),
			RealizedPnl:     parseDecimalOrZero(r.RealizedPnl),
			Commission:      parseDecimalOrZero(r.Commission),
			CommissionAsset: r.CommissionAsset,
			Time:            time.UnixMilli(r.Time).UTC(),
		})
	}
	return trades, nil
}

// parseOrderResult is shared by PlaceMarketOrder and GetOrder.
func parseOrderResult(body []byte) (OrderResult, error) {
	var r orderRESULTResp
	if err := json.Unmarshal(body, &r); err != nil {
		return OrderResult{}, fmt.Errorf("parse order resp: %w", err)
	}
	return OrderResult{
		OrderID:       r.OrderID,
		ClientOrderID: r.ClientOrderID,
		Symbol:        r.Symbol,
		Status:        r.Status,
		AvgPrice:      parseDecimalOrZero(r.AvgPrice),
		ExecutedQty:   parseDecimalOrZero(r.ExecutedQty),
		CumQuote:      parseDecimalOrZero(r.CumQuote),
		UpdateTime:    time.UnixMilli(r.UpdateTime),
	}, nil
}
