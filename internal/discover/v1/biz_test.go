package v1

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	commonv1 "rednote/gen/go/common/v1"
	discoverv1 "rednote/gen/go/discover/v1"
	"rednote/packages/errmsg"
	"rednote/packages/meili"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeMeili 覆写帖子搜索领域接口（未覆写方法调用会 panic），记录调用并支持错误注入。
// 配置/请求/解码/重排已收敛在 meili 包，此处只桩领域结果
type fakeMeili struct {
	meili.MeiliRepo

	result     *meili.PostSearchResult
	err        error
	gotQuery   string
	gotOffset  int64
	gotLimit   int64
	usedLatest bool
}

func (f *fakeMeili) SearchPosts(_ context.Context, query string, offset, limit int64) (*meili.PostSearchResult, error) {
	f.gotQuery, f.gotOffset, f.gotLimit = query, offset, limit
	f.usedLatest = false
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func (f *fakeMeili) SearchLatestPosts(_ context.Context, query string, offset, limit int64) (*meili.PostSearchResult, error) {
	f.gotQuery, f.gotOffset, f.gotLimit = query, offset, limit
	f.usedLatest = true
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func newTestUsecase(m meili.MeiliRepo) *DiscoverUsecaseImpl {
	return &DiscoverUsecaseImpl{Meili: m, log: slog.Default()}
}

// TestSearchPosts_Comprehensive 综合模式：走 SearchPosts（重排在 meili 包内），结果映射 + 透传
func TestSearchPosts_Comprehensive(t *testing.T) {
	m := &fakeMeili{result: &meili.PostSearchResult{
		Hits:       []meili.PostHit{{PostID: "p-new", Score: 0.3}, {PostID: "p-old", Score: 1.0}},
		NextOffset: 2,
	}}
	u := newTestUsecase(m)

	resp, err := u.SearchPosts(context.Background(), &discoverv1.SearchPostsRequest{Keyword: "kw"})
	require.NoError(t, err)
	require.False(t, resp.Fail)

	require.Len(t, resp.GetHits(), 2)
	assert.Equal(t, "p-new", resp.GetHits()[0].GetPostId(), "顺序保持 meili 侧（已重排）")
	assert.InDelta(t, 0.3, resp.GetHits()[0].GetScore(), 1e-9, "score 透传")
	assert.Equal(t, "kw", m.gotQuery)
	assert.False(t, m.usedLatest, "默认走综合排序")
	assert.Empty(t, m.gotOffset, "首页 offset 为 0")
}

// TestSearchPosts_Latest 最新模式：走 SearchLatestPosts，不重排
func TestSearchPosts_Latest(t *testing.T) {
	m := &fakeMeili{result: &meili.PostSearchResult{
		Hits:       []meili.PostHit{{PostID: "p1", Score: 0.1}, {PostID: "p2", Score: 0.9}},
		NextOffset: 2,
	}}
	u := newTestUsecase(m)

	resp, err := u.SearchPosts(context.Background(), &discoverv1.SearchPostsRequest{
		Keyword: "kw",
		Sort:    discoverv1.SearchSort_SEARCH_SORT_LATEST,
	})
	require.NoError(t, err)
	require.False(t, resp.Fail)

	require.Len(t, resp.GetHits(), 2)
	assert.Equal(t, "p1", resp.GetHits()[0].GetPostId(), "最新模式保持返回序不因 score 重排")
	assert.True(t, m.usedLatest, "最新排序走独立接口")
}

// TestSearchPosts_Cursor offset 游标推进与终止口径
func TestSearchPosts_Cursor(t *testing.T) {
	t.Run("游标按 NextOffset 推进", func(t *testing.T) {
		m := &fakeMeili{result: &meili.PostSearchResult{
			Hits:       []meili.PostHit{{PostID: "p1"}, {PostID: "p2"}},
			NextOffset: 7,
		}}
		u := newTestUsecase(m)

		resp, err := u.SearchPosts(context.Background(), &discoverv1.SearchPostsRequest{
			Keyword: "kw",
			Cursor:  &commonv1.Cursor{Str: "5", Limit: 10},
		})
		require.NoError(t, err)

		assert.Equal(t, int64(5), m.gotOffset)
		assert.Equal(t, int64(10), m.gotLimit)
		require.NotNil(t, resp.GetCursor())
		assert.Equal(t, "7", resp.GetCursor().GetStr())
		assert.Equal(t, int64(10), resp.GetCursor().GetLimit())
	})

	t.Run("空页即到底：不返回游标", func(t *testing.T) {
		m := &fakeMeili{result: &meili.PostSearchResult{NextOffset: 0}}
		u := newTestUsecase(m)

		resp, err := u.SearchPosts(context.Background(), &discoverv1.SearchPostsRequest{Keyword: "kw"})
		require.NoError(t, err)
		assert.Empty(t, resp.GetHits())
		assert.Nil(t, resp.GetCursor())
	})

	t.Run("坏命中跳过仍推进游标（NextOffset 含跳过口径）", func(t *testing.T) {
		m := &fakeMeili{result: &meili.PostSearchResult{
			Hits:       []meili.PostHit{{PostID: "p1"}}, // 坏命中已在 meili 包跳过
			NextOffset: 2,
		}}
		u := newTestUsecase(m)

		resp, err := u.SearchPosts(context.Background(), &discoverv1.SearchPostsRequest{Keyword: "kw"})
		require.NoError(t, err)
		require.Len(t, resp.GetHits(), 1)
		assert.Equal(t, "2", resp.GetCursor().GetStr())
	})
}

// TestSearchPosts_MeiliError 检索失败 → fail + InternalError（不外泄连接层错误）
func TestSearchPosts_MeiliError(t *testing.T) {
	m := &fakeMeili{err: errors.New("meili down")}
	u := newTestUsecase(m)

	resp, err := u.SearchPosts(context.Background(), &discoverv1.SearchPostsRequest{Keyword: "kw"})
	require.NoError(t, err)
	assert.True(t, resp.Fail)
	assert.Equal(t, errmsg.InternalError.Code, resp.GetErr().GetCode())
}
