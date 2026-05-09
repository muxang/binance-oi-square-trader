package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trader/internal/binance"
	"trader/internal/square"
	"trader/internal/storage/postgres/gen"
)

// --- mocks (each collector_test reimplements its own; no cross-pkg testutil) ---

type fakeValidator struct{ valid map[string]bool }

func (f *fakeValidator) IsValidPerpetual(_ context.Context, s string) (bool, error) {
	return f.valid[s], nil
}

type squareRewriteTransport struct{ target *url.URL }

func (r *squareRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme, req.URL.Host = r.target.Scheme, r.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

type squareTestProxy struct{ target *url.URL }

func (p *squareTestProxy) HTTPClient(context.Context) (*http.Client, string, error) {
	return &http.Client{Transport: &squareRewriteTransport{target: p.target}}, "fake", nil
}
func (p *squareTestProxy) WSDialer(context.Context) (*websocket.Dialer, string, error) {
	return nil, "", errors.New("unused")
}
func (*squareTestProxy) ReportFailure(string, error) {}
func (*squareTestProxy) ReportSuccess(string)        {}
func (*squareTestProxy) Stats() binance.ProxyStats   { return binance.ProxyStats{Mode: "fake"} }

type noopLimiter struct{}

func (noopLimiter) Acquire(context.Context, int) error { return nil }

// fakeBatchResults implements pgx.BatchResults — failures rows return error,
// rest succeed. Used by fakeDBTX.SendBatch to simulate sqlc batch outcomes.
type fakeBatchResults struct{ failures, idx int }

func (f *fakeBatchResults) Exec() (pgconn.CommandTag, error) {
	defer func() { f.idx++ }()
	if f.idx < f.failures {
		return pgconn.CommandTag{}, errors.New("simulated row failure")
	}
	return pgconn.CommandTag{}, nil
}
func (f *fakeBatchResults) Query() (pgx.Rows, error) { return nil, nil }
func (f *fakeBatchResults) QueryRow() pgx.Row        { return nil }
func (f *fakeBatchResults) Close() error             { return nil }

// fakeDBTX implements gen.DBTX. SendBatch is the only non-trivial method —
// alternates between (postFailures, mentionsFailures) per call.
type fakeDBTX struct {
	postFailures, mentionsFailures int
	calls                          atomic.Int32
}

func (*fakeDBTX) Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (*fakeDBTX) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	return nil, nil
}
func (*fakeDBTX) QueryRow(context.Context, string, ...interface{}) pgx.Row { return nil }
func (d *fakeDBTX) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults {
	c := d.calls.Add(1)
	if c == 1 {
		return &fakeBatchResults{failures: d.postFailures}
	}
	return &fakeBatchResults{failures: d.mentionsFailures}
}

// --- helpers ---

func squareTestServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func newSquareTestCollector(t *testing.T, srv *httptest.Server, validator SymbolValidator, db *fakeDBTX, logOut *bytes.Buffer) *SquareCollector {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	target, _ := url.Parse(srv.URL)
	sc, err := square.NewSquareClient(context.Background(), &squareTestProxy{target: target}, noopLimiter{}, rdb, true, zerolog.Nop())
	require.NoError(t, err)
	log := zerolog.Nop()
	if logOut != nil {
		log = zerolog.New(logOut)
	}
	cfg := squareDefaults(SquareCollectorConfig{PerTickTimeout: 5 * time.Second})
	return &SquareCollector{
		client: sc, validator: validator, queries: gen.New(db),
		log: log, cfg: cfg, nowFunc: time.Now,
	}
}

// postFixture builds a /feed-recommend response body containing posts with the
// given ids; each post has $BTC in content so cashtag extraction triggers.
func postFixture(ids ...string) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf(`{"id":%q,"content":"watching $BTC","authorId":"a%d","authorName":"alice","authorType":"USER","title":"t","viewCount":100,"likeCount":50,"commentCount":10}`, id, i)
	}
	return fmt.Sprintf(`{"data":{"contents":[%s]}}`, strings.Join(parts, ","))
}

func passingValidator() *fakeValidator {
	return &fakeValidator{valid: map[string]bool{"BTCUSDT": true, "ETHUSDT": true, "SOLUSDT": true}}
}

