// Phase 4 Round 2: retry strategy for signed write requests.
//
// Policy:
//   - Network errors (connection refused, timeout, EOF) → retry up to 3 times
//     with exponential backoff 1s/2s/4s.
//   - HTTP 5xx / 429 (rate limit) → same retry policy.
//   - -1021 timestamp outside recvWindow → retry once (clock or proxy spike).
//   - Permanent business errors (-2019 margin, -1111 precision) → no retry.
//   - -4116 duplicate clientOrderId → no retry; caller looks up existing order.
//   - -4046 / -4059 idempotent success → no retry; caller treats as OK.
//
// recvWindow=60000 is set in client.doRequest, so -1021 should be rare.

package binance

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"trader/internal/pkg/metrics"
)

// retryBackoffs is the exponential backoff schedule per attempt index (0-based).
// attempt=0 is the initial call (no backoff); attempts 1/2/3 wait 1s/2s/4s.
var retryBackoffs = []time.Duration{0, 1 * time.Second, 2 * time.Second, 4 * time.Second}

// doWriteRetry wraps doWrite with the Round 2 retry policy. It returns the
// final response body, the final error, and the number of retry attempts made
// (0 = first try succeeded).
//
// -1021 is allowed exactly 1 retry (server clock / proxy latency spike). All
// other retryable errors get the full 3-attempt budget.
func (c *Client) doWriteRetry(ctx context.Context, method, path string, params url.Values, weight int) ([]byte, error) {
	var lastErr error
	var minus1021Retried bool

	for attempt := 0; attempt < len(retryBackoffs); attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(retryBackoffs[attempt]):
			}
		}
		body, err := c.doWrite(ctx, method, path, params, weight)
		if err == nil {
			return body, nil
		}
		lastErr = err

		// Network / context errors (no APIError wrapper) — retry as transient.
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			metrics.OrdersRetryTotal.WithLabelValues(path, "network", strconv.Itoa(attempt+1)).Inc()
			continue
		}
		action := ClassifyError(apiErr.HTTPCode, apiErr.BizCode)
		switch action {
		case ActionRetryBackoff:
			metrics.OrdersRetryTotal.WithLabelValues(path, strconv.Itoa(apiErr.BizCode), strconv.Itoa(attempt+1)).Inc()
			continue
		case ActionRetryNow:
			// -1021: 1 retry only.
			if minus1021Retried {
				return nil, err
			}
			minus1021Retried = true
			metrics.OrdersRetryTotal.WithLabelValues(path, "-1021", strconv.Itoa(attempt+1)).Inc()
			continue
		default:
			// ActionPermanent / ActionTreatAs* / ActionFatal — caller decides.
			return nil, err
		}
	}
	return nil, lastErr
}

// isWriteMethodForRetry is true for HTTP verbs we permit retries on.
// (DELETE retries are safe because Binance treats cancel of already-gone orders
// as -2011/-2013, classified ActionTreatAsCanceled by caller.)
func isWriteMethodForRetry(method string) bool {
	return method == http.MethodPost || method == http.MethodDelete || method == http.MethodPut
}
