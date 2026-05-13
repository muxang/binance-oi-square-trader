// Phase 5.2 Round 1: CSRF token middleware.
//
// Threat model: Caddy basic auth caches credentials in the browser. Without
// CSRF, a malicious site could trigger POST /api/admin/* from the user's
// already-authenticated browser (CORS doesn't block writes, only reads of
// responses). CSRF token is read via GET /api/admin/csrf-token (auth required)
// and required as X-CSRF-Token header on writes. Cross-origin attackers cannot
// read the token (CORS preflight blocks it), so they cannot construct valid
// writes even with cached credentials.
//
// Implementation: in-memory token map with 30min TTL. Single-admin (mu) means
// no per-user token isolation needed. Server restart invalidates all tokens
// (admin-web re-fetches on next write).
package admin

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

const (
	csrfTokenTTL    = 30 * time.Minute
	csrfTokenHeader = "X-CSRF-Token"
)

type csrfStore struct {
	mu     sync.Mutex
	tokens map[string]time.Time // token → expires_at
	nowFn  func() time.Time
}

func newCsrfStore() *csrfStore {
	return &csrfStore{
		tokens: make(map[string]time.Time),
		nowFn:  time.Now,
	}
}

// Issue mints a fresh token + records expiry. Caller cleans up expired tokens
// opportunistically (no background goroutine to avoid lifecycle complexity).
func (s *csrfStore) Issue() (token string, expiresAt time.Time) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand.Read failures are rare enough to panic
		panic("csrf: rand read failed: " + err.Error())
	}
	token = hex.EncodeToString(buf)
	expiresAt = s.nowFn().Add(csrfTokenTTL)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked()
	s.tokens[token] = expiresAt
	return
}

// Validate returns true iff token exists + not expired.
func (s *csrfStore) Validate(token string) bool {
	if token == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expiresAt, ok := s.tokens[token]
	if !ok {
		return false
	}
	if s.nowFn().After(expiresAt) {
		delete(s.tokens, token)
		return false
	}
	return true
}

// gcLocked drops expired entries. Caller must hold s.mu.
func (s *csrfStore) gcLocked() {
	now := s.nowFn()
	for t, exp := range s.tokens {
		if now.After(exp) {
			delete(s.tokens, t)
		}
	}
}

// handleCsrfToken issues a fresh CSRF token. Caddy basic auth at the path
// matcher guards this endpoint (browser will prompt on first call per session).
func (s *Server) handleCsrfToken(w http.ResponseWriter, _ *http.Request) {
	token, exp := s.csrf.Issue()
	s.writeJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"expires_at": exp.UTC().Format(time.RFC3339),
	})
}

// requireCsrf wraps a write handler with CSRF token validation.
//   - GET requests pass through (defense layer is method-based; this guard runs
//     after the router, which is already method-restricted for write endpoints)
//   - missing header → 403
//   - invalid/expired token → 403
func (s *Server) requireCsrf(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodOptions {
			next(w, r)
			return
		}
		token := r.Header.Get(csrfTokenHeader)
		if !s.csrf.Validate(token) {
			s.log.Warn().
				Str("path", r.URL.Path).
				Str("remote", r.RemoteAddr).
				Bool("token_present", token != "").
				Msg("admin.csrf: token missing or invalid")
			s.writeError(w, http.StatusForbidden, "csrf token missing or invalid")
			return
		}
		next(w, r)
	}
}
