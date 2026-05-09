package square

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractCashtags(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{"Empty", "", nil},
		{"NoMatch", "hello world", nil},
		{"SingleDollar", "$BTC", []string{"BTC"}},
		{"SingleHash", "#BTC", []string{"BTC"}},
		{"LowercaseUppercased", "$btc", []string{"BTC"}},
		{"MultipleUnique", "$BTC $ETH", []string{"BTC", "ETH"}},
		{"Dedup_SameCase", "$BTC $BTC", []string{"BTC"}},
		{"Dedup_DifferentCase", "$btc $BTC", []string{"BTC"}},
		{"MixedTokens", "$BTC #ETH 涨 $sol 跌", []string{"BTC", "ETH", "SOL"}},
		{"TooShort_Skipped", "$A", nil}, // 1 alphanumeric — regex needs >= 2
		// server.js regex is greedy, no word-boundary; a 17-char token
		// truncates to a 16-char prefix (NOT skipped). Test name reflects
		// actual regex behavior, not the spec's "Skipped" intent.
		{"TooLong_TruncatesAt16", "$ABCDEFGHIJKLMNOPQ", []string{"ABCDEFGHIJKLMNOP"}},
		{"StartsWithDigit_Skipped", "$1BTC", nil},
		{"AlphaNumericMix", "$BTC123", []string{"BTC123"}},
		{
			"RealPostExample",
			"$BTC bullish to $100K! Also watching $ETH and #SOL. Don't sleep on $DOGE 🚀",
			[]string{"BTC", "ETH", "SOL", "DOGE"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.ElementsMatch(t, tt.want, ExtractCashtags(tt.content))
		})
	}
}

func TestToBinancePerpetual(t *testing.T) {
	assert.Equal(t, "BTCUSDT", ToBinancePerpetual("BTC"))
	assert.Equal(t, "ETHUSDT", ToBinancePerpetual("ETH"))
	assert.Equal(t, "USDT", ToBinancePerpetual(""))
}
