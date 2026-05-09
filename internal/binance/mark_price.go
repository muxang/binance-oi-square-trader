package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/shopspring/decimal"
)

// MarkPriceData is one symbol's mark price snapshot. Other fields the BAPI
// returns (indexPrice, lastFundingRate, nextFundingTime, time, etc.) are
// not exposed — T5 stop-loss decisions only need markPrice (YAGNI). Add
// when a caller actually needs them.
//
// ref: references/binance/urls.md §「Mark Price」
// docs: https://developers.binance.com/docs/derivatives/usds-margined-futures/market-data/rest-api/Mark-Price
type MarkPriceData struct {
	Symbol    string
	MarkPrice decimal.Decimal
}

// FetchMarkPrice fetches a single symbol's mark price (GET premiumIndex
// with the `symbol` query param, weight=1). Use markPrice not lastPrice
// for stop-loss / take-profit decisions — markPrice is wick / manipulation
// resistant. Response shape is a single object (vs the array returned when
// no symbol is passed).
func (c *Client) FetchMarkPrice(ctx context.Context, symbol string) (MarkPriceData, error) {
	body, err := c.DoRead(ctx, "/fapi/v1/premiumIndex", url.Values{"symbol": {symbol}}, 1)
	if err != nil {
		return MarkPriceData{}, fmt.Errorf("premiumIndex: %w", err)
	}
	var raw struct {
		Symbol    string `json:"symbol"`
		MarkPrice string `json:"markPrice"` // BAPI returns string; never float64
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return MarkPriceData{}, fmt.Errorf("parse: %w", err)
	}
	mp, err := decimal.NewFromString(raw.MarkPrice)
	if err != nil {
		return MarkPriceData{}, fmt.Errorf("markPrice: %w", err)
	}
	return MarkPriceData{Symbol: raw.Symbol, MarkPrice: mp}, nil
}
