package gen

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// TestQueriesIntegration_InsertSignal_RoundTrip verifies sqlc-generated
// InsertSignal works against the real schema. Opt-in via INTEGRATION_PG=1
// (deploy/docker-compose.yml must have postgres up). Uses BEGIN/ROLLBACK
// so prod data is untouched.
//
// Catches: schema vs query mismatch (e.g. column rename, type change) early
// — Round 5 won't hit "InsertSignal panics with malformed param" mid-longrun.
func TestQueriesIntegration_InsertSignal_RoundTrip(t *testing.T) {
	if os.Getenv("INTEGRATION_PG") == "" {
		t.Skip("set INTEGRATION_PG=1 (with deploy/docker-compose postgres up) to run")
	}
	dsn := os.Getenv("INTEGRATION_PG_DSN")
	if dsn == "" {
		dsn = "postgres://trader:trader@127.0.0.1:5432/trader?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, pool.Ping(ctx))

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()

	q := New(tx)
	const testSymbol = "INTEGRATIONTESTUSDT"
	err = q.InsertSignal(ctx, InsertSignalParams{
		Ts:              time.Now().UTC(),
		Symbol:          testSymbol,
		OiTriggered:     true,
		OiData:          []byte(`{"triggered":true,"growth_from_min":"0.4444"}`),
		SquareHot:       true,
		SquareData:      []byte(`{"hot":true,"mode":"standard","ratio":"2.5"}`),
		Decision:        "entered_full",
		RejectionReason: pgtype.Text{Valid: false},
	})
	require.NoError(t, err, "InsertSignal must succeed against real schema")

	// Verify row visible in tx
	var count int
	err = tx.QueryRow(ctx, "SELECT COUNT(*) FROM signals WHERE symbol = $1", testSymbol).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count, "1 row inserted in tx, rollback will remove it")
}
