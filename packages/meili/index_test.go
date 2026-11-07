package meili

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ms "github.com/meilisearch/meilisearch-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewPostIndexDoc 索引文档组装：字段映射 + created_at 解析 + 主键 canonical + 非法降级
func TestNewPostIndexDoc(t *testing.T) {
	t.Run("字段映射 + created_at 解析 + 主键 canonical", func(t *testing.T) {
		doc, err := NewPostIndexDoc("1700000000000000001", map[string]any{
			"title": "标题", "full_text": "正文内容", "author_id": "1700000000000000002",
			"created_at": "1700000000123",
			"like_count": int64(1), "favourite_count": int64(2), "comment_count": int64(3), "view_count": int64(4),
		})
		require.NoError(t, err)
		assert.Equal(t, "1700000000000000001", doc.PostID, "主键取消息侧 canonical ID")
		assert.Equal(t, "标题", doc.Title)
		assert.Equal(t, "正文内容", doc.FullText)
		assert.Equal(t, "1700000000000000002", doc.AuthorID)
		assert.Equal(t, int64(1700000000123), doc.CreatedAt)
		assert.Equal(t, int64(1), doc.LikeCount)
		assert.Equal(t, int64(2), doc.FavouriteCount)
		assert.Equal(t, int64(3), doc.CommentCount)
		assert.Equal(t, int64(4), doc.ViewCount)
	})

	t.Run("created_at 非法降级 0 并返回 error", func(t *testing.T) {
		doc, err := NewPostIndexDoc("1700000000000000001", map[string]any{"created_at": "not-a-timestamp"})
		require.Error(t, err, "降级需返回 error 供调用方 Warn 记录")
		assert.Equal(t, int64(0), doc.CreatedAt)
		assert.Equal(t, "1700000000000000001", doc.PostID, "降级不影响主键")
	})

	t.Run("int 字段类型降级", func(t *testing.T) {
		doc, err := NewPostIndexDoc("1", map[string]any{"created_at": "1700000000123", "like_count": 5, "view_count": int32(7)})
		require.NoError(t, err)
		assert.Equal(t, int64(5), doc.LikeCount, "int 支持")
		assert.Equal(t, int64(7), doc.ViewCount, "int32 支持")
		assert.Equal(t, int64(0), doc.CommentCount, "缺失字段降级 0")
	})
}

// TestPostsIndexSettings 排序键必须声明为 sortable（Meili 对未声明的 sort 属性报 not sortable，
// 主键不豁免）。回归：search.go 的 sort 键（created_at, post_id）变更时须同步本设置
func TestPostsIndexSettings(t *testing.T) {
	s := PostsIndexSettings()
	assert.ElementsMatch(t, []string{"created_at", "post_id"}, s.SortableAttributes,
		"SEARCH_SORT_LATEST 的 sort 键必须在 sortableAttributes 声明")
	assert.ElementsMatch(t, []string{"title", "full_text"}, s.SearchableAttributes,
		"检索面限定标题+正文，统计/ID 字段不参与匹配")
}

// mockMeiliWrite 构造响应 posts 索引写入路径的假 Meili：捕获请求体/路径，
// 任务接口回放 succeeded（满足 waitTask 阻塞语义）
func mockMeiliWrite(t *testing.T, capture map[string]any, paths *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*paths = append(*paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/tasks") {
			w.WriteHeader(http.StatusOK) // GET /tasks/:id 期望 200
			_, _ = w.Write([]byte(`{"taskUid":1,"status":"succeeded"}`))
			return
		}
		var body any
		_ = json.NewDecoder(r.Body).Decode(&body)
		capture[r.URL.Path] = body
		// 写操作（创建索引/改设置/增删文档）返回 202 + task（满足 waitTask 阻塞语义）
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"taskUid":1,"status":"enqueued","index":"posts"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestUpsertPost 帖子入库：单条 upsert 走 posts 索引 + 文档序列化正确
func TestUpsertPost(t *testing.T) {
	capture := map[string]any{}
	var paths []string
	srv := mockMeiliWrite(t, capture, &paths)

	mc := &MeiliClient{ServiceManager: ms.New(srv.URL, ms.WithAPIKey("test-key"))}
	err := mc.UpsertPost(context.Background(), PostIndexDoc{PostID: "p1", Title: "标题", CreatedAt: 1700000000123})
	require.NoError(t, err)

	docPath := "/indexes/posts/documents"
	require.Contains(t, capture, docPath)
	docs := capture[docPath].([]any)
	require.Len(t, docs, 1)
	assert.Equal(t, "p1", docs[0].(map[string]any)["post_id"])
	assert.Equal(t, "标题", docs[0].(map[string]any)["title"])
	assert.Equal(t, float64(1700000000123), docs[0].(map[string]any)["created_at"])
}

// TestRemovePost 帖子删除：按主键删走 posts 索引（删不存在键为 no-op）
func TestRemovePost(t *testing.T) {
	capture := map[string]any{}
	var paths []string
	srv := mockMeiliWrite(t, capture, &paths)

	mc := &MeiliClient{ServiceManager: ms.New(srv.URL, ms.WithAPIKey("test-key"))}
	err := mc.RemovePost(context.Background(), "p1")
	require.NoError(t, err)

	assert.Contains(t, capture, "/indexes/posts/documents/delete-batch")
	assert.Equal(t, []any{"p1"}, capture["/indexes/posts/documents/delete-batch"])
}

// TestEnsurePostsIndex 确保索引：不存在则创建 + 应用设置（主键/排序/检索面）
func TestEnsurePostsIndex(t *testing.T) {
	capture := map[string]any{}
	var paths []string
	srv := mockMeiliWrite(t, capture, &paths)

	mc := &MeiliClient{ServiceManager: ms.New(srv.URL, ms.WithAPIKey("test-key"))}
	err := mc.EnsurePostsIndex(context.Background())
	require.NoError(t, err)

	// 创建索引（含 primary key）+ 更新设置（sortable/searchable）
	assert.Contains(t, capture, "/indexes", "应触发创建索引")
	settings := capture["/indexes/posts/settings"].(map[string]any)
	assert.ElementsMatch(t, []any{"created_at", "post_id"}, settings["sortableAttributes"])
	assert.ElementsMatch(t, []any{"title", "full_text"}, settings["searchableAttributes"])
}
