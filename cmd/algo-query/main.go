// One-shot tool: dump raw Binance algoOrder JSON for a given algoId.
// Bypasses the unmarshal in binance.QueryAlgoOrder which drops fields
// like activationPrice / callbackRate.
//
// Usage (inside trader container so .env + proxy reach Binance):
//   /app/algo-query 1000001637727079

package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/rs/zerolog"

	"trader/internal/binance"
	"trader/internal/config"
	"trader/internal/pkg/ratelimit"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: algo-query <algoId>")
		os.Exit(2)
	}
	algoID, err := strconv.ParseInt(os.Args[1], 10, 64)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid algoId:", err)
		os.Exit(2)
	}

	log := zerolog.New(os.Stderr).With().Timestamp().Logger()
	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("config load")
	}
	pm, err := binance.NewProxyManager(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("proxy")
	}
	limiter := ratelimit.NewTokenBucket(1920, 32)
	c, err := binance.New(cfg, pm, limiter, log)
	if err != nil {
		log.Fatal().Err(err).Msg("binance client")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	params := url.Values{}
	params.Set("algoId", strconv.FormatInt(algoID, 10))
	body, err := c.DoReadAccount(ctx, "/fapi/v1/algoOrder", params, 1)
	if err != nil {
		log.Fatal().Err(err).Msg("doread")
	}
	// Print raw JSON to stdout.
	fmt.Println(string(body))
}
