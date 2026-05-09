package binance

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/net/proxy"

	"trader/internal/config"
	"trader/internal/pkg/timez"
)

// ProxyManager abstracts how Binance REST and WS callers obtain a proxied
// transport. HTTPClient and WSDialer return the proxy URL string used so the
// caller can correlate later ReportFailure / ReportSuccess feedback with the
// proxy that actually handled the request.
type ProxyManager interface {
	HTTPClient(ctx context.Context) (*http.Client, string, error)
	WSDialer(ctx context.Context) (*websocket.Dialer, string, error)
	ReportFailure(proxyURL string, err error)
	ReportSuccess(proxyURL string)
	Stats() ProxyStats
}

// ProxyStats summarises pool health for /metrics and TG status views.
type ProxyStats struct {
	Mode         string // "none" | "single" | "pool"
	Total        int
	ActiveCount  int
	EvictedCount int
}

// proxyEntry is a single proxy in a Pool. evictedAt zero == healthy.
type proxyEntry struct {
	raw       string // original URL string for caller-side correlation
	parsed    *url.URL
	kind      ProxyKind
	failCount int
	evictedAt time.Time
}

// New returns the ProxyManager matching cfg.Proxy.Mode.
// Errors here are configuration errors; runtime proxy failures surface via
// HTTPClient/WSDialer returning errors and ReportFailure feedback.
func New(cfg *config.Config) (ProxyManager, error) {
	switch cfg.Proxy.Mode {
	case "none":
		return &noopManager{}, nil
	case "single":
		parsed, kind, err := ParseProxyURL(cfg.Proxy.URL)
		if err != nil {
			return nil, fmt.Errorf("single proxy URL: %w", err)
		}
		return &singleManager{raw: cfg.Proxy.URL, parsed: parsed, kind: kind}, nil
	case "pool":
		if len(cfg.Proxy.PoolURLs) == 0 {
			return nil, errors.New("pool mode requires at least one proxy URL")
		}
		entries := make([]*proxyEntry, 0, len(cfg.Proxy.PoolURLs))
		for _, raw := range cfg.Proxy.PoolURLs {
			parsed, kind, err := ParseProxyURL(raw)
			if err != nil {
				return nil, fmt.Errorf("pool proxy URL %q: %w", raw, err)
			}
			entries = append(entries, &proxyEntry{raw: raw, parsed: parsed, kind: kind})
		}
		return &Pool{
			entries:          entries,
			strategy:         cfg.Proxy.PoolStrategy,
			failureThreshold: cfg.Proxy.FailureThreshold,
			recoveryDuration: time.Duration(cfg.Proxy.RecoveryMinutes) * time.Minute,
			nowFunc:          timez.NowUTC,
			rng:              rand.New(rand.NewSource(timez.NowUTC().UnixNano())),
		}, nil
	default:
		return nil, fmt.Errorf("unknown proxy mode %q", cfg.Proxy.Mode)
	}
}

// Pool is the multi-proxy ProxyManager. State (entries / rrIdx) is guarded by
// mu; callers must not hold the lock across network IO. nowFunc is a test seam
// so eviction-recovery scenarios can advance time without sleeping.
type Pool struct {
	mu               sync.Mutex
	entries          []*proxyEntry
	rrIdx            int
	strategy         string
	failureThreshold int
	recoveryDuration time.Duration
	nowFunc          func() time.Time
	rng              *rand.Rand
}

