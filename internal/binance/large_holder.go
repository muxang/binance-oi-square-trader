package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/shopspring/decimal"
)

// LargeHolderRatio is one snapshot from either /futures/data/topLongShortAccountRatio
// or /futures/data/topLongShortPositionRatio. The two endpoints share an identical
// response schema — the difference is *what* is being counted: top traders by
// account count (account ratio) vs by aggregated position size (position ratio).
//
// ref: references/binance/urls.md §「Top Trader Long Short {Account,Position} Ratio」
// docs (account): https://developers.binance.com/docs/derivatives/usds-margined-futures/market-data/rest-api/Top-Long-Short-Account-Ratio
// docs (position): https://developers.binance.com/docs/derivatives/usds-margined-futures/market-data/rest-api/Top-Trader-Long-Short-Ratio
// ref: references/user-snippets/contract-monitor.js (checkLargeHolderRatioWithData)
//
// fetched: 2026-05-20
//
// LongShortRatio = LongAccount / ShortAccount (computed by Binance).
// Values like 1.4342 mean longs outnumber shorts ~3:2; <1 means shorts dominate.
type LargeHolderRatio struct {
	Symbol         string
	LongShortRatio decimal.Decimal
	LongAccount    decimal.Decimal // fraction (0.5891 = 58.91%)
	ShortAccount   decimal.Decimal
	Timestamp      time.Time
}

// rawHolderEntry unmarshals one row from the BAPI response array. Both endpoints
// return STRING numerics (BAPI convention) — never float64.
type rawHolderEntry struct {
	Symbol         string      `json:"symbol"`
	LongShortRatio string      `json:"longShortRatio"`
	LongAccount    string      `json:"longAccount"`
	ShortAccount   string      `json:"shortAccount"`
	Timestamp      json.Number `json:"timestamp"` // doc says STRING; live returns number — json.Number handles both
}

// FetchTopLongShortAccountRatio fetches the latest N snapshots of "top traders
// long-short *account* ratio" (count-weighted) for symbol. period in
// {5m,15m,30m,1h,2h,4h,6h,12h,1d}; limit ≤500 (default 30). weight=0 (separate
// 1000 req/5min/IP bucket, same as openInterestHist).
func (c *Client) FetchTopLongShortAccountRatio(
	ctx context.Context, symbol, period string, limit int,
) ([]LargeHolderRatio, error) {
	return c.fetchHolderRatio(ctx, "/futures/data/topLongShortAccountRatio", symbol, period, limit)
}

// FetchTopLongShortPositionRatio fetches the latest N snapshots of "top traders
// long-short *position* ratio" (notional-weighted). Identical params/response
// to the account variant.
func (c *Client) FetchTopLongShortPositionRatio(
	ctx context.Context, symbol, period string, limit int,
) ([]LargeHolderRatio, error) {
	return c.fetchHolderRatio(ctx, "/futures/data/topLongShortPositionRatio", symbol, period, limit)
}

func (c *Client) fetchHolderRatio(
	ctx context.Context, path, symbol, period string, limit int,
) ([]LargeHolderRatio, error) {
	params := url.Values{
		"symbol": {symbol},
		"period": {period},
	}
	if limit > 0 {
		params.Set("limit", fmt.Sprintf("%d", limit))
	}
	body, err := c.DoRead(ctx, path, params, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var raw []rawHolderEntry
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	out := make([]LargeHolderRatio, 0, len(raw))
	for _, r := range raw {
		ratio, err := decimal.NewFromString(r.LongShortRatio)
		if err != nil {
			return nil, fmt.Errorf("longShortRatio: %w", err)
		}
		longAcct, err := decimal.NewFromString(r.LongAccount)
		if err != nil {
			return nil, fmt.Errorf("longAccount: %w", err)
		}
		shortAcct, err := decimal.NewFromString(r.ShortAccount)
		if err != nil {
			return nil, fmt.Errorf("shortAccount: %w", err)
		}
		ms, err := r.Timestamp.Int64()
		if err != nil {
			return nil, fmt.Errorf("timestamp: %w", err)
		}
		out = append(out, LargeHolderRatio{
			Symbol:         r.Symbol,
			LongShortRatio: ratio,
			LongAccount:    longAcct,
			ShortAccount:   shortAcct,
			Timestamp:      time.UnixMilli(ms).UTC(),
		})
	}
	return out, nil
}
