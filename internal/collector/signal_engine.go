// Phase 2 信号引擎.
//
// 数据流:
//
//	T3 (全采集 ~530 symbol) → square_hashtag_history (全历史)
//	T4 → watchlist:current (动态池 ~150)
//	signal_engine → 读 watchlist:current 拿池中 symbol
//	              → 对每个调 oi_surge + hot → signals
//	              → hot 算法依赖 T3 全采集的完整历史
//
// 关键认知: 评估范围 = 池中 (~150), 历史范围 = 全 (~530)
// T3 全采集让 "池子动态进出" 不影响 hot 判定的数据完整性。
//
// 写入策略: 每 5min 评估池中所有 symbol, 全写 (含 rejected), 不去重。
// 24h 不二次入场过滤是 Phase 3 决策引擎职责, Phase 2 仅产出原始 signals.

package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"
	"golang.org/x/sync/errgroup"

	"trader/internal/pkg/metrics"
	"trader/internal/pkg/timez"
	"trader/internal/signal"
	"trader/internal/storage/postgres/gen"
)

const (
	signalEngineWatchlistKey    = "watchlist:current"
	signalEngineKlinesTimeframe = "15m"
	signalEngineKlinesLimit     = 5 // last 5 × 15min = 75min back, covers 60min ago bar
)

type SignalEngineConfig struct {
	PerTickTimeout time.Duration
	Concurrency    int
	CompoundCfg    signal.CompoundConfig
}

func signalEngineDefaults(cfg SignalEngineConfig) SignalEngineConfig {
	if cfg.PerTickTimeout == 0 {
		cfg.PerTickTimeout = 4 * time.Minute
	}
	if cfg.Concurrency == 0 {
		cfg.Concurrency = 10
	}
	return cfg
}

// SignalEngineCollector implements Phase 2 信号引擎, registered in main.go
// with cron */5 (5min decision window per SPEC §信号 L45).
type SignalEngineCollector struct {
	redis   *redis.Client
	deps    signal.SignalDataAccess
	log     zerolog.Logger
	cfg     SignalEngineConfig
	nowFunc func() time.Time
}

func NewSignalEngineCollector(queries *gen.Queries, rdb *redis.Client, log zerolog.Logger, cfg SignalEngineConfig) *SignalEngineCollector {
	cfg = signalEngineDefaults(cfg)
	c := &SignalEngineCollector{
		redis:   rdb,
		log:     log,
		cfg:     cfg,
		nowFunc: timez.NowUTC,
	}
	c.deps = &signalDataAccess{queries: queries, log: log, nowFunc: c.nowFunc}
	return c
}

func (c *SignalEngineCollector) Name() string { return "signal_engine" }

func (c *SignalEngineCollector) Run(ctx context.Context) error {
	tickCtx, cancel := context.WithTimeout(ctx, c.cfg.PerTickTimeout)
	defer cancel()

	pool, err := c.readWatchlist(tickCtx)
	if err != nil {
		return fmt.Errorf("read watchlist: %w", err)
	}
	if len(pool) == 0 {
		c.log.Warn().Msg("signal_engine: empty watchlist (T4 not yet run / Redis hiccup)")
		return nil
	}

	var (
		mu                             sync.Mutex
		full, half, rejected, errCount int
	)
	g, gctx := errgroup.WithContext(tickCtx)
	g.SetLimit(c.cfg.Concurrency)
	for _, symbol := range pool {
		g.Go(func() error {
			rec, evalErr := signal.Evaluate(gctx, symbol, c.nowFunc(), c.deps, c.cfg.CompoundCfg)
			mu.Lock()
			defer mu.Unlock()
			if evalErr != nil {
				errCount++
				c.log.Warn().Err(evalErr).Str("symbol", symbol).Msg("signal_engine: per-symbol eval failed")
				metrics.SignalEvaluationsTotal.WithLabelValues("error").Inc()
				return nil // 不杀整 tick, 跨 symbol 隔离 (Phase 1 模式)
			}
			switch rec.Decision {
			case "entered_full":
				full++
			case "entered_half":
				half++
			case "rejected":
				rejected++
			}
			metrics.SignalEvaluationsTotal.WithLabelValues(rec.Decision).Inc()
			return nil
		})
	}
	_ = g.Wait()

	c.log.Info().
		Int("pool_size", len(pool)).
		Int("entered_full", full).
		Int("entered_half", half).
		Int("rejected", rejected).
		Int("error", errCount).
		Msg("signal_engine tick complete")
	return nil
}

