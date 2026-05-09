package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"
	"github.com/tidwall/gjson"

	"trader/internal/pkg/timez"
	"trader/internal/square"
	"trader/internal/storage/postgres/gen"
)

const feedRecommendPath = "/bapi/composite/v9/friendly/pgc/feed/feed-recommend/list"

// SymbolValidator is the minimum surface SquareCollector needs from a
// symbol cache (CLAUDE.md §18 — accept interfaces in the consumer).
// *binance.SymbolService implements this implicitly via duck typing.
type SymbolValidator interface {
	IsValidPerpetual(ctx context.Context, symbol string) (bool, error)
}

type SquareCollectorConfig struct {
	MaxIterations  int
	PageSize       int
	MaxPosts       int
	PerTickTimeout time.Duration
	Scenes         []string
}

type ParsedPost struct {
	ID           string
	ContentText  string
	AuthorID     string
	AuthorName   string
	AuthorType   string
	Title        string
	ViewCount    int64
	LikeCount    int64
	CommentCount int64
	RawJSON      json.RawMessage
	FetchedAt    time.Time
}

type MentionRecord struct {
	PostID string
	Symbol string
	Weight decimal.Decimal
	Ts     time.Time
}

// SquareCollector implements T2: every 1h, fetch ~100 posts from Square's
// recommendation feed via paginated scene-rotated POSTs, extract cashtag
// mentions, persist posts + mentions to PG. Failures skip to next cron;
// no in-tick retry (per references/square/urls.md). DB writes live in
// square_feed_writers.go.
type SquareCollector struct {
	client    *square.SquareClient
	validator SymbolValidator
	pool      *pgxpool.Pool
	queries   *gen.Queries
	log       zerolog.Logger
	cfg       SquareCollectorConfig
	nowFunc   func() time.Time
}

func NewSquareCollector(client *square.SquareClient, validator SymbolValidator, pool *pgxpool.Pool, log zerolog.Logger, cfg SquareCollectorConfig) *SquareCollector {
	cfg = squareDefaults(cfg)
	return &SquareCollector{
		client:    client,
		validator: validator,
		pool:      pool,
		queries:   gen.New(pool),
		log:       log,
		cfg:       cfg,
		nowFunc:   timez.NowUTC,
	}
}

func squareDefaults(cfg SquareCollectorConfig) SquareCollectorConfig {
	if cfg.MaxIterations == 0 {
		cfg.MaxIterations = 8
	}
	if cfg.PageSize == 0 {
		cfg.PageSize = 50
	}
	if cfg.MaxPosts == 0 {
		cfg.MaxPosts = 100
	}
	if cfg.PerTickTimeout == 0 {
		cfg.PerTickTimeout = 4 * time.Minute
	}
	if len(cfg.Scenes) == 0 {
		cfg.Scenes = square.FeedRecommendScenes
	}
	return cfg
}

func (c *SquareCollector) Name() string { return "square_feed" }

func (c *SquareCollector) Run(ctx context.Context) error {
	tickCtx, cancel := context.WithTimeout(ctx, c.cfg.PerTickTimeout)
	defer cancel()

	seenIDs := make(map[string]struct{})
	var allPosts []ParsedPost

	for iter := 0; iter < c.cfg.MaxIterations; iter++ {
		scene := c.cfg.Scenes[iter%len(c.cfg.Scenes)]
		req := square.FeedRecommendRequest{
			PageIndex:  1,
			PageSize:   c.cfg.PageSize,
			Scene:      scene,
			ContentIds: setKeys(seenIDs),
		}
		rawBody, err := c.client.DoPost(tickCtx, feedRecommendPath, req)
		if err != nil {
			return fmt.Errorf("iter %d: %w", iter, err)
		}
		newPosts := 0
		for _, p := range c.parsePosts(rawBody) {
			if _, seen := seenIDs[p.ID]; seen {
				continue
			}
			seenIDs[p.ID] = struct{}{}
			allPosts = append(allPosts, p)
			newPosts++
			if len(allPosts) >= c.cfg.MaxPosts {
				break
			}
		}
		if newPosts == 0 || len(allPosts) >= c.cfg.MaxPosts {
			break
		}
	}

	mentions := c.extractMentions(tickCtx, allPosts)
	successPosts := c.batchInsertPosts(tickCtx, allPosts)
	successMentions := c.batchInsertMentions(tickCtx, mentions)

	c.log.Info().
		Int("posts_fetched", len(allPosts)).
		Int("posts_inserted", successPosts).
		Int("mentions", len(mentions)).
		Int("mentions_inserted", successMentions).
		Msg("square_feed tick complete")
	return nil
}

func setKeys(s map[string]struct{}) []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	return out
}

// parsePosts walks `data.vos[]` via gjson; missing id or content drops the
// post (per references/square/urls.md "key fields critical").
//
// Field names verified against live BAPI 2026-05-09 (1.4 真数据 catch):
//   - posts live under data.vos (not data.contents as initially assumed)
//   - author identity is squareAuthorId + authorRole (not authorId + authorType)
func (c *SquareCollector) parsePosts(rawBody []byte) []ParsedPost {
	now := c.nowFunc()
	var posts []ParsedPost
	gjson.GetBytes(rawBody, "data.vos").ForEach(func(_, v gjson.Result) bool {
		id := v.Get("id").String()
		content := v.Get("content").String()
		if id == "" || content == "" {
			c.log.Warn().Str("id", id).Msg("square_feed: skip post — missing id or content")
			return true
		}
		posts = append(posts, ParsedPost{
			ID:           id,
			ContentText:  content,
			AuthorID:     v.Get("squareAuthorId").String(),
			AuthorName:   v.Get("authorName").String(),
			AuthorType:   v.Get("authorRole").String(),
			Title:        v.Get("title").String(),
			ViewCount:    v.Get("viewCount").Int(),
			LikeCount:    v.Get("likeCount").Int(),
			CommentCount: v.Get("commentCount").Int(),
			RawJSON:      json.RawMessage(v.Raw),
			FetchedAt:    now,
		})
		return true
	})
	return posts
}

func (c *SquareCollector) extractMentions(ctx context.Context, posts []ParsedPost) []MentionRecord {
	weight := decimal.NewFromInt(1) // v0.1 placeholder; see square_mentions.sql
	var mentions []MentionRecord
	for _, p := range posts {
		for _, tag := range square.ExtractCashtags(p.ContentText) {
			symbol := square.ToBinancePerpetual(tag)
			valid, err := c.validator.IsValidPerpetual(ctx, symbol)
			if err != nil {
				c.log.Warn().Err(err).Str("symbol", symbol).Msg("validator error, skip cashtag")
				continue
			}
			if !valid {
				continue
			}
			mentions = append(mentions, MentionRecord{
				PostID: p.ID,
				Symbol: symbol,
				Weight: weight,
				Ts:     p.FetchedAt,
			})
		}
	}
	return mentions
}
