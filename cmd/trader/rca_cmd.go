// Phase 4 Round 4: halt RCA cmd-line subcommands.
//
// Usage:
//
//	./trader rca-list                    # list un-acked halt_rca rows
//	./trader rca-ack <id> <action>       # mark a row acknowledged
//	                                     # action ∈ resolved | investigating | ignore
//
// v0.1 reads DSN from env (TRADER_DB_DSN or DATABASE_URL fallback) — same
// pattern as the trader daemon. v0.2 / Phase 5 will replace this with a
// Feishu integration where mu can react to halt notifications.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"trader/internal/storage/postgres/gen"
)

const rcaActionResolved = "resolved"

func runRCACommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: ./trader rca-list | rca-ack <id> <action>")
	}
	dsn := os.Getenv("TRADER_DB_DSN")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		return fmt.Errorf("missing TRADER_DB_DSN or DATABASE_URL env")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("pgxpool.New: %w", err)
	}
	defer pool.Close()
	q := gen.New(pool)

	switch args[0] {
	case "rca-list":
		return rcaList(ctx, q)
	case "rca-ack":
		if len(args) < 3 {
			return fmt.Errorf("usage: ./trader rca-ack <id> <action>")
		}
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid id %q: %w", args[1], err)
		}
		action := args[2]
		if action != rcaActionResolved && action != "investigating" && action != "ignore" {
			return fmt.Errorf("action must be one of: resolved | investigating | ignore")
		}
		return rcaAck(ctx, q, id, action)
	}
	return fmt.Errorf("unknown subcommand: %s", args[0])
}

func rcaList(ctx context.Context, q *gen.Queries) error {
	rows, err := q.ListUnacknowledgedHaltRCA(ctx)
	if err != nil {
		return fmt.Errorf("list: %w", err)
	}
	if len(rows) == 0 {
		fmt.Println("no unacknowledged halt RCA records.")
		return nil
	}
	fmt.Printf("%-6s %-26s %-30s %s\n", "ID", "TRIGGERED_AT", "HALT_TYPE", "CONTEXT")
	for _, r := range rows {
		// Pretty-print context json with indent for terminal readability.
		var pretty any
		_ = json.Unmarshal(r.ContextJson, &pretty)
		compact, _ := json.Marshal(pretty)
		fmt.Printf("%-6d %-26s %-30s %s\n",
			r.ID, r.TriggeredAt.Format(time.RFC3339), r.HaltType, string(compact))
	}
	return nil
}

func rcaAck(ctx context.Context, q *gen.Queries, id int64, action string) error {
	if err := q.AcknowledgeHaltRCA(ctx, gen.AcknowledgeHaltRCAParams{
		ID:       id,
		MuAction: pgtype.Text{String: action, Valid: true},
	}); err != nil {
		return fmt.Errorf("ack: %w", err)
	}
	fmt.Printf("halt_rca id=%d acknowledged action=%s\n", id, action)
	return nil
}
