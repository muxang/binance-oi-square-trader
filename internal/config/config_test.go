package config

import (
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDecimalHook_Direct probes the hook in isolation, bypassing viper.
// If this passes but TestLoad_AllFieldsBound's decimal asserts fail,
// the bug is in mapstructure integration, not in our hook itself.
func TestDecimalHook_Direct(t *testing.T) {
	h := decimalHook()
	out, err := h(reflect.TypeOf(""), reflect.TypeOf(decimal.Decimal{}), "60000000")
	require.NoError(t, err)
	d, ok := out.(decimal.Decimal)
	require.True(t, ok, "expected decimal.Decimal got %T", out)
	assert.True(t, d.Equal(decimal.NewFromInt(60000000)))

	out2, err := h(reflect.TypeOf(""), reflect.TypeOf(decimal.Decimal{}), "not-a-number")
	require.Error(t, err, "got %v", out2)
}

// validBase is a complete env-var fixture that produces a valid Config.
// Tests start from this and override specific keys to exercise specific paths.
var validBase = map[string]string{
	"TRADER_MODE":                           "testnet",
	"TRADER_MAINNET_CONFIRM":                "",
	"TZ":                                    "Asia/Shanghai",
	"DAILY_RESET_TZ":                        "Asia/Shanghai",
	"LOG_LEVEL":                             "info",
	"LOG_FORMAT":                            "pretty",
	"BINANCE_API_KEY":                       "k",
	"BINANCE_API_SECRET":                    "s",
	"BINANCE_ALGO_MIGRATION_DATE":           "2025-12-09T00:00:00Z",
	"BINANCE_PROXY_MODE":                    "none",
	"BINANCE_PROXY_URL":                     "",
	"BINANCE_PROXY_POOL_URLS":               "",
	"BINANCE_PROXY_POOL_STRATEGY":           "round_robin",
	"BINANCE_PROXY_FAILURE_THRESHOLD":       "5",
	"BINANCE_PROXY_RECOVERY_MINUTES":        "5",
	"SQUARE_USE_PROXY":                      "true",
	"DATABASE_URL":                          "postgres://t:t@localhost:5432/t",
	"REDIS_URL":                             "redis://localhost:6379/0",
	"TG_BOT_TOKEN":                          "tok",
	"TG_CHAT_ID":                            "12345",
	"SENTRY_DSN":                            "",
	"HTTP_PORT":                             "8080",
	"DASHBOARD_PORT":                        "3000",
	"WATCHLIST_MAX_SIZE":                    "150",
	"WATCHLIST_MIN_VOLUME_USD":              "10000000",
	"WATCHLIST_MIN_LIST_DAYS":               "7",
	"WATCHLIST_BLACKLIST":                   "USDC,BUSD",
	"OI_SURGE_FROM_LOW_PCT":                 "0.05",
	"OI_SURGE_RECENT_GROWTH_PCT":            "0.03",
	"OI_SURGE_LOOKBACK_PERIODS":             "10",
	"OI_SURGE_RECENT_PERIODS":               "6",
	"OI_SURGE_MIN_GROWING_RATIO":            "0.5",
	"SQUARE_HOT_MULTIPLIER":                 "2.0",
	"SQUARE_HOT_LOOKBACK_MIN":               "60",
	"SQUARE_BASELINE_LOOKBACK_HOURS":        "24",
	"MARGIN_PER_TRADE_FULL":                 "50",
	"MARGIN_PER_TRADE_HALF":                 "25",
	"LEVERAGE":                              "10",
	"MAX_CONCURRENT_POSITIONS":              "5",
	"SAME_SYMBOL_COOLDOWN_HOURS":            "24",
	"DISASTER_STOP_PCT":                     "0.06",
	"ATR_PERIOD":                            "14",
	"ATR_TIMEFRAME":                         "15m",
	"SIGNAL_FAIL_OI_DROP_PCT":               "0.08",
	"SIGNAL_FAIL_PRICE_LOW_BUFFER_PCT":      "0.03",
	"TP_STAGE1_PCT":                         "0.05",
	"TP_STAGE1_RATIO":                       "0.30",
	"TP_STAGE2_PCT":                         "0.12",
	"TP_STAGE2_RATIO":                       "0.30",
	"TRAILING_ACTIVATE_PCT":                 "0.03",
	"TRAILING_DISTANCE_ATR_MULT":            "2.0",
	"SOFT_TIMEOUT_HOURS":                    "24",
	"HARD_TIMEOUT_HOURS":                    "72",
	"DAILY_LOSS_HALT_PCT":                   "0.05",
	"CONSECUTIVE_LOSS_HALT_COUNT":           "5",
	"CONSECUTIVE_LOSS_HALT_HOURS":           "24",
	"BTC_CRASH_HALT_PCT":                    "0.03",
	"BTC_CRASH_HALT_MINUTES":                "30",
	"TOTAL_FLOAT_LOSS_HALT_PCT":             "0.08",
	"API_ERROR_RATE_LIMIT":                  "3",
	"OI_COLLECTOR_CONCURRENCY":              "8",
	"SQUARE_HASHTAG_CONCURRENCY":            "10",
	"SQUARE_HASHTAG_RETRY_COUNT":            "2",
	"SQUARE_HASHTAG_TIMEOUT_SECONDS":        "8",
	"SQUARE_HASHTAG_RETRY_INTERVAL_MS":      "1000",
	"SQUARE_HASHTAG_BATCH_DEADLINE_MINUTES": "4",
}

// decEq asserts that a decimal.Decimal field equals the expected string-form value.
// Using strings (not float literals) avoids float→decimal precision drift in the test itself.
func decEq(t *testing.T, expected string, actual decimal.Decimal, name string) {
	t.Helper()
	e := decimal.RequireFromString(expected)
	assert.True(t, e.Equal(actual), "%s: expected %s got %s", name, expected, actual.String())
}

// setEnv applies validBase plus overrides via t.Setenv (auto-restored on cleanup).
// t.Chdir to a temp dir prevents accidental .env file shadowing in dev environments.
func setEnv(t *testing.T, overrides map[string]string) {
	t.Helper()
	t.Chdir(t.TempDir())
	for k, v := range validBase {
		if _, ok := overrides[k]; ok {
			continue
		}
		t.Setenv(k, v)
	}
	for k, v := range overrides {
		t.Setenv(k, v)
	}
}

// TestLoad_AllFieldsBound is the core safety test: every env var maps to a unique
// non-default value, and every Config field is asserted. Any tag drift,
// mis-binding, or hook regression fails the corresponding line.
func TestLoad_AllFieldsBound(t *testing.T) {
	setEnv(t, map[string]string{
		"TRADER_MODE":                           "testnet",
		"TZ":                                    "UTC",
		"DAILY_RESET_TZ":                        "America/New_York",
		"LOG_LEVEL":                             "debug",
		"LOG_FORMAT":                            "json",
		"BINANCE_API_KEY":                       "test-key-001",
		"BINANCE_API_SECRET":                    "test-secret-002",
		"BINANCE_ALGO_MIGRATION_DATE":           "2026-01-15T08:00:00Z",
		"BINANCE_PROXY_MODE":                    "single",
		"BINANCE_PROXY_URL":                     "http://p.example.com:8080",
		"BINANCE_PROXY_POOL_URLS":               "http://a.example:1080,http://b.example:1080",
		"BINANCE_PROXY_POOL_STRATEGY":           "random",
		"BINANCE_PROXY_FAILURE_THRESHOLD":       "11",
		"BINANCE_PROXY_RECOVERY_MINUTES":        "13",
		"SQUARE_USE_PROXY":                      "false",
		"DATABASE_URL":                          "postgres://x:y@h:1/d",
		"REDIS_URL":                             "redis://h:2/3",
		"TG_BOT_TOKEN":                          "bot-token-003",
		"TG_CHAT_ID":                            "987654",
		"SENTRY_DSN":                            "https://sentry.example/proj/1",
		"HTTP_PORT":                             "9101",
		"DASHBOARD_PORT":                        "9102",
		"WATCHLIST_MAX_SIZE":                    "75",
		"WATCHLIST_MIN_VOLUME_USD":              "60000000",
		"WATCHLIST_MIN_LIST_DAYS":               "9",
		"WATCHLIST_BLACKLIST":                   "AAA,BBB,CCC",
		"OI_SURGE_FROM_LOW_PCT":                 "0.06",
		"OI_SURGE_RECENT_GROWTH_PCT":            "0.04",
		"OI_SURGE_LOOKBACK_PERIODS":             "11",
		"OI_SURGE_RECENT_PERIODS":               "7",
		"OI_SURGE_MIN_GROWING_RATIO":            "0.6",
		"SQUARE_HOT_MULTIPLIER":                 "2.5",
		"SQUARE_HOT_LOOKBACK_MIN":               "65",
		"SQUARE_BASELINE_LOOKBACK_HOURS":        "26",
		"MARGIN_PER_TRADE_FULL":                 "73.50",
		"MARGIN_PER_TRADE_HALF":                 "36.25",
		"LEVERAGE":                              "12",
		"MAX_CONCURRENT_POSITIONS":              "6",
		"SAME_SYMBOL_COOLDOWN_HOURS":            "26",
		"DISASTER_STOP_PCT":                     "0.07",
		"ATR_PERIOD":                            "15",
		"ATR_TIMEFRAME":                         "30m",
		"SIGNAL_FAIL_OI_DROP_PCT":               "0.09",
		"SIGNAL_FAIL_PRICE_LOW_BUFFER_PCT":      "0.04",
		"TP_STAGE1_PCT":                         "0.06",
		"TP_STAGE1_RATIO":                       "0.31",
		"TP_STAGE2_PCT":                         "0.13",
		"TP_STAGE2_RATIO":                       "0.32",
		"TRAILING_ACTIVATE_PCT":                 "0.04",
		"TRAILING_DISTANCE_ATR_MULT":            "2.5",
		"SOFT_TIMEOUT_HOURS":                    "26",
		"HARD_TIMEOUT_HOURS":                    "76",
		"DAILY_LOSS_HALT_PCT":                   "0.06",
		"CONSECUTIVE_LOSS_HALT_COUNT":           "6",
		"CONSECUTIVE_LOSS_HALT_HOURS":           "26",
		"BTC_CRASH_HALT_PCT":                    "0.04",
		"BTC_CRASH_HALT_MINUTES":                "31",
		"TOTAL_FLOAT_LOSS_HALT_PCT":             "0.09",
		"API_ERROR_RATE_LIMIT":                  "4",
		"OI_COLLECTOR_CONCURRENCY":              "9",
		"SQUARE_HASHTAG_CONCURRENCY":            "7",
		"SQUARE_HASHTAG_RETRY_COUNT":            "3",
		"SQUARE_HASHTAG_TIMEOUT_SECONDS":        "11",
		"SQUARE_HASHTAG_RETRY_INTERVAL_MS":      "1500",
		"SQUARE_HASHTAG_BATCH_DEADLINE_MINUTES": "5",
	})
	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "testnet", cfg.Mode)
	assert.Equal(t, "UTC", cfg.TZ)
	assert.Equal(t, "America/New_York", cfg.DailyResetTZ)
	assert.Equal(t, "UTC", cfg.AppLocation.String())
	assert.Equal(t, "America/New_York", cfg.DailyResetLoc.String())

	assert.Equal(t, "debug", cfg.Log.Level)
	assert.Equal(t, "json", cfg.Log.Format)

	assert.Equal(t, "test-key-001", cfg.Binance.APIKey)
	assert.Equal(t, "test-secret-002", cfg.Binance.APISecret)
	assert.Equal(t, "2026-01-15T08:00:00Z", cfg.Binance.AlgoMigrationDate.UTC().Format(time.RFC3339))

	assert.Equal(t, "single", cfg.Proxy.Mode)
	assert.Equal(t, "http://p.example.com:8080", cfg.Proxy.URL)
	assert.Equal(t, []string{"http://a.example:1080", "http://b.example:1080"}, cfg.Proxy.PoolURLs)
	assert.Equal(t, "random", cfg.Proxy.PoolStrategy)
	assert.Equal(t, 11, cfg.Proxy.FailureThreshold)
	assert.Equal(t, 13, cfg.Proxy.RecoveryMinutes)

	assert.False(t, cfg.Square.UseProxy)
	assert.Equal(t, 7, cfg.Square.HashtagConcurrency)
	assert.Equal(t, 3, cfg.Square.HashtagRetryCount)
	assert.Equal(t, 11*time.Second, cfg.Square.HashtagTimeout)
	assert.Equal(t, 1500*time.Millisecond, cfg.Square.HashtagRetryInterval)
	assert.Equal(t, 5*time.Minute, cfg.Square.HashtagBatchDeadline)

	assert.Equal(t, "postgres://x:y@h:1/d", cfg.DB.PostgresURL)
	assert.Equal(t, "redis://h:2/3", cfg.DB.RedisURL)
	assert.Equal(t, "bot-token-003", cfg.TG.BotToken)
	assert.Equal(t, int64(987654), cfg.TG.ChatID)
	assert.Equal(t, "https://sentry.example/proj/1", cfg.Sentry.DSN)

	assert.Equal(t, 9101, cfg.HTTP.Port)
	assert.Equal(t, 9102, cfg.HTTP.DashboardPort)

	assert.Equal(t, 75, cfg.Watchlist.MaxSize)
	assert.True(t, decimal.RequireFromString("60000000").Equal(cfg.Watchlist.MinVolumeUSD), "MinVolumeUSD")
	assert.Equal(t, 9, cfg.Watchlist.MinListDays)
	assert.Equal(t, []string{"AAA", "BBB", "CCC"}, cfg.Watchlist.Blacklist)

	decEq(t, "0.06", cfg.OISurge.FromLowPct, "OISurge.FromLowPct")
	decEq(t, "0.04", cfg.OISurge.RecentGrowthPct, "OISurge.RecentGrowthPct")
	assert.Equal(t, 11, cfg.OISurge.LookbackPeriods)
	assert.Equal(t, 7, cfg.OISurge.RecentPeriods)
	decEq(t, "0.6", cfg.OISurge.MinGrowingRatio, "OISurge.MinGrowingRatio")

	decEq(t, "2.5", cfg.SquareHot.Multiplier, "SquareHot.Multiplier")
	assert.Equal(t, 65, cfg.SquareHot.LookbackMin)
	assert.Equal(t, 26, cfg.SquareHot.BaselineLookbackHours)

	decEq(t, "73.50", cfg.Position.MarginPerTradeFull, "Position.MarginPerTradeFull")
	decEq(t, "36.25", cfg.Position.MarginPerTradeHalf, "Position.MarginPerTradeHalf")
	assert.Equal(t, 12, cfg.Position.Leverage)
	assert.Equal(t, 6, cfg.Position.MaxConcurrent)
	assert.Equal(t, 26, cfg.Position.SameSymbolCooldownHours)

	decEq(t, "0.07", cfg.Exit.DisasterStopPct, "Exit.DisasterStopPct")
	assert.Equal(t, 15, cfg.Exit.ATRPeriod)
	assert.Equal(t, "30m", cfg.Exit.ATRTimeframe)
	decEq(t, "0.09", cfg.Exit.SignalFailOIDropPct, "Exit.SignalFailOIDropPct")
	decEq(t, "0.04", cfg.Exit.SignalFailPriceLowBufferPct, "Exit.SignalFailPriceLowBufferPct")
	decEq(t, "0.06", cfg.Exit.TPStage1Pct, "Exit.TPStage1Pct")
	decEq(t, "0.31", cfg.Exit.TPStage1Ratio, "Exit.TPStage1Ratio")
	decEq(t, "0.13", cfg.Exit.TPStage2Pct, "Exit.TPStage2Pct")
	decEq(t, "0.32", cfg.Exit.TPStage2Ratio, "Exit.TPStage2Ratio")
	decEq(t, "0.04", cfg.Exit.TrailingActivatePct, "Exit.TrailingActivatePct")
	decEq(t, "2.5", cfg.Exit.TrailingDistanceATRMult, "Exit.TrailingDistanceATRMult")
	assert.Equal(t, 26, cfg.Exit.SoftTimeoutHours)
	assert.Equal(t, 76, cfg.Exit.HardTimeoutHours)

	decEq(t, "0.06", cfg.Risk.DailyLossHaltPct, "Risk.DailyLossHaltPct")
	assert.Equal(t, 6, cfg.Risk.ConsecutiveLossHaltCount)
	assert.Equal(t, 26, cfg.Risk.ConsecutiveLossHaltHours)
	decEq(t, "0.04", cfg.Risk.BTCCrashHaltPct, "Risk.BTCCrashHaltPct")
	assert.Equal(t, 31, cfg.Risk.BTCCrashHaltMinutes)
	decEq(t, "0.09", cfg.Risk.TotalFloatLossHaltPct, "Risk.TotalFloatLossHaltPct")
	assert.Equal(t, 4, cfg.Risk.APIErrorRateLimit)

	assert.Equal(t, 9, cfg.Collector.OIConcurrency)
}

