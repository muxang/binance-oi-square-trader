// Phase 4 Round 3: position sync via Position Information V3.
//
// ref: references/binance/urls.md §「Position Information V3」GET /fapi/v3/positionRisk
// docs: https://developers.binance.com/docs/derivatives/usds-margined-futures/trade/rest-api/Position-Information-V3
// fetched: 2026-05-11

package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/shopspring/decimal"
)

// PositionRisk is the relevant subset of /fapi/v3/positionRisk row.
// Only positions with positionAmt != 0 are populated (Binance filters empties).
type PositionRisk struct {
	Symbol           string
	PositionAmt      decimal.Decimal // signed: +LONG / -SHORT (one-way mode)
	EntryPrice       decimal.Decimal
	MarkPrice        decimal.Decimal
	UnrealizedProfit decimal.Decimal
	Notional         decimal.Decimal // = abs(PositionAmt × MarkPrice)
	IsolatedMargin   decimal.Decimal // ISOLATED only; 0 in CROSSED
	IsolatedWallet   decimal.Decimal
	PositionSide     string // BOTH (one-way) / LONG / SHORT (hedge)
}

type positionRiskResp struct {
	Symbol           string `json:"symbol"`
	PositionAmt      string `json:"positionAmt"`
	EntryPrice       string `json:"entryPrice"`
	MarkPrice        string `json:"markPrice"`
	UnRealizedProfit string `json:"unRealizedProfit"`
	Notional         string `json:"notional"`
	IsolatedMargin   string `json:"isolatedMargin"`
	IsolatedWallet   string `json:"isolatedWallet"`
	PositionSide     string `json:"positionSide"`
}

// GetPositionRisk queries account-wide active positions via V3 endpoint.
// Empty symbol returns all positions. Specifying symbol scopes to one.
// V3 (vs V2) is recommended per Round 0 Catch 7 and supports the cleaner
// `notional` + `isolatedMargin` fields used for MARGIN_CALL computation.
func (c *Client) GetPositionRisk(ctx context.Context, symbol string) ([]PositionRisk, error) {
	params := url.Values{}
	if symbol != "" {
		params.Set("symbol", symbol)
	}
	// Account data — DoReadAccount routes to write base (testnet in testnet
	// mode) to match the API key's scope; mainnet read base would 401/-2015.
	body, err := c.DoReadAccount(ctx, "/fapi/v3/positionRisk", params, 5)
	if err != nil {
		return nil, fmt.Errorf("get position risk: %w", err)
	}
	var raw []positionRiskResp
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse position risk: %w", err)
	}
	out := make([]PositionRisk, 0, len(raw))
	for _, r := range raw {
		amt := parseDecimalOrZero(r.PositionAmt)
		if amt.IsZero() {
			continue // V3 already filters but defensive
		}
		out = append(out, PositionRisk{
			Symbol:           r.Symbol,
			PositionAmt:      amt,
			EntryPrice:       parseDecimalOrZero(r.EntryPrice),
			MarkPrice:        parseDecimalOrZero(r.MarkPrice),
			UnrealizedProfit: parseDecimalOrZero(r.UnRealizedProfit),
			Notional:         parseDecimalOrZero(r.Notional),
			IsolatedMargin:   parseDecimalOrZero(r.IsolatedMargin),
			IsolatedWallet:   parseDecimalOrZero(r.IsolatedWallet),
			PositionSide:     r.PositionSide,
		})
	}
	return out, nil
}
