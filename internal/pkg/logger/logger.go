package logger

import (
	"io"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"trader/internal/config"
	"trader/internal/pkg/timez"
)

// Version is set at build time via -ldflags "-X trader/internal/pkg/logger.Version=v0.1.0".
var Version = "dev"

// outputWriter is the destination for Init-created loggers. Tests swap to a
// bytes.Buffer (with t.Cleanup) to inspect output without redirecting stdout.
var outputWriter io.Writer = os.Stdout

// sleepFunc is a test seam for the mainnet 5-second pause.
var sleepFunc = time.Sleep

// Init applies global zerolog settings (UTC timestamps via timez.NowUTC) and
// returns the root logger. Format and level come from cfg.Log.
func Init(cfg *config.Config) zerolog.Logger {
	zerolog.TimeFieldFormat = time.RFC3339Nano
	zerolog.TimestampFunc = timez.NowUTC

	var out io.Writer = outputWriter
	if cfg.Log.Format == "pretty" {
		out = zerolog.ConsoleWriter{Out: outputWriter, TimeFormat: time.RFC3339Nano}
	}

	level := zerolog.InfoLevel
	if cfg.Log.Level != "" {
		if l, err := zerolog.ParseLevel(cfg.Log.Level); err == nil {
			level = l
		}
	}
	return zerolog.New(out).Level(level).With().Timestamp().Logger()
}

// StartupBanner emits the runtime config summary and (in mainnet mode) prints
// 5 high-priority warnings + a 5-second pause to give the operator time to abort.
// Mainnet warnings use .Warn() so they cannot be silenced by LOG_LEVEL=error.
func StartupBanner(log zerolog.Logger, cfg *config.Config) {
	now := timez.NowUTC()
	log.Info().
		Str("version", Version).
		Str("mode", cfg.Mode).
		Str("proxy_mode", cfg.Proxy.Mode).
		Str("timezone", cfg.TZ).
		Str("utc_now", now.Format(time.RFC3339)).
		Str("bjt_now", timez.FormatBJT(now, time.RFC3339)).
		Str("log_level", cfg.Log.Level).
		Str("log_format", cfg.Log.Format).
		Str("db_url", SanitizeURL(cfg.DB.PostgresURL)).
		Str("redis_url", SanitizeURL(cfg.DB.RedisURL)).
		Msg("startup banner")

	if cfg.Mode == "mainnet" {
		for i := 0; i < 5; i++ {
			log.Warn().Msg("⚠️ MAINNET MODE — REAL MONEY. Pre-flight checklist verified?")
		}
		sleepFunc(5 * time.Second)
	}
}

// Sanitize masks a secret. Strings shorter than 10 chars are fully masked; longer
// strings show the first 4 and last 4 characters with *** in between.
func Sanitize(s string) string {
	if len(s) < 10 {
		return "***"
	}
	return s[:4] + "***" + s[len(s)-4:]
}

// SanitizeURL masks the password component of a `scheme://user:password@host` URL.
// Password is run through Sanitize. Empty/no-userinfo URLs are returned unchanged.
func SanitizeURL(s string) string {
	idx := strings.Index(s, "://")
	if idx < 0 {
		return s
	}
	rest := s[idx+3:]
	at := strings.Index(rest, "@")
	if at < 0 {
		return s
	}
	userinfo := rest[:at]
	colon := strings.Index(userinfo, ":")
	if colon < 0 {
		return s
	}
	return s[:idx+3] + userinfo[:colon] + ":" + Sanitize(userinfo[colon+1:]) + rest[at:]
}
