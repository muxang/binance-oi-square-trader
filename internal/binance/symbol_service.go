package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"

	"trader/internal/pkg/timez"
)

// exchangeFilter mirrors one entry of `symbols[].filters[]`. Binance returns
// per-symbol trading constraints as a heterogeneous array — each filter has
// `filterType` plus type-specific string fields. We capture the union; only
// fields matching filterType are populated, others stay empty.
type exchangeFilter struct {
	FilterType  string `json:"filterType"`
	StepSize    string `json:"stepSize,omitempty"`    // LOT_SIZE
	MinQty      string `json:"minQty,omitempty"`      // LOT_SIZE
	TickSize    string `json:"tickSize,omitempty"`    // PRICE_FILTER
	Notional    string `json:"notional,omitempty"`    // MIN_NOTIONAL (futures)
	MinNotional string `json:"minNotional,omitempty"` // MIN_NOTIONAL (spot fallback; futures uses Notional)
}

// exchangeInfoResp captures only the fields used by symbol filtering.
// Defined here (binance pkg) so SymbolService is the canonical source —
// at 1.10 cleanup OICollector / KlinesCollector drop their private copies.
type exchangeInfoResp struct {
	Symbols []struct {
		Symbol       string           `json:"symbol"`
		ContractType string           `json:"contractType"`
		Status       string           `json:"status"`
		QuoteAsset   string           `json:"quoteAsset"`
		MarginAsset  string           `json:"marginAsset"`
		OnboardDate  int64            `json:"onboardDate"` // ms unix; T4 uses for listing-age filter
		Filters      []exchangeFilter `json:"filters"`     // Phase 3 sizing reads LOT_SIZE / MIN_NOTIONAL / PRICE_FILTER
	} `json:"symbols"`
}

// TradingFilters captures the per-symbol constraints Phase 3 sizing needs.
// Sourced from exchangeInfo.symbols[].filters[]; cached alongside symbol set.
//
//	StepSize    — LOT_SIZE.stepSize, quantity must be a multiple
//	MinQty      — LOT_SIZE.minQty, quantity floor
//	MinNotional — MIN_NOTIONAL.notional (futures field name), notional = price × qty floor
//	TickSize    — PRICE_FILTER.tickSize, price granularity (Phase 4 limit orders)
type TradingFilters struct {
	StepSize    decimal.Decimal
	MinQty      decimal.Decimal
	MinNotional decimal.Decimal
	TickSize    decimal.Decimal
}

// SymbolService caches active USDⓈ-M perpetual symbols, refreshed lazily.
// T2/T3 use IsValidPerpetual to validate cashtags; T1/T7 switch to
// ListSymbols in 1.10 cleanup (currently both keep their inline copies).
// Phase 3 adds GetTradingFilters for sizing — reuses the same cache.
type SymbolService struct {
	client  *Client
	log     zerolog.Logger
	ttl     time.Duration
	nowFunc func() time.Time

	mu           sync.Mutex
	set          map[string]struct{}
	list         []string
	onboardDates map[string]int64
	filters      map[string]TradingFilters
	cachedAt     time.Time
}

func NewSymbolService(client *Client, log zerolog.Logger) *SymbolService {
	return &SymbolService{
		client:  client,
		log:     log,
		ttl:     time.Hour,
		nowFunc: timez.NowUTC,
	}
}

// IsValidPerpetual reports whether `symbol` is currently a USDT-quoted /
// USDT-margined / TRADING perpetual.
func (s *SymbolService) IsValidPerpetual(ctx context.Context, symbol string) (bool, error) {
	if err := s.refreshIfStale(ctx); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.set[symbol]
	return ok, nil
}

// ListSymbols returns a copy of the cached symbol slice.
func (s *SymbolService) ListSymbols(ctx context.Context) ([]string, error) {
	if err := s.refreshIfStale(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.list))
	copy(out, s.list)
	return out, nil
}

