package admin

import (
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgtype"
)

type SquareTrendingItem struct {
	Symbol     string `json:"symbol"`
	Mentions   int64  `json:"mentions"`
	Views      int64  `json:"views"`
	Likes      int64  `json:"likes"`
	LatestTsMs int64  `json:"latest_ts_ms"`
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
		SELECT
			m.symbol,
			COUNT(DISTINCT m.post_id) AS mentions,
			COALESCE(SUM(p.view_count), 0)  AS views,
			COALESCE(SUM(p.like_count), 0)  AS likes,
			MAX(m.ts)                        AS latest_ts,
			COUNT(*) OVER()                  AS total
		FROM square_mentions m
		JOIN square_posts p ON p.id = m.post_id
		WHERE m.ts >= NOW() - INTERVAL '24 hours'
		GROUP BY m.symbol
		ORDER BY mentions DESC
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
			mentions int64
			views    int64
			likes    int64
			ts       pgtype.Timestamptz
			totalCnt int
		)
		if err := rows.Scan(&sym, &mentions, &views, &likes, &ts, &totalCnt); err != nil {
			continue
		}
		total = totalCnt
		var tsMs int64
		if ts.Valid {
			tsMs = ts.Time.UnixMilli()
		}
		items = append(items, SquareTrendingItem{
			Symbol:     sym,
			Mentions:   mentions,
			Views:      views,
			Likes:      likes,
			LatestTsMs: tsMs,
		})
	}
	s.writeJSON(w, http.StatusOK, SquareTrendingResponse{Total: total, Items: items})
}
