// v0.2 Round 4: User Data Stream — WS-based real-time event source.
//
// Design: WS acts as a WAKEUP signal generator, not a replacement for cron.
//   · ORDER_TRADE_UPDATE FILLED (SELL reduceOnly) → algo_reconciler.ReconcileTick()
//   · ACCOUNT_UPDATE                              → position_manager.SyncTick()
//   · MARGIN_CALL                                 → log warn + algo_reconciler tick
//   · listenKeyExpired                            → disconnect (outer loop reconnects)
//
// Existing 1min crons (algo_polling / position_sync / exit_manager / trail_upgrader)
// remain as defense-in-depth — WS misses an event → cron catches it within 60s.
// InsertTradeExitIdempotent prevents double-close from WS + cron races.
//
// Connection lifecycle:
//   1. POST listenKey → 60min TTL
//   2. WS dial {WSBase}/ws/{listenKey}
//   3. Goroutine: read events loop
//   4. Goroutine: keepalive PUT every 30min
//   5. On error / context cancel: close conn + DELETE listenKey, return
//   6. Outer Run() loop: exponential backoff reconnect (1s → 60s cap)
//
// ref: references/binance/urls.md §「User Data Streams」
// ref: docs/V0_2_TRADER_DESIGN.md §5
package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"

	"trader/internal/binance"
	"trader/internal/pkg/metrics"
	"trader/internal/pkg/timez"
)

const (
	listenKeyKeepaliveInterval = 30 * time.Minute
	wsReadDeadline             = 5 * time.Minute // larger than Binance ping interval (~3min)
	wsBackoffInitial           = 1 * time.Second
	wsBackoffMax               = 60 * time.Second
)

// UserStreamCallbacks routes WS events to existing collectors.
// Each callback runs in the WS read goroutine — keep handlers fast OR fork.
type UserStreamCallbacks struct {
	OnOrderFilled  func(ctx context.Context, sym string, orderID int64) // SELL reduceOnly FILLED → reconcile
	OnAccountUpd   func(ctx context.Context)                            // ACCOUNT_UPDATE → position sync
	OnMarginCall   func(ctx context.Context, sym string)                // MARGIN_CALL → emergency
}

// UserStream owns the listenKey + WS lifecycle.
type UserStream struct {
	bc        *binance.Client
	callbacks UserStreamCallbacks
	log       zerolog.Logger
	nowFn     func() time.Time

	mu        sync.Mutex
	listenKey string
	conn      *websocket.Conn
	closed    bool
}

func NewUserStream(bc *binance.Client, cb UserStreamCallbacks, log zerolog.Logger) *UserStream {
	return &UserStream{bc: bc, callbacks: cb, log: log, nowFn: timez.NowUTC}
}

// Run is the supervised long-running goroutine entry point. Returns only when
// ctx is cancelled. On any error, it backs off and reconnects.
//
// Wire via errgroup in main:
//
//	eg.Go(func() error { return userStream.Run(ctx) })
func (us *UserStream) Run(ctx context.Context) error {
	backoff := wsBackoffInitial
	for {
		if err := ctx.Err(); err != nil {
			us.shutdown()
			return err
		}
		err := us.session(ctx)
		us.shutdown()
		if err != nil && !errors.Is(err, context.Canceled) {
			us.log.Warn().Err(err).Dur("backoff", backoff).Msg("user_stream: session ended, reconnecting")
			metrics.UserStreamReconnectTotal.Inc()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > wsBackoffMax {
			backoff = wsBackoffMax
		}
	}
}

// session runs one connection attempt: create listenKey → dial WS → read loop.
// Returns nil on graceful disconnect (e.g. listenKeyExpired triggers outer reconnect).
func (us *UserStream) session(ctx context.Context) error {
	lk, err := us.bc.CreateListenKey(ctx)
	if err != nil {
		return fmt.Errorf("create listen key: %w", err)
	}
	dialer, proxyURL, err := us.bc.WSDialer(ctx)
	if err != nil {
		return fmt.Errorf("ws dialer: %w", err)
	}
	wsURL := us.bc.WSBase() + "/ws/" + lk
	us.log.Info().Str("ws_url_host", us.bc.WSBase()).Str("proxy", proxyURL).
		Msg("user_stream: dialing WS")
	conn, _, err := dialer.DialContext(ctx, wsURL, http.Header{})
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}
	us.setConn(lk, conn)
	defer us.closeConn()

	metrics.UserStreamConnectedTotal.Inc()
	us.log.Info().Msg("user_stream: connected")

	// Goroutine 1: keepalive every 30min.
	keepaliveCtx, cancelKeepalive := context.WithCancel(ctx)
	defer cancelKeepalive()
	go us.keepaliveLoop(keepaliveCtx)

	// Goroutine 2 (this): read loop. Returns on error or ctx cancel.
	return us.readLoop(ctx)
}

func (us *UserStream) keepaliveLoop(ctx context.Context) {
	ticker := time.NewTicker(listenKeyKeepaliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := us.bc.KeepaliveListenKey(ctx); err != nil {
				us.log.Warn().Err(err).Msg("user_stream: keepalive failed (next read will likely fail → reconnect)")
				metrics.UserStreamKeepaliveErrors.Inc()
			} else {
				us.log.Debug().Msg("user_stream: keepalive ok")
			}
		}
	}
}

