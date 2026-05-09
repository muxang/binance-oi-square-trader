// Package handlers contains HTTP request handlers for the Dashboard API.
package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"trader/internal/pkg/timez"
)

// HealthDeps wires the health endpoint's runtime probes. Ping closures keep
// this package free of pgx / redis imports — main.go adapts the concrete
// drivers into func(ctx) error here.
type HealthDeps struct {
	PingPG    func(ctx context.Context) error
	PingRedis func(ctx context.Context) error
	Version   string
	Mode      string
	StartTime time.Time
}

// Health serves GET /health.
//
// Contract: the endpoint ALWAYS returns HTTP 200 (Docker / K8s liveness must
// not flap on transient dep failures). Per-dependency status appears in the
// JSON body's "deps" map as "ok" / "fail" / "not_configured" / "not_checked".
type Health struct{ deps HealthDeps }

func NewHealth(deps HealthDeps) *Health { return &Health{deps: deps} }

func (h *Health) Handle(c echo.Context) error {
	const pingTimeout = time.Second
	parent := c.Request().Context()
	return c.JSON(http.StatusOK, map[string]any{
		"status":  "ok",
		"mode":    h.deps.Mode,
		"version": h.deps.Version,
		"uptime":  timez.NowUTC().Sub(h.deps.StartTime).String(),
		"deps": map[string]string{
			"pg":                pingStatus(parent, pingTimeout, h.deps.PingPG),
			"redis":             pingStatus(parent, pingTimeout, h.deps.PingRedis),
			"binance_reachable": "not_checked", // Phase 1+ wires a real probe
		},
	})
}

func pingStatus(parent context.Context, timeout time.Duration, ping func(context.Context) error) string {
	if ping == nil {
		return "not_configured"
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	if err := ping(ctx); err != nil {
		return "fail"
	}
	return "ok"
}
