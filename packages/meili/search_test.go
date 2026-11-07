package meili

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	ms "github.com/meilisearch/meilisearch-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockMeiliSearch 构造只响应 /indexes/posts/search 的假 Meili，捕获请求体并回放 hits
func mockMeiliSearch(t *testing.T, hits string, capture map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/indexes/posts/search" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body) // 测试假服务，解析失败可忽略（capture 断言兜底）
		maps.Copy(capture, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hits":` + hits + `,"query":"kw","processingTimeMs":1}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// rawHit 构造 schemaless 命中（post_id / created_at / _rankingScore）
func rawHit(postID string, createdAt int64, score float64) string {
	return `{"post_id":` + strconv.Quote(postID) +
		`,"created_at":` + strconv.FormatInt(createdAt, 10) +
		`,"_rankingScore":` + strconv.FormatFloat(score, 'f', -1, 64) + `}`
}

// newSearchClient 连 mock 服务构造客户端（bypass NewClient 健康检查）
func newSearchClient(srv *httptest.Server) *MeiliClient {
	return &MeiliClient{
		ServiceManager: ms.New(srv.URL, ms.WithAPIKey("test-key")),
	}
}

// TestSearchPosts 综合排序：rankingScore × 时间衰减当页重排 + 请求口径
func TestSearchPosts(t *testing.T) {
	now := time.Now().UnixMilli()
	dayMs := int64(24 * time.Hour / time.Millisecond)
	// A 相关度高但很旧；B 相关度低但新鲜——综合分 B 胜出
	hits := `[` + rawHit("p-old", now-100*dayMs, 1.0) + `,` + rawHit("p-new", now-1*dayMs, 0.3) + `]`
	capture := map[string]any{}
	srv := mockMeiliSearch(t, hits, capture)

	res, err := newSearchClient(srv).SearchPosts(context.Background(), "kw", 0, 10)
	require.NoError(t, err)

	require.Len(t, res.Hits, 2)
	assert.Equal(t, "p-new", res.Hits[0].PostID, "新鲜帖综合分更高应排前")
	assert.Equal(t, "p-old", res.Hits[1].PostID)
	assert.InDelta(t, 1.0, res.Hits[1].Score, 1e-9, "score 透传 rankingScore 而非综合分")
	assert.Equal(t, int64(2), res.NextOffset)

	// 请求口径：无 sort + 取回相关度得分 + 只取排序字段
	_, hasSort := capture["sort"]
	assert.False(t, hasSort)
	assert.Equal(t, true, capture["showRankingScore"])
	_, hasOffset := capture["offset"]
	assert.False(t, hasOffset, "首页 offset=0 因 omitempty 不序列化")
	assert.Equal(t, float64(10), capture["limit"])
}

// TestSearchLatestPosts 最新排序：(created_at, post_id) 倒序直出无重排
func TestSearchLatestPosts(t *testing.T) {
	now := time.Now().UnixMilli()
	hits := `[` + rawHit("p1", now, 0.1) + `,` + rawHit("p2", now-1000, 0.9) + `]`
	capture := map[string]any{}
	srv := mockMeiliSearch(t, hits, capture)

	res, err := newSearchClient(srv).SearchLatestPosts(context.Background(), "kw", 5, 10)
	require.NoError(t, err)

	// 保持返回序（不因 score 重排）
	require.Len(t, res.Hits, 2)
	assert.Equal(t, "p1", res.Hits[0].PostID)
	assert.Equal(t, "p2", res.Hits[1].PostID)
	assert.Equal(t, int64(7), res.NextOffset, "偏移按实际消费推进")

	// 请求口径：属性排序 + 不取 rankingScore
	assert.Equal(t, []any{"created_at:desc", "post_id:desc"}, capture["sort"])
	_, hasScore := capture["showRankingScore"]
	assert.False(t, hasScore)
}

// TestSearchPosts_DecodeSkip 坏命中跳过 + 游标不回拨
func TestSearchPosts_DecodeSkip(t *testing.T) {
	hits := `[{"created_at":1700000000123},` + rawHit("p1", 1700000000123, 0.5) + `]`
	capture := map[string]any{}
	srv := mockMeiliSearch(t, hits, capture)

	res, err := newSearchClient(srv).SearchLatestPosts(context.Background(), "kw", 0, 10)
	require.NoError(t, err)
	require.Len(t, res.Hits, 1, "缺 post_id 的坏命中被跳过")
	assert.Equal(t, "p1", res.Hits[0].PostID)
	assert.Equal(t, int64(2), res.NextOffset, "游标按原始命中数推进，避免翻页回退")
}

// TestSearchPosts_Error 检索失败返回错误（不吞连接层问题）
func TestSearchPosts_Error(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // 立即关闭：连接必然失败

	_, err := (&MeiliClient{ServiceManager: ms.New(url, ms.WithAPIKey("test-key"))}).SearchPosts(context.Background(), "kw", 0, 10)
	assert.Error(t, err)
}

// TestPostCombinedScore 综合分 = score / (1 + age_days/14)
func TestPostCombinedScore(t *testing.T) {
	now := time.Now().UnixMilli()
	dayMs := int64(24 * time.Hour / time.Millisecond)

	assert.InDelta(t, 1.0, postCombinedScore(PostHit{Score: 1.0, CreatedAt: now}, now), 1e-9, "刚发布无衰减")
	assert.InDelta(t, 0.5, postCombinedScore(PostHit{Score: 1.0, CreatedAt: now - 14*dayMs}, now), 1e-9, "满 2 周减半")
	assert.InDelta(t, 1.0, postCombinedScore(PostHit{Score: 1.0, CreatedAt: now + dayMs}, now), 1e-9, "未来时间按 0 龄处理无衰减")
}
