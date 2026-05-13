// Phase 5.2 Round 4: Feishu (Lark) webhook client for mu's mobile alerting.
//
// mu uses Feishu (not Telegram) — drives the outbound alert path for halt
// trips, manual halts, entries, and the BJT 00:00 daily report. All sends
// are best-effort (log + drop on failure); trader logic NEVER blocks on
// notify.
//
// Webhook signing: optional FEISHU_WEBHOOK_SECRET enables Lark's signed-bot
// verification — payload includes timestamp + sign = HMAC-SHA256 + base64.
// See https://open.feishu.cn/document/client-docs/bot-v3/use-custom-bots-in-a-group
//
// Rate limit per level (suppresses duplicate noise on bursts):
//   critical: 1 send per 5 min per dedupe key
//   warning : 1 send per 1 min per dedupe key
//   info    : no limit (each call sends)
//   daily   : no limit (cron fires once per day already)
//
// Dry-run mode: if FEISHU_WEBHOOK_URL is empty, all Send calls return nil
// without HTTP traffic and log a single INFO line so dev/local runs don't
// need a real webhook.

package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

type Level string

const (
	LevelCritical Level = "critical"
	LevelWarning  Level = "warning"
	LevelInfo     Level = "info"
	LevelDaily    Level = "daily"
)

var levelEmoji = map[Level]string{
	LevelCritical: "🔴",
	LevelWarning:  "🟡",
	LevelInfo:     "🟢",
	LevelDaily:    "⚪",
}

var levelCooldown = map[Level]time.Duration{
	LevelCritical: 5 * time.Minute,
	LevelWarning:  1 * time.Minute,
	LevelInfo:     0,
	LevelDaily:    0,
}

// Config wires the webhook + flags. Sourced from env via internal/config.
type Config struct {
	URL     string // FEISHU_WEBHOOK_URL — empty disables sends (dry-run)
	Secret  string // FEISHU_WEBHOOK_SECRET — optional signed-bot
	Enabled bool   // FEISHU_ENABLED — master switch (false → dry-run even with URL)
}

// Feishu is the alert sender. Safe for concurrent use.
type Feishu struct {
	cfg     Config
	client  *http.Client
	log     zerolog.Logger
	mu      sync.Mutex
	lastFor map[string]time.Time // dedupe key → last send timestamp
}

func New(cfg Config, log zerolog.Logger) *Feishu {
	return &Feishu{
		cfg:     cfg,
		client:  &http.Client{Timeout: 5 * time.Second},
		log:     log,
		lastFor: make(map[string]time.Time),
	}
}

// dryRun reports whether Send calls should silently no-op.
func (f *Feishu) dryRun() bool { return !f.cfg.Enabled || f.cfg.URL == "" }

// allowed reports whether a level+key is within its cooldown window.
// Pure side-effect on lastFor; returns true on allowed (and records the send).
func (f *Feishu) allowed(level Level, dedupeKey string) bool {
	cd := levelCooldown[level]
	if cd == 0 {
		return true
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := string(level) + ":" + dedupeKey
	if last, ok := f.lastFor[key]; ok && time.Since(last) < cd {
		return false
	}
	f.lastFor[key] = time.Now()
	return true
}

// Send posts a message at the given level. dedupeKey is used for cooldown;
// pass a stable identifier (e.g. "halt:local_only_orphan", "entry:BTCUSDT")
// so duplicate triggers in the window are suppressed. msg is the human-
// readable body; title is the bold header.
//
// Returns nil on dry-run / cooldown skip / success. Returns err only on
// transient retry failure (3 attempts: 1s/2s/4s). Caller decides whether
// to log the error or ignore (typically ignore — notify is non-critical).
func (f *Feishu) Send(ctx context.Context, level Level, dedupeKey, title, body string) error {
	if f.dryRun() {
		f.log.Info().Str("level", string(level)).Str("title", title).
			Msg("notify.feishu: dry-run (FEISHU_WEBHOOK_URL unset or FEISHU_ENABLED=false)")
		return nil
	}
	if !f.allowed(level, dedupeKey) {
		f.log.Debug().Str("level", string(level)).Str("dedupe", dedupeKey).
			Msg("notify.feishu: cooldown skipped")
		return nil
	}

	payload := f.buildPayload(level, title, body)
	pBytes, _ := json.Marshal(payload)

	var lastErr error
	delay := 1 * time.Second
	for attempt := 1; attempt <= 3; attempt++ {
		err := f.post(ctx, pBytes)
		if err == nil {
			f.log.Info().Str("level", string(level)).Str("title", title).
				Int("attempt", attempt).Msg("notify.feishu: sent")
			return nil
		}
		lastErr = err
		f.log.Warn().Err(err).Int("attempt", attempt).
			Msg("notify.feishu: send failed (will retry)")
		if attempt < 3 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
				delay *= 2
			}
		}
	}
	f.log.Error().Err(lastErr).Str("level", string(level)).Str("title", title).
		Msg("notify.feishu: all retries failed")
	return lastErr
}

// buildPayload constructs the Lark custom-bot message JSON. Uses the
// post.zh_cn block format so titles render bold + body wraps cleanly on
// mobile clients.
func (f *Feishu) buildPayload(level Level, title, body string) map[string]any {
	emoji := levelEmoji[level]
	header := emoji + " " + title

	// Include all body lines as a single text element; Lark renders \n as
	// line breaks in post messages.
	postBlock := map[string]any{
		"zh_cn": map[string]any{
			"title": header,
			"content": [][]map[string]any{
				{{"tag": "text", "text": body}},
			},
		},
	}
	payload := map[string]any{
		"msg_type": "post",
		"content":  map[string]any{"post": postBlock},
	}

	// Signed bot: when FEISHU_WEBHOOK_SECRET is set, Lark requires
	// timestamp + sign at the top of the payload (not inside content).
	if f.cfg.Secret != "" {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		payload["timestamp"] = ts
		payload["sign"] = f.computeSign(ts)
	}
	return payload
}

// computeSign implements Lark's signed-bot algorithm:
//
//	stringToSign = timestamp + "\n" + secret
//	HMAC-SHA256 with key = stringToSign and message = empty bytes
//	then base64-encode the resulting digest.
//
// Yes — the "key" and "message" arguments are swapped relative to a
// typical HMAC, per Lark's spec.
func (f *Feishu) computeSign(timestamp string) string {
	stringToSign := timestamp + "\n" + f.cfg.Secret
	mac := hmac.New(sha256.New, []byte(stringToSign))
	mac.Write([]byte{})
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// post executes one HTTP attempt. Lark returns 200 + {"code": 0} on success;
// any non-zero code is treated as failure (retries) so signed-bot misconfig
// is surfaced quickly.
func (f *Feishu) post(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("feishu http %d: %s", resp.StatusCode, string(respBody))
	}
	// Lark returns {"code":0,"msg":"ok"} on success.
	var parsed struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(respBody, &parsed); err == nil && parsed.Code != 0 {
		return fmt.Errorf("feishu code=%d msg=%q", parsed.Code, parsed.Msg)
	}
	return nil
}