func TestLoad_DefaultMode_IsTestnet(t *testing.T) {
	setEnv(t, nil)
	// Unset the validBase value so viper falls back to its registered default.
	os.Unsetenv("TRADER_MODE")
	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "testnet", cfg.Mode)
}

func TestLoad_MainnetGate_NoConfirm(t *testing.T) {
	setEnv(t, map[string]string{"TRADER_MODE": "mainnet", "TRADER_MAINNET_CONFIRM": ""})
	_, err := Load()
	require.ErrorContains(t, err, "TRADER_MAINNET_CONFIRM=I_UNDERSTAND")
}

func TestLoad_MainnetGate_WrongConfirm(t *testing.T) {
	setEnv(t, map[string]string{"TRADER_MODE": "mainnet", "TRADER_MAINNET_CONFIRM": "yolo"})
	_, err := Load()
	require.ErrorContains(t, err, "TRADER_MAINNET_CONFIRM=I_UNDERSTAND")
}

func TestLoad_MainnetGate_OK(t *testing.T) {
	setEnv(t, map[string]string{"TRADER_MODE": "mainnet", "TRADER_MAINNET_CONFIRM": "I_UNDERSTAND"})
	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "mainnet", cfg.Mode)
}

func TestLoad_Mode_Invalid(t *testing.T) {
	setEnv(t, map[string]string{"TRADER_MODE": "staging"})
	_, err := Load()
	require.ErrorContains(t, err, "TRADER_MODE")
}

