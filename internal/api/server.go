package api

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"trader/internal/api/handlers"
)

// Deps re-exports HealthDeps for callers wiring the server in main.go.
type Deps = handlers.HealthDeps

// New builds the Echo server: hide the framework's banner and port log (we own
// startup logging via logger.StartupBanner), install only Recover middleware
// (no CORS / auth / rate-limit at Phase 0), and register /health plus a
// /metrics placeholder. Bind only to the internal network.
func New(deps Deps) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(middleware.Recover())

	h := handlers.NewHealth(deps)
	e.GET("/health", h.Handle)
	e.GET("/metrics", func(c echo.Context) error {
		// Phase 0 placeholder — empty Prometheus exposition is valid; Phase 1
		// wires real metrics via a registry on top of this same handler.
		return c.String(http.StatusOK, "# no metrics registered yet\n")
	})
	return e
}