// --- Run orchestration tests ------------------------------------------------

func TestSquareRun_SinglePage_NoNewContent_Terminates(t *testing.T) {
	body := postFixture("p1", "p2", "p3")
	var calls atomic.Int32
	srv := squareTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(body))
	})
	c := newSquareTestCollector(t, srv, passingValidator(), &fakeDBTX{}, nil)
	require.NoError(t, c.Run(context.Background()))
	// Iter 1 collects 3 new posts; iter 2 sees same IDs → newPosts=0 → break.
	assert.EqualValues(t, 2, calls.Load(), "must terminate after iter 2 (no new content)")
}

func TestSquareRun_MaxPostsReached_Terminates(t *testing.T) {
	ids := make([]string, 110)
	for i := range ids {
		ids[i] = fmt.Sprintf("p%d", i)
	}
	body := postFixture(ids...)
	var calls atomic.Int32
	srv := squareTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(body))
	})
	c := newSquareTestCollector(t, srv, passingValidator(), &fakeDBTX{}, nil)
	require.NoError(t, c.Run(context.Background()))
	assert.EqualValues(t, 1, calls.Load(), "first iter already hits MaxPosts=100")
}

func TestSquareRun_MaxIterations_Terminates(t *testing.T) {
	var calls atomic.Int32
	srv := squareTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		_, _ = w.Write([]byte(postFixture(fmt.Sprintf("only_%d", n))))
	})
	c := newSquareTestCollector(t, srv, passingValidator(), &fakeDBTX{}, nil)
	require.NoError(t, c.Run(context.Background()))
	assert.EqualValues(t, 8, calls.Load(), "must run all MaxIterations=8 when each iter yields 1 unique post")
}

func TestSquareRun_FetchError_ReturnsError(t *testing.T) {
	srv := squareTestServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) })
	c := newSquareTestCollector(t, srv, passingValidator(), &fakeDBTX{}, nil)
	err := c.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "iter 0")
}

func TestSquareRun_ContextDeadline_AbortsLoop(t *testing.T) {
	srv := squareTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(postFixture("x")))
	})
	c := newSquareTestCollector(t, srv, passingValidator(), &fakeDBTX{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel
	require.Error(t, c.Run(ctx))
}

// --- parsePosts tests -------------------------------------------------------

func newParseCollector() *SquareCollector {
	return &SquareCollector{log: zerolog.Nop(), nowFunc: func() time.Time { return time.Unix(1700000000, 0).UTC() }}
}

func TestSquareParsePosts_ValidResponse(t *testing.T) {
	body := []byte(`{"data":{"contents":[{"id":"p1","content":"hello","authorId":"a","authorName":"n","authorType":"u","title":"t","viewCount":42,"likeCount":7,"commentCount":3}]}}`)
	posts := newParseCollector().parsePosts(body)
	require.Len(t, posts, 1)
	p := posts[0]
	assert.Equal(t, "p1", p.ID)
	assert.Equal(t, "hello", p.ContentText)
	assert.Equal(t, "a", p.AuthorID)
	assert.Equal(t, "n", p.AuthorName)
	assert.Equal(t, "u", p.AuthorType)
	assert.Equal(t, "t", p.Title)
	assert.EqualValues(t, 42, p.ViewCount)
	assert.EqualValues(t, 7, p.LikeCount)
	assert.EqualValues(t, 3, p.CommentCount)
}

func TestSquareParsePosts_MissingId_Skips(t *testing.T) {
	body := []byte(`{"data":{"contents":[{"content":"x"},{"id":"p2","content":"y"}]}}`)
	posts := newParseCollector().parsePosts(body)
	require.Len(t, posts, 1)
	assert.Equal(t, "p2", posts[0].ID)
}

func TestSquareParsePosts_MissingContent_Skips(t *testing.T) {
	body := []byte(`{"data":{"contents":[{"id":"p1"},{"id":"p2","content":"y"}]}}`)
	posts := newParseCollector().parsePosts(body)
	require.Len(t, posts, 1)
	assert.Equal(t, "p2", posts[0].ID)
}

func TestSquareParsePosts_OptionalFieldsMissing_FillsZero(t *testing.T) {
	body := []byte(`{"data":{"contents":[{"id":"p1","content":"x"}]}}`)
	posts := newParseCollector().parsePosts(body)
	require.Len(t, posts, 1)
	assert.Empty(t, posts[0].AuthorID)
	assert.EqualValues(t, 0, posts[0].ViewCount)
}

func TestSquareParsePosts_RawJSONPreserved(t *testing.T) {
	body := []byte(`{"data":{"contents":[{"id":"p1","content":"x","extraField":"keepMe","unknown":99}]}}`)
	posts := newParseCollector().parsePosts(body)
	require.Len(t, posts, 1)
	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal(posts[0].RawJSON, &raw))
	assert.Equal(t, "keepMe", raw["extraField"], "raw_json must preserve unknown fields for future-proofing")
}