func TestLoad_ProxyMode_None_OK(t *testing.T) {
	setEnv(t, map[string]string{"BINANCE_PROXY_MODE": "none"})
	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "none", cfg.Proxy.Mode)
}

func TestLoad_ProxyMode_Single_NoURL(t *testing.T) {
	setEnv(t, map[string]string{"BINANCE_PROXY_MODE": "single", "BINANCE_PROXY_URL": ""})
	_, err := Load()
	require.ErrorContains(t, err, "BINANCE_PROXY_URL")
}

func TestLoad_ProxyMode_Single_OK(t *testing.T) {
	setEnv(t, map[string]string{"BINANCE_PROXY_MODE": "single", "BINANCE_PROXY_URL": "http://x:8080"})
	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "http://x:8080", cfg.Proxy.URL)
}

func TestLoad_ProxyMode_Pool_NoURLs(t *testing.T) {
	setEnv(t, map[string]string{"BINANCE_PROXY_MODE": "pool", "BINANCE_PROXY_POOL_URLS": ""})
	_, err := Load()
	require.ErrorContains(t, err, "BINANCE_PROXY_POOL_URLS")
}

func TestLoad_ProxyMode_Pool_OK(t *testing.T) {
	setEnv(t, map[string]string{
		"BINANCE_PROXY_MODE":      "pool",
		"BINANCE_PROXY_POOL_URLS": "http://a:1080, http://b:1080",
	})
	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, []string{"http://a:1080", "http://b:1080"}, cfg.Proxy.PoolURLs)
}

