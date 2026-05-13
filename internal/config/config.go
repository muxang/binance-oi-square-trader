package config

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/shopspring/decimal"
	"github.com/spf13/viper"
)

// Config holds all runtime configuration. Each leaf field carries a mapstructure tag
// that names the env var it reads from. Sub-structs use ",squash" so all fields share
// one flat namespace (matching the flat env var layout in .env.example).
type Config struct {
	Mode           string         `mapstructure:"TRADER_MODE"`
	MainnetConfirm string         `mapstructure:"TRADER_MAINNET_CONFIRM"`
	TZ             string         `mapstructure:"TZ"`
	DailyResetTZ   string         `mapstructure:"DAILY_RESET_TZ"`
	AppLocation    *time.Location `mapstructure:"-"` // derived from TZ
	DailyResetLoc  *time.Location `mapstructure:"-"` // derived from DailyResetTZ

	Log       LogConfig       `mapstructure:",squash"`
	Binance   BinanceConfig   `mapstructure:",squash"`
	Proxy     ProxyConfig     `mapstructure:",squash"`
	Square    SquareConfig    `mapstructure:",squash"`
	DB        DBConfig        `mapstructure:",squash"`
	TG        TGConfig        `mapstructure:",squash"`
	Sentry    SentryConfig    `mapstructure:",squash"`
	HTTP      HTTPConfig      `mapstructure:",squash"`
	Watchlist WatchlistConfig `mapstructure:",squash"`
	OISurge   OISurgeConfig   `mapstructure:",squash"`
	SquareHot SquareHotConfig `mapstructure:",squash"`
	Position  PositionConfig  `mapstructure:",squash"`
	Exit      ExitConfig      `mapstructure:",squash"`
	Risk      RiskConfig      `mapstructure:",squash"`
	Collector CollectorConfig `mapstructure:",squash"`
}