// next picks one entry to use for an outgoing request. Eviction-skip + passive
// recovery: evicted entries past their recovery window are eligible probes;
// their evictedAt remains set until ReportSuccess actually clears it.
func (p *Pool) next() (*proxyEntry, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.nowFunc()
	candidates := make([]int, 0, len(p.entries))
	for i, e := range p.entries {
		if e.evictedAt.IsZero() || now.Sub(e.evictedAt) >= p.recoveryDuration {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) == 0 {
		return nil, errors.New("all proxies evicted")
	}
	var idx int
	switch p.strategy {
	case "random":
		idx = candidates[p.rng.Intn(len(candidates))]
	default: // round_robin
		idx = candidates[0]
		for _, c := range candidates {
			if c >= p.rrIdx {
				idx = c
				break
			}
		}
		p.rrIdx = (idx + 1) % len(p.entries)
	}
	return p.entries[idx], nil
}

func (p *Pool) HTTPClient(_ context.Context) (*http.Client, string, error) {
	e, err := p.next()
	if err != nil {
		return nil, "", err
	}
	return httpClientFor(e.parsed), e.raw, nil
}

func (p *Pool) WSDialer(_ context.Context) (*websocket.Dialer, string, error) {
	e, err := p.next()
	if err != nil {
		return nil, "", err
	}
	d, err := wsDialerFor(e.parsed, e.kind)
	if err != nil {
		return nil, "", err
	}
	return d, e.raw, nil
}

func (p *Pool) ReportFailure(proxyURL string, _ error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.entries {
		if e.raw == proxyURL {
			e.failCount++
			if e.failCount >= p.failureThreshold {
				e.evictedAt = p.nowFunc()
			}
			return
		}
	}
}

func (p *Pool) ReportSuccess(proxyURL string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.entries {
		if e.raw == proxyURL {
			e.failCount = 0
			e.evictedAt = time.Time{}
			return
		}
	}
}

func (p *Pool) Stats() ProxyStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := ProxyStats{Mode: "pool", Total: len(p.entries)}
	now := p.nowFunc()
	for _, e := range p.entries {
		if e.evictedAt.IsZero() || now.Sub(e.evictedAt) >= p.recoveryDuration {
			s.ActiveCount++
		} else {
			s.EvictedCount++
		}
	}
	return s
}

// singleManager forwards every request through a single proxy URL. ReportXxx
// are no-ops because evicting the only proxy yields nothing to fall back to;
// circuit-breaker logic at the business layer handles that scenario.
type singleManager struct {
	raw    string
	parsed *url.URL
	kind   ProxyKind
}

func (m *singleManager) HTTPClient(_ context.Context) (*http.Client, string, error) {
	return httpClientFor(m.parsed), m.raw, nil
}
func (m *singleManager) WSDialer(_ context.Context) (*websocket.Dialer, string, error) {
	d, err := wsDialerFor(m.parsed, m.kind)
	if err != nil {
		return nil, "", err
	}
	return d, m.raw, nil
}
func (m *singleManager) ReportFailure(string, error) {}
func (m *singleManager) ReportSuccess(string)        {}
func (m *singleManager) Stats() ProxyStats {
	return ProxyStats{Mode: "single", Total: 1, ActiveCount: 1}
}

// noopManager is used when BINANCE_PROXY_MODE=none. All clients are direct.
type noopManager struct{}

func (m *noopManager) HTTPClient(_ context.Context) (*http.Client, string, error) {
	return &http.Client{Timeout: 30 * time.Second}, "", nil
}
func (m *noopManager) WSDialer(_ context.Context) (*websocket.Dialer, string, error) {
	return &websocket.Dialer{HandshakeTimeout: 10 * time.Second}, "", nil
}
func (m *noopManager) ReportFailure(string, error) {}
func (m *noopManager) ReportSuccess(string)        {}
func (m *noopManager) Stats() ProxyStats           { return ProxyStats{Mode: "none"} }

// httpClientFor returns an *http.Client that routes through the given proxy URL.
func httpClientFor(parsed *url.URL) *http.Client {
	return &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(parsed)},
		Timeout:   30 * time.Second,
	}
}

// wsDialerFor returns a *websocket.Dialer wired for the given proxy.
// HTTP/HTTPS proxies set Dialer.Proxy; SOCKS5 wires a x/net/proxy SOCKS5 dialer
// into NetDialContext, preferring a ContextDialer assertion when available.
func wsDialerFor(parsed *url.URL, kind ProxyKind) (*websocket.Dialer, error) {
	d := &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	switch kind {
	case KindHTTP, KindHTTPS:
		d.Proxy = http.ProxyURL(parsed)
	case KindSOCKS5:
		var auth *proxy.Auth
		if parsed.User != nil {
			password, _ := parsed.User.Password()
			auth = &proxy.Auth{User: parsed.User.Username(), Password: password}
		}
		sock, err := proxy.SOCKS5("tcp", parsed.Host, auth, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("socks5 dialer: %w", err)
		}
		if cd, ok := sock.(proxy.ContextDialer); ok {
			d.NetDialContext = cd.DialContext
		} else {
			d.NetDialContext = func(_ context.Context, network, addr string) (net.Conn, error) {
				return sock.Dial(network, addr)
			}
		}
	default:
		return nil, fmt.Errorf("unsupported proxy kind: %v", kind)
	}
	return d, nil
}
