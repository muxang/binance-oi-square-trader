// Phase 5.2 Round 4: Feishu webhook unit tests — focus on the parts that
// can't be observed in production: signing math, cooldown logic, retry
// behavior, dry-run no-op.

package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFeishu_DryRun_NoOpWhenURLEmpty(t *testing.T) {
	f := New(Config{URL: "", Secret: "", Enabled: true}, zerolog.Nop())
	err := f.Send(context.Background(), LevelInfo, "test", "title", "body")
	assert.NoError(t, err, "dry-run returns nil even with Enabled=true when URL empty")
}

func TestFeishu_DryRun_NoOpWhenDisabled(t *testing.T) {
	f := New(Config{URL: "http://example.com", Enabled: false}, zerolog.Nop())
	err := f.Send(context.Background(), LevelInfo, "test", "title", "body")
	assert.NoError(t, err, "Enabled=false drops sends even with URL")
}

func TestFeishu_Send_SuccessReturnsNil(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok"}`))
	}))
	defer srv.Close()

	f := New(Config{URL: srv.URL, Enabled: true}, zerolog.Nop())
	err := f.Send(context.Background(), LevelInfo, "k", "t", "b")
	assert.NoError(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

func TestFeishu_Send_RetriesOnNon200(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	f := New(Config{URL: srv.URL, Enabled: true}, zerolog.Nop())
	// Use a short context to keep test fast — retry delays are 1s/2s so
	// short ctx triggers ctx.Err on the wait, exercising the abort path.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := f.Send(ctx, LevelInfo, "k", "t", "b")
	assert.Error(t, err)
	assert.GreaterOrEqual(t, atomic.LoadInt32(&calls), int32(1), "at least one attempt before ctx expiry")
}

func TestFeishu_Cooldown_CriticalSuppressedWithinWindow(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer srv.Close()

	f := New(Config{URL: srv.URL, Enabled: true}, zerolog.Nop())
	// First critical fires, second is suppressed (same dedupe key, same level).
	require.NoError(t, f.Send(context.Background(), LevelCritical, "halt:x", "t", "b"))
	require.NoError(t, f.Send(context.Background(), LevelCritical, "halt:x", "t", "b"))
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "second critical inside 5min window dropped")
}

func TestFeishu_Cooldown_DifferentKeysIndependent(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer srv.Close()

	f := New(Config{URL: srv.URL, Enabled: true}, zerolog.Nop())
	require.NoError(t, f.Send(context.Background(), LevelCritical, "halt:a", "t", "b"))
	require.NoError(t, f.Send(context.Background(), LevelCritical, "halt:b", "t", "b"))
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls), "different dedupe keys → independent buckets")
}

func TestFeishu_Cooldown_InfoNoLimit(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer srv.Close()

	f := New(Config{URL: srv.URL, Enabled: true}, zerolog.Nop())
	for i := 0; i < 5; i++ {
		require.NoError(t, f.Send(context.Background(), LevelInfo, "entry:1", "t", "b"))
	}
	assert.Equal(t, int32(5), atomic.LoadInt32(&calls), "info level has no cooldown")
}

func TestFeishu_PayloadShape(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer srv.Close()

	f := New(Config{URL: srv.URL, Enabled: true}, zerolog.Nop())
	require.NoError(t, f.Send(context.Background(), LevelCritical, "halt:test", "Trader halt 触发", "body line\n2nd line"))

	require.Equal(t, "post", captured["msg_type"])
	content := captured["content"].(map[string]any)
	post := content["post"].(map[string]any)
	zh := post["zh_cn"].(map[string]any)
	assert.Contains(t, zh["title"], "🔴", "title prefixed with level emoji")
	assert.Contains(t, zh["title"], "Trader halt 触发")
}

func TestFeishu_PayloadSignedWhenSecretSet(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer srv.Close()

	f := New(Config{URL: srv.URL, Secret: "topsecret", Enabled: true}, zerolog.Nop())
	require.NoError(t, f.Send(context.Background(), LevelInfo, "k", "t", "b"))
	assert.Contains(t, captured, "timestamp", "signed bot payload includes timestamp")
	assert.Contains(t, captured, "sign", "signed bot payload includes sign hash")
	assert.NotEmpty(t, captured["sign"], "sign value populated")
}

func TestTemplates_HaltDeepLink(t *testing.T) {
	_, dedupe, title, body := Halt("local_only_orphan", "trade missing on Binance", 259, `{"symbol":"X"}`)
	assert.Equal(t, "halt:local_only_orphan", dedupe)
	assert.Contains(t, title, "Trader halt")
	assert.Contains(t, body, "/admin/audit?halt_event=259", "deep link points to RCA detail")
}

func TestTemplates_EntryDeepLink(t *testing.T) {
	_, dedupe, _, body := Entry("BTCUSDT",
		decimal.NewFromFloat(0.001),
		decimal.NewFromFloat(80000),
		decimal.NewFromFloat(80),
		42)
	assert.Equal(t, "entry:42", dedupe)
	assert.Contains(t, body, "/admin/trade/42")
	assert.Contains(t, body, "BTCUSDT")
}

func TestTemplates_DailyReportWinRate(t *testing.T) {
	_, _, title, body := DailyReport(
		decimal.NewFromFloat(31.16),
		decimal.NewFromFloat(870.07),
		0, 4, 3)
	assert.Contains(t, title, "Trader 日报")
	assert.Contains(t, body, "胜率 75.0%", "win_rate computed from win_count/total_trades")
}
