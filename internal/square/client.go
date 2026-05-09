package square

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"trader/internal/binance"
)

// BaseURL is the production www.binance.com host. Square has no testnet
// equivalent; mode=testnet/mainnet does not affect this base.
const BaseURL = "https://www.binance.com"

// bncUUIDRedisKey persists a per-installation UUID v4 across restarts.
// Generated once on first launch; reused thereafter to keep the same
// anonymous identity (per references/square/urls.md).
const bncUUIDRedisKey = "bnc_uuid"

// SquareError captures non-2xx responses. Square has no public error code
// taxonomy; callers log + skip rather than classify.
type SquareError struct {
	HTTPCode int
	Body     string
}

func (e *SquareError) Error() string { return fmt.Sprintf("square: HTTP %d", e.HTTPCode) }

// SquareClient is the Square BAPI client. Headers, base URL, and Bnc-Uuid
// are fixed at construction (per fetch_data.ps1 — references/square/urls.md).
type SquareClient struct {
	proxy       binance.ProxyManager
	rateLimiter binance.RateLimiter
	redis       *redis.Client
	log         zerolog.Logger
	httpClient  *http.Client // direct client when useProxy=false

	baseURL  string
	bncUUID  string
	useProxy bool
}

// NewSquareClient wires a SquareClient. UUID is fetched from Redis or
// generated + persisted at startup. Fails fast on Redis error — the main
// flow can't run without a stable Bnc-Uuid.
func NewSquareClient(ctx context.Context, proxy binance.ProxyManager, limiter binance.RateLimiter, rdb *redis.Client, useProxy bool, log zerolog.Logger) (*SquareClient, error) {
	uid, err := initBncUUID(ctx, rdb)
	if err != nil {
		return nil, fmt.Errorf("init bnc_uuid: %w", err)
	}
	return &SquareClient{
		proxy:       proxy,
		rateLimiter: limiter,
		redis:       rdb,
		log:         log,
		httpClient:  &http.Client{Timeout: 30 * time.Second},
		baseURL:     BaseURL,
		bncUUID:     uid,
		useProxy:    useProxy,
	}, nil
}

// initBncUUID returns the Redis-stored UUID; generates + persists if absent.
// Wrapped in a 5s timeout so startup doesn't hang on slow Redis.
func initBncUUID(ctx context.Context, rdb *redis.Client) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	uid, err := rdb.Get(cctx, bncUUIDRedisKey).Result()
	if err == nil {
		return uid, nil
	}
	if !errors.Is(err, redis.Nil) {
		return "", fmt.Errorf("redis get: %w", err)
	}
	uid = uuid.NewString()
	if err := rdb.Set(cctx, bncUUIDRedisKey, uid, 0).Err(); err != nil {
		return "", fmt.Errorf("redis set: %w", err)
	}
	return uid, nil
}

// DoPost issues a POST to baseURL+path, JSON-marshaling body. Returns raw
// bytes for gjson parsing (per references/square/urls.md "宽松解析").
// Non-2xx returns *SquareError; transport errors wrap with %w.
func (c *SquareClient) DoPost(ctx context.Context, path string, body any) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	return c.do(ctx, req)
}

// DoGet issues a GET to baseURL+path with optional query params. Returns
// raw bytes for gjson parsing. Same headers / proxy / rate-limit / error
// handling as DoPost — they share the do() helper.
func (c *SquareClient) DoGet(ctx context.Context, path string, params url.Values) ([]byte, error) {
	full := c.baseURL + path
	if len(params) > 0 {
		full += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	return c.do(ctx, req)
}

// do executes the prepared request: rate limit → proxy/direct client →
// header injection → status classification → proxy success/failure report.
// Internal helper used by DoPost / DoGet to keep behavior identical.
func (c *SquareClient) do(ctx context.Context, req *http.Request) ([]byte, error) {
	if err := c.rateLimiter.Acquire(ctx, 1); err != nil {
		return nil, fmt.Errorf("rate limit: %w", err)
	}
	httpClient := c.httpClient
	var proxyURL string
	if c.useProxy {
		client, pURL, perr := c.proxy.HTTPClient(ctx)
		if perr != nil {
			return nil, fmt.Errorf("proxy: %w", perr)
		}
		httpClient, proxyURL = client, pURL
	}
	c.applyHeaders(req)
	resp, err := httpClient.Do(req)
	if err != nil {
		if c.useProxy {
			c.proxy.ReportFailure(proxyURL, err)
		}
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		if c.useProxy {
			c.proxy.ReportFailure(proxyURL, fmt.Errorf("http %d", resp.StatusCode))
		}
		return nil, &SquareError{HTTPCode: resp.StatusCode, Body: string(body)}
	}
	if c.useProxy {
		c.proxy.ReportSuccess(proxyURL)
	}
	return body, nil
}

// applyHeaders sets the 8 headers per fetch_data.ps1 (references/square/urls.md).
// Versioncode added in 1.4 after web_fetch confirmed it's in the GitHub source.
func (c *SquareClient) applyHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Bnc-Uuid", c.bncUUID)
	req.Header.Set("Clienttype", "web")
	req.Header.Set("Versioncode", "web")
	req.Header.Set("Origin", "https://www.binance.com")
	req.Header.Set("Referer", "https://www.binance.com/zh-CN/square")
	req.Header.Set("Cookie", fmt.Sprintf("bnc-uid=%s; lang=zh-CN", c.bncUUID))
}