func TestLoad_ProxyMode_Pool_BadStrategy(t *testing.T) {
	setEnv(t, map[string]string{
		"BINANCE_PROXY_MODE":          "pool",
		"BINANCE_PROXY_POOL_URLS":     "http://a:1080",
		"BINANCE_PROXY_POOL_STRATEGY": "weighted",
	})
	_, err := Load()
	require.ErrorContains(t, err, "BINANCE_PROXY_POOL_STRATEGY")
}

func TestLoad_ProxyMode_Invalid(t *testing.T) {
	setEnv(t, map[string]string{"BINANCE_PROXY_MODE": "tunnel"})
	_, err := Load()
	require.ErrorContains(t, err, "BINANCE_PROXY_MODE")
}

func TestLoad_Timezone_InvalidTZ(t *testing.T) {
	setEnv(t, map[string]string{"TZ": "Foo/Bar"})
	_, err := Load()
	require.ErrorContains(t, err, "TZ")
}

func TestLoad_Timezone_InvalidDailyResetTZ(t *testing.T) {
	setEnv(t, map[string]string{"DAILY_RESET_TZ": "Foo/Bar"})
	_, err := Load()
	require.ErrorContains(t, err, "DAILY_RESET_TZ")
}

func TestLoad_Required_Fields(t *testing.T) {
	for _, key := range []string{"BINANCE_API_KEY", "BINANCE_API_SECRET", "DATABASE_URL", "REDIS_URL", "TG_BOT_TOKEN"} {
		t.Run(key, func(t *testing.T) {
			setEnv(t, map[string]string{key: ""})
			_, err := Load()
			require.ErrorContains(t, err, key)
		})
	}
}

func TestLoad_TGChatID_Empty(t *testing.T) {
	setEnv(t, map[string]string{"TG_CHAT_ID": ""})
	_, err := Load()
	require.Error(t, err)
}

func TestLoad_TGChatID_NotNumeric(t *testing.T) {
	setEnv(t, map[string]string{"TG_CHAT_ID": "abc"})
	_, err := Load()
	require.Error(t, err)
}

func TestLoad_DecimalParse_Invalid(t *testing.T) {
	setEnv(t, map[string]string{"MARGIN_PER_TRADE_FULL": "not-a-number"})
	_, err := Load()
	require.Error(t, err)
}

func TestLoad_AlgoDate_Invalid(t *testing.T) {
	setEnv(t, map[string]string{"BINANCE_ALGO_MIGRATION_DATE": "yesterday"})
	_, err := Load()
	require.Error(t, err)
}
