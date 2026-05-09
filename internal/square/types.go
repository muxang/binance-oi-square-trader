package square

// FeedRecommendRequest is the JSON body for
// POST /bapi/composite/v9/friendly/pgc/feed/feed-recommend/list.
//
// Per skingchan/Binance-Square-Analysis (fetch_data.ps1 + README.md):
//   - PageIndex is fixed at 1; the server ignores it for pagination
//   - PageSize is 50 per request
//   - Scene rotates across FeedRecommendScenes to surface fresh content
//   - ContentIds is the accumulated set of seen post IDs (server-side dedup)
//
// Response is parsed via gjson (no strong-typed Response struct — the
// non-official BAPI may add/rename fields without notice).
type FeedRecommendRequest struct {
	PageIndex  int      `json:"pageIndex"`
	PageSize   int      `json:"pageSize"`
	Scene      string   `json:"scene"`
	ContentIds []string `json:"contentIds"`
}

// FeedRecommendScenes is the rotation set used by fetch_data.ps1 to surface
// new content across iterations of the same paging session.
var FeedRecommendScenes = []string{
	"web-homepage",
	"web-trending",
	"web-square",
	"web-explore",
}
