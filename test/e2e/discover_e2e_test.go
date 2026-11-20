// discover 信息流黑盒端到端测试：发帖 → outbox 消费扇出/入池 → 关注流 / 热榜 / 推荐流 三条读路径。
//
// 前置条件（推流全链路涉及的部署件）：
//   - user 服务（:8083）：建用户档案（扇出依赖 GetUserProfile 判定，无档案则消费失败）
//   - post 服务（:8082）：发帖 RPC（真实走 outbox 消息）
//   - discover 服务（:8085）：信息流读 RPC
//   - consumer 进程：消费 post outbox（扇出收件箱/发件箱缓存 + 发帖钩子入热池）——
//     非 RPC 进程，不可达无法探测，扇出未完成时表现为轮询超时（排查方向见超时提示）
//   - 依赖：PostgreSQL / Redis / NATS JetStream（consumer 与 discover 共用）
//
// 运行：
//
//	go test ./test/e2e/... -run TestDiscoverFeedE2E -count=1          # 默认 http://localhost:8085 / :8082
//	DISCOVER_E2E_ADDR=http://host:8085 go test ./test/e2e/... -run TestDiscoverFeedE2E
//	go test -short ./...                                             # -short 跳过
//
// 注：e2e 产生的数据（帖子、扇出行、热池成员、seen 账本）留在服务端不清理。
package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"

	commonv1 "rednote/gen/go/common/v1"
	discoverv1 "rednote/gen/go/discover/v1"
	discoverv1connect "rednote/gen/go/discover/v1/discoverv1connect"
	mediav1 "rednote/gen/go/media/v1"
	postv1 "rednote/gen/go/post/v1"
	userv1 "rednote/gen/go/user/v1"
	"rednote/packages/errmsg"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const defaultDiscoverAddr = "http://localhost:8085"

func discoverAddr() string { return e2eAddr("DISCOVER_E2E_ADDR", defaultDiscoverAddr) }

// newDiscoverClient 创建 discover connect 客户端并探测可达性（不可达则跳过）
func newDiscoverClient(t *testing.T) discoverv1connect.DiscoverServiceClient {
	t.Helper()

	addr := discoverAddr()
	mustReachable(t, addr, "/discover.v1.DiscoverService/")
	return discoverv1connect.NewDiscoverServiceClient(http.DefaultClient, addr)
}