func (us *UserStream) readLoop(ctx context.Context) error {
	for {
		if err := us.conn.SetReadDeadline(us.nowFn().Add(wsReadDeadline)); err != nil {
			return fmt.Errorf("set read deadline: %w", err)
		}
		_, msg, err := us.conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("ws read: %w", err)
		}
		metrics.UserStreamEventsTotal.WithLabelValues("raw").Inc()
		us.dispatch(ctx, msg)
	}
}

// dispatch parses the event envelope and routes to the right callback.
func (us *UserStream) dispatch(ctx context.Context, msg []byte) {
	var env struct {
		EventType string `json:"e"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		us.log.Warn().Err(err).Bytes("msg", trimMsg(msg)).Msg("user_stream: dispatch parse failed")
		return
	}
	switch env.EventType {
	case "ORDER_TRADE_UPDATE":
		us.handleOrderUpdate(ctx, msg)
	case "ACCOUNT_UPDATE":
		us.handleAccountUpdate(ctx, msg)
	case "MARGIN_CALL":
		us.handleMarginCall(ctx, msg)
	case "listenKeyExpired":
		us.log.Warn().Msg("user_stream: listenKey expired (server-side); will reconnect")
		// readLoop will return error on next read; outer Run reconnects.
		_ = us.conn.Close()
	default:
		us.log.Debug().Str("event_type", env.EventType).Msg("user_stream: event ignored")
	}
	metrics.UserStreamEventsTotal.WithLabelValues(env.EventType).Inc()
}

// orderUpdateMsg is the ORDER_TRADE_UPDATE 'o' field subset we care about.
//
// ref: https://developers.binance.com/docs/derivatives/usds-margined-futures/user-data-streams/Event-Order-Update
type orderUpdateMsg struct {
	Order struct {
		Symbol     string `json:"s"`
		ClientOID  string `json:"c"`
		Side       string `json:"S"`
		Type       string `json:"o"` // MARKET / TRAILING_STOP_MARKET / TAKE_PROFIT_MARKET / etc.
		Status     string `json:"X"` // NEW / FILLED / CANCELED / etc.
		OrderID    int64  `json:"i"`
		ExecType   string `json:"x"` // TRADE / EXPIRED / CANCELED / etc.
	} `json:"o"`
}

func (us *UserStream) handleOrderUpdate(ctx context.Context, msg []byte) {
	var p orderUpdateMsg
	if err := json.Unmarshal(msg, &p); err != nil {
		us.log.Warn().Err(err).Msg("user_stream: order update parse failed")
		return
	}
	// Only act on FILLED SELL orders — these are exit fills (TP/trail/disaster/manual).
	// BUY FILLED is entry which executor handles synchronously.
	if p.Order.Status != "FILLED" || p.Order.Side != "SELL" {
		return
	}
	us.log.Info().
		Str("symbol", p.Order.Symbol).
		Int64("order_id", p.Order.OrderID).
		Str("type", p.Order.Type).
		Str("client_oid", p.Order.ClientOID).
		Msg("user_stream: SELL FILLED → wake algo_reconciler")
	if us.callbacks.OnOrderFilled != nil {
		us.callbacks.OnOrderFilled(ctx, p.Order.Symbol, p.Order.OrderID)
	}
}

func (us *UserStream) handleAccountUpdate(ctx context.Context, msg []byte) {
	us.log.Debug().Int("bytes", len(msg)).Msg("user_stream: ACCOUNT_UPDATE → wake position_sync")
	if us.callbacks.OnAccountUpd != nil {
		us.callbacks.OnAccountUpd(ctx)
	}
}

type marginCallMsg struct {
	Positions []struct {
		Symbol string `json:"s"`
	} `json:"p"`
}

func (us *UserStream) handleMarginCall(ctx context.Context, msg []byte) {
	var p marginCallMsg
	if err := json.Unmarshal(msg, &p); err != nil {
		us.log.Warn().Err(err).Msg("user_stream: margin call parse failed")
		return
	}
	for _, pos := range p.Positions {
		us.log.Error().Str("symbol", pos.Symbol).Msg("user_stream: MARGIN_CALL")
		if us.callbacks.OnMarginCall != nil {
			us.callbacks.OnMarginCall(ctx, pos.Symbol)
		}
		metrics.MarginCallTriggeredTotal.WithLabelValues(pos.Symbol).Inc()
	}
}

func (us *UserStream) setConn(lk string, conn *websocket.Conn) {
	us.mu.Lock()
	defer us.mu.Unlock()
	us.listenKey = lk
	us.conn = conn
}

func (us *UserStream) closeConn() {
	us.mu.Lock()
	c := us.conn
	us.conn = nil
	us.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

// shutdown attempts a graceful DELETE listenKey (best-effort, short timeout).
func (us *UserStream) shutdown() {
	us.mu.Lock()
	lk := us.listenKey
	us.listenKey = ""
	us.mu.Unlock()
	if lk == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = us.bc.CloseListenKey(ctx)
}

// trimMsg shortens a payload to <=200 bytes for logging.
func trimMsg(b []byte) []byte {
	if len(b) > 200 {
		return b[:200]
	}
	return b
}