// GetOnboardDates returns a snapshot of symbol → onboardDate (ms unix) for
// the cached active perpetuals. T4 uses this for the 7-day listing filter.
// Reuses the same exchangeInfo cache as IsValidPerpetual / ListSymbols —
// no extra API call.
func (s *SymbolService) GetOnboardDates(ctx context.Context) (map[string]int64, error) {
	if err := s.refreshIfStale(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int64, len(s.onboardDates))
	for k, v := range s.onboardDates {
		out[k] = v
	}
	return out, nil
}

// GetTradingFilters returns the LOT_SIZE / MIN_NOTIONAL / PRICE_FILTER values
// for a symbol. Phase 3 sizing uses StepSize + MinQty to round quantity, plus
// MinNotional to validate the floor. Returns zero TradingFilters + error if
// symbol is not in cache (i.e. not a TRADING USDT perpetual).
// Reuses the same exchangeInfo cache — no extra API call.
func (s *SymbolService) GetTradingFilters(ctx context.Context, symbol string) (TradingFilters, error) {
	if err := s.refreshIfStale(ctx); err != nil {
		return TradingFilters{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tf, ok := s.filters[symbol]
	if !ok {
		return TradingFilters{}, fmt.Errorf("symbol %q not found (not a TRADING USDT perpetual)", symbol)
	}
	return tf, nil
}

// refreshIfStale double-checks cache age and re-fetches outside the lock so
// readers don't block on the network call.
func (s *SymbolService) refreshIfStale(ctx context.Context) error {
	s.mu.Lock()
	fresh := s.set != nil && s.nowFunc().Sub(s.cachedAt) < s.ttl
	s.mu.Unlock()
	if fresh {
		return nil
	}
	body, err := s.client.DoRead(ctx, "/fapi/v1/exchangeInfo", nil, 1)
	if err != nil {
		return fmt.Errorf("exchangeInfo: %w", err)
	}
	var resp exchangeInfoResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("exchangeInfo parse: %w", err)
	}
	set := make(map[string]struct{}, len(resp.Symbols))
	list := make([]string, 0, len(resp.Symbols))
	dates := make(map[string]int64, len(resp.Symbols))
	filters := make(map[string]TradingFilters, len(resp.Symbols))
	for _, sym := range resp.Symbols {
		if sym.ContractType == "PERPETUAL" && sym.QuoteAsset == "USDT" && sym.MarginAsset == "USDT" && sym.Status == "TRADING" {
			set[sym.Symbol] = struct{}{}
			list = append(list, sym.Symbol)
			dates[sym.Symbol] = sym.OnboardDate
			filters[sym.Symbol] = parseTradingFilters(sym.Filters)
		}
	}
	s.mu.Lock()
	s.set = set
	s.list = list
	s.onboardDates = dates
	s.filters = filters
	s.cachedAt = s.nowFunc()
	s.mu.Unlock()
	return nil
}

// parseTradingFilters extracts LOT_SIZE / MIN_NOTIONAL / PRICE_FILTER values
// from the heterogeneous filters array. Missing fields stay zero — caller
// must validate non-zero before relying (Phase 3 sizing checks).
func parseTradingFilters(filters []exchangeFilter) TradingFilters {
	var tf TradingFilters
	for _, f := range filters {
		switch f.FilterType {
		case "LOT_SIZE":
			tf.StepSize = parseDecimalOrZero(f.StepSize)
			tf.MinQty = parseDecimalOrZero(f.MinQty)
		case "MIN_NOTIONAL":
			// futures uses `notional`, spot uses `minNotional` — prefer notional, fall back.
			if f.Notional != "" {
				tf.MinNotional = parseDecimalOrZero(f.Notional)
			} else {
				tf.MinNotional = parseDecimalOrZero(f.MinNotional)
			}
		case "PRICE_FILTER":
			tf.TickSize = parseDecimalOrZero(f.TickSize)
		}
	}
	return tf
}

func parseDecimalOrZero(s string) decimal.Decimal {
	if s == "" {
		return decimal.Zero
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero
	}
	return d
}
