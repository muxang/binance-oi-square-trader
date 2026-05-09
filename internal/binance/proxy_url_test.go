package binance

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseProxyURL_HTTP(t *testing.T) {
	u, k, err := ParseProxyURL("http://proxy.example.com:8080")
	require.NoError(t, err)
	assert.Equal(t, KindHTTP, k)
	assert.Equal(t, "proxy.example.com:8080", u.Host)
}

func TestParseProxyURL_HTTPS(t *testing.T) {
	u, k, err := ParseProxyURL("https://proxy.example.com:8443")
	require.NoError(t, err)
	assert.Equal(t, KindHTTPS, k)
	assert.Equal(t, "proxy.example.com:8443", u.Host)
}

func TestParseProxyURL_Socks5(t *testing.T) {
	u, k, err := ParseProxyURL("socks5://localhost:1080")
	require.NoError(t, err)
	assert.Equal(t, KindSOCKS5, k)
	assert.Equal(t, "localhost:1080", u.Host)
}

func TestParseProxyURL_WithAuth(t *testing.T) {
	u, k, err := ParseProxyURL("http://user:pass@p.example.com:8080")
	require.NoError(t, err)
	assert.Equal(t, KindHTTP, k)
	require.NotNil(t, u.User)
	assert.Equal(t, "user", u.User.Username())
	pwd, ok := u.User.Password()
	assert.True(t, ok)
	assert.Equal(t, "pass", pwd)
}

func TestParseProxyURL_Invalid(t *testing.T) {
	cases := []struct{ name, input string }{
		{"empty", ""},
		{"unknown_scheme", "ftp://x:21"},
		{"no_host", "http://"},
		{"missing_scheme", "proxy.example.com:8080"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := ParseProxyURL(c.input)
			assert.Error(t, err)
		})
	}
}
