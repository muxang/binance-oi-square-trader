// Decode hooks for viper.Unmarshal.
// Internal to the config package — business code should not import these helpers.

package config

import (
	"reflect"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/shopspring/decimal"
)

// decimalHook converts string/float/int to decimal.Decimal.
// An empty string maps to decimal.Zero so missing-but-typed fields don't error here;
// validate() in config.go enforces non-zero requirements where applicable.
func decimalHook() mapstructure.DecodeHookFuncType {
	return func(_, t reflect.Type, data any) (any, error) {
		if t != reflect.TypeOf(decimal.Decimal{}) {
			return data, nil
		}
		switch s := data.(type) {
		case string:
			if s == "" {
				return decimal.Zero, nil
			}
			return decimal.NewFromString(s)
		case float64:
			return decimal.NewFromFloat(s), nil
		case int:
			return decimal.NewFromInt(int64(s)), nil
		}
		return data, nil
	}
}

// rfc3339Hook parses a string into time.Time using RFC3339 layout.
func rfc3339Hook() mapstructure.DecodeHookFuncType {
	return func(_, t reflect.Type, data any) (any, error) {
		if t != reflect.TypeOf(time.Time{}) {
			return data, nil
		}
		s, ok := data.(string)
		if !ok || s == "" {
			return data, nil
		}
		return time.Parse(time.RFC3339, s)
	}
}

// trimmedSliceHook splits a comma-separated string into []string and trims each element.
// Replaces mapstructure.StringToSliceHookFunc which does not trim spaces.
func trimmedSliceHook() mapstructure.DecodeHookFuncType {
	return func(f, t reflect.Type, data any) (any, error) {
		if f.Kind() != reflect.String || t.Kind() != reflect.Slice || t.Elem().Kind() != reflect.String {
			return data, nil
		}
		s, _ := data.(string)
		if s = strings.TrimSpace(s); s == "" {
			return []string{}, nil
		}
		parts := strings.Split(s, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out, nil
	}
}
