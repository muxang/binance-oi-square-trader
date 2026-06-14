// R.30 market data WebSocket stream.
//
// Subscribes to Binance Futures `!ticker@arr` — every 1s, an array of 24hr
// ticker objects for every symbol that changed in that window. We extract
// `s` (symbol), `c` (last close), `P` (24h price change pct) and write to
// Redis under `latest_price:<sym>` and `price_24h_pct:<sym>` with 30s TTL.
//
// Replaces the REST-polling path (5min cron) for "市场扫描" page display
// fields. Indicators / ATR / SIGFAIL still read PG klines (5m bars), so the
// money-handling logic in CLAUDE.md §2 modules is untouched.
//
// Connection lifecycle mirrors user_stream.go:
//   1. WS dial {WSBase}/ws/!ticker@arr via proxy
//   2. Read loop with 5min read deadline
//   3. On error / ctx cancel: close, exponential backoff (1s → 60s cap)
//
// ref: https://developers.binance.com/docs/derivatives/usds-margined-futures/websocket-market-streams/All-Market-Tickers-Stream
package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"trader/internal/binance"
	"trader/internal/pkg/metrics"
	"trader/internal/pkg/timez"
)

const (
	marketStreamReadDeadline = 5 * time.Minute
	marketStreamBackoffInit  = 1 * time.Second
	marketStreamBackoffMax   = 60 * time.Second
	// Wider than the ~1s push interval so a brief disconnect doesn't blank
	// the price cache; downstream falls back to PG klines if TTL expires.
	marketStreamPriceTTL = 30 * time.Second
)

// tickerEvent matches the per-symbol fields we use from !ticker@arr.
//
// IMPORTANT — Go json case-insensitive collision trap:
//   The stream uses fields whose names differ only in case (e/E, p/P, q/Q, etc.).
//   Go's encoding/json falls back to case-insensitive matching when no exact
//   match is found. Without DECLARING both halves of each pair, the wrong
//   value lands on the wrong field (e.g. event time `E` (number) gets stuffed
//   into a `e`-tagged string field → "cannot unmarshal number into string").
//
//   Defense: declare BOTH casings of every pair we care about, so the exact
//   matcher always wins. Fields we don't use are tagged with `-` if needed,
//   or left as ignored unused fields (Go json discards unknown fields).
//
// ref: https://developers.binance.com/docs/derivatives/usds-margined-futures/websocket-market-streams/Individual-Symbol-Ticker-Streams
type tickerEvent struct {
	// e/E: type/time — declared both halves so the exact matcher binds each
	// and Go's case-insensitive fallback never collides with our real fields.
	EventTypeLower string `json:"e"` // "24hrTicker"
	EventTimeMs    int64  `json:"E"` // event time ms

	Symbol      string `json:"s"`
	SymbolUpper string `json:"S"` // unused, declared to absorb the upper-case key

	PriceChangeDec string `json:"p"` // 24h price change (decimal) — unused
	PriceChangePct string `json:"P"` // 24h price change percent — used

	QuoteVolume string `json:"q"` // unused
	LastTradeQ  string `json:"Q"` // unused

	ClosePrice string `json:"c"` // last price — used
	CloseTime  int64  `json:"C"` // unused (event close time)
}

// MarketStream owns the WS lifecycle. One stream per trader process suffices —
// !ticker@arr is global (all symbols).
type MarketStream struct {
	bc  *binance.Client
	rdb *redis.Client
	log zerolog.Logger

	mu           sync.Mutex
	conn         *websocket.Conn
	sampleLogged atomic.Bool // one-shot diagnostic: prints first parsed batch shape
}

func NewMarketStream(bc *binance.Client, rdb *redis.Client, log zerolog.Logger) *MarketStream {
	return &MarketStream{bc: bc, rdb: rdb, log: log}
}

