package binance

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyError(t *testing.T) {
	cases := []struct {
		name    string
		http    int
		biz     int
		want    Action
		comment string
	}{
		{"http_418_ip_ban", 418, 0, ActionFatal, "IP banned"},
		{"http_429_rate_limit", 429, 0, ActionRetryBackoff, "throttled"},
		{"http_500_server", 500, 0, ActionRetryBackoff, "5xx transient"},
		{"http_502_bad_gateway", 502, 0, ActionRetryBackoff, "5xx transient"},
		{"http_503_unavailable", 503, 0, ActionRetryBackoff, "5xx transient"},
		{"biz_-1006_unknown", 200, -1006, ActionMaybeSucceeded, "must query order"},
		{"biz_-1007_timeout", 200, -1007, ActionMaybeSucceeded, "must query order"},
		{"biz_-2011_no_order", 400, -2011, ActionTreatAsCanceled, "treat as canceled"},
		{"biz_-2013_unknown_order", 400, -2013, ActionTreatAsCanceled, "treat as canceled"},
		{"biz_-4046_idempotent", 400, -4046, ActionTreatAsSuccess, "no-op"},
		{"biz_-4059_idempotent", 400, -4059, ActionTreatAsSuccess, "no-op"},
		{"biz_-1021_recvwindow", 400, -1021, ActionRetryNow, "resync clock"},
		{"biz_-1022_bad_sig", 400, -1022, ActionFatal, "auth"},
		{"biz_-2014_bad_key", 401, -2014, ActionFatal, "auth"},
		{"biz_-2015_bad_perm", 401, -2015, ActionFatal, "auth"},
		{"unknown_biz_code", 400, -9999, ActionPermanent, "fail-safe default"},
		{"unknown_no_biz", 400, 0, ActionPermanent, "no biz code, no http handler"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ClassifyError(c.http, c.biz)
			assert.Equal(t, c.want, got, "%s: %s", c.name, c.comment)
		})
	}
}

func TestClassifyError_UnknownDefaultsPermanent(t *testing.T) {
	// Pin the safety-critical default. Anyone changing this needs to read
	// the comment in errors.go about why retry is unsafe for unknown codes.
	assert.Equal(t, ActionPermanent, ClassifyError(400, -424242))
	assert.Equal(t, ActionPermanent, ClassifyError(403, 0))
}

func TestParseError_ValidJSON(t *testing.T) {
	body := `{"code":-2011,"msg":"Unknown order sent."}`
	resp := &http.Response{
		StatusCode: 400,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	apiErr, err := ParseError(resp)
	require.NoError(t, err)
	assert.Equal(t, 400, apiErr.HTTPCode)
	assert.Equal(t, -2011, apiErr.BizCode)
	assert.Equal(t, "Unknown order sent.", apiErr.Message)
}

func TestParseError_NonJSONBody(t *testing.T) {
	body := `<html>502 Bad Gateway</html>`
	resp := &http.Response{
		StatusCode: 502,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	apiErr, err := ParseError(resp)
	require.NoError(t, err)
	assert.Equal(t, 502, apiErr.HTTPCode)
	assert.Equal(t, 0, apiErr.BizCode)
	assert.Contains(t, apiErr.Message, "Bad Gateway")
}

func TestAPIError_ErrorString(t *testing.T) {
	e := &APIError{HTTPCode: 400, BizCode: -2011, Message: "Unknown order"}
	s := e.Error()
	assert.Contains(t, s, "400")
	assert.Contains(t, s, "-2011")
	assert.Contains(t, s, "Unknown order")
}

func TestAction_String(t *testing.T) {
	// Spot-check the labels — Prometheus / log fields will use these.
	assert.Equal(t, "permanent", ActionPermanent.String())
	assert.Equal(t, "retry_backoff", ActionRetryBackoff.String())
	assert.Equal(t, "maybe_succeeded", ActionMaybeSucceeded.String())
	assert.Equal(t, "fatal", ActionFatal.String())
}
