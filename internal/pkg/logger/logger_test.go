package logger

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trader/internal/config"
)

func newValidCfg() *config.Config {
	return &config.Config{
		Mode:  "testnet",
		TZ:    "Asia/Shanghai",
		Proxy: config.ProxyConfig{Mode: "none"},
		DB: config.DBConfig{
			PostgresURL: "postgres://trader:secretpass@localhost:5432/trader",
			RedisURL:    "redis://localhost:6379/0",
		},
		Log: config.LogConfig{Format: "json", Level: "info"},
	}
}

// swapOutput redirects Init's writer to buf and restores on cleanup.
func swapOutput(t *testing.T, w io.Writer) {
	t.Helper()
	saved := outputWriter
	outputWriter = w
	t.Cleanup(func() { outputWriter = saved })
}

// saveZerolog captures and restores zerolog package globals mutated by Init.
func saveZerolog(t *testing.T) {
	t.Helper()
	savedFmt := zerolog.TimeFieldFormat
	savedFn := zerolog.TimestampFunc
	t.Cleanup(func() {
		zerolog.TimeFieldFormat = savedFmt
		zerolog.TimestampFunc = savedFn
	})
}

func swapSleep(t *testing.T, fn func(time.Duration)) {
	t.Helper()
	saved := sleepFunc
	sleepFunc = fn
	t.Cleanup(func() { sleepFunc = saved })
}

func TestInit_DefaultLevel(t *testing.T) {
	saveZerolog(t)
	cfg := newValidCfg()
	cfg.Log.Level = ""
	log := Init(cfg)
	assert.Equal(t, zerolog.InfoLevel, log.GetLevel())
}

func TestInit_RespectsLevel(t *testing.T) {
	saveZerolog(t)
	cfg := newValidCfg()
	cfg.Log.Level = "debug"
	log := Init(cfg)
	assert.Equal(t, zerolog.DebugLevel, log.GetLevel())
	assert.True(t, log.Debug().Enabled())
}

func TestInit_TimestampIsUTC(t *testing.T) {
	saveZerolog(t)
	var buf bytes.Buffer
	swapOutput(t, &buf)
	log := Init(newValidCfg())
	log.Info().Msg("hello")
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &parsed))
	ts, ok := parsed["time"].(string)
	require.True(t, ok, "time field missing")
	assert.True(t, strings.HasSuffix(ts, "Z"), "expected UTC Z suffix, got %q", ts)
	assert.NotContains(t, ts, "+08:00", "BJT must not appear in log timestamp")
}

func TestInit_JSONFormat(t *testing.T) {
	saveZerolog(t)
	var buf bytes.Buffer
	swapOutput(t, &buf)
	log := Init(newValidCfg())
	log.Info().Msg("hi")
	out := buf.String()
	assert.Contains(t, out, `"level":"info"`)
	assert.Contains(t, out, `"message":"hi"`)
}

func TestInit_PrettyFormat(t *testing.T) {
	saveZerolog(t)
	var buf bytes.Buffer
	swapOutput(t, &buf)
	cfg := newValidCfg()
	cfg.Log.Format = "pretty"
	log := Init(cfg)
	log.Info().Msg("hi")
	out := buf.String()
	assert.Contains(t, out, "INF", "ConsoleWriter should emit INF level marker")
	assert.Contains(t, out, "hi")
}

func TestStartupBanner_AllFields(t *testing.T) {
	saveZerolog(t)
	var buf bytes.Buffer
	swapOutput(t, &buf)
	cfg := newValidCfg()
	log := Init(cfg)
	StartupBanner(log, cfg)
	out := buf.String()
	for _, field := range []string{"version", "mode", "proxy_mode", "timezone",
		"utc_now", "bjt_now", "log_level", "log_format", "db_url", "redis_url"} {
		assert.Contains(t, out, field, "missing field: %s", field)
	}
	// DB password must be sanitized; raw value must not leak.
	assert.NotContains(t, out, "secretpass")
}

func TestStartupBanner_MainnetWarning(t *testing.T) {
	saveZerolog(t)
	var buf bytes.Buffer
	swapOutput(t, &buf)
	swapSleep(t, func(time.Duration) {}) // skip the 5s pause in tests
	cfg := newValidCfg()
	cfg.Mode = "mainnet"
	log := Init(cfg)
	StartupBanner(log, cfg)
	out := buf.String()
	assert.GreaterOrEqual(t, strings.Count(out, "MAINNET"), 5,
		"expected ≥5 MAINNET warning lines, got:\n%s", out)
}

func TestSanitize(t *testing.T) {
	assert.Equal(t, "***", Sanitize(""))
	assert.Equal(t, "***", Sanitize("abc"))
	assert.Equal(t, "***", Sanitize("abcdefghi"))          // 9 chars
	assert.Equal(t, "abcd***wxyz", Sanitize("abcdefwxyz")) // 10 chars (boundary)
	assert.Equal(t, "abcd***wxyz", Sanitize("abcdefghijklmnopqrstuvwxyz"))
}
