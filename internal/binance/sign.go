package binance

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
)

// Sign returns hex(HMAC-SHA256(secret, totalParams)). The input MUST be the
// exact byte sequence Binance will sign on its side — this function does NOT
// re-encode or sort. Callers needing url.Values support use BuildQueryString
// or BuildBody, which encode then sign the already-encoded string.
//
// ref: references/binance/urls.md §「General Info / SIGNED endpoints」
// docs: https://developers.binance.com/docs/derivatives/usds-margined-futures/general-info
func Sign(totalParams, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(totalParams))
	return hex.EncodeToString(mac.Sum(nil))
}

// BuildQueryString encodes values (alphabetically sorted by url.Values.Encode)
// and appends `signature=<hex>` last per Binance's "signature must be last"
// rule. Used for GET endpoints whose signed params travel in the query string.
//
// Implementation note: signature is concatenated AFTER Encode() — never added
// to url.Values before encoding, because Encode sorts and would shuffle it.
func BuildQueryString(values url.Values, secret string) string {
	encoded := values.Encode()
	sig := Sign(encoded, secret)
	if encoded == "" {
		return "signature=" + sig
	}
	return encoded + "&signature=" + sig
}

// BuildBody is identical to BuildQueryString, alias for clarity at call sites.
// POST/PUT/DELETE bodies are application/x-www-form-urlencoded per Binance.
func BuildBody(values url.Values, secret string) string {
	return BuildQueryString(values, secret)
}
