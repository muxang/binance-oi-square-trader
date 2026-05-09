package binance

import (
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSign_OfficialExample anchors our HMAC-SHA256 to Binance's published test
// vector. Spot and futures share the same algorithm; we use spot's example
// because it is publicly documented with a known input/output pair.
//
// secret, totalParams, expected lifted verbatim from the Binance spot docs:
// https://github.com/binance/binance-spot-api-docs/blob/master/rest-api.md
// (web_fetched 2026-05-09 from raw.githubusercontent.com mirror).
func TestSign_OfficialExample(t *testing.T) {
	secret := "NhqPtmdSJYdKjVHjA7PZj4Mge3R5YNiP1e3UZjInClVN65XAbvqqM6A7H5fATj0j"
	totalParams := "symbol=LTCBTC&side=BUY&type=LIMIT&timeInForce=GTC&quantity=1&price=0.1&recvWindow=5000&timestamp=1499827319559"
	expected := "c8db56825ae71d6d79447849e617115f4a920fa2acdcab2b053c4b2838bd6b71"
	assert.Equal(t, expected, Sign(totalParams, secret))
}

func TestSign_EmptyParams(t *testing.T) {
	// HMAC-SHA256 of empty string with a known key — algorithm sanity.
	got := Sign("", "secret")
	assert.Len(t, got, 64, "hex(SHA-256) is always 64 chars")
}

func TestBuildQueryString_AppendsSignatureLast(t *testing.T) {
	v := url.Values{}
	v.Set("symbol", "BTCUSDT")
	v.Set("side", "BUY")
	out := BuildQueryString(v, "test-secret")
	assert.True(t, strings.HasPrefix(out, "side=BUY&symbol=BTCUSDT&signature="),
		"expected sorted params then signature last, got %q", out)
	// signature must be exactly one occurrence and at the end.
	assert.Equal(t, 1, strings.Count(out, "signature="))
}

func TestBuildQueryString_EmptyValues(t *testing.T) {
	out := BuildQueryString(url.Values{}, "test-secret")
	assert.True(t, strings.HasPrefix(out, "signature="))
	// Sanity: no leading "&".
	assert.False(t, strings.HasPrefix(out, "&"))
}

func TestBuildQueryString_SignatureNotInSortedSet(t *testing.T) {
	// Regression guard for the pitfall the project lead called out:
	// adding "signature" into url.Values before Encode would alphabetically
	// reorder it (s comes after price/quantity but before symbol/timeInForce).
	// Our impl appends AFTER Encode; this test pins that behaviour.
	v := url.Values{}
	v.Set("zzz", "last-key")
	v.Set("aaa", "first-key")
	out := BuildQueryString(v, "k")
	// signature must come after BOTH zzz=... and aaa=...
	zzzIdx := strings.Index(out, "zzz=")
	sigIdx := strings.Index(out, "signature=")
	assert.Greater(t, sigIdx, zzzIdx, "signature must come after the last url.Values key")
}

func TestBuildBody_AliasesQueryString(t *testing.T) {
	v := url.Values{}
	v.Set("symbol", "BTCUSDT")
	assert.Equal(t, BuildQueryString(v, "k"), BuildBody(v, "k"))
}
