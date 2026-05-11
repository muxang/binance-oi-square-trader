package collector

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trader/internal/decision"
	"trader/internal/storage/postgres/gen"
)

// --- 1 unit: symbolClass bucketing (no DB needed) ---

func TestDecisionEngine_SymbolClass(t *testing.T) {
	cases := []struct {
		price decimal.Decimal
		want  string
	}{
		{decimal.NewFromInt(80000), "high_price"}, // BTC
		{decimal.NewFromInt(2500), "high_price"},  // ETH
		{decimal.NewFromInt(150), "mid_price"},    // SOL
		{decimal.NewFromInt(1), "mid_price"},      // boundary low
		{decimal.NewFromFloat(0.999), "low_price"},
		{decimal.NewFromFloat(0.00001), "low_price"}, // PEPE
	}
	for _, c := range cases {
		assert.Equal(t, c.want, symbolClass(c.price), "price=%s", c.price)
	}
}

// --- 1 unit: adapter constructor wires deps correctly ---

func TestDecisionEngine_NewCollector_NameAndDeps(t *testing.T) {
	c := NewDecisionEngineCollector(nil, nil, nil, nil, nil, zerolog.Nop(), DecisionEngineConfig{})
	assert.Equal(t, "decision_engine", c.Name())
	assert.NotNil(t, c.deps, "adapter wired")
}

// --- 2 integration tests (opt-in INTEGRATION_PG=1, BEGIN/ROLLBACK) ---

func TestDecisionEngineAdapter_GetLatestClose_FromKlines(t *testing.T) {
	if os.Getenv("INTEGRATION_PG") == "" {
		t.Skip("set INTEGRATION_PG=1 to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := openIntegrationPGForDecision(t, ctx)
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()

	q := gen.New(tx)
	const sym = "DECCLOSETEST"
	now := time.Now().UTC().Truncate(15 * time.Minute)
	_, err = tx.Exec(ctx,
		"INSERT INTO klines (symbol, timeframe, open_time, open, high, low, close, volume, quote_volume) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)",
		sym, "15m", now, decimal.NewFromInt(80000), decimal.NewFromInt(81000), decimal.NewFromInt(79000), decimal.NewFromInt(80500),
		decimal.NewFromInt(100), decimal.NewFromInt(50000))
	require.NoError(t, err)

	a := &decisionDataAccess{queries: q, log: zerolog.Nop()}
	close, err := a.GetLatestClose(ctx, sym)
	require.NoError(t, err)
	assert.True(t, close.Equal(decimal.NewFromInt(80500)), "got %s", close)
}

func TestDecisionEngineAdapter_FullChain_RoundTrip(t *testing.T) {
	if os.Getenv("INTEGRATION_PG") == "" {
		t.Skip("set INTEGRATION_PG=1 to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := openIntegrationPGForDecision(t, ctx)
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()

	q := gen.New(tx)
	a := &decisionDataAccess{queries: q, log: zerolog.Nop()}

	// 1. Insert a signal (need parent for trades.signal_id FK)
	const sym = "FULLCHAINTEST"
	var signalID int64
	err = tx.QueryRow(ctx,
		"INSERT INTO signals (ts, symbol, oi_triggered, oi_data, square_hot, square_data, decision) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id",
		time.Now().UTC(), sym, true, []byte(`{"triggered":true}`), false, []byte(`{"hot":false}`), "entered_full",
	).Scan(&signalID)
	require.NoError(t, err)

	// 2. Insert via adapter (the SUT path)
	tradeID, err := a.InsertEnteringTrade(ctx, signalID, sym, "LONG", fmt.Sprintf("t%d_r0", signalID), decimal.NewFromInt(50), decimal.NewFromInt(480), 10)
	require.NoError(t, err)
	assert.Greater(t, tradeID, int64(0))

	// 3. Verify CountActive (entering counts)
	count, err := a.CountActive(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, count, int64(1), "trade just inserted should count")

	// 4. Verify HasRecent24hAttempt (signals.ts JOIN path)
	cutoff := time.Now().UTC().Add(-1 * time.Hour)
	has, err := a.HasRecent24hAttempt(ctx, sym, cutoff)
	require.NoError(t, err)
	assert.True(t, has, "trade just inserted with signal.ts in window → has recent")

	// 5. Verify GetState reads (defensive: row should exist from migration)
	state, err := a.GetState(ctx)
	require.NoError(t, err)
	assert.Equal(t, int16(1), state.ID)
}

// --- helpers ---

func openIntegrationPGForDecision(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("INTEGRATION_PG_DSN")
	if dsn == "" {
		dsn = "postgres://trader:trader@127.0.0.1:5432/trader?sslmode=disable"
	}
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	require.NoError(t, pool.Ping(ctx))
	return pool
}

// Avoid unused import warnings; reference exported types lightly.
var (
	_ decision.EngineDeps            = (*decisionDataAccess)(nil) // adapter implements interface
	_                                = pgtype.Timestamptz{}       // referenced in adapter
	_                                = redis.Nil                  // referenced in adapter
	_ gen.GetRecentEnteredSignalsRow                              // referenced
)
