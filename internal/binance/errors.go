package binance

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Action is the recommended caller behaviour for a failed request.
//
// Default fallback for UNKNOWN bizCodes is ActionPermanent, NOT retry:
// retrying an unclassified error risks duplicate orders / cancels and is the
// wrong fail-safe for a money path. Operators triage via the unclassified
// metric and add codes to the classifier as they appear.
type Action int

const (
	ActionPermanent       Action = iota // unknown / unrecognised — do not retry, alert + business handles
	ActionRetryNow                      // immediate retry (e.g. -1021 timestamp out of recvWindow → resync clock)
	ActionRetryBackoff                  // retry with exponential backoff (429, 5XX, transient network)
	ActionMaybeSucceeded                // -1006 / -1007 — order may have succeeded; query order state to confirm
	ActionTreatAsCanceled               // -2011 / -2013 — order not found, treat as already canceled / filled
	ActionTreatAsSuccess                // -4046 / -4059 — idempotent ops at desired state
	ActionTreatAsExisting               // -4116 — duplicate clientOrderId; caller looks up existing order
	ActionFatal                         // -1022 / -2014 / -2015 / 418 — process-level alert, halt API
)

func (a Action) String() string {
	switch a {
	case ActionRetryNow:
		return "retry_now"
	case ActionRetryBackoff:
		return "retry_backoff"
	case ActionMaybeSucceeded:
		return "maybe_succeeded"
	case ActionTreatAsCanceled:
		return "treat_as_canceled"
	case ActionTreatAsSuccess:
		return "treat_as_success"
	case ActionTreatAsExisting:
		return "treat_as_existing"
	case ActionFatal:
		return "fatal"
	case ActionPermanent:
		return "permanent"
	}
	return "unknown"
}

// APIError is the structured form of a Binance error response.
type APIError struct {
	HTTPCode int
	BizCode  int
	Message  string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("binance api: http=%d code=%d msg=%q", e.HTTPCode, e.BizCode, e.Message)
}

// ParseError reads a Binance error response body and extracts {code, msg}.
// A non-JSON body (e.g. HTML 502 page) yields an APIError with BizCode=0 and
// the raw body as Message — caller can still classify by HTTP status alone.
func ParseError(resp *http.Response) (*APIError, error) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read error body: %w", err)
	}
	var bin struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if jsonErr := json.Unmarshal(body, &bin); jsonErr != nil {
		return &APIError{HTTPCode: resp.StatusCode, Message: string(body)}, nil
	}
	return &APIError{HTTPCode: resp.StatusCode, BizCode: bin.Code, Message: bin.Msg}, nil
}

// ClassifyError maps an HTTP status + Binance bizCode to an Action.
//
// Decision matrix anchored to references/binance/urls.md §「特殊错误处理」 +
// official error-code page web_fetched on first hit. Codes not in the table
// fall through to ActionPermanent — the safe default for "we don't know".
//
// ref: https://developers.binance.com/docs/derivatives/usds-margined-futures/error-code
func ClassifyError(httpCode, bizCode int) Action {
	if httpCode == 418 {
		return ActionFatal
	}
	if httpCode == 429 {
		return ActionRetryBackoff
	}
	if httpCode >= 500 && httpCode < 600 {
		return ActionRetryBackoff
	}
	switch bizCode {
	case -1006, -1007:
		return ActionMaybeSucceeded
	case -2011, -2013:
		return ActionTreatAsCanceled
	case -4046, -4059:
		return ActionTreatAsSuccess
	case -4116:
		// Duplicate clientOrderId — order with this clientOrderId already exists.
		// Caller (PlaceMarketOrder) must look up by clientOrderId and reconcile.
		// Verified on testnet 2026-05-11: Binance returns code=-4116 "ClientOrderId
		// is duplicated.", NOT -2022 as some legacy refs suggested.
		return ActionTreatAsExisting
	case -1021:
		return ActionRetryNow
	case -1022, -2014, -2015:
		return ActionFatal
	}
	return ActionPermanent
}
