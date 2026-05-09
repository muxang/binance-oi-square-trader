package collector

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/rs/zerolog"

	"trader/internal/storage/postgres/gen"
)

func (c *SquareCollector) batchInsertPosts(ctx context.Context, posts []ParsedPost) int {
	if len(posts) == 0 {
		return 0
	}
	params := make([]gen.BatchInsertSquarePostsParams, len(posts))
	for i, p := range posts {
		params[i] = gen.BatchInsertSquarePostsParams{
			ID:           p.ID,
			ContentText:  pgtype.Text{String: p.ContentText, Valid: true},
			AuthorID:     pgtype.Text{String: p.AuthorID, Valid: true},
			AuthorName:   pgtype.Text{String: p.AuthorName, Valid: true},
			AuthorType:   pgtype.Text{String: p.AuthorType, Valid: true},
			Title:        pgtype.Text{String: p.Title, Valid: true},
			ViewCount:    pgtype.Int8{Int64: p.ViewCount, Valid: true},
			LikeCount:    pgtype.Int8{Int64: p.LikeCount, Valid: true},
			CommentCount: pgtype.Int8{Int64: p.CommentCount, Valid: true},
			RawJson:      []byte(p.RawJSON),
			FetchedAt:    p.FetchedAt,
		}
	}
	return execBatch(c.queries.BatchInsertSquarePosts(ctx, params), c.log, "square_posts", len(params))
}

func (c *SquareCollector) batchInsertMentions(ctx context.Context, mentions []MentionRecord) int {
	if len(mentions) == 0 {
		return 0
	}
	params := make([]gen.BatchInsertSquareMentionsParams, len(mentions))
	for i, m := range mentions {
		params[i] = gen.BatchInsertSquareMentionsParams{
			PostID: m.PostID,
			Symbol: m.Symbol,
			Weight: m.Weight,
			Ts:     m.Ts,
		}
	}
	return execBatch(c.queries.BatchInsertSquareMentions(ctx, params), c.log, "square_mentions", len(params))
}

// batchExecResults captures the common surface of sqlc's per-query batch
// wrapper types. All :batchexec outputs implement Exec(func(int,error)) +
// Close().
type batchExecResults interface {
	Exec(func(int, error))
	Close() error
}

func execBatch(br batchExecResults, log zerolog.Logger, table string, attempted int) int {
	defer br.Close()
	ok := 0
	var firstErr error
	br.Exec(func(_ int, err error) {
		if err == nil {
			ok++
		} else if firstErr == nil {
			firstErr = err
		}
	})
	if firstErr != nil {
		log.Error().Err(firstErr).Str("table", table).Int("attempted", attempted).Int("ok", ok).Msg("batch insert errors")
	}
	return ok
}