type LogConfig struct {
	Level  string `mapstructure:"LOG_LEVEL"`
	Format string `mapstructure:"LOG_FORMAT"`
}
type BinanceConfig struct {
	APIKey            string    `mapstructure:"BINANCE_API_KEY"`
	APISecret         string    `mapstructure:"BINANCE_API_SECRET"`
	AlgoMigrationDate time.Time `mapstructure:"BINANCE_ALGO_MIGRATION_DATE"`
}
type ProxyConfig struct {
	Mode             string   `mapstructure:"BINANCE_PROXY_MODE"`
	URL              string   `mapstructure:"BINANCE_PROXY_URL"`
	PoolURLs         []string `mapstructure:"BINANCE_PROXY_POOL_URLS"`
	PoolFile         string   `mapstructure:"BINANCE_PROXY_POOL_FILE"` // takes precedence over PoolURLs
	PoolStrategy     string   `mapstructure:"BINANCE_PROXY_POOL_STRATEGY"`
	FailureThreshold int      `mapstructure:"BINANCE_PROXY_FAILURE_THRESHOLD"`
	RecoveryMinutes  int      `mapstructure:"BINANCE_PROXY_RECOVERY_MINUTES"`
}
type SquareConfig struct {
	UseProxy             bool          `mapstructure:"SQUARE_USE_PROXY"`
	HashtagConcurrency   int           `mapstructure:"SQUARE_HASHTAG_CONCURRENCY"`
	HashtagRetryCount    int           `mapstructure:"SQUARE_HASHTAG_RETRY_COUNT"`
	HashtagTimeout       time.Duration `mapstructure:"-"` // derived from SQUARE_HASHTAG_TIMEOUT_SECONDS
	HashtagRetryInterval time.Duration `mapstructure:"-"` // derived from SQUARE_HASHTAG_RETRY_INTERVAL_MS
	HashtagBatchDeadline time.Duration `mapstructure:"-"` // derived from SQUARE_HASHTAG_BATCH_DEADLINE_MINUTES
}
type DBConfig struct {
	PostgresURL string `mapstructure:"DATABASE_URL"`
	RedisURL    string `mapstructure:"REDIS_URL"`
}
type TGConfig struct {
	BotToken string `mapstructure:"TG_BOT_TOKEN"`
	ChatID   int64  `mapstructure:"TG_CHAT_ID"`
}
type SentryConfig struct {
	DSN string `mapstructure:"SENTRY_DSN"`
}
type HTTPConfig struct {
	Port          int `mapstructure:"HTTP_PORT"`
	DashboardPort int `mapstructure:"DASHBOARD_PORT"`
}
type WatchlistConfig struct {
	MaxSize               int             `mapstructure:"WATCHLIST_MAX_SIZE"`
	MinSize               int             `mapstructure:"WATCHLIST_MIN_SIZE"`
	MinVolumeUSD          decimal.Decimal `mapstructure:"WATCHLIST_MIN_VOLUME_USD"`
	MinListDays           int             `mapstructure:"WATCHLIST_MIN_LIST_DAYS"`
	Blacklist             []string        `mapstructure:"WATCHLIST_BLACKLIST"`
	LeverageTokenSuffixes []string        `mapstructure:"WATCHLIST_LEVERAGE_TOKEN_SUFFIXES"`
	SquareTopN            int             `mapstructure:"WATCHLIST_SQUARE_TOP_N"`
	OITopN                int             `mapstructure:"WATCHLIST_OI_TOP_N"`
	PriceTopN             int             `mapstructure:"WATCHLIST_PRICE_TOP_N"`
}
type OISurgeConfig struct {
	FromLowPct      decimal.Decimal `mapstructure:"OI_SURGE_FROM_LOW_PCT"`
	RecentGrowthPct decimal.Decimal `mapstructure:"OI_SURGE_RECENT_GROWTH_PCT"`
	LookbackPeriods int             `mapstructure:"OI_SURGE_LOOKBACK_PERIODS"`
	RecentPeriods   int             `mapstructure:"OI_SURGE_RECENT_PERIODS"`
	MinGrowingRatio decimal.Decimal `mapstructure:"OI_SURGE_MIN_GROWING_RATIO"`
}
type SquareHotConfig struct {
	Multiplier            decimal.Decimal `mapstructure:"SQUARE_HOT_MULTIPLIER"`
	LookbackMin           int             `mapstructure:"SQUARE_HOT_LOOKBACK_MIN"`
	BaselineLookbackHours int             `mapstructure:"SQUARE_BASELINE_LOOKBACK_HOURS"`
}
type PositionConfig struct {
	MarginPerTradeFull      decimal.Decimal `mapstructure:"MARGIN_PER_TRADE_FULL"`
	MarginPerTradeHalf      decimal.Decimal `mapstructure:"MARGIN_PER_TRADE_HALF"`
	Leverage                int             `mapstructure:"LEVERAGE"`
	MaxConcurrent           int             `mapstructure:"MAX_CONCURRENT_POSITIONS"`
	SameSymbolCooldownHours int             `mapstructure:"SAME_SYMBOL_COOLDOWN_HOURS"`
}
type ExitConfig struct {
	DisasterStopPct             decimal.Decimal `mapstructure:"DISASTER_STOP_PCT"`
	MinStopPct                  decimal.Decimal `mapstructure:"MIN_STOP_PCT"`
	MaxStopPct                  decimal.Decimal `mapstructure:"MAX_STOP_PCT"`
	ATRPeriod                   int             `mapstructure:"ATR_PERIOD"`
	ATRTimeframe                string          `mapstructure:"ATR_TIMEFRAME"`
	SignalFailOIDropPct         decimal.Decimal `mapstructure:"SIGNAL_FAIL_OI_DROP_PCT"`
	SignalFailPriceLowBufferPct decimal.Decimal `mapstructure:"SIGNAL_FAIL_PRICE_LOW_BUFFER_PCT"`
	TPStage1Pct                 decimal.Decimal `mapstructure:"TP_STAGE1_PCT"`
	TPStage1Ratio               decimal.Decimal `mapstructure:"TP_STAGE1_RATIO"`
	TPStage2Pct                 decimal.Decimal `mapstructure:"TP_STAGE2_PCT"`
	TPStage2Ratio               decimal.Decimal `mapstructure:"TP_STAGE2_RATIO"`
	TrailingActivatePct         decimal.Decimal `mapstructure:"TRAILING_ACTIVATE_PCT"`
	TrailingDistanceATRMult     decimal.Decimal `mapstructure:"TRAILING_DISTANCE_ATR_MULT"`
	// v0.2 Round 1: 4-stage trailing thresholds (all decimal — S1/S2 callbacks
	// are multiplied by 100 when handed to Binance %).
	TrailStage1ActivatePct  decimal.Decimal `mapstructure:"TRAIL_STAGE1_ACTIVATE_PCT"`
	TrailStage1CallbackRate decimal.Decimal `mapstructure:"TRAIL_STAGE1_CALLBACK_RATE"`
	TrailStage2UpgradePct   decimal.Decimal `mapstructure:"TRAIL_STAGE2_UPGRADE_PCT"`
	TrailStage2CallbackRate decimal.Decimal `mapstructure:"TRAIL_STAGE2_CALLBACK_RATE"`
	TrailStage3UpgradePct   decimal.Decimal `mapstructure:"TRAIL_STAGE3_UPGRADE_PCT"`
	TrailStage3CallbackRate decimal.Decimal `mapstructure:"TRAIL_STAGE3_CALLBACK_RATE"`
	TrailStage4UpgradePct   decimal.Decimal `mapstructure:"TRAIL_STAGE4_UPGRADE_PCT"`
	TrailStage4CallbackRate decimal.Decimal `mapstructure:"TRAIL_STAGE4_CALLBACK_RATE"`
	SoftTimeoutHours            int             `mapstructure:"SOFT_TIMEOUT_HOURS"`
	HardTimeoutHours            int             `mapstructure:"HARD_TIMEOUT_HOURS"`
}
type RiskConfig struct {
	DailyLossHaltPct         decimal.Decimal `mapstructure:"DAILY_LOSS_HALT_PCT"`
	ConsecutiveLossHaltCount int             `mapstructure:"CONSECUTIVE_LOSS_HALT_COUNT"`
	ConsecutiveLossHaltHours int             `mapstructure:"CONSECUTIVE_LOSS_HALT_HOURS"`
	BTCCrashHaltPct          decimal.Decimal `mapstructure:"BTC_CRASH_HALT_PCT"`
	BTCCrashHaltMinutes      int             `mapstructure:"BTC_CRASH_HALT_MINUTES"`
	TotalFloatLossHaltPct    decimal.Decimal `mapstructure:"TOTAL_FLOAT_LOSS_HALT_PCT"`
	APIErrorRateLimit        int             `mapstructure:"API_ERROR_RATE_LIMIT"`
}
type CollectorConfig struct {
	OIConcurrency int `mapstructure:"OI_COLLECTOR_CONCURRENCY"`
}

