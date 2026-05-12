package admin

import (
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgtype"
)

type SquareTrendingItem struct {
	Symbol       string  `json:"symbol"`
	ContentCount int64   `json:"content_count"`
	ViewCount    int64   `json:"view_count"`
	Growth24h    int64   `json:"growth_24h"`    // content_count delta vs 24h ago
	LatestTsMs   int64   `json:"latest_ts_ms"`
}

type SquareTrendingResponse struct {
	Total int                  `json:"total"`
	Items []SquareTrendingItem `json:"items"`
}

func (s *Server) handleSquareTrending(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	ctx := r.Context()
	rows, err := s.db.Query(ctx, `
		WITH latest AS (
			SELECT DISTINCT ON (symbol)
				symbol, content_count, view_count, ts
			FROM square_hashtag_history
			ORDER BY symbol, ts DESC
		),
		prev24h AS (
			SELECT DISTINCT ON (symbol)
				symbol, content_count AS prev_count
			FROM square_hashtag_history
			WHERE ts <= NOW() - INTERVAL '24 hours'
			ORDER BY symbol, ts DESC
		)
		SELECT
			l.symbol, l.content_count, l.view_count,
			l.content_count - COALESCE(p.prev_count, 0) AS growth_24h,
			l.ts,
			COUNT(*) OVER() AS total
		FROM latest l
		LEFT JOIN prev24h p ON p.symbol = l.symbol
		WHERE l.content_count > 0
		ORDER BY l.content_count DESC
		LIMIT $1
	`, limit)
	if err != nil {
		s.log.Error().Err(err).Msg("square trending")
		s.writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	items := make([]SquareTrendingItem, 0, limit)
	var total int
	for rows.Next() {
		var (
			sym      string
			cnt      int64
			views    int64
			growth   int64
			ts       pgtype.Timestamptz
			totalCnt int
		)
		if err := rows.Scan(&sym, &cnt, &views, &growth, &ts, &totalCnt); err != nil {
			continue
		}
		total = totalCnt
		var tsMs int64
		if ts.Valid {
			tsMs = ts.Time.UnixMilli()
		}
		items = append(items, SquareTrendingItem{
			Symbol:       sym,
			ContentCount: cnt,
			ViewCount:    views,
			Growth24h:    growth,
			LatestTsMs:   tsMs,
		})
	}
	s.writeJSON(w, http.StatusOK, SquareTrendingResponse{Total: total, Items: items})
}
