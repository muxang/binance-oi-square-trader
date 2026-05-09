package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trader/internal/api/handlers"
)

func TestServer_HealthRoute(t *testing.T) {
	e := New(handlers.HealthDeps{
		PingPG:    func(context.Context) error { return nil },
		PingRedis: func(context.Context) error { return nil },
		Version:   "test", Mode: "testnet",
		StartTime: time.Now(),
	})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "ok", body["status"])
	assert.Equal(t, "testnet", body["mode"])
}

func TestServer_MetricsPlaceholder(t *testing.T) {
	e := New(handlers.HealthDeps{Version: "x", Mode: "testnet"})
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}