// Load reads .env (if present) and environment variables, applies defaults,
// and returns a validated Config. Env vars override .env file; defaults are lowest.
func Load() (*Config, error) {
	v := viper.New()
	v.SetConfigFile(".env")
	v.SetConfigType("env")
	if err := v.ReadInConfig(); err != nil {
		var fnf viper.ConfigFileNotFoundError
		if !errors.As(err, &fnf) && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("read .env: %w", err)
		}
	}
	v.AutomaticEnv()
	bindEnvFromTags(v, reflect.TypeOf(Config{}))
	setDefaults(v)

	var c Config
	if err := v.Unmarshal(&c, viper.DecodeHook(mapstructure.ComposeDecodeHookFunc(
		decimalHook(), rfc3339Hook(), trimmedSliceHook(),
	)), func(dc *mapstructure.DecoderConfig) { dc.WeaklyTypedInput = true }); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	// Derived Duration fields: env var carries unit in its name.
	c.Square.HashtagTimeout = time.Duration(v.GetInt("SQUARE_HASHTAG_TIMEOUT_SECONDS")) * time.Second
	c.Square.HashtagRetryInterval = time.Duration(v.GetInt("SQUARE_HASHTAG_RETRY_INTERVAL_MS")) * time.Millisecond
	c.Square.HashtagBatchDeadline = time.Duration(v.GetInt("SQUARE_HASHTAG_BATCH_DEADLINE_MINUTES")) * time.Minute

	var err error
	if c.AppLocation, err = time.LoadLocation(c.TZ); err != nil {
		return nil, fmt.Errorf("TZ %q: %w", c.TZ, err)
	}
	if c.DailyResetLoc, err = time.LoadLocation(c.DailyResetTZ); err != nil {
		return nil, fmt.Errorf("DAILY_RESET_TZ %q: %w", c.DailyResetTZ, err)
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// bindEnvFromTags walks the Config type and registers BindEnv for every leaf field's
// mapstructure tag. A named tag (e.g. "BINANCE_API_KEY") is a leaf, even when the field
// type is itself a struct (decimal.Decimal, time.Time). Only ",squash" or empty tags
// trigger recursion into nested config sub-structs.
func bindEnvFromTags(v *viper.Viper, t reflect.Type) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag, _, _ := strings.Cut(f.Tag.Get("mapstructure"), ",")
		if tag == "-" {
			continue
		}
		if tag != "" {
			_ = v.BindEnv(tag)
			continue
		}
		if f.Type.Kind() == reflect.Struct {
			bindEnvFromTags(v, f.Type)
		}
	}
}

