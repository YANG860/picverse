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
