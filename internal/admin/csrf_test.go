// Phase 5.2 Round 1: CSRF store + middleware tests.
package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCsrfStore_IssueAndValidate(t *testing.T) {
	s := newCsrfStore()
	token, exp := s.Issue()
	require.NotEmpty(t, token)
	require.True(t, exp.After(time.Now()))
	assert.True(t, s.Validate(token))
}

func TestCsrfStore_RejectExpired(t *testing.T) {
	s := newCsrfStore()
	frozen := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.nowFn = func() time.Time { return frozen }
	token, _ := s.Issue()
	require.True(t, s.Validate(token))
	// fast-forward 31min
	s.nowFn = func() time.Time { return frozen.Add(31 * time.Minute) }
	assert.False(t, s.Validate(token), "31min past issue → expired → reject")
}

func TestCsrfStore_RejectEmpty(t *testing.T) {
	s := newCsrfStore()
	assert.False(t, s.Validate(""))
}

func TestCsrfStore_RejectUnknown(t *testing.T) {
	s := newCsrfStore()
	assert.False(t, s.Validate("deadbeef0123"))
}

func TestCsrfStore_GcOnIssue(t *testing.T) {
	s := newCsrfStore()
	frozen := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.nowFn = func() time.Time { return frozen }
	t1, _ := s.Issue()
	t2, _ := s.Issue()
	require.Len(t, s.tokens, 2)
	// fast-forward 31min, then issue → gc drops t1+t2
	s.nowFn = func() time.Time { return frozen.Add(31 * time.Minute) }
	_, _ = s.Issue()
	// t1+t2 gc'd, only new one remains
	assert.False(t, s.Validate(t1))
	assert.False(t, s.Validate(t2))
	assert.Len(t, s.tokens, 1, "gc dropped expired entries on Issue")
}

func TestRequireCsrf_PassesGET(t *testing.T) {
	srv := &Server{csrf: newCsrfStore(), log: zerolog.Nop()}
	called := false
	handler := srv.requireCsrf(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(200)
	})
	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)
	assert.True(t, called, "GET passes through (router enforces method)")
	assert.Equal(t, 200, rec.Code)
}

func TestRequireCsrf_BlocksPOST_NoToken(t *testing.T) {
	srv := &Server{csrf: newCsrfStore(), log: zerolog.Nop()}
	called := false
	handler := srv.requireCsrf(func(_ http.ResponseWriter, _ *http.Request) { called = true })
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)
	assert.False(t, called)
	assert.Equal(t, 403, rec.Code)
}

func TestRequireCsrf_BlocksPOST_InvalidToken(t *testing.T) {
	srv := &Server{csrf: newCsrfStore(), log: zerolog.Nop()}
	called := false
	handler := srv.requireCsrf(func(_ http.ResponseWriter, _ *http.Request) { called = true })
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("X-CSRF-Token", "garbage")
	rec := httptest.NewRecorder()
	handler(rec, req)
	assert.False(t, called)
	assert.Equal(t, 403, rec.Code)
}

func TestRequireCsrf_AllowsPOST_ValidToken(t *testing.T) {
	srv := &Server{csrf: newCsrfStore(), log: zerolog.Nop()}
	token, _ := srv.csrf.Issue()
	called := false
	handler := srv.requireCsrf(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(200)
	})
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("X-CSRF-Token", token)
	rec := httptest.NewRecorder()
	handler(rec, req)
	assert.True(t, called)
	assert.Equal(t, 200, rec.Code)
}

func TestHandleCsrfToken_ReturnsTokenJSON(t *testing.T) {
	srv := &Server{csrf: newCsrfStore(), log: zerolog.Nop()}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/csrf-token", nil)
	rec := httptest.NewRecorder()
	srv.handleCsrfToken(rec, req)
	assert.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), `"token":"`)
	assert.Contains(t, rec.Body.String(), `"expires_at":"`)
}