// --- extractMentions tests --------------------------------------------------

func TestSquareExtractMentions_ValidatesSymbolBeforeRecord(t *testing.T) {
	posts := []ParsedPost{{ID: "p1", ContentText: "$BTC and $UNKNOWN", FetchedAt: time.Now()}}
	c := &SquareCollector{log: zerolog.Nop(), validator: &fakeValidator{valid: map[string]bool{"BTCUSDT": true}}}
	mentions := c.extractMentions(context.Background(), posts)
	require.Len(t, mentions, 1, "UNKNOWN cashtag must be filtered (not in validator)")
	assert.Equal(t, "BTCUSDT", mentions[0].Symbol)
}

func TestSquareExtractMentions_MultipleCashtagsPerPost(t *testing.T) {
	posts := []ParsedPost{{ID: "p1", ContentText: "$BTC $ETH $SOL", FetchedAt: time.Now()}}
	c := &SquareCollector{log: zerolog.Nop(), validator: passingValidator()}
	mentions := c.extractMentions(context.Background(), posts)
	require.Len(t, mentions, 3)
	syms := []string{mentions[0].Symbol, mentions[1].Symbol, mentions[2].Symbol}
	assert.ElementsMatch(t, []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"}, syms)
}

func TestSquareExtractMentions_DedupAcrossPosts(t *testing.T) {
	posts := []ParsedPost{
		{ID: "p1", ContentText: "$BTC", FetchedAt: time.Now()},
		{ID: "p2", ContentText: "$BTC", FetchedAt: time.Now()},
	}
	c := &SquareCollector{log: zerolog.Nop(), validator: passingValidator()}
	mentions := c.extractMentions(context.Background(), posts)
	// 同 symbol 不同 post → 应是 2 行 (PK = post_id + symbol). 不是 1 行.
	require.Len(t, mentions, 2)
	assert.Equal(t, "p1", mentions[0].PostID)
	assert.Equal(t, "p2", mentions[1].PostID)
}

// --- batch insert / execBatch tests -----------------------------------------

func TestSquareRun_BatchInsertPosts_PartialFailure_Continues(t *testing.T) {
	body := postFixture("p1", "p2", "p3")
	srv := squareTestServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
	var logbuf bytes.Buffer
	db := &fakeDBTX{postFailures: 2} // first 2 post rows fail; mentions OK
	c := newSquareTestCollector(t, srv, passingValidator(), db, &logbuf)
	require.NoError(t, c.Run(context.Background()), "Run must not propagate batch row errors")
	out := logbuf.String()
	assert.Contains(t, out, "square_posts", "should log posts batch error")
	assert.Contains(t, out, "square_feed tick complete", "should still log tick complete (mentions phase ran)")
}

func TestSquareExecBatch_AllFailed_LogsError(t *testing.T) {
	stub := &stubBatchResults{errs: []error{errors.New("e1"), errors.New("e2"), errors.New("e3")}}
	var logbuf bytes.Buffer
	log := zerolog.New(&logbuf)
	ok := execBatch(stub, log, "test_table", 3)
	assert.Equal(t, 0, ok)
	out := logbuf.String()
	assert.Contains(t, out, "batch insert errors")
	assert.Contains(t, out, "test_table")
}

type stubBatchResults struct{ errs []error }

func (s *stubBatchResults) Exec(f func(int, error)) {
	for i, e := range s.errs {
		f(i, e)
	}
}
func (*stubBatchResults) Close() error { return nil }
