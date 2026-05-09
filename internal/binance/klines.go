package binance

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// KlineBar is a parsed Binance kline / candlestick. Fields beyond Volume
// (close_time, trades, taker_buy_*, etc.) are not exposed — add them only
// when a caller needs them (YAGNI).
//
// ref: references/binance/urls.md §「Kline / Candlestick」
// docs: https://developers.binance.com/docs/derivatives/usds-margined-futures/market-data/rest-api/Kline-Candlestick-Data
type KlineBar struct {
	OpenTime time.Time
	Open     decimal.Decimal
	High     decimal.Decimal
	Low      decimal.Decimal
	Close    decimal.Decimal
	Volume   decimal.Decimal
}

// ParseKlines parses the raw JSON body of /fapi/v1/klines into a slice of
// KlineBar. Binance returns each kline as a heterogeneous array
//
//	[open_time_ms, "open", "high", "low", "close", "volume", ...]
//
// We decode position-by-position; OHLCV strings go through decimal.Decimal
// so no float64 round-trip happens on the money-safety path (CLAUDE.md §19).
func ParseKlines(raw []byte) ([]KlineBar, error) {
	var rows [][]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("klines outer parse: %w", err)
	}
	out := make([]KlineBar, 0, len(rows))
	for i, row := range rows {
		bar, err := parseKlineBar(row)
		if err != nil {
			return nil, fmt.Errorf("klines[%d]: %w", i, err)
		}
		out = append(out, bar)
	}
	return out, nil
}

// parseKlineBar decodes one heterogeneous tuple. Indices 0-5 cover
// open_time / OHLCV; later indices are unused by Phase 1 collectors.
func parseKlineBar(row []json.RawMessage) (KlineBar, error) {
	if len(row) < 6 {
		return KlineBar{}, fmt.Errorf("kline fields=%d, want ≥6", len(row))
	}
	var openMS int64
	if err := json.Unmarshal(row[0], &openMS); err != nil {
		return KlineBar{}, fmt.Errorf("open_time: %w", err)
	}
	parseDec := func(idx int, name string) (decimal.Decimal, error) {
		var s string
		if err := json.Unmarshal(row[idx], &s); err != nil {
			return decimal.Decimal{}, fmt.Errorf("%s: %w", name, err)
		}
		d, err := decimal.NewFromString(s)
		if err != nil {
			return decimal.Decimal{}, fmt.Errorf("%s decimal: %w", name, err)
		}
		return d, nil
	}
	open, err := parseDec(1, "open")
	if err != nil {
		return KlineBar{}, err
	}
	high, err := parseDec(2, "high")
	if err != nil {
		return KlineBar{}, err
	}
	low, err := parseDec(3, "low")
	if err != nil {
		return KlineBar{}, err
	}
	closePx, err := parseDec(4, "close")
	if err != nil {
		return KlineBar{}, err
	}
	volume, err := parseDec(5, "volume")
	if err != nil {
		return KlineBar{}, err
	}
	return KlineBar{
		OpenTime: time.UnixMilli(openMS).UTC(),
		Open:     open,
		High:     high,
		Low:      low,
		Close:    closePx,
		Volume:   volume,
	}, nil
}