// pollUntilIDs 条件等待：每 500ms 轮询取数函数，直到包含全部 wantIDs 或超时。
// 禁 sleep 硬等——扇出/入池是异步消费，以结果出现为完成条件，超时携带排查提示。
func pollUntilIDs(t *testing.T, what string, wantIDs []string, fetch func() ([]string, error)) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	var got []string
	for time.Now().Before(deadline) {
		ids, err := fetch()
		require.NoError(t, err, "%s 轮询 RPC", what)
		got = ids
		if containsAll(ids, wantIDs) {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("%s 超时（30s）未见全部 %v（实际 %v）；排查：consumer 进程是否运行并消费 outbox",
		what, wantIDs, got)
}

func containsAll(got, want []string) bool {
	set := make(map[string]struct{}, len(got))
	for _, id := range got {
		set[id] = struct{}{}
	}
	for _, id := range want {
		if _, ok := set[id]; !ok {
			return false
		}
	}
	return true
}

// TestDiscoverFeedE2E 推流全链路：发帖 → 扇出/入池 → 三条流读取。
// 步骤间有依赖（先有供给才有读取），在一个测试内按序执行：
//
//	建用户 ×3 → 发帖 ×3 → 关注流推模式入箱（轮询）→ 翻页到底 → 热榜（钩子入池）
//	→ 推荐流 seen 去重（同 uid 复看为空 / 异 uid 可见）→ 契约错误路径
func TestDiscoverFeedE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: -short 模式跳过依赖外部部署服务的测试")
	}

	userClient := newUserClient(t)
	postClient := newPostClient(t)
	disc := newDiscoverClient(t)
	ctx := context.Background()

	now := time.Now().UnixNano()
	authorUID := e2eID(now)
	fanUID := e2eID(now + 1)
	otherUID := e2eID(now + 2)

	// ---- 0. Arrange：建用户档案（扇出/大V 判定依赖 GetUserProfile，无档案消费失败）；头像必填 ----
	for i, u := range []struct{ uid, name string }{
		{authorUID, "e2e-author"}, {fanUID, "e2e-fan"}, {otherUID, "e2e-other"},
	} {
		created, err := userClient.CreateUser(ctx, connect.NewRequest(&userv1.CreateUserRequest{
			Uid:    u.uid,
			Name:   u.name,
			Avatar: e2eMediaFile(e2eID(now+30+int64(i)), e2eID(now+40+int64(i)), "e2e/avatar.jpg"),
		}))
		require.NoError(t, err, "CreateUser RPC (%s)", u.uid)
		require.False(t, created.Msg.GetFail(), "建用户业务失败(%s): %v", u.uid, created.Msg.GetErr())
	}

	// 关注关系（扇出按粉丝列表写收件箱：author 的粉丝 = fan / other）
	for _, f := range [][2]string{{fanUID, authorUID}, {otherUID, authorUID}} {
		followed, err := userClient.SetFollow(ctx, connect.NewRequest(&userv1.SetFollowRequest{Uid: f[0], TargetUid: f[1]}))
		require.NoError(t, err, "SetFollow RPC (%s -> %s)", f[0], f[1])
		require.False(t, followed.Msg.GetFail(), "关注业务失败(%s->%s): %v", f[0], f[1], followed.Msg.GetErr())
	}

	// ---- 1. Arrange：author 发 3 帖（真实走 outbox → consumer 扇出/入池）----
	// 媒体必须真上传到 ACTIVE：audit_post 会校验帖内媒体资产，伪造 asset_id 会被回退 DRAFT、扇出永不发生
	feedMedias := activeMediaFiles(t, 3, now+20)
	newPostIDs := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		draft, err := postClient.CreateDraft(ctx, connect.NewRequest(&postv1.CreateDraftRequest{
			Uid:      authorUID,
			Title:    "e2e discover feed post " + e2eID(int64(i)),
			FullText: "e2e discover feed full text",
			Medias:   []*mediav1.MediaFile{feedMedias[i]},
		}))
		require.NoError(t, err, "CreateDraft RPC")
		require.False(t, draft.Msg.GetFail(), "创建草稿业务失败: %v", draft.Msg.GetErr())

		pub, err := postClient.PublishDraft(ctx, connect.NewRequest(&postv1.PublishDraftRequest{
			Uid: authorUID, PostId: draft.Msg.GetDraft().GetPostId(),
		}))
		require.NoError(t, err, "PublishDraft RPC")
		require.False(t, pub.Msg.GetFail(), "发布业务失败: %v", pub.Msg.GetErr())
		newPostIDs = append(newPostIDs, draft.Msg.GetDraft().GetPostId())
	}

	following := []string{authorUID}
	fetchFollowing := func() ([]string, error) {
		resp, err := disc.GetFollowingFeed(ctx, connect.NewRequest(&discoverv1.GetFollowingFeedRequest{
			Uid: fanUID, FollowingIds: following,
		}))
		if err != nil {
			return nil, err
		}
		require.False(t, resp.Msg.GetFail(), "GetFollowingFeed 业务失败: %v", resp.Msg.GetErr())
		return resp.Msg.GetPostIds(), nil
	}

	// ---- 2. 推模式入收件箱：扇出异步完成的判定 = 收件箱可见全部新帖 ----
	pollUntilIDs(t, "关注流收件箱供给", newPostIDs, fetchFollowing)

	// ---- 3. 关注边界：未关注的 uid → 供给为空（用独立 uid，避开已关注的 other）----
	noFollow, err := disc.GetFollowingFeed(ctx, connect.NewRequest(&discoverv1.GetFollowingFeedRequest{
		Uid: e2eID(now + 9),
	}))
	require.NoError(t, err, "GetFollowingFeed（无关注）RPC")
	require.False(t, noFollow.Msg.GetFail())
	assert.Empty(t, noFollow.Msg.GetPostIds(), "未关注任何作者应无供给")
	assert.Nil(t, noFollow.Msg.GetCursor(), "空页应无游标（到底）")

	// ---- 4. 翻页到底：页间不重不漏，短取 = 到底（游标置空）----
	firstPage, err := disc.GetFollowingFeed(ctx, connect.NewRequest(&discoverv1.GetFollowingFeedRequest{
		Uid: fanUID, FollowingIds: following,
		Cursor: &commonv1.Cursor{Str: "0", Limit: 2},
	}))
	require.NoError(t, err, "GetFollowingFeed 第一页 RPC")
	require.False(t, firstPage.Msg.GetFail())
	require.Len(t, firstPage.Msg.GetPostIds(), 2, "limit=2 应短取一页")
	require.NotNil(t, firstPage.Msg.GetCursor(), "未到底应带游标")

	seenIDs := append([]string{}, firstPage.Msg.GetPostIds()...)
	cursor := firstPage.Msg.GetCursor()
	for cursor.GetStr() != "" {
		page, err := disc.GetFollowingFeed(ctx, connect.NewRequest(&discoverv1.GetFollowingFeedRequest{
			Uid: fanUID, FollowingIds: following, Cursor: cursor,
		}))
		require.NoError(t, err, "GetFollowingFeed 翻页 RPC")
		require.False(t, page.Msg.GetFail())
		for _, id := range page.Msg.GetPostIds() {
			assert.NotContains(t, seenIDs, id, "翻页不重复: %s", id)
		}
		seenIDs = append(seenIDs, page.Msg.GetPostIds()...)
		cursor = page.Msg.GetCursor()
	}
	assert.ElementsMatch(t, newPostIDs, seenIDs, "翻页到底应收敛全部新帖（不漏）")

	// ---- 5. 热榜：热度降序全序翻页，不重不漏 ----
	// 口径：热池只由 poller 按 hot.refresh_interval（默认 10m）整池重建，发帖**不会**即时入池。
	// 因此"本轮新帖已在榜"不是确定性语义（10m 周期 vs 本用例秒级窗口），只记录观测；
	// 硬断言留给热榜自身的契约——顺序/游标全序翻页不重不漏。
	hotFirst, err := disc.GetHotPosts(ctx, connect.NewRequest(&discoverv1.GetHotPostsRequest{}))
	require.NoError(t, err, "GetHotPosts RPC")
	require.False(t, hotFirst.Msg.GetFail(), "GetHotPosts 业务失败: %v", hotFirst.Msg.GetErr())
	if len(hotFirst.Msg.GetPostIds()) == 0 {
		t.Logf("热榜池当前为空（poller 刷新周期未到），跳过热榜顺序断言")
	} else {
		t.Logf("热榜池可见 %d 条，是否含本轮新帖: %v", len(hotFirst.Msg.GetPostIds()),
			containsAll(hotFirst.Msg.GetPostIds(), newPostIDs))
	}

	hotSeen := make([]string, 0, 8)
	hotCursor := &commonv1.Cursor{Str: "0", Limit: 2}
	for {
		page, err := disc.GetHotPosts(ctx, connect.NewRequest(&discoverv1.GetHotPostsRequest{Cursor: hotCursor}))
		require.NoError(t, err, "GetHotPosts 翻页 RPC")
		require.False(t, page.Msg.GetFail())
		if len(page.Msg.GetPostIds()) == 0 && page.Msg.GetCursor().GetStr() == "" {
			break // 到底
		}
		for _, id := range page.Msg.GetPostIds() {
			assert.NotContains(t, hotSeen, id, "热榜翻页不重复: %s", id)
		}
		hotSeen = append(hotSeen, page.Msg.GetPostIds()...)
		hotCursor = page.Msg.GetCursor()
		if hotCursor.GetStr() == "" {
			break
		}
	}
	// 池非空时全序翻页必须收敛到成员（不重复由翻页循环里的 NotContains 保证）
	if len(hotFirst.Msg.GetPostIds()) > 0 {
		assert.NotEmpty(t, hotSeen, "热榜池非空时全序翻页应收敛到成员")
	}

	// ---- 6. 推荐流 seen 去重：同 uid 复看为空（全 seen 过滤），异 uid 可见 ----
	fetchReco := func(uid string) ([]string, error) {
		ids := make([]string, 0, 8)
		cursor := &commonv1.Cursor{Str: "0", Limit: 2}
		for {
			resp, err := disc.GetRecommendFeed(ctx, connect.NewRequest(&discoverv1.GetRecommendFeedRequest{
				Uid: uid, Cursor: cursor,
			}))
			if err != nil {
				return nil, err
			}
			require.False(t, resp.Msg.GetFail(), "GetRecommendFeed 业务失败: %v", resp.Msg.GetErr())
			for _, id := range resp.Msg.GetPostIds() {
				assert.NotContains(t, ids, id, "推荐流页间不重复: %s", id)
			}
			ids = append(ids, resp.Msg.GetPostIds()...)
			cursor = resp.Msg.GetCursor()
			if len(resp.Msg.GetPostIds()) == 0 && cursor.GetStr() == "" {
				break // 扫到池尾：批短且批内用尽才到底（短取 ≠ 到底）
			}
		}
		return ids, nil
	}

	firstRound, err := fetchReco(otherUID)
	require.NoError(t, err, "推荐流首轮")
	assert.True(t, containsAll(firstRound, newPostIDs), "首轮应可见全部本轮入池帖（池可含历史 e2e 帖）")

	secondRound, err := fetchReco(otherUID)
	require.NoError(t, err, "推荐流次轮（同 uid）")
	assert.Empty(t, secondRound, "同 uid 次轮应被 seen 全过滤（小池场景）")

	freshRound, err := fetchReco(fanUID)
	require.NoError(t, err, "推荐流（异 uid 无 seen）")
	assert.True(t, containsAll(freshRound, newPostIDs), "异 uid 无 seen 账本应可见全部本轮入池帖")

	// ---- 7. 契约错误路径（非法 uid / following_ids → InvalidArgument）----
	badUID, err := disc.GetFollowingFeed(ctx, connect.NewRequest(&discoverv1.GetFollowingFeedRequest{
		Uid: "not-a-uid",
	}))
	require.NoError(t, err, "GetFollowingFeed 非法 uid RPC")
	assertBizErr(t, badUID.Msg.GetFail(), badUID.Msg.GetErr(), errmsg.InvalidArgument, "关注流非法 uid")

	badFID, err := disc.GetFollowingFeed(ctx, connect.NewRequest(&discoverv1.GetFollowingFeedRequest{
		Uid: fanUID, FollowingIds: []string{"bad"},
	}))
	require.NoError(t, err, "GetFollowingFeed 非法 following_id RPC")
	assertBizErr(t, badFID.Msg.GetFail(), badFID.Msg.GetErr(), errmsg.InvalidArgument, "关注流非法 following_id")

	badRecoUID, err := disc.GetRecommendFeed(ctx, connect.NewRequest(&discoverv1.GetRecommendFeedRequest{Uid: "0"}))
	require.NoError(t, err, "GetRecommendFeed 非法 uid RPC")
	assertBizErr(t, badRecoUID.Msg.GetFail(), badRecoUID.Msg.GetErr(), errmsg.InvalidArgument, "推荐流非法 uid")
}
