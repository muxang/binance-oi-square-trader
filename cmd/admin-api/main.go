package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"trader/internal/admin"
)

func main() {
	log.Logger = zerolog.New(os.Stdout).With().Timestamp().Logger()

	dbURL := mustEnv("DATABASE_URL")
	redisURL := mustEnv("REDIS_URL")
	prometheusURL := envOr("PROMETHEUS_URL", "http://trader-prometheus:9090")
	addr := envOr("ADMIN_API_ADDR", ":3002")

	ctx := context.Background()

	pgCfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		log.Fatal().Err(err).Msg("parse db url")
	}
	pgCfg.MaxConns = 5
	pgCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET default_transaction_read_only = on")
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, pgCfg)
	if err != nil {
		log.Fatal().Err(err).Msg("connect db")
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatal().Err(err).Msg("ping db")
	}
	log.Info().Msg("DB connected (read-only, max_conns=5)")

	// v0.2 Round R.1 Part 2 (Phase 5.2 admin v1.1 first write op): separate
	// writable pool with tight max_conns=2 for the few write endpoints
	// (manual halt reset; future: manual close, threshold updates).
	// Kept distinct from the read pool so any write bug can't starve reads.
	writeCfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		log.Fatal().Err(err).Msg("parse db url (write)")
	}
	writeCfg.MaxConns = 2
	writePool, err := pgxpool.NewWithConfig(ctx, writeCfg)
	if err != nil {
		log.Fatal().Err(err).Msg("connect db (write)")
	}
	defer writePool.Close()
	if err := writePool.Ping(ctx); err != nil {
		log.Fatal().Err(err).Msg("ping db (write)")
	}
	log.Info().Msg("DB write pool connected (max_conns=2)")

	redisOpts, err := redis.ParseURL(redisURL)
	if err != nil {
		log.Fatal().Err(err).Msg("parse redis url")
	}
	rdb := redis.NewClient(redisOpts)
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Warn().Err(err).Msg("redis ping failed (non-fatal)")
	}

	srv := admin.NewServer(pool, writePool, rdb, prometheusURL, log.Logger)
	httpSrv := &http.Server{
		Addr:         addr,
		Handler:      srv.Routes(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	go func() {
		log.Info().Str("addr", addr).Msg("admin-api listening")
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("http serve")
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutCtx); err != nil {
		log.Error().Err(err).Msg("shutdown")
	}
	log.Info().Msg("admin-api stopped")
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fmt.Fprintf(os.Stderr, "ERROR: %s is required\n", key)
		os.Exit(1)
	}
	return v
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
