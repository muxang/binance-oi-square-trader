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
	"github.com/redis/go-redis/v9"

	"trader/internal/api"
	"trader/internal/api/handlers"
	"trader/internal/binance"
	"trader/internal/collector"
	"trader/internal/config"
	"trader/internal/pkg/logger"
	"trader/internal/pkg/ratelimit"
	"trader/internal/pkg/timez"
	"trader/internal/square"
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
	_ = client // consumed by collector / execution layers in Phase 1+
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
	hashtagCol := collector.NewSquareHashtagCollector(squareClient, rdb, pgPool, log, collector.SquareHashtagConfig{
		PerTickTimeout:    cfg.Square.HashtagBatchDeadline,
		PerSymbolTimeout:  cfg.Square.HashtagTimeout,
		Concurrency:       cfg.Square.HashtagConcurrency,
		RetryCount:        cfg.Square.HashtagRetryCount,
		RetryInterval:     1 * time.Second,
		HighFailureRate:   0.30,
		WatchlistRedisKey: "watchlist:current",
	})
	if err := runner.Register(hashtagCol, "*/5 * * * *"); err != nil {
		log.Fatal().Err(err).Msg("register square hashtag collector")
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

	// 10. Wait for signal or fatal server error.
	select {
	case <-ctx.Done():
		log.Info().Msg("shutdown signal received")
	case err := <-serverErr:
		log.Error().Err(err).Msg("http server crashed")
	}

	// Graceful shutdown in reverse dependency order.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("http server shutdown failed")
	}
	log.Info().Msg("http server stopped")
	if err := runner.Stop(10 * time.Second); err != nil {
		log.Error().Err(err).Msg("collector runner stop failed")
	}
	if err := rdb.Close(); err != nil {
		log.Error().Err(err).Msg("redis close failed")
	}
	pgPool.Close()
	log.Info().Msg("shutdown complete")
	return nil
}
