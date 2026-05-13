// Phase 5.2 Round 4: daily report cron — fires at BJT 00:00 to push the
// ⚪ daily Feishu message: balance + daily_pnl + open positions + cumulative
// win rate. Self-contained collector so registration mirrors other cron jobs.

package collector

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"

	"trader/internal/notify"
)

// DailyReportBalanceFetcher abstracts the binance balance call so tests can
// stub it. Production wires *binance.Client.GetUSDTBalance.
type DailyReportBalanceFetcher interface {
	GetUSDTBalance(ctx context.Context) (decimal.Decimal, error)
}

type DailyReportConfig struct {
	PerTickTimeout time.Duration
}

func dailyReportDefaults(cfg DailyReportConfig) DailyReportConfig {
	if cfg.PerTickTimeout == 0 {
		cfg.PerTickTimeout = 15 * time.Second
	}
	return cfg
}

type DailyReportCollector struct {
	db    *pgxpool.Pool
	bc    DailyReportBalanceFetcher
	feish *notify.Feishu // nil → log only
	log   zerolog.Logger
	cfg   DailyReportConfig
}

func NewDailyReportCollector(db *pgxpool.Pool, bc DailyReportBalanceFetcher, feish *notify.Feishu, log zerolog.Logger, cfg DailyReportConfig) *DailyReportCollector {
	return &DailyReportCollector{db: db, bc: bc, feish: feish, log: log, cfg: dailyReportDefaults(cfg)}
}

func (c *DailyReportCollector) Name() string { return "daily_report" }

func (c *DailyReportCollector) Run(ctx context.Context) error {
	tickCtx, cancel := context.WithTimeout(ctx, c.cfg.PerTickTimeout)
	defer cancel()

	// Read circuit_breaker_state + open positions + cumulative stats in one shot.
	var (
		dailyPnl      decimal.Decimal
		openPositions int
		totalTrades   int
		winCount      int
	)
	if err := c.db.QueryRow(tickCtx, `SELECT COALESCE(daily_pnl, 0) FROM circuit_breaker_state WHERE id=1`).Scan(&dailyPnl); err != nil {
		c.log.Warn().Err(err).Msg("daily_report: read daily_pnl failed (continuing with 0)")
	}
	if err := c.db.QueryRow(tickCtx, `SELECT COUNT(*) FROM trades WHERE status IN ('open','partial')`).Scan(&openPositions); err != nil {
		c.log.Warn().Err(err).Msg("daily_report: count open trades failed (continuing with 0)")
	}
	// Cumulative all-time stats — keep simple; future Round 5+ can scope to "this month".
	if err := c.db.QueryRow(tickCtx, `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE realized_pnl > 0)
		FROM trades WHERE status = 'closed'
	`).Scan(&totalTrades, &winCount); err != nil {
		c.log.Warn().Err(err).Msg("daily_report: cumulative stats failed (continuing with 0)")
	}

	// Balance via Binance API (read-only, fast).
	balance, err := c.bc.GetUSDTBalance(tickCtx)
	if err != nil {
		c.log.Warn().Err(err).Msg("daily_report: balance fetch failed (sending 0)")
		balance = decimal.Zero
	}

	c.log.Info().
		Str("daily_pnl", dailyPnl.String()).
		Str("balance", balance.String()).
		Int("open_positions", openPositions).
		Int("total_trades", totalTrades).
		Int("win_count", winCount).
		Msg("daily_report.tick")

	if c.feish != nil {
		level, dedupe, title, body := notify.DailyReport(dailyPnl, balance, openPositions, totalTrades, winCount)
		_ = c.feish.Send(tickCtx, level, dedupe, title, body)
	}
	return nil
}
