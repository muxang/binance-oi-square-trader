package binance

import (
	"fmt"
	"net/url"
)

// ProxyKind enumerates the proxy schemes supported by the proxy layer.
type ProxyKind int

const (
	KindUnknown ProxyKind = iota
	KindHTTP
	KindHTTPS
	KindSOCKS5
)

func (k ProxyKind) String() string {
	switch k {
	case KindHTTP:
		return "http"
	case KindHTTPS:
		return "https"
	case KindSOCKS5:
		return "socks5"
	default:
		return "unknown"
	}
}

// ParseProxyURL parses a raw proxy URL and returns the URL plus its kind.
// Recognised schemes: http, https, socks5. Any other scheme is rejected so
// configuration drift cannot silently produce a direct connection.
func ParseProxyURL(raw string) (*url.URL, ProxyKind, error) {
	if raw == "" {
		return nil, KindUnknown, fmt.Errorf("empty proxy URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, KindUnknown, fmt.Errorf("parse %q: %w", raw, err)
	}
	if u.Host == "" {
		return nil, KindUnknown, fmt.Errorf("proxy URL %q missing host", raw)
	}
	switch u.Scheme {
	case "http":
		return u, KindHTTP, nil
	case "https":
		return u, KindHTTPS, nil
	case "socks5":
		return u, KindSOCKS5, nil
	default:
		return nil, KindUnknown, fmt.Errorf("unsupported proxy scheme %q", u.Scheme)
	}
}