func setDefaults(v *viper.Viper) {
	for k, val := range map[string]any{
		"TRADER_MODE": "testnet", "TZ": "Asia/Shanghai", "DAILY_RESET_TZ": "Asia/Shanghai",
		"LOG_LEVEL": "info", "LOG_FORMAT": "pretty",
		"BINANCE_PROXY_MODE": "none", "BINANCE_PROXY_POOL_STRATEGY": "round_robin",
		"BINANCE_PROXY_FAILURE_THRESHOLD": 5, "BINANCE_PROXY_RECOVERY_MINUTES": 5,
		"HTTP_PORT": 8080, "DASHBOARD_PORT": 3000,
		"MIN_STOP_PCT": "0.06", "MAX_STOP_PCT": "0.075",
		"TRAIL_STAGE1_ACTIVATE_PCT": "0.03", "TRAIL_STAGE1_CALLBACK_RATE": "0.03",
		"TRAIL_STAGE2_UPGRADE_PCT":  "0.15", "TRAIL_STAGE2_CALLBACK_RATE": "0.05",
		"TRAIL_STAGE3_UPGRADE_PCT":  "0.30", "TRAIL_STAGE3_CALLBACK_RATE": "0.10",
		"TRAIL_STAGE4_UPGRADE_PCT":  "0.60", "TRAIL_STAGE4_CALLBACK_RATE": "0.15",
		"BINANCE_ALGO_MIGRATION_DATE": "2025-12-09T00:00:00Z",
		"WATCHLIST_MAX_SIZE":          150, "WATCHLIST_MIN_SIZE": 50,
		"WATCHLIST_MIN_VOLUME_USD": "10000000", "WATCHLIST_MIN_LIST_DAYS": 7,
		"WATCHLIST_SQUARE_TOP_N": 50, "WATCHLIST_OI_TOP_N": 30, "WATCHLIST_PRICE_TOP_N": 20,
		"WATCHLIST_BLACKLIST":                   "USDC,BUSD,FDUSD,DAI,TUSD,PAX,USDP",
		"WATCHLIST_LEVERAGE_TOKEN_SUFFIXES":     "UPUSDT,DOWNUSDT,BULLUSDT,BEARUSDT",
		"SQUARE_HASHTAG_CONCURRENCY":            10,
		"SQUARE_HASHTAG_RETRY_COUNT":            2,
		"SQUARE_HASHTAG_TIMEOUT_SECONDS":        8,
		"SQUARE_HASHTAG_RETRY_INTERVAL_MS":      1000,
		"SQUARE_HASHTAG_BATCH_DEADLINE_MINUTES": 4,
	} {
		v.SetDefault(k, val)
	}
}

func (c *Config) validate() error {
	switch c.Mode {
	case "testnet", "mainnet":
	default:
		return fmt.Errorf("TRADER_MODE must be testnet or mainnet, got %q", c.Mode)
	}
	if c.Mode == "mainnet" && c.MainnetConfirm != "I_UNDERSTAND" {
		return errors.New("TRADER_MODE=mainnet requires TRADER_MAINNET_CONFIRM=I_UNDERSTAND")
	}
	for _, e := range []struct{ key, val string }{
		{"BINANCE_API_KEY", c.Binance.APIKey}, {"BINANCE_API_SECRET", c.Binance.APISecret},
		{"DATABASE_URL", c.DB.PostgresURL}, {"REDIS_URL", c.DB.RedisURL},
		{"TG_BOT_TOKEN", c.TG.BotToken},
	} {
		if e.val == "" {
			return fmt.Errorf("%s is required", e.key)
		}
	}
	if c.TG.ChatID == 0 {
		return errors.New("TG_CHAT_ID is required and must be a non-zero integer")
	}
	switch c.Proxy.Mode {
	case "none":
	case "single":
		if c.Proxy.URL == "" {
			return errors.New("BINANCE_PROXY_MODE=single requires BINANCE_PROXY_URL")
		}
	case "pool":
		if len(c.Proxy.PoolURLs) == 0 && c.Proxy.PoolFile == "" {
			return errors.New("BINANCE_PROXY_MODE=pool requires BINANCE_PROXY_POOL_URLS or BINANCE_PROXY_POOL_FILE")
		}
		if c.Proxy.PoolStrategy != "round_robin" && c.Proxy.PoolStrategy != "random" {
			return fmt.Errorf("BINANCE_PROXY_POOL_STRATEGY must be round_robin or random, got %q", c.Proxy.PoolStrategy)
		}
	default:
		return fmt.Errorf("BINANCE_PROXY_MODE must be none/single/pool, got %q", c.Proxy.Mode)
	}
	return nil
}
