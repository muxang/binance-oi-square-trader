// binance-oi-square-trader entry point.
//
// Phase 0 wiring: config → logger → proxy → rate-limiter → binance client →
// PG / Redis connectivity → Echo /health server → graceful shutdown. No
// collector / signal / decision / position wiring lives here yet — those
// arrive in Phase 1+.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"

	"trader/internal/api"
	"trader/internal/api/handlers"
	"trader/internal/binance"
	"trader/internal/collector"
	"trader/internal/config"
	"trader/internal/execution"
	"trader/internal/pkg/logger"
	"trader/internal/pkg/ratelimit"
	"trader/internal/pkg/timez"
	"trader/internal/square"
	"trader/internal/storage/postgres/gen"
)

func main() {
	if err := run(); err != nil {
		// Logger may not be ready yet on early failure — emit a minimal
		// structured line to stderr so container logs still capture it.
		fmt.Fprintf(os.Stderr, `{"level":"fatal","error":%q}`+"\n", err.Error())
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config load: %w", err)
	}
	log := logger.Init(cfg)
	logger.StartupBanner(log, cfg) // mainnet 5⚠️ + 5s pause inside

	// Lifecycle context: cancelled on SIGINT/SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	startTime := timez.NowUTC()

	// 5+6+7. Proxy → noop limiter → Binance client.
	proxy, err := binance.NewProxyManager(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("proxy manager init failed")
	}
	log.Info().Str("mode", cfg.Proxy.Mode).Msg("proxy manager ready")

	// Token bucket: 80% of Binance IP weight (2400/min) → 1920 burst, 32/sec
	// refill (ARCHITECTURE §9). Replaces the Phase 0 noop without changing the
	// binance.RateLimiter contract.
	limiter := ratelimit.NewTokenBucket(1920, 32)
	log.Info().Int("capacity", 1920).Int("refill_per_sec", 32).Msg("rate limiter ready")

	client, err := binance.New(cfg, proxy, limiter, log)
	if err != nil {
		log.Fatal().Err(err).Msg("binance client init failed")
	}
	log.Info().Msg("binance client ready")

	// 8a. Postgres pool + ping.
	pgCtx, pgCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pgCancel()
	pgPool, err := pgxpool.New(pgCtx, cfg.DB.PostgresURL)
	if err != nil {
		log.Fatal().Err(err).Msg("postgres pool init failed")
	}
	if err := pgPool.Ping(pgCtx); err != nil {
		log.Fatal().Err(err).Msg("postgres ping failed")
	}
	log.Info().Msg("postgres ready")

	// Phase 4 Round 1 follow-up: startup orphan cleanup for Phase 3 v0.1 PARTIAL
	// legacy 'entering' rows (no client_order_id, no entry_ts). Round 1+ in-flight
	// orders set client_order_id at INSERT time, so this never touches them.
	if n, err := gen.New(pgPool).CleanupOrphanEnteringTrades(pgCtx); err != nil {
		log.Warn().Err(err).Msg("orphan entering cleanup failed (non-fatal)")
	} else if n > 0 {
		log.Info().Int64("rows", n).Msg("orphan entering trades cleaned")
	}

	// Phase 4 Round 2 startup recovery: reconcile Round 1+ 'entering' rows
	// (have client_order_id) against Binance order state. FILLED → open,
	// NEW/PARTIAL → cancel+fail, not_found → fail. Per-row try; non-fatal.
	recoveryCtx, recoveryCancel := context.WithTimeout(ctx, 30*time.Second)
	_, _ = execution.RecoverEnteringTrades(recoveryCtx, gen.New(pgPool), client, log)
	recoveryCancel()

	// 8b. Redis client + ping.
	redisOpts, err := redis.ParseURL(cfg.DB.RedisURL)
	if err != nil {
		log.Fatal().Err(err).Msg("redis URL parse failed")
	}
	rdb := redis.NewClient(redisOpts)
	rdsCtx, rdsCancel := context.WithTimeout(ctx, 5*time.Second)
	defer rdsCancel()
	if err := rdb.Ping(rdsCtx).Err(); err != nil {
		log.Fatal().Err(err).Msg("redis ping failed")
	}
	log.Info().Msg("redis ready")

	// 8c. Collector runner. T1 (OI history) registered in 1.1; T2-T7 follow in 1.2+.
	runner := collector.New(log)
	oiCol := collector.NewOICollector(client, pgPool, log, collector.OICollectorConfig{
		Concurrency: cfg.Collector.OIConcurrency,
	})
	if err := runner.Register(oiCol, "*/5 * * * *"); err != nil {
		log.Fatal().Err(err).Msg("register oi collector")
	}
	btcCol := collector.NewBTCRegimeCollector(client, rdb, log, collector.BTCRegimeConfig{})
	if err := runner.Register(btcCol, "* * * * *"); err != nil {
		log.Fatal().Err(err).Msg("register btc_regime collector")
	}
	klinesCol := collector.NewKlinesCollector(client, pgPool, rdb, log, collector.KlinesCollectorConfig{
		Concurrency:     cfg.Collector.OIConcurrency,
		SymbolCacheTTL:  1 * time.Hour,
		KlineLimit:      30,
		KlineInterval:   "15m",
		ATRPeriod:       14,
		EMAPeriod:       20,
		ATRRedisTTL:     30 * time.Minute,
		EMARedisTTL:     30 * time.Minute,
		HighFailureRate: 0.30,
	})
	if err := runner.Register(klinesCol, "*/5 * * * *"); err != nil {
		log.Fatal().Err(err).Msg("register klines collector")
	}
	symbolService := binance.NewSymbolService(client, log)
	log.Info().Msg("symbol service ready")
	squareClient, err := square.NewSquareClient(ctx, proxy, limiter, rdb, cfg.Square.UseProxy, log)
	if err != nil {
		log.Fatal().Err(err).Msg("init square client")
	}
	log.Info().Msg("square client ready")
	squareCol := collector.NewSquareCollector(squareClient, symbolService, pgPool, log, collector.SquareCollectorConfig{})
	if err := runner.Register(squareCol, "0 * * * *"); err != nil {
		log.Fatal().Err(err).Msg("register square collector")
	}
	hashtagCol := collector.NewSquareHashtagCollector(squareClient, symbolService, pgPool, log, collector.SquareHashtagConfig{
		PerTickTimeout:   cfg.Square.HashtagBatchDeadline,
		PerSymbolTimeout: cfg.Square.HashtagTimeout,
		Concurrency:      cfg.Square.HashtagConcurrency,
		RetryCount:       cfg.Square.HashtagRetryCount,
		RetryInterval:    1 * time.Second,
		HighFailureRate:  0.30,
	})
	// Phase 2 v0.1: cron 5min -> 15min (全采集 + 自适应 hot 算法不需 5min 粒度)
	if err := runner.Register(hashtagCol, "*/15 * * * *"); err != nil {
		log.Fatal().Err(err).Msg("register square hashtag collector")
	}
	watchlistCol := collector.NewWatchlistCollector(symbolService, client, gen.New(pgPool), rdb, log, collector.WatchlistCollectorConfig{
		SquareTopN:            cfg.Watchlist.SquareTopN,
		OITopN:                cfg.Watchlist.OITopN,
		PriceTopN:             cfg.Watchlist.PriceTopN,
		MaxSize:               cfg.Watchlist.MaxSize,
		MinSize:               cfg.Watchlist.MinSize,
		MinListingDays:        cfg.Watchlist.MinListDays,  // env: WATCHLIST_MIN_LIST_DAYS
		MinQuoteVolume:        cfg.Watchlist.MinVolumeUSD, // env: WATCHLIST_MIN_VOLUME_USD
		Blacklist:             cfg.Watchlist.Blacklist,
		LeverageTokenSuffixes: cfg.Watchlist.LeverageTokenSuffixes,
		RedisKey:              "watchlist:current",
	})
	if err := runner.Register(watchlistCol, "0 * * * *"); err != nil {
		log.Fatal().Err(err).Msg("register watchlist collector")
	}
	posPriceCol := collector.NewPositionPriceCollector(client, gen.New(pgPool), rdb, log, collector.PositionPriceConfig{
		PerTickTimeout:   25 * time.Second,
		PerSymbolTimeout: 8 * time.Second,
		Concurrency:      5,
		RetryCount:       2,
		RetryInterval:    1 * time.Second,
		RedisTTL:         5 * time.Minute,
	})
	if err := runner.Register(posPriceCol, "* * * * *"); err != nil {
		log.Fatal().Err(err).Msg("register position_price collector")
	}
	// Phase 2 v0.1: signal_engine — 5min cron, 评估 watchlist:current 池中 symbols,
	// 写 signals 表 (含 rejected). 详见 internal/collector/signal_engine.go 文件头.
	sigEngineCol := collector.NewSignalEngineCollector(gen.New(pgPool), rdb, log, collector.SignalEngineConfig{
		PerTickTimeout: 4 * time.Minute,
		Concurrency:    10,
	})
	if err := runner.Register(sigEngineCol, "*/5 * * * *"); err != nil {
		log.Fatal().Err(err).Msg("register signal_engine collector")
	}
	// Phase 4 v0.1: Executor — wires binance client + DB into the entry flow.
	// DisasterStopPct and Leverage read from cfg (DISASTER_STOP_PCT, LEVERAGE).
	executor := execution.New(client, gen.New(pgPool), execution.Config{
		DisasterStopPct: cfg.Exit.DisasterStopPct,
		Leverage:        cfg.Position.Leverage,
	}, log)
	log.Info().
		Str("disaster_stop_pct", cfg.Exit.DisasterStopPct.String()).
		Int("leverage", cfg.Position.Leverage).
		Msg("executor ready")

	// Phase 4 Round 3: position_manager — 1min sync of open positions against
	// /fapi/v3/positionRisk + Redis zset positions_active + MARGIN_CALL detect.
	positionManager := execution.NewPositionManager(gen.New(pgPool), client, rdb, log)
	positionManagerCol := collector.NewPositionManagerCollector(positionManager, log, collector.PositionManagerConfig{})
	if err := runner.Register(positionManagerCol, "*/1 * * * *"); err != nil {
		log.Fatal().Err(err).Msg("register position_manager collector")
	}

	// Phase 3 v0.1: decision_engine — 5min cron, reads entered_* signals,
	// runs filters + sizing → trades.entering. Phase 4: fires executor.PlaceEntry.
	decisionEngineCol := collector.NewDecisionEngineCollector(
		gen.New(pgPool), rdb, symbolService, executor, log, collector.DecisionEngineConfig{
			PerTickTimeout: 4 * time.Minute,
		},
	)
	if err := runner.Register(decisionEngineCol, "*/5 * * * *"); err != nil {
		log.Fatal().Err(err).Msg("register decision_engine collector")
	}
	runner.Start()

	// 9. HTTP server with /health backed by real ping closures.
	deps := handlers.HealthDeps{
		PingPG:    pgPool.Ping,
		PingRedis: func(ctx context.Context) error { return rdb.Ping(ctx).Err() },
		Version:   logger.Version,
		Mode:      cfg.Mode,
		StartTime: startTime,
	}
	e := api.New(deps)
	addr := ":" + strconv.Itoa(cfg.HTTP.Port)
	serverErr := make(chan error, 1)
	go func() {
		if err := e.Start(addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()
	log.Info().Int("port", cfg.HTTP.Port).Msg("http server listening")

	// 9b. Dedicated Prometheus /metrics on :2112. Internal-only — separate port
	// keeps scrape traffic off the dashboard server and lets us enforce a
	// different bind/firewall policy on the metrics endpoint later.
	metricsServer := &http.Server{
		Addr:         ":2112",
		Handler:      promhttp.Handler(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	go func() {
		log.Info().Str("addr", metricsServer.Addr).Msg("metrics server starting")
		if err := metricsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error().Err(err).Msg("metrics server error")
		}
	}()

	// 10. Wait for signal or fatal server error.
	select {
	case <-ctx.Done():
		log.Info().Msg("shutdown signal received")
	case err := <-serverErr:
		log.Error().Err(err).Msg("http server crashed")
	}

	// Graceful shutdown order: collector → api(:8080) → metrics(:2112) →
	// redis → postgres. Metrics goes last among HTTP servers so Prometheus's
	// final scrape captures terminal counters (collector stop bumps panic /
	// error counters that we want recorded).
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runner.Stop(10 * time.Second); err != nil {
		log.Error().Err(err).Msg("collector runner stop failed")
	}
	if err := e.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("http server shutdown failed")
	}
	log.Info().Msg("http server stopped")
	if err := metricsServer.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("metrics server shutdown failed")
	}
	log.Info().Msg("metrics server stopped")
	if err := rdb.Close(); err != nil {
		log.Error().Err(err).Msg("redis close failed")
	}
	pgPool.Close()
	log.Info().Msg("shutdown complete")
	return nil
}
