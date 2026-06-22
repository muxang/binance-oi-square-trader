package binance

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/shopspring/decimal"
)

// Ticker24hData is one element from /fapi/v1/ticker/24hr — only the fields
// T4 watchlist needs (symbol + quoteVolume). Other fields (priceChange,
// lastPrice, volume, etc.) are not exposed (YAGNI — add when a caller
// actually needs them).
//
// ref: references/binance/urls.md §「24hr Ticker」
// docs: https://developers.binance.com/docs/derivatives/usds-margined-futures/market-data/rest-api/24hr-Ticker-Price-Change-Statistics
type Ticker24hData struct {
	Symbol      string
	QuoteVolume decimal.Decimal
}

// FetchAll24hTicker fetches 24h ticker stats for ALL symbols in one call
// (no `symbol` query param). Weight is 40 — far cheaper than N × weight=1
// per-symbol calls when the caller needs broad coverage (e.g. T4 watchlist
// quoteVolume filter across ~700 symbols).
func (c *Client) FetchAll24hTicker(ctx context.Context) ([]Ticker24hData, error) {
	body, err := c.DoRead(ctx, "/fapi/v1/ticker/24hr", nil, 40)
	if err != nil {
		return nil, fmt.Errorf("ticker/24hr: %w", err)
	}
	var raw []struct {
		Symbol      string `json:"symbol"`
		QuoteVolume string `json:"quoteVolume"` // BAPI returns string; never float64
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	out := make([]Ticker24hData, 0, len(raw))
	for _, r := range raw {
		// Tolerate empty quoteVolume (Binance returns "" for symbols that have
		// not traded in the 24h window — common right after a new listing).
		// Treat as zero; downstream callers (uptrend collector top-200 sort)
		// naturally filter zero-volume symbols out via the volume ranking.
		// Pre-R.34: empty string failed the whole response → uptrend collector
		// stalled (mu observed: every uptrend scan errored, frontend empty).
		if r.QuoteVolume == "" {
			out = append(out, Ticker24hData{Symbol: r.Symbol, QuoteVolume: decimal.Zero})
			continue
		}
		qv, err := decimal.NewFromString(r.QuoteVolume)
		if err != nil {
			// Genuine parse error (non-empty but non-numeric). Skip this row
			// rather than fail the whole batch — symbol just gets zero volume.
			continue
		}
		out = append(out, Ticker24hData{Symbol: r.Symbol, QuoteVolume: qv})
	}
	return out, nil
}