func (c *SignalEngineCollector) readWatchlist(ctx context.Context) ([]string, error) {
	val, err := c.redis.Get(ctx, signalEngineWatchlistKey).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var symbols []string
	if err := json.Unmarshal([]byte(val), &symbols); err != nil {
		return nil, fmt.Errorf("unmarshal watchlist: %w", err)
	}
	return symbols, nil
}

// --- adapter: signal.SignalDataAccess ⇐ gen.Queries ---

type signalDataAccess struct {
	queries *gen.Queries
	log     zerolog.Logger
	nowFunc func() time.Time
}

func reverseDecimals(in []decimal.Decimal) []decimal.Decimal {
	n := len(in)
	out := make([]decimal.Decimal, n)
	for i := 0; i < n; i++ {
		out[i] = in[n-1-i]
	}
	return out
}

func (a *signalDataAccess) GetOIHistory(ctx context.Context, symbol string, limit int) ([]decimal.Decimal, error) {
	rows, err := a.queries.GetLatestOIHistory(ctx, gen.GetLatestOIHistoryParams{Symbol: symbol, Limit: int32(limit)})
	if err != nil {
		return nil, err
	}
	desc := make([]decimal.Decimal, len(rows))
	for i, r := range rows {
		desc[i] = r.Oi
	}
	return reverseDecimals(desc), nil
}

func (a *signalDataAccess) GetHashtagHistory(ctx context.Context, symbol string, limit int) ([]decimal.Decimal, error) {
	rows, err := a.queries.GetLatestHashtagHistory(ctx, gen.GetLatestHashtagHistoryParams{Symbol: symbol, Limit: int32(limit)})
	if err != nil {
		return nil, err
	}
	desc := make([]decimal.Decimal, len(rows))
	for i, r := range rows {
		desc[i] = decimal.NewFromInt(r.ContentCount)
	}
	return reverseDecimals(desc), nil
}

func (a *signalDataAccess) GetKlinesCloseNowAndPrior(ctx context.Context, symbol string, priorAgo time.Duration) (decimal.Decimal, decimal.Decimal, error) {
	rows, err := a.queries.GetLatestKlines(ctx, gen.GetLatestKlinesParams{
		Symbol: symbol, Timeframe: signalEngineKlinesTimeframe, Limit: signalEngineKlinesLimit,
	})
	if err != nil {
		return decimal.Zero, decimal.Zero, err
	}
	if len(rows) == 0 {
		return decimal.Zero, decimal.Zero, fmt.Errorf("no klines for %s", symbol)
	}
	closeNow := rows[0].Close
	target := rows[0].OpenTime.Add(-priorAgo)
	bestIdx := 0
	bestDelta := time.Duration(math.MaxInt64)
	for i, r := range rows {
		delta := r.OpenTime.Sub(target)
		if delta < 0 {
			delta = -delta
		}
		if delta < bestDelta {
			bestDelta = delta
			bestIdx = i
		}
	}
	return closeNow, rows[bestIdx].Close, nil
}

func (a *signalDataAccess) InsertSignal(ctx context.Context, rec signal.SignalRecord) error {
	oiData, err := signal.MarshalOIDataJSON(rec.OIData)
	if err != nil {
		return fmt.Errorf("marshal oi_data: %w", err)
	}
	squareData, err := signal.MarshalSquareDataJSON(rec.SquareData)
	if err != nil {
		return fmt.Errorf("marshal square_data: %w", err)
	}
	rejReason := pgtype.Text{Valid: false}
	if rec.RejectionReason != "" {
		rejReason = pgtype.Text{String: rec.RejectionReason, Valid: true}
	}
	return a.queries.InsertSignal(ctx, gen.InsertSignalParams{
		Ts:              rec.Ts,
		Symbol:          rec.Symbol,
		OiTriggered:     rec.OITriggered,
		OiData:          oiData,
		SquareHot:       rec.SquareHot,
		SquareData:      squareData,
		Decision:        rec.Decision,
		RejectionReason: rejReason,
	})
}
