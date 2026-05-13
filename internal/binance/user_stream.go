// v0.2 Round 4: USER_STREAM auth — listenKey lifecycle + WS dialer access.
//
// USER_STREAM endpoints require X-MBX-APIKEY header only, NO signature.
// (Unlike doRequest/doRequestDirect which sign all requests.)
//
//	POST   /fapi/v1/listenKey  → {"listenKey": "..."} (60min TTL)
//	PUT    /fapi/v1/listenKey  → 30min keepalive
//	DELETE /fapi/v1/listenKey  → close stream
//
// WS URL: {WSBase}/ws/{listenKey}
//
// ref: references/binance/urls.md §「User Data Streams」
package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/gorilla/websocket"
)

// WSBase returns the WebSocket base URL for the configured mode.
func (c *Client) WSBase() string {
	if c.mode == "mainnet" {
		return MainnetWS
	}
	return TestnetWS
}

// WSDialer exposes the proxy-aware WebSocket dialer. Used by execution/user_stream.go
// to open the listenKey stream. Returns the dialer + proxy URL string (for logging).
func (c *Client) WSDialer(ctx context.Context) (*websocket.Dialer, string, error) {
	return c.proxy.WSDialer(ctx)
}

// CreateListenKey opens a new user data stream and returns the listenKey.
// The key has a 60min server-side TTL — caller must KeepaliveListenKey every
// 30min to keep the stream alive. Account / trading endpoint → uses direct
// (no-proxy) path so the API key's IP whitelist matches.
func (c *Client) CreateListenKey(ctx context.Context) (string, error) {
	body, err := c.doUserStream(ctx, http.MethodPost)
	if err != nil {
		return "", fmt.Errorf("create listen key: %w", err)
	}
	var resp struct {
		ListenKey string `json:"listenKey"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parse listen key resp: %w", err)
	}
	if resp.ListenKey == "" {
		return "", fmt.Errorf("listen key empty (resp=%s)", string(body))
	}
	return resp.ListenKey, nil
}

// KeepaliveListenKey extends the listenKey TTL by 60min. Call every 30min for
// safety margin. 200 OK on success. Failure → caller should re-create.
func (c *Client) KeepaliveListenKey(ctx context.Context) error {
	_, err := c.doUserStream(ctx, http.MethodPut)
	if err != nil {
		return fmt.Errorf("keepalive listen key: %w", err)
	}
	return nil
}

// CloseListenKey explicitly tears down the user data stream. Idempotent —
// Binance returns success for an already-closed key. Called on graceful shutdown.
func (c *Client) CloseListenKey(ctx context.Context) error {
	_, err := c.doUserStream(ctx, http.MethodDelete)
	if err != nil {
		return fmt.Errorf("close listen key: %w", err)
	}
	return nil
}

// doUserStream issues an X-MBX-APIKEY-authenticated request (no signature).
// Always uses the direct HTTP client (testnet mode → testnet base; mainnet → mainnet)
// because listenKey is account-scoped and the API key's IP whitelist enforces direct.
func (c *Client) doUserStream(ctx context.Context, method string) ([]byte, error) {
	if err := c.limiter.Acquire(ctx, 1); err != nil {
		return nil, fmt.Errorf("rate limiter: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.restBaseWrite+"/fapi/v1/listenKey", nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("X-MBX-APIKEY", c.apiKey)
	resp, err := c.directHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		apiErr, _ := ParseError(resp)
		c.recordAPIError(ctx, "UserStream:"+method, "/fapi/v1/listenKey", apiErr)
		return nil, apiErr
	}
	return io.ReadAll(resp.Body)
}
