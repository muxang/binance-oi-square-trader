package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"

	"trader/internal/binance"
	"trader/internal/pkg/timez"
)

// BTCRegimeConfig parameterises T6.
type BTCRegimeConfig struct {
	RedisKey string        // default "btc_5m_change"
	RedisTTL time.Duration // default 5min — auto-expires if collector dies
}

// BTCRegimeData is the JSON shape persisted to Redis. Phase 3 risk module
// reads + json.Unmarshal + checks DropPct > BTCCrashHaltPct (default 0.03).
// All numeric fields are decimal.Decimal — float64 is forbidden on the
// money-safety path (CLAUDE.md §19).
type BTCRegimeData struct {
	DropPct   decimal.Decimal `json:"drop_pct"`
	Open      decimal.Decimal `json:"open"`
	Close     decimal.Decimal `json:"close"`
	OpenTime  time.Time       `json:"open_time"`
	CheckedAt time.Time       `json:"checked_at"`
}

// BTCRegimeCollector implements T6: every minute, fetch BTCUSDT 5min klines
// and persist the in-progress bar's drop percentage to Redis. The 1-minute
// cron polls the in-progress bar's running close so a fast crash inside a
// 5-minute window is detected within ~60s rather than at bar close.
type BTCRegimeCollector struct {
	client  *binance.Client
	redis   *redis.Client
	log     zerolog.Logger
	cfg     BTCRegimeConfig
	nowFunc func() time.Time
}

func NewBTCRegimeCollector(client *binance.Client, rdb *redis.Client, log zerolog.Logger, cfg BTCRegimeConfig) *BTCRegimeCollector {
	if cfg.RedisKey == "" {
		cfg.RedisKey = "btc_5m_change"
	}
	if cfg.RedisTTL == 0 {
		cfg.RedisTTL = 5 * time.Minute
	}
	return &BTCRegimeCollector{
		client:  client,
		redis:   rdb,
		log:     log,
		cfg:     cfg,
		nowFunc: timez.NowUTC,
	}
}

func (c *BTCRegimeCollector) Name() string { return "btc_regime" }

func (c *BTCRegimeCollector) Run(ctx context.Context) error {
	bars, err := c.fetchKlines(ctx)
	if err != nil {
		return fmt.Errorf("fetchKlines: %w", err)
	}
	if len(bars) == 0 {
		return errors.New("klines: empty response")
	}
	// Last element is the in-progress (or just-closed) bar — its open is the
	// price 0–5 min ago, close is the latest trade. (open-close)/open >0 is a
	// drop in that window. Negative means a rise — keep the sign.
	bar := bars[len(bars)-1]
	if bar.Open.IsZero() {
		c.log.Error().Time("open_time", bar.OpenTime).Msg("btc_regime: open=0 from binance — anomalous bar, skipping")
		return errors.New("btc_regime: open=0 (anomalous bar)")
	}
	dropPct := bar.Open.Sub(bar.Close).Div(bar.Open)
	data := BTCRegimeData{
		DropPct:   dropPct,
		Open:      bar.Open,
		Close:     bar.Close,
		OpenTime:  bar.OpenTime,
		CheckedAt: c.nowFunc(),
	}
	payload, err := json.Marshal(&data)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := c.redis.Set(ctx, c.cfg.RedisKey, payload, c.cfg.RedisTTL).Err(); err != nil {
		return fmt.Errorf("redis set: %w", err)
	}
	c.log.Info().
		Str("drop_pct", dropPct.StringFixed(6)).
		Str("open", bar.Open.String()).
		Str("close", bar.Close.String()).
		Time("open_time", bar.OpenTime).
		Msg("btc regime tick complete")
	return nil
}

func (c *BTCRegimeCollector) fetchKlines(ctx context.Context) ([]binance.KlineBar, error) {
	params := url.Values{
		"symbol":   {"BTCUSDT"},
		"interval": {"5m"},
		"limit":    {"2"},
	}
	body, err := c.client.DoRead(ctx, "/fapi/v1/klines", params, 1)
	if err != nil {
		return nil, err
	}
	return binance.ParseKlines(body)
}
