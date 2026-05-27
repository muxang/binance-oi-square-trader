// Round R.14 (mu 2026-05-27): price-mark watcher. Polls active price_marks,
// fetches each symbol's mark price (抗插针, same basis as exit logic), and on a
// hit flips the mark to status=triggered (one-shot) + sends a 🟡 warning Feishu.
// The admin UI shows a banner until mu acknowledges.
//
// Reuses MarkPriceFetcher (defined in position_price.go, same package).
// Raw SQL on the pool — same lightweight pattern as daily_report; no sqlc regen.
//
// ref: SPEC § (none — operator convenience feature requested 2026-05-27)
// ref: internal/binance/mark_price.go (FetchMarkPrice, weight=1)

package collector

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"
	"golang.org/x/sync/errgroup"

	"trader/internal/notify"
	"trader/internal/pkg/timez"
)

type PriceMarkConfig struct {
	PerTickTimeout   time.Duration
	PerSymbolTimeout time.Duration
	Concurrency      int
}

func priceMarkDefaults(cfg PriceMarkConfig) PriceMarkConfig {
	if cfg.PerTickTimeout == 0 {
		cfg.PerTickTimeout = 30 * time.Second
	}
	if cfg.PerSymbolTimeout == 0 {
		cfg.PerSymbolTimeout = 8 * time.Second
	}
	if cfg.Concurrency == 0 {
		cfg.Concurrency = 5
	}
	return cfg
}

type priceMark struct {
	id        int64
	symbol    string
	target    decimal.Decimal
	direction string
	note      string
}

type PriceMarkCollector struct {
	db      *pgxpool.Pool
	fetcher MarkPriceFetcher
	feish   *notify.Feishu // nil → log only
	log     zerolog.Logger
	cfg     PriceMarkConfig
}

func NewPriceMarkCollector(db *pgxpool.Pool, fetcher MarkPriceFetcher, feish *notify.Feishu, log zerolog.Logger, cfg PriceMarkConfig) *PriceMarkCollector {
	return &PriceMarkCollector{db: db, fetcher: fetcher, feish: feish, log: log, cfg: priceMarkDefaults(cfg)}
}

func (c *PriceMarkCollector) Name() string { return "price_mark" }

func (c *PriceMarkCollector) Run(ctx context.Context) error {
	tickCtx, cancel := context.WithTimeout(ctx, c.cfg.PerTickTimeout)
	defer cancel()

	marks, err := c.activeMarks(tickCtx)
	if err != nil {
		return fmt.Errorf("load active marks: %w", err)
	}
	if len(marks) == 0 {
		return nil // no active marks is the steady state — no log noise
	}

	prices := c.fetchPrices(tickCtx, uniqueMarkSymbols(marks))

	triggered := 0
	for _, m := range marks {
		mp, ok := prices[m.symbol]
		if !ok {
			continue // price unavailable this tick — retry next tick
		}
		if !hitTarget(m.direction, mp, m.target) {
			continue
		}
		if c.fire(tickCtx, m, mp) {
			triggered++
		}
	}
	c.log.Info().Int("active", len(marks)).Int("triggered", triggered).Msg("price_mark tick complete")
	return nil
}

func (c *PriceMarkCollector) activeMarks(ctx context.Context) ([]priceMark, error) {
	rows, err := c.db.Query(ctx, `
		SELECT id, symbol, target_price, direction, note
		FROM price_marks WHERE status = 'active'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []priceMark
	for rows.Next() {
		var m priceMark
		if err := rows.Scan(&m.id, &m.symbol, &m.target, &m.direction, &m.note); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func uniqueMarkSymbols(marks []priceMark) []string {
	seen := make(map[string]struct{}, len(marks))
	out := make([]string, 0, len(marks))
	for _, m := range marks {
		if _, ok := seen[m.symbol]; ok {
			continue
		}
		seen[m.symbol] = struct{}{}
		out = append(out, m.symbol)
	}
	return out
}

// fetchPrices fetches mark price per symbol concurrently. A symbol whose fetch
// fails is simply absent from the map (logged, retried next tick) — never
// triggers a false alert.
func (c *PriceMarkCollector) fetchPrices(ctx context.Context, symbols []string) map[string]decimal.Decimal {
	type res struct {
		symbol string
		price  decimal.Decimal
		ok     bool
	}
	results := make([]res, len(symbols))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(c.cfg.Concurrency)
	for i, sym := range symbols {
		g.Go(func() error {
			sCtx, cancel := context.WithTimeout(gctx, c.cfg.PerSymbolTimeout)
			defer cancel()
			d, err := c.fetcher.FetchMarkPrice(sCtx, sym)
			if err != nil {
				c.log.Warn().Err(err).Str("symbol", sym).Msg("price_mark: fetch mark price failed")
				results[i] = res{symbol: sym}
				return nil // never propagate — keep peers running
			}
			results[i] = res{symbol: sym, price: d.MarkPrice, ok: true}
			return nil
		})
	}
	_ = g.Wait()
	out := make(map[string]decimal.Decimal, len(symbols))
	for _, r := range results {
		if r.ok {
			out[r.symbol] = r.price
		}
	}
	return out
}

func hitTarget(direction string, price, target decimal.Decimal) bool {
	switch direction {
	case "above":
		return price.GreaterThanOrEqual(target)
	case "below":
		return price.LessThanOrEqual(target)
	}
	return false
}

// fire flips the mark to triggered (idempotent via WHERE status='active') and,
// only if this tick actually won the flip, sends the Feishu alert. Returns true
// when it owned the transition — guards against a double send if two ticks
// overlap.
func (c *PriceMarkCollector) fire(ctx context.Context, m priceMark, price decimal.Decimal) bool {
	tag, err := c.db.Exec(ctx, `
		UPDATE price_marks
		SET status = 'triggered', triggered_at = $2, triggered_price = $3, updated_at = $2
		WHERE id = $1 AND status = 'active'`,
		m.id, timez.NowUTC(), price)
	if err != nil {
		c.log.Error().Err(err).Int64("mark_id", m.id).Str("symbol", m.symbol).Msg("price_mark: flip triggered failed")
		return false
	}
	if tag.RowsAffected() != 1 {
		return false // another tick already fired it
	}

	arrow := "↑"
	if m.direction == "below" {
		arrow = "↓"
	}
	title := fmt.Sprintf("价格标记触发 %s %s", m.symbol, arrow)
	body := fmt.Sprintf("%s 现价 %s 已%s目标 %s\n当前标记价 %s%s",
		m.symbol, price.String(), map[string]string{"above": "达到/突破", "below": "跌破"}[m.direction],
		m.target.String(), price.String(), noteSuffix(m.note))
	if c.feish != nil {
		if err := c.feish.Send(ctx, notify.LevelWarning, fmt.Sprintf("price_mark:%d", m.id), title, body); err != nil {
			c.log.Warn().Err(err).Int64("mark_id", m.id).Msg("price_mark: feishu send failed")
		}
	}
	c.log.Info().Int64("mark_id", m.id).Str("symbol", m.symbol).
		Str("direction", m.direction).Str("target", m.target.String()).
		Str("price", price.String()).Msg("price_mark triggered")
	return true
}

func noteSuffix(note string) string {
	if note == "" {
		return ""
	}
	return "\n备注: " + note
}
