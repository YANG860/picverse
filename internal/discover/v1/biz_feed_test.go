package v1

import (
	"context"
	"log/slog"
	"testing"
	"time"

	commonv1 "rednote/gen/go/common/v1"
	discoverv1 "rednote/gen/go/discover/v1"
	models "rednote/internal/discover/models"
	"rednote/packages/cache"
	"rednote/packages/logger"
	"rednote/packages/utils"
	"rednote/test/testdb"
	"rednote/test/testredis"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// feedLF GetFollowingFeed 测试用日志工厂（嵌接口，未覆写方法 panic 兜底）
type feedLF struct{ logger.LoggerFactory }

func (feedLF) GetLoggerOf(string) *slog.Logger { return slog.Default() }

// newFeedUsecase 真库收件箱 + 内存 Redis 发件箱缓存的关注流用例
func newFeedUsecase(t *testing.T) (*DiscoverUsecaseImpl, FanoutRepo) {
	t.Helper()
	db := testdb.Setup(t)
	fo := NewFanoutRepo(db)
	tl := cache.NewAuthorTimelineCache(testredis.New(), nil, feedLF{})
	return &DiscoverUsecaseImpl{Fanout: fo, Timeline: tl, log: slog.Default()}, fo
}

// feedReq 组装请求：uid=1，following_ids 由调用方给
func feedReq(following []string, cursorTS, cursorID, limit int64) *discoverv1.GetFollowingFeedRequest {
	req := &discoverv1.GetFollowingFeedRequest{Uid: "1", FollowingIds: following}
	if cursorTS > 0 || cursorID > 0 || limit > 0 {
		req.Cursor = &commonv1.Cursor{Str: utils.NewMergeCursor(cursorTS, cursorID, limit).GetStr(), Limit: limit}
	}
	return req
}

// TestMergeFeed 两路归并：跨路 (created_at, post_id) DESC、post_id 去重、top-limit 截断
func TestMergeFeed(t *testing.T) {
	inbox := []models.FanoutTimeline{
		{PostID: 3, PostCreatedAt: 300},
		{PostID: 1, PostCreatedAt: 100},
	}
	pull := []cache.TimelineEntry{
		{PostID: 4, CreatedAt: 400},
		{PostID: 2, CreatedAt: 200},
		{PostID: 3, CreatedAt: 300}, // 与收件箱重复（边界双写）→ 去重兜底
	}

	merged := mergeFeed(inbox, pull, 10)
	require.Equal(t, []cache.TimelineEntry{
		{PostID: 4, CreatedAt: 400}, {PostID: 3, CreatedAt: 300},
		{PostID: 2, CreatedAt: 200}, {PostID: 1, CreatedAt: 100},
	}, merged, "跨路倒序 + 去重")

	require.Len(t, mergeFeed(inbox, pull, 3), 3, "top-limit 截断")
}

// TestGetFollowingFeedInboxOnly 纯收件箱路（无 pull 供给）：排序 + 游标翻页 + 到底置空
func TestGetFollowingFeedInboxOnly(t *testing.T) {
	u, fo := newFeedUsecase(t)
	ctx := context.Background()

	// uid=1 收件箱 5 条：(ts, post_id) = (t-400,1)(t-350,2)(t-300,3)(t-200,4)(t-100,5)（时间戳须在承诺窗口内）
	t0 := time.Now().Unix()
	for _, p := range []struct{ postID, ts int64 }{{1, t0 - 400}, {2, t0 - 350}, {3, t0 - 300}, {4, t0 - 200}, {5, t0 - 100}} {
		require.NoError(t, fo.WriteInbox(ctx, p.postID, 500, p.ts, []int64{1}))
	}

	// 首页 limit=2：top-2 + 满页游标
	res, err := u.GetFollowingFeed(ctx, feedReq(nil, 0, 0, 2))
	require.NoError(t, err)
	require.False(t, res.GetFail())
	require.Equal(t, []string{"5", "4"}, res.GetPostIds())
	require.NotNil(t, res.GetCursor(), "满页须给游标")

	// 翻页：严格早于游标 (t-200,4) → (t-300,3)(t-350,2)
	ts, id, _ := utils.ParseMergeCursor(res.GetCursor())
	res, err = u.GetFollowingFeed(ctx, feedReq(nil, ts, id, 2))
	require.NoError(t, err)
	require.Equal(t, []string{"3", "2"}, res.GetPostIds())

	// 末页：剩 (t-400,1) 一条 < limit → 短页即到底（游标置空）
	ts, id, _ = utils.ParseMergeCursor(res.GetCursor())
	res, err = u.GetFollowingFeed(ctx, feedReq(nil, ts, id, 2))
	require.NoError(t, err)
	require.Equal(t, []string{"1"}, res.GetPostIds())
	require.Nil(t, res.GetCursor(), "短页即到底")
}

// TestGetFollowingFeedMergePull 两路归并：收件箱（普通作者）× 发件箱缓存（大V）跨路排序
func TestGetFollowingFeedMergePull(t *testing.T) {
	u, fo := newFeedUsecase(t)
	ctx := context.Background()

	// 普通作者 500：扇出进收件箱；大V 2002：只在发件箱缓存（写侧分流，不扇出）
	t0 := time.Now().Unix()
	require.NoError(t, fo.WriteInbox(ctx, 1, 500, t0-400, []int64{1}))
	require.NoError(t, fo.WriteInbox(ctx, 3, 500, t0-200, []int64{1}))
	require.NoError(t, u.Timeline.Append(ctx, 2002, 2, t0-300))
	require.NoError(t, u.Timeline.Append(ctx, 2002, 4, t0-100))

	res, err := u.GetFollowingFeed(ctx, feedReq([]string{"2002"}, 0, 0, 10))
	require.NoError(t, err)
	require.False(t, res.GetFail())
	require.Equal(t, []string{"4", "3", "2", "1"}, res.GetPostIds(), "跨路归并排序")
	require.Nil(t, res.GetCursor(), "短页即到底")
}

// TestGetFollowingFeedDedup 同帖双路供给（边界双写）→ post_id 去重兜底（宁重不漏）
func TestGetFollowingFeedDedup(t *testing.T) {
	u, fo := newFeedUsecase(t)
	ctx := context.Background()

	t0 := time.Now().Unix()
	require.NoError(t, fo.WriteInbox(ctx, 2, 500, t0-100, []int64{1})) // 溢出/降级边界双写
	require.NoError(t, u.Timeline.Append(ctx, 2002, 2, t0-100))

	res, err := u.GetFollowingFeed(ctx, feedReq([]string{"2002"}, 0, 0, 10))
	require.NoError(t, err)
	require.Equal(t, []string{"2"}, res.GetPostIds(), "同帖去重后一条")
}

// TestGetFollowingFeedMissSkip miss 作者（普通作者/超窗不活跃）分流收件箱路，不阻塞 pull 归并
func TestGetFollowingFeedMissSkip(t *testing.T) {
	u, fo := newFeedUsecase(t)
	ctx := context.Background()

	t0 := time.Now().Unix()
	require.NoError(t, fo.WriteInbox(ctx, 1, 500, t0-200, []int64{1}))
	require.NoError(t, u.Timeline.Append(ctx, 2002, 2, t0-100))

	// "9001" 未缓存（普通作者）→ 跳过；"2002" pull 供给
	res, err := u.GetFollowingFeed(ctx, feedReq([]string{"9001", "2002"}, 0, 0, 10))
	require.NoError(t, err)
	require.Equal(t, []string{"2", "1"}, res.GetPostIds())
}

// TestGetFollowingFeedInvalidUID 非法 uid → fail 信封（InvalidArgument）
func TestGetFollowingFeedInvalidUID(t *testing.T) {
	u, _ := newFeedUsecase(t)
	req := &discoverv1.GetFollowingFeedRequest{Uid: "not-a-number"}
	res, err := u.GetFollowingFeed(context.Background(), req)
	require.NoError(t, err)
	require.True(t, res.GetFail())
}

// TestMergeCursorRoundTrip 复合游标编解码往返 + 非法回首页（与 ParseOffsetCursor 同口径）
func TestMergeCursorRoundTrip(t *testing.T) {
	c := utils.NewMergeCursor(1700000000, 42, 20)
	ts, id, limit := utils.ParseMergeCursor(c)
	require.Equal(t, int64(1700000000), ts)
	require.Equal(t, int64(42), id)
	require.Equal(t, int64(20), limit)

	// 空 / 非法 → 零值首页
	for _, bad := range []*commonv1.Cursor{nil, {}, {Str: "junk"}, {Str: "1:"}, {Str: ":2"}} {
		ts, id, _ = utils.ParseMergeCursor(bad)
		require.Zero(t, ts)
		require.Zero(t, id)
	}
}

// newHotUsecase 带热池/账本的用例组装（testredis 真身；推荐流参数固定小值便于断言）
func newHotUsecase(t *testing.T) *DiscoverUsecaseImpl {
	t.Helper()
	tl := cache.NewAuthorTimelineCache(testredis.New(), nil, feedLF{})
	hp := cache.NewHotPoolCache(testredis.New(), nil)
	seen := cache.NewSeenCache(testredis.New(), nil)
	return &DiscoverUsecaseImpl{
		Timeline: tl, HotPool: hp, Seen: seen, log: slog.Default(),
		recommendScanFactor: 3, recommendScatterWindow: 3, recommendExploreRatio: 0.1,
	}
}

// TestGetHotPosts 热榜翻页：热度降序 + offset 游标往返 + 短取即到底（游标置空）
func TestGetHotPosts(t *testing.T) {
	u := newHotUsecase(t)
	ctx := context.Background()
	require.NoError(t, u.HotPool.Rebuild(ctx, []cache.HotEntry{
		{PostID: 11, AuthorID: 1, Score: 1}, {PostID: 55, AuthorID: 2, Score: 5}, {PostID: 33, AuthorID: 3, Score: 3},
	}))

	// 首页（limit=2）：热度降序，满页续游标
	resp, err := u.GetHotPosts(ctx, &discoverv1.GetHotPostsRequest{Cursor: &commonv1.Cursor{Limit: 2}})
	require.NoError(t, err)
	require.False(t, resp.GetFail())
	assert.Equal(t, []string{"55", "33"}, resp.GetPostIds())
	require.NotNil(t, resp.GetCursor(), "满页续游标")

	// 次页：短取（1 条 < limit）→ 到底（游标置空）
	resp, err = u.GetHotPosts(ctx, &discoverv1.GetHotPostsRequest{Cursor: resp.GetCursor()})
	require.NoError(t, err)
	assert.Equal(t, []string{"11"}, resp.GetPostIds())
	assert.Nil(t, resp.GetCursor(), "短取即到底")
}

// TestGetHotPostsEmpty 池空（未维护/停摆消亡）：降级空页（fail-soft），非错误
func TestGetHotPostsEmpty(t *testing.T) {
	u := newHotUsecase(t)
	resp, err := u.GetHotPosts(context.Background(), &discoverv1.GetHotPostsRequest{Cursor: &commonv1.Cursor{Limit: 10}})
	require.NoError(t, err)
	assert.False(t, resp.GetFail())
	assert.Empty(t, resp.GetPostIds())
	assert.Nil(t, resp.GetCursor())
}

// recommendReq 组装请求：uid=1
func recommendReq(cursorOffset, limit int64) *discoverv1.GetRecommendFeedRequest {
	req := &discoverv1.GetRecommendFeedRequest{Uid: "1"}
	if cursorOffset > 0 || limit > 0 {
		req.Cursor = &commonv1.Cursor{Str: utils.NewOffsetCursor(cursorOffset, limit).GetStr(), Limit: limit}
	}
	return req
}

// TestGetRecommendFeed 基本流：seen 去重翻页 + 曝光记账 + 探索位页尾（quota=limit-1）
func TestGetRecommendFeed(t *testing.T) {
	u := newHotUsecase(t)
	ctx := context.Background()
	require.NoError(t, u.HotPool.Rebuild(ctx, []cache.HotEntry{
		{PostID: 1, AuthorID: 10, Score: 5},
		{PostID: 2, AuthorID: 20, Score: 4},
		{PostID: 3, AuthorID: 30, Score: 3},
		{PostID: 4, AuthorID: 40, Score: 2},
		{PostID: 5, AuthorID: 50, Score: 1},
	}))

	// 首页 limit=3：正文 quota=2（1,2）+ 探索位 1 条页尾；满扫（3 < 6=scanBatch？不——扫 3×3=9 → 全池 5 条 < 9 → 批短）
	resp, err := u.GetRecommendFeed(ctx, recommendReq(0, 3))
	require.NoError(t, err)
	require.False(t, resp.GetFail())
	assert.Equal(t, []string{"1", "2"}, resp.GetPostIds()[:2], "正文按热度序")
	assert.Len(t, resp.GetPostIds(), 3, "正文 2 + 探索位 1")
	assert.NotNil(t, resp.GetCursor(), "批短但批内未用尽（3/5）→ 续游标")

	// 二次请求：已曝光的不再出现（1,2 + 探索位已记账）
	seen, err := u.Seen.Filter(ctx, 1, []int64{1, 2, 3, 4, 5})
	require.NoError(t, err)
	assert.True(t, seen[0] && seen[1], "产出即记账")
}

// TestGetRecommendFeedScatter 同作者打散：滑窗内同作者不相邻（quota=3）
func TestGetRecommendFeedScatter(t *testing.T) {
	u := newHotUsecase(t)
	ctx := context.Background()
	require.NoError(t, u.HotPool.Rebuild(ctx, []cache.HotEntry{
		{PostID: 1, AuthorID: 10, Score: 5},
		{PostID: 2, AuthorID: 10, Score: 4}, // 同作者连续
		{PostID: 3, AuthorID: 30, Score: 3},
		{PostID: 4, AuthorID: 40, Score: 2},
		{PostID: 5, AuthorID: 50, Score: 1},
	}))

	// limit=4 → quota=3：(1,10)(2,10)(3,30) → 打散 2 号与 3 号换位
	resp, err := u.GetRecommendFeed(ctx, recommendReq(0, 4))
	require.NoError(t, err)
	assert.Equal(t, []string{"1", "3", "2"}, resp.GetPostIds()[:3], "窗口内同作者不相邻")
}

// TestGetRecommendFeedEnd 到底语义：扫到池尾（批短且批内用尽）→ 游标置空；
// 批内未用尽 → 续游标继续（短取 ≠ 到底）
func TestGetRecommendFeedEnd(t *testing.T) {
	u := newHotUsecase(t)
	ctx := context.Background()
	require.NoError(t, u.HotPool.Rebuild(ctx, []cache.HotEntry{
		{PostID: 1, AuthorID: 10, Score: 5},
		{PostID: 2, AuthorID: 20, Score: 4},
	}))

	// limit=10：scanBatch=30 > 池 2 条 → 批短；quota=9 收 2 条 → 批内用尽 → 到底
	resp, err := u.GetRecommendFeed(ctx, recommendReq(0, 10))
	require.NoError(t, err)
	assert.Len(t, resp.GetPostIds(), 2)
	assert.Nil(t, resp.GetCursor(), "扫尾到底，游标置空")

	// 空池后续：空页空游标
	resp, err = u.GetRecommendFeed(ctx, recommendReq(2, 10))
	require.NoError(t, err)
	assert.Empty(t, resp.GetPostIds())
	assert.Nil(t, resp.GetCursor())
}

// TestGetRecommendFeedInvalidUID 非法 uid 拒绝
func TestGetRecommendFeedInvalidUID(t *testing.T) {
	u := newHotUsecase(t)
	resp, err := u.GetRecommendFeed(context.Background(), &discoverv1.GetRecommendFeedRequest{Uid: "abc"})
	require.NoError(t, err)
	assert.True(t, resp.GetFail())
}
