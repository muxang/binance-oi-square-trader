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
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/shopspring/decimal"
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
	body, err := c.doWrite(ctx, http.MethodPost, "/fapi/v1/order", params, 1)
	if err != nil {
		return OrderResult{}, fmt.Errorf("place market order %s %s: %w", symbol, side, err)
	}
	return parseOrderResult(body)
}

// GetOrder queries a single order by its exchange order ID.
func (c *Client) GetOrder(ctx context.Context, symbol string, orderID int64) (OrderResult, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("orderId", strconv.FormatInt(orderID, 10))
	body, err := c.DoRead(ctx, "/fapi/v1/order", params, 1)
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
	if err == nil {
		return nil
	}
	if apiErr, ok := err.(*APIError); ok && ClassifyError(apiErr.HTTPCode, apiErr.BizCode) == ActionTreatAsCanceled {
		return nil
	}
	return fmt.Errorf("cancel order %s %d: %w", symbol, orderID, err)
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
	body, err := c.doWrite(ctx, http.MethodPost, "/fapi/v1/algoOrder", params, 1)
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