// Run is the supervised entry point. Wire via errgroup.Go in main.
func (ms *MarketStream) Run(ctx context.Context) error {
	backoff := marketStreamBackoffInit
	for {
		if err := ctx.Err(); err != nil {
			ms.closeConn()
			return err
		}
		err := ms.session(ctx)
		ms.closeConn()
		if err != nil && !errors.Is(err, context.Canceled) {
			ms.log.Warn().Err(err).Dur("backoff", backoff).Msg("market_stream: session ended, reconnecting")
			metrics.MarketStreamReconnectTotal.Inc()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > marketStreamBackoffMax {
			backoff = marketStreamBackoffMax
		}
	}
}

// session opens one WS connection and reads until error / ctx cancel.
func (ms *MarketStream) session(ctx context.Context) error {
	dialer, proxyURL, err := ms.bc.WSDialer(ctx)
	if err != nil {
		return fmt.Errorf("ws dialer: %w", err)
	}
	wsURL := ms.bc.WSBase() + "/ws/!ticker@arr"
	ms.log.Info().Str("ws_url_host", ms.bc.WSBase()).Str("proxy", proxyURL).
		Msg("market_stream: dialing !ticker@arr")
	conn, _, err := dialer.DialContext(ctx, wsURL, http.Header{})
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}
	ms.setConn(conn)
	metrics.MarketStreamConnected.Set(1)
	defer metrics.MarketStreamConnected.Set(0)
	ms.log.Info().Msg("market_stream: connected")

	// Reset backoff on successful connect (caller handles outer backoff loop).
	for {
		if err := conn.SetReadDeadline(timez.NowUTC().Add(marketStreamReadDeadline)); err != nil {
			return fmt.Errorf("set read deadline: %w", err)
		}
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("ws read: %w", err)
		}
		ms.dispatch(ctx, msg)
	}
}

// dispatch parses one WS frame and pipelines Redis writes.
func (ms *MarketStream) dispatch(ctx context.Context, msg []byte) {
	metrics.MarketStreamMessagesTotal.Inc()

	// !ticker@arr pushes a bare JSON array.
	var events []tickerEvent
	parseErr := json.Unmarshal(msg, &events)
	if parseErr != nil || len(events) == 0 {
		// Combined-stream variant wraps in { stream, data }.
		var envelope struct {
			Data []tickerEvent `json:"data"`
		}
		if err2 := json.Unmarshal(msg, &envelope); err2 != nil {
			ms.log.Warn().Err(parseErr).AnErr("err2", err2).
				Bytes("msg_head", trimMsg(msg)).
				Msg("market_stream: parse failed both formats")
			return
		}
		events = envelope.Data
	}
	if len(events) == 0 {
		ms.log.Warn().Bytes("msg_head", trimMsg(msg)).Msg("market_stream: zero events parsed (check field tags)")
		return
	}
	// One-shot sample log for first batch — confirms format.
	if ms.sampleLogged.CompareAndSwap(false, true) {
		ms.log.Info().Int("events", len(events)).
			Str("first_symbol", events[0].Symbol).
			Str("first_close", events[0].ClosePrice).
			Str("first_24h_pct", events[0].PriceChangePct).
			Bytes("first_msg_head", trimMsg(msg)).
			Msg("market_stream: first batch parsed")
	}

	pipe := ms.rdb.Pipeline()
	written := 0
	for _, e := range events {
		if e.Symbol == "" {
			continue
		}
		if e.ClosePrice != "" {
			pipe.Set(ctx, "latest_price:"+e.Symbol, e.ClosePrice, marketStreamPriceTTL)
		}
		if e.PriceChangePct != "" {
			pipe.Set(ctx, "price_24h_pct:"+e.Symbol, e.PriceChangePct, marketStreamPriceTTL)
		}
		written++
	}
	if _, err := pipe.Exec(ctx); err != nil {
		ms.log.Debug().Err(err).Msg("market_stream: redis pipeline failed (non-fatal, next tick resyncs)")
		return
	}
	metrics.MarketStreamSymbolsUpdated.Add(float64(written))
}

func (ms *MarketStream) setConn(conn *websocket.Conn) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.conn = conn
}

func (ms *MarketStream) closeConn() {
	ms.mu.Lock()
	c := ms.conn
	ms.conn = nil
	ms.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}
