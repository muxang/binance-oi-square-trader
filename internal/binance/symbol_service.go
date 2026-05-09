package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"trader/internal/pkg/timez"
)

// exchangeInfoResp captures only the fields used by symbol filtering.
// Defined here (binance pkg) so SymbolService is the canonical source —
// at 1.10 cleanup OICollector / KlinesCollector drop their private copies.
type exchangeInfoResp struct {
	Symbols []struct {
		Symbol       string `json:"symbol"`
		ContractType string `json:"contractType"`
		Status       string `json:"status"`
		QuoteAsset   string `json:"quoteAsset"`
		MarginAsset  string `json:"marginAsset"`
		OnboardDate  int64  `json:"onboardDate"` // ms unix; T4 uses for listing-age filter
	} `json:"symbols"`
}

// SymbolService caches active USDⓈ-M perpetual symbols, refreshed lazily.
// T2/T3 use IsValidPerpetual to validate cashtags; T1/T7 switch to
// ListSymbols in 1.10 cleanup (currently both keep their inline copies).
type SymbolService struct {
	client  *Client
	log     zerolog.Logger
	ttl     time.Duration
	nowFunc func() time.Time

	mu           sync.Mutex
	set          map[string]struct{}
	list         []string
	onboardDates map[string]int64
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
	for _, sym := range resp.Symbols {
		if sym.ContractType == "PERPETUAL" && sym.QuoteAsset == "USDT" && sym.MarginAsset == "USDT" && sym.Status == "TRADING" {
			set[sym.Symbol] = struct{}{}
			list = append(list, sym.Symbol)
			dates[sym.Symbol] = sym.OnboardDate
		}
	}
	s.mu.Lock()
	s.set = set
	s.list = list
	s.onboardDates = dates
	s.cachedAt = s.nowFunc()
	s.mu.Unlock()
	return nil
}
