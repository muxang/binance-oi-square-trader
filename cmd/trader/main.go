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

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"

	"trader/internal/api"
	"trader/internal/api/handlers"
	"trader/internal/binance"
	"trader/internal/coingecko"
	"trader/internal/collector"
	"trader/internal/notify"
	"trader/internal/config"
	"trader/internal/decision"
	"trader/internal/execution"
	"trader/internal/pkg/logger"
	"trader/internal/pkg/metrics"
	"trader/internal/pkg/ratelimit"
	"trader/internal/pkg/timez"
	"trader/internal/square"
	"trader/internal/storage/postgres/gen"
)

// restartResult maps StartupRecoveryReport to a single metric label.
// Priority: halt_triggered > with_reconciles > clean. "error" is reserved
// for future use (StartupRecovery never returns an error currently).
func restartResult(r execution.StartupRecoveryReport) string {
	if r.HaltAfterRecovery {
		return "halt_triggered"
	}
	if r.EnteringReconciled > 0 || r.EnteringFailed > 0 {
		return "with_reconciles"
	}
	return "clean"
}

func main() {
	// Round 4: rca subcommands for halt root-cause review without standing up
	// the full trader. `./trader rca-list` and `./trader rca-ack <id> <action>`
	// share the trader's DB connection config.
	if len(os.Args) > 1 && (os.Args[1] == "rca-list" || os.Args[1] == "rca-ack") {
		if err := runRCACommand(os.Args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, `{"level":"fatal","cmd":%q,"error":%q}`+"\n", os.Args[1], err.Error())
			os.Exit(1)
		}
		return
	}
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

	// Phase 5.2 Round 2.x: seed the hot-reloadable runtime config from .env
	// baseline. config_reloader (registered below) layers admin_overrides on
	// top per-tick. Consumers (circuit_breaker today; more coming) read via
	// config.Get() each evaluation.
	config.InitRuntimeFromConfig(cfg)

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
	// (api_error hook wired AFTER pool is ready — see SetAPIErrorHook below.)
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

	// v0.2 Gap 2: wire api_errors auto-populate hook now that pgPool is ready.
	// Each surfaced binance error → INSERT api_errors row. Round 6
	// TripAPIErrorRate counts rows in the last 1min window and trips at ≥3.
	apiErrLogger := log.With().Str("component", "api_error_hook").Logger()
	apiErrQueries := gen.New(pgPool)
	client.SetAPIErrorHook(func(hookCtx context.Context, source, endpoint string, httpCode, bizCode int, message string) {
		// Truncate message to avoid pathological 100kb error bodies bloating DB.
		const maxMsgLen = 1024
		if len(message) > maxMsgLen {
			message = message[:maxMsgLen]
		}
		params := gen.InsertAPIErrorParams{
			Ts:        timez.NowUTC(),
			Source:    source,
			Endpoint:  pgtype.Text{String: endpoint, Valid: endpoint != ""},
			HttpCode:  pgtype.Int4{Int32: int32(httpCode), Valid: httpCode != 0},
			ErrorCode: pgtype.Int4{Int32: int32(bizCode), Valid: bizCode != 0},
			Message:   pgtype.Text{String: message, Valid: message != ""},
		}
		// Non-blocking: insert with own short timeout so a stuck DB doesn't
		// pile up goroutines on every API error during an outage. Errors here
		// are logged + dropped (better than blocking caller's tick).
		insertCtx, cancel := context.WithTimeout(hookCtx, 2*time.Second)
		defer cancel()
		if err := apiErrQueries.InsertAPIError(insertCtx, params); err != nil {
			apiErrLogger.Warn().Err(err).
				Str("source", source).Str("endpoint", endpoint).
				Int("http_code", httpCode).Int("biz_code", bizCode).
				Msg("api_errors insert failed (dropped)")
		}
	})
	log.Info().Msg("api_error hook wired")

	// Phase 4 Round 1 follow-up: startup orphan cleanup for Phase 3 v0.1 PARTIAL
	// legacy 'entering' rows (no client_order_id, no entry_ts). Round 1+ in-flight
	// orders set client_order_id at INSERT time, so this never touches them.
	if n, err := gen.New(pgPool).CleanupOrphanEnteringTrades(pgCtx); err != nil {
		log.Warn().Err(err).Msg("orphan entering cleanup failed (non-fatal)")
	} else if n > 0 {
		log.Info().Int64("rows", n).Msg("orphan entering trades cleaned")
	}

	// Phase 4 Round 7: full startup recovery is run AFTER all subsystems are
	// constructed (positionManager / exitManager / circuitBreaker) so the
	// orchestrator can call them eagerly. See block below `circuit_breaker config`.

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
	// Round R.11.A2c-1: large-trader long/short ratio (account + position) for
	// every watchlist symbol, 5min cron. ref: references/user-snippets/contract-monitor.js
	lhCol := collector.NewLargeHolderCollector(client, pgPool, log, collector.LargeHolderCollectorConfig{
		Concurrency: cfg.Collector.OIConcurrency,
	})
	if err := runner.Register(lhCol, "*/5 * * * *"); err != nil {
		log.Fatal().Err(err).Msg("register large_holder collector")
	}
	// Round R.11.A2b: CoinGecko symbol→id mapping refresh (twice daily BJT;
	// run once at startup if table is empty). Failures non-fatal — circulating-
	// supply collector tolerates missing mappings (writes NULL market_cap_ratio).
	// ref: references/external/coingecko.md
	// CoinGecko Demo plan rate-limits per-IP. Route through binance ProxyPool
	// so requests rotate IPs — bypasses 429 throttle without needing a paid key.
	// proxyHTTP fn maps proxy.ProxyManager → coingecko.HTTPClientFn shape.
	cgCli := coingecko.NewClient(os.Getenv("COINGECKO_DEMO_API_KEY")).WithProxyHTTP(
		func(ctx context.Context) (*http.Client, error) {
			hc, _, err := proxy.HTTPClient(ctx)
			return hc, err
		})
	cgMapCol := collector.NewCoingeckoSymbolMapCollector(cgCli, pgPool, log)
	cgMapCol.EnsureMappingPopulated(ctx)
	if err := runner.Register(cgMapCol, "0 0,12 * * *"); err != nil {
		log.Fatal().Err(err).Msg("register coingecko_symbol_map collector")
	}
	// Round R.11.A2c-2: 6h cron back-fills large_holder_ratios.market_cap_ratio_pct
	// using CoinGecko /coins/markets supply + cached oi_history.oi_value_usd.
	// ref: references/user-snippets/contract-monitor.js (calculateMarketCapRatio)
	suppCol := collector.NewCirculatingSupplyCollector(cgCli, pgPool, log)
	if err := runner.Register(suppCol, "0 */6 * * *"); err != nil {
		log.Fatal().Err(err).Msg("register circulating_supply collector")
	}
	// One-shot startup kick. Delays 30s so symbol_map collector's startup
	// CoinGecko hits (5 calls in 12s) clear the Demo plan's per-minute rate
	// window before supply collector adds its own 2-3 batch calls. Without
	// the delay both collectors race the 30/min ceiling and the second one
	// gets 429 across all batches.
	go func() {
		select {
		case <-time.After(30 * time.Second):
		case <-ctx.Done():
			return
		}
		if err := suppCol.Run(ctx); err != nil {
			log.Warn().Err(err).Msg("circulating_supply: startup one-shot run failed (cron will retry)")
		}
	}()
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
	// v0.1.x: Executor — wires binance client + DB + Redis into the entry flow.
	// ATR-based disaster stop: clip(ATR/price × ATRStopMult, MinStopPct, MaxStopPct).
	// Falls back to DisasterStopPct when ATR is unavailable in Redis.
	executor := execution.New(client, gen.New(pgPool), rdb, execution.Config{
		DisasterStopPct:         cfg.Exit.DisasterStopPct,
		ATRStopMult:             cfg.Exit.TrailingDistanceATRMult,
		MinStopPct:              cfg.Exit.MinStopPct,
		MaxStopPct:              cfg.Exit.MaxStopPct,
		TrailStage1ActivatePct:  cfg.Exit.TrailStage1ActivatePct,
		TrailStage1CallbackRate: cfg.Exit.TrailStage1CallbackRate,
		TP1Pct:                  cfg.Exit.TP1Pct,
		TP1Ratio:                cfg.Exit.TP1Ratio,
		TP2Pct:                  cfg.Exit.TP2Pct,
		TP2Ratio:                cfg.Exit.TP2Ratio,
		Leverage:                cfg.Position.Leverage,
	}, log)
	log.Info().
		Str("disaster_stop_pct_fallback", cfg.Exit.DisasterStopPct.String()).
		Str("atr_stop_mult", cfg.Exit.TrailingDistanceATRMult.String()).
		Str("min_stop_pct", cfg.Exit.MinStopPct.String()).
		Str("max_stop_pct", cfg.Exit.MaxStopPct.String()).
		Str("trail_s1_activate", cfg.Exit.TrailStage1ActivatePct.String()).
		Str("trail_s1_callback", cfg.Exit.TrailStage1CallbackRate.String()).
		Str("tp1_pct", cfg.Exit.TP1Pct.String()).Str("tp1_ratio", cfg.Exit.TP1Ratio.String()).
		Str("tp2_pct", cfg.Exit.TP2Pct.String()).Str("tp2_ratio", cfg.Exit.TP2Ratio.String()).
		Int("leverage", cfg.Position.Leverage).
		Msg("executor ready (ATR-based disaster stop + S1 trail + TP1/TP2 at entry)")

	// v0.2 Gap 1: algo_polling — 1min poll of disaster-stop Algo orders to
	// auto-close trades when Binance reports algoStatus=FINISHED. Registered
	// BEFORE position_manager so the orphan-detection branch is the fallback,
	// not the primary path (per mu §4.5 v0.2 mini-round directive).
	algoReconciler := execution.NewAlgoReconciler(gen.New(pgPool), client, rdb, log).
		WithFeesFetcher(client)
	algoPollingCol := collector.NewAlgoPollingCollector(algoReconciler, log, collector.AlgoPollingConfig{})
	if err := runner.Register(algoPollingCol, "*/1 * * * *"); err != nil {
		log.Fatal().Err(err).Msg("register algo_polling collector")
	}

	// Round R.3: orphan_algo_cleaner — 1min sweep that cancels SELL reduceOnly
	// Algos whose Binance position is already closed. Reclaims algo limit slots
	// + silences algo_polling noise. Coexists with position_manager (different
	// concerns: PM protects trade state; cleaner reclaims Binance resources).
	orphanAlgoCleaner := execution.NewOrphanAlgoCleaner(client, pgPool, log)
	orphanAlgoCleanerCol := collector.NewOrphanAlgoCleanerCollector(orphanAlgoCleaner, log, collector.OrphanAlgoCleanerConfig{})
	if err := runner.Register(orphanAlgoCleanerCol, "*/1 * * * *"); err != nil {
		log.Fatal().Err(err).Msg("register orphan_algo_cleaner collector")
	}
	log.Info().Msg("orphan_algo_cleaner ready (Round R.3, 1min cron)")

	// Phase 5.2 Round 2.x: config_reloader — 1min cron that reads admin_overrides
	// table and atomically swaps the runtime config. Trader consumers
	// (circuit_breaker today) call config.Get() each evaluation, so admin Web UI
	// changes take effect within 60s.
	baselineRuntime := &config.Runtime{
		DailyLossHaltPct:      cfg.Risk.DailyLossHaltPct,
		ConsecutiveLossesHalt: cfg.Risk.ConsecutiveLossHaltCount,
		// Round 2.y: 4 more wired keys.
		TotalFloatLossHaltPct: cfg.Risk.TotalFloatLossHaltPct,
		BTCCrashHaltPct:       cfg.Risk.BTCCrashHaltPct,
		MaxStopPct:            cfg.Exit.MaxStopPct,
		Leverage:              cfg.Position.Leverage,
		// signal_engine refactor (Round 2.y final 2 keys).
		OiGrowthFromMinPct:  cfg.OISurge.FromLowPct,
		SquareHotMultiplier: cfg.SquareHot.Multiplier,
		// Round 2.z trail thresholds (mu 真盘 owner catch).
		TrailStage1ActivatePct: cfg.Exit.TrailStage1ActivatePct,
		TrailStage2UpgradePct:  cfg.Exit.TrailStage2UpgradePct,
		TrailStage3UpgradePct:  cfg.Exit.TrailStage3UpgradePct,
		TrailStage4UpgradePct:  cfg.Exit.TrailStage4UpgradePct,
		// Round 2.w trail callback rates (mu owner 2026-05-14 catch — 回撤值之前没能调).
		TrailStage1CallbackRate: cfg.Exit.TrailStage1CallbackRate,
		TrailStage2CallbackRate: cfg.Exit.TrailStage2CallbackRate,
		TrailStage3CallbackRate: cfg.Exit.TrailStage3CallbackRate,
		TrailStage4CallbackRate: cfg.Exit.TrailStage4CallbackRate,
		// Round R.7 F2 (mu owner 13:30 BJT 2026-05-14 catch — proxy 13s outage
		// false halt). Threshold tunable via admin Web UI.
		APIErrorRateLimit: cfg.Risk.APIErrorRateLimit,
	}
	// Seed atomic Runtime so consumer getters see baseline before the first reloader tick.
	config.Set(baselineRuntime)
	configReloader := collector.NewConfigReloader(pgPool, baselineRuntime, log, collector.ConfigReloaderConfig{})
	if err := runner.Register(configReloader, "*/1 * * * *"); err != nil {
		log.Fatal().Err(err).Msg("register config_reloader collector")
	}
	log.Info().
		Str("daily_loss_halt_pct_baseline", baselineRuntime.DailyLossHaltPct.String()).
		Int("consecutive_losses_halt_baseline", baselineRuntime.ConsecutiveLossesHalt).
		Str("total_float_loss_halt_pct_baseline", baselineRuntime.TotalFloatLossHaltPct.String()).
		Str("btc_panic_drop_pct_baseline", baselineRuntime.BTCCrashHaltPct.String()).
		Str("max_stop_pct_baseline", baselineRuntime.MaxStopPct.String()).
		Int("leverage_baseline", baselineRuntime.Leverage).
		Str("oi_growth_from_min_pct_baseline", baselineRuntime.OiGrowthFromMinPct.String()).
		Str("square_hot_multiplier_baseline", baselineRuntime.SquareHotMultiplier.String()).
		Str("trail_s1_activate_baseline", baselineRuntime.TrailStage1ActivatePct.String()).
		Str("trail_s2_upgrade_baseline", baselineRuntime.TrailStage2UpgradePct.String()).
		Str("trail_s3_upgrade_baseline", baselineRuntime.TrailStage3UpgradePct.String()).
		Str("trail_s4_upgrade_baseline", baselineRuntime.TrailStage4UpgradePct.String()).
		Str("trail_s1_callback_baseline", baselineRuntime.TrailStage1CallbackRate.String()).
		Str("trail_s2_callback_baseline", baselineRuntime.TrailStage2CallbackRate.String()).
		Str("trail_s3_callback_baseline", baselineRuntime.TrailStage3CallbackRate.String()).
		Str("trail_s4_callback_baseline", baselineRuntime.TrailStage4CallbackRate.String()).
		Int("api_error_rate_limit_baseline", baselineRuntime.APIErrorRateLimit).
		Int("wired_keys", 17).
		Msg("config_reloader ready (Phase 5.2 Round 2.w + R.7 F2, 1min cron)")

	// v0.2 Round 1 Module B + Round 1.y: trail_upgrader — 1min sweep (was 5min).
	// Activates S1 fallback, upgrades S1→S2→S3→S4, ratchets S3/S4 stop higher.
	// RatchetMinPct deadband prevents API churn at 1min cadence (default 0.5% high move).
	trailUpgrader := execution.NewTrailUpgrader(gen.New(pgPool), client, symbolService, rdb, execution.TrailConfig{
		Stage1ActivatePct:  cfg.Exit.TrailStage1ActivatePct,
		Stage1CallbackRate: cfg.Exit.TrailStage1CallbackRate,
		Stage2UpgradePct:   cfg.Exit.TrailStage2UpgradePct,
		Stage2CallbackRate: cfg.Exit.TrailStage2CallbackRate,
		Stage3UpgradePct:   cfg.Exit.TrailStage3UpgradePct,
		Stage3CallbackRate: cfg.Exit.TrailStage3CallbackRate,
		Stage4UpgradePct:   cfg.Exit.TrailStage4UpgradePct,
		Stage4CallbackRate: cfg.Exit.TrailStage4CallbackRate,
		RatchetMinPct:      cfg.Exit.TrailRatchetMinPct,
	}, log)
	trailUpgraderCol := collector.NewTrailUpgraderCollector(trailUpgrader, log, collector.TrailUpgraderConfig{})
	if err := runner.Register(trailUpgraderCol, "* * * * *"); err != nil {
		log.Fatal().Err(err).Msg("register trail_upgrader collector")
	}
	log.Info().
		Str("s1_activate", cfg.Exit.TrailStage1ActivatePct.String()).Str("s1_callback", cfg.Exit.TrailStage1CallbackRate.String()).
		Str("s2_upgrade", cfg.Exit.TrailStage2UpgradePct.String()).Str("s2_callback", cfg.Exit.TrailStage2CallbackRate.String()).
		Str("s3_upgrade", cfg.Exit.TrailStage3UpgradePct.String()).Str("s3_callback", cfg.Exit.TrailStage3CallbackRate.String()).
		Str("s4_upgrade", cfg.Exit.TrailStage4UpgradePct.String()).Str("s4_callback", cfg.Exit.TrailStage4CallbackRate.String()).
		Str("ratchet_min", cfg.Exit.TrailRatchetMinPct.String()).
		Str("cron", "1min").
		Msg("trail_upgrader ready (Module B 4-stage, Round 1.y 1min cron)")

	// Phase 4 Round 3: position_manager — 1min sync of open positions against
	// /fapi/v3/positionRisk + Redis zset positions_active + MARGIN_CALL detect.
	positionManager := execution.NewPositionManager(gen.New(pgPool), client, rdb, log)
	// v0.2 Step 5: wire algo_reconciler so the local_only_orphan branch can
	// defensively reconcile FINISHED algos before tripping halt (cron-ordering
	// race window elimination).
	positionManager.SetAlgoReconciler(algoReconciler)
	positionManagerCol := collector.NewPositionManagerCollector(positionManager, log, collector.PositionManagerConfig{})
	if err := runner.Register(positionManagerCol, "*/1 * * * *"); err != nil {
		log.Fatal().Err(err).Msg("register position_manager collector")
	}

	// Phase 4 Round 5: exit_manager — 1min cron evaluates soft/hard timeout
	// + drives close pipeline (cancel Algo + MARKET SELL + DB writes).
	exitManager := execution.NewExitManager(gen.New(pgPool), client, rdb, log).
		WithFeesFetcher(client)
	exitManagerCol := collector.NewExitManagerCollector(exitManager, log, collector.ExitManagerConfig{})
	if err := runner.Register(exitManagerCol, "*/1 * * * *"); err != nil {
		log.Fatal().Err(err).Msg("register exit_manager collector")
	}

	// v0.2 Round 3 Module C: signal_fail_detector — 5min cron evaluates SIGFAIL
	// conditions (OI drop + EMA20 break) and calls ExitManager.ClosePosition on fire.
	// 5min cadence matches OI collector + klines/EMA refresh (finer = stale reads).
	sigfailDetector := execution.NewSigfailDetector(
		gen.New(pgPool), exitManager, rdb,
		execution.SigfailConfig{
			OIDropPct:         cfg.Exit.SigfailOIDropPct,
			EMA20KLines:       cfg.Exit.SigfailEMA20KLines,
			Logic:             cfg.Exit.SigfailLogic,
			LowBreakBufferPct: cfg.Exit.SigfailLowBreakBufferPct,
			LowLookbackMin:    cfg.Exit.SigfailLowLookbackMin,
		}, log)
	sigfailCol := collector.NewSigfailDetectorCollector(sigfailDetector, log, collector.SigfailDetectorConfig{})
	if err := runner.Register(sigfailCol, "*/5 * * * *"); err != nil {
		log.Fatal().Err(err).Msg("register sigfail_detector collector")
	}
	log.Info().
		Str("oi_drop_pct", cfg.Exit.SigfailOIDropPct.String()).
		Int("ema20_k_lines", cfg.Exit.SigfailEMA20KLines).
		Str("low_break_buffer", cfg.Exit.SigfailLowBreakBufferPct.String()).
		Int("low_lookback_min", cfg.Exit.SigfailLowLookbackMin).
		Str("logic", cfg.Exit.SigfailLogic).
		Msg("sigfail_detector ready (Module C, 5min cron, Round 3.x 3 conditions)")

	// Phase 4 Round 6: 5-item circuit breaker tripper (called from decision_engine).
	// mu's decision B (2026-05-11): default thresholds 8% daily / 8 consec / 3% BTC / 12% float / 3 api_err.
	cbCfg := execution.CircuitBreakerConfig{
		DailyLossHaltPct:      cfg.Risk.DailyLossHaltPct,
		ConsecutiveLossCount:  cfg.Risk.ConsecutiveLossHaltCount,
		BTCCrashHaltPct:       cfg.Risk.BTCCrashHaltPct,
		TotalFloatLossHaltPct: cfg.Risk.TotalFloatLossHaltPct,
		APIErrorRateLimit:     cfg.Risk.APIErrorRateLimit,
	}
	log.Info().
		Str("daily_loss_pct", cbCfg.DailyLossHaltPct.String()).
		Int("consec_count", cbCfg.ConsecutiveLossCount).
		Str("btc_crash_pct", cbCfg.BTCCrashHaltPct.String()).
		Str("total_float_pct", cbCfg.TotalFloatLossHaltPct.String()).
		Int("api_err_limit", cbCfg.APIErrorRateLimit).
		Msg("circuit_breaker config")
	circuitBreaker := execution.NewCircuitBreakerTripper(gen.New(pgPool), client, rdb, cbCfg, log)

	// Phase 5.2 Round 4: Feishu alerter. Dry-run when FEISHU_WEBHOOK_URL is unset
	// or FEISHU_ENABLED=false; consumers nil-check so the wire is fully optional.
	feishu := notify.New(notify.Config{
		URL:     cfg.Feishu.WebhookURL,
		Secret:  cfg.Feishu.WebhookSecret,
		Enabled: cfg.Feishu.Enabled,
	}, log)
	circuitBreaker.SetNotifier(feishu)
	positionManager.SetNotifier(feishu)
	executor.SetNotifier(feishu)
	log.Info().
		Bool("enabled", cfg.Feishu.Enabled).
		Bool("has_url", cfg.Feishu.WebhookURL != "").
		Bool("has_secret", cfg.Feishu.WebhookSecret != "").
		Msg("notify.feishu ready (dry-run when enabled=false or url empty)")

	// Daily report cron — BJT 00:00. cron.WithLocation(timez.BJT) so this fires
	// at midnight Beijing regardless of host TZ.
	dailyReport := collector.NewDailyReportCollector(pgPool, client, feishu, log, collector.DailyReportConfig{})
	if err := runner.Register(dailyReport, "0 0 * * *"); err != nil {
		log.Fatal().Err(err).Msg("register daily_report collector")
	}

	// R.14: price-mark watcher — */1 polls active marks, flips on hit + 🟡 Feishu.
	priceMarkCol := collector.NewPriceMarkCollector(pgPool, client, feishu, log, collector.PriceMarkConfig{})
	if err := runner.Register(priceMarkCol, "* * * * *"); err != nil {
		log.Fatal().Err(err).Msg("register price_mark collector")
	}

	// Phase 4 Round 7: orchestrated startup recovery. Order:
	//  1. RecoverEnteringTrades (Round 2): clean stuck 'entering' via Binance lookup.
	//  2. positionManager.SyncTick: immediate reconcile (no 1min wait).
	//  3. exitManager.EvaluateTick: retry closing-state trades + due timeouts.
	//  4. circuitBreaker.EvaluateAll: re-evaluate halt — catches yesterday's
	//     daily_pnl / consec_losses state that survived the restart.
	// All best-effort; failures logged, do not block startup.
	startupCtx, startupCancel := context.WithTimeout(ctx, 60*time.Second)
	report := execution.RunStartupRecovery(startupCtx,
		gen.New(pgPool), client,
		positionManager.SyncTick,
		exitManager.EvaluateTick,
		circuitBreaker.EvaluateAll,
		log,
	)
	startupCancel()
	metrics.RestartRecoveryRunsTotal.WithLabelValues(restartResult(report)).Inc()

	// Phase 3 v0.1 + Round R.1 fix: decision_engine — 5min cron, reads entered_* signals,
	// runs filters + sizing → trades.entering. Phase 4: fires executor.PlaceEntry.
	//
	// Round R.1 bug fix: sizing config was using SizingConfig defaults (Leverage=10)
	// because the EngineConfig.Sizing was never wired from cfg.Position. This caused
	// sizing to compute qty for 10x while executor.SetLeverage set Binance to 5x,
	// resulting in user-visible margin = notional / 5 = $50 instead of $25 (mu catch
	// 2026-05-13 16:05 INJUSDT entry).
	decisionEngineCol := collector.NewDecisionEngineCollector(
		gen.New(pgPool), rdb, symbolService, executor, circuitBreaker, log, collector.DecisionEngineConfig{
			PerTickTimeout: 4 * time.Minute,
			EngineCfg: decision.EngineConfig{
				Sizing: decision.SizingConfig{
					FullMarginUSDT: cfg.Position.MarginPerTradeFull,
					HalfMarginUSDT: cfg.Position.MarginPerTradeHalf,
					Leverage:       int32(cfg.Position.Leverage),
				},
			},
		},
	)
	if err := runner.Register(decisionEngineCol, "*/5 * * * *"); err != nil {
		log.Fatal().Err(err).Msg("register decision_engine collector")
	}
	runner.Start()

	// v0.2 Round 4: User Data Stream — WS-based wakeup signal for FINISHED algos
	// + MARGIN_CALL + ACCOUNT_UPDATE. Existing 1min crons remain as defense-in-depth.
	// Run in supervised goroutine; reconnects on any error with exponential backoff.
	userStream := execution.NewUserStream(client, execution.UserStreamCallbacks{
		OnOrderFilled: func(ctx context.Context, sym string, orderID int64) {
			// Wake algo_reconciler immediately rather than wait for next 1min tick.
			algoReconciler.ReconcileTick(ctx)
		},
		OnAccountUpd: func(ctx context.Context) {
			// position_manager.SyncTick is the canonical sync path; wake it.
			// (Not strictly needed for correctness — 1min cron catches all — but
			// reduces position_states staleness from 60s → near-instant.)
		},
		OnMarginCall: func(ctx context.Context, sym string) {
			// Margin call is the rarest + most urgent path; wake algo_reconciler
			// + position_manager so the orphan/disaster paths trigger immediately.
			algoReconciler.ReconcileTick(ctx)
		},
	}, log)
	go func() {
		if err := userStream.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error().Err(err).Msg("user_stream: Run exited unexpectedly")
		}
	}()
	log.Info().Str("ws_base", client.WSBase()).Msg("user_stream ready (Round 4 WS wakeup signal)")

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
