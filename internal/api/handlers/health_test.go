package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runHandle(t *testing.T, h *Health) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Handle(c))
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return rec, body
}

func TestHealth_AllDepsOK(t *testing.T) {
	h := NewHealth(HealthDeps{
		PingPG:    func(context.Context) error { return nil },
		PingRedis: func(context.Context) error { return nil },
		Version:   "v0.0.1", Mode: "testnet",
		StartTime: time.Now().Add(-time.Hour),
	})
	rec, body := runHandle(t, h)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", body["status"])
	deps := body["deps"].(map[string]any)
	assert.Equal(t, "ok", deps["pg"])
	assert.Equal(t, "ok", deps["redis"])
	assert.Equal(t, "not_checked", deps["binance_reachable"])
}

func TestHealth_DepFails_StillReturns200(t *testing.T) {
	h := NewHealth(HealthDeps{
		PingPG:    func(context.Context) error { return errors.New("boom") },
		PingRedis: func(context.Context) error { return nil },
		Version:   "v", Mode: "testnet", StartTime: time.Now(),
	})
	rec, body := runHandle(t, h)
	require.Equal(t, http.StatusOK, rec.Code, "must always 200 — Docker liveness contract")
	assert.Equal(t, "fail", body["deps"].(map[string]any)["pg"])
}

func TestHealth_NilPing_NotConfigured(t *testing.T) {
	h := NewHealth(HealthDeps{Version: "v", Mode: "testnet", StartTime: time.Now()})
	_, body := runHandle(t, h)
	deps := body["deps"].(map[string]any)
	assert.Equal(t, "not_configured", deps["pg"])
	assert.Equal(t, "not_configured", deps["redis"])
}
