package square

import (
	"regexp"
	"strings"
)

// cashtagRe matches $TOKEN or #TOKEN where TOKEN is 2-16 alphanumeric chars
// starting with a letter. Verbatim from skingchan/Binance-Square-Analysis
// server.js — references/square/urls.md anchors this regex; do not "improve".
var cashtagRe = regexp.MustCompile(`[\$#]([A-Za-z][A-Za-z0-9]{1,15})`)

// ExtractCashtags returns unique uppercase cashtags from content. Order is
// not guaranteed — callers must use set-equality comparisons.
func ExtractCashtags(content string) []string {
	matches := cashtagRe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		sym := strings.ToUpper(m[1])
		if _, dup := seen[sym]; dup {
			continue
		}
		seen[sym] = struct{}{}
		out = append(out, sym)
	}
	return out
}

// ToBinancePerpetual maps a base cashtag to its USDⓈ-M perpetual symbol.
// Pure string concatenation; the caller verifies whether the produced
// symbol is actually listed (via SymbolService.IsValidPerpetual).
func ToBinancePerpetual(cashtag string) string { return cashtag + "USDT" }
