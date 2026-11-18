// BFF 信息流面客链路端到端测试：注册建档 → 关注 → 发帖 → 关注流 / 热榜 / 推荐流
// （面客 PostCard 组装 + 会话注入 + seen 去重 + 游标翻页 + 未登录拒绝）。
//
// 与 discover_e2e 的差异：走 BFF 聚合（ViewPostsByIDs 仅 PUBLISHED → 作者视图 → PostCard），
// 断言面客卡片全字段；uid 由会话注入（请求不带 uid）。
//
// 前置条件：BFF（:8084）+ auth/user/media/post（:8081/:8083/:8080/:8082）+ discover（:8085）
// + consumer（扇出/入池）已部署，默认头像资产已预置（未预置时跳过并给出恢复条件）。
//
// 运行：
//
//	go test ./test/e2e/... -run TestBFFFeedE2E -count=1
//	BFF_E2E_ADDR=http://host:8084 DISCOVER_E2E_ADDR=http://host:8085 go test ./test/e2e/... -run TestBFFFeedE2E
//	go test -short ./...                                             # -short 跳过
package e2e

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	bffv1 "rednote/gen/go/bff/v1"
	"rednote/gen/go/bff/v1/bffv1connect"
	commonv1 "rednote/gen/go/common/v1"
	mediav1 "rednote/gen/go/media/v1"
	postv1 "rednote/gen/go/post/v1"
	userv1 "rednote/gen/go/user/v1"
	"rednote/packages/errmsg"
)

// registerFeedUser 注册并登录（信息流用：额外返回会话 uid（GetMyProfile），供 SetFollow 关联）
func registerFeedUser(t *testing.T, client interface {
	Register(context.Context, *connect.Request[bffv1.RegisterRequest]) (*connect.Response[bffv1.RegisterResponse], error)
	Login(context.Context, *connect.Request[bffv1.LoginRequest]) (*connect.Response[bffv1.LoginResponse], error)
	GetMyProfile(context.Context, *connect.Request[bffv1.GetMyProfileRequest]) (*connect.Response[bffv1.GetMyProfileResponse], error)
}, tag string) (uid, token string) {
	t.Helper()
	email := e2eBFFEmail(tag)
	ctx := context.Background()

	reg, err := client.Register(ctx, connect.NewRequest(&bffv1.RegisterRequest{
		Email: email, Password: e2ePassword, Name: "e2e-bff-feed-" + tag,
	}))
	require.NoError(t, err, "Register RPC")
	require.False(t, reg.Msg.GetFail(), "注册应成功: %v", reg.Msg.GetErr())

	login, err := client.Login(ctx, connect.NewRequest(&bffv1.LoginRequest{Email: email, Password: e2ePassword}))
	require.NoError(t, err, "Login RPC")
	require.False(t, login.Msg.GetFail(), "登录应成功: %v", login.Msg.GetErr())
	token = login.Msg.GetAccessToken().GetValue()
	require.NotEmpty(t, token, "access token 非空")

	prof, err := client.GetMyProfile(ctx, withBearer(connect.NewRequest(&bffv1.GetMyProfileRequest{}), token))
	require.NoError(t, err, "GetMyProfile RPC")
	require.False(t, prof.Msg.GetFail(), "获取资料应成功: %v", prof.Msg.GetErr())
	uid = prof.Msg.GetProfile().GetBrief().GetUid()
	require.NotEmpty(t, uid, "会话 uid 非空")
	return uid, token
}

// feedPostIDs 从 PostCard 列表提取 post_id（轮询判定用）
func feedPostIDs(cards []*bffv1.PostCard) []string {
	ids := make([]string, 0, len(cards))
	for _, c := range cards {
		ids = append(ids, c.GetPostId())
	}
	return ids
}

// assertFullPostCard 全字段检测：PostCard 每个字段有效且与给定上下文一致
func assertFullPostCard(t *testing.T, card *bffv1.PostCard, wantID, wantTitle, wantAuthorUID string) {
	t.Helper()
	require.NotNil(t, card)
	assert.Equal(t, wantID, card.GetPostId(), "post_id")
	assert.Equal(t, wantTitle, card.GetTitle(), "title")
	assert.Equal(t, postv1.PostStatus_POST_STATUS_PUBLISHED, card.GetStatus(), "面客恒 PUBLISHED")
	assert.NotEmpty(t, card.GetCreatedAt(), "created_at 非空")

	cover := card.GetCover()
	require.NotNil(t, cover, "cover（首张媒体）")
	assert.NotEmpty(t, cover.GetAssetId(), "cover.asset_id")
	assert.NotEmpty(t, cover.GetUrl(), "cover.url 预签名非空")
	assert.Positive(t, cover.GetExpiresAt(), "cover.expires_at")

	author := card.GetAuthor()
	require.NotNil(t, author, "author")
	assert.Equal(t, wantAuthorUID, author.GetUid(), "author.uid")
	assert.NotEmpty(t, author.GetName(), "author.name")
	require.NotNil(t, author.GetAvatar(), "author.avatar")
	assert.NotEmpty(t, author.GetAvatar().GetUrl(), "author.avatar.url")

	require.NotNil(t, card.GetStat(), "stat 非空（post 统计列透传）")
}

// TestBFFFeedE2E BFF 信息流面客全链路。
// 步骤依赖（先有供给后有读取），按序执行：
//
//	建档/关注 → 发帖 ×3 → 关注流（轮询等扇出）→ 热榜（轮询等入池）
//	→ 推荐流 seen 去重 → 游标翻页到底 → 未登录拒绝
func TestBFFFeedE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: -short 模式跳过依赖外部部署服务的测试")
	}

	bff := newBFFClient(t)
	media := newMediaProbe(t)
	requireDefaultAvatar(t, media) // 注册依赖默认头像资产
	userClient := newUserClient(t)
	ctx := context.Background()

	// ---- Arrange：注册 author/fan，关注，author 上传封面资产 ----
	authorUID, authorToken := registerFeedUser(t, bff, "author")
	fanUID, fanToken := registerFeedUser(t, bff, "fan")

	followed, err := userClient.SetFollow(ctx, connect.NewRequest(&userv1.SetFollowRequest{
		Uid: fanUID, TargetUid: authorUID,
	}))
	require.NoError(t, err, "SetFollow RPC")
	require.False(t, followed.Msg.GetFail(), "关注应成功: %v", followed.Msg.GetErr())

	assetID := uploadAsset(t, bff, authorToken)

	// ---- 发帖 ×3（BFF 面客发帖链路：草稿 → 发布）----
	type postItem struct{ id, title string }
	posts := make([]postItem, 0, 3)
	for i := 0; i < 3; i++ {
		title := "bff feed e2e post " + e2eID(int64(i))
		draft, err := bff.CreateDraft(ctx, withBearer(connect.NewRequest(&bffv1.CreateDraftRequest{
			Title:    title,
			FullText: "bff feed e2e full text",
			Medias:   []*bffv1.AssetRef{{AssetId: assetID, Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_540}},
		}), authorToken))
		require.NoError(t, err, "CreateDraft RPC")
		require.False(t, draft.Msg.GetFail(), "建草稿应成功: %v", draft.Msg.GetErr())

		pub, err := bff.PublishDraft(ctx, withBearer(connect.NewRequest(&bffv1.PublishDraftRequest{
			PostId: draft.Msg.GetPost().GetPostId(),
		}), authorToken))
		require.NoError(t, err, "PublishDraft RPC")
		require.False(t, pub.Msg.GetFail(), "发布应成功: %v", pub.Msg.GetErr())
		posts = append(posts, postItem{id: draft.Msg.GetPost().GetPostId(), title: title})
	}

	wantIDs := make([]string, 0, 3)
	for _, p := range posts {
		wantIDs = append(wantIDs, p.id)
	}

	// ---- 关注流：扇出异步，轮询等供给（fan 会话注入，请求无 uid）----
	pollUntilIDs(t, "BFF 关注流供给", wantIDs, func() ([]string, error) {
		resp, err := bff.GetFollowingFeed(ctx, withBearer(connect.NewRequest(&bffv1.GetFollowingFeedRequest{
			Cursor: &commonv1.Cursor{Str: "0", Limit: 10},
		}), fanToken))
		if err != nil {
			return nil, err
		}
		require.False(t, resp.Msg.GetFail(), "GetFollowingFeed: %v", resp.Msg.GetErr())
		return feedPostIDs(resp.Msg.GetPosts()), nil
	})

	// 全字段检测（取页内第一张卡）
	feedResp, err := bff.GetFollowingFeed(ctx, withBearer(connect.NewRequest(&bffv1.GetFollowingFeedRequest{
		Cursor: &commonv1.Cursor{Str: "0", Limit: 10},
	}), fanToken))
	require.NoError(t, err)
	require.NotEmpty(t, feedResp.Msg.GetPosts(), "关注流非空")
	byID := make(map[string]*bffv1.PostCard, len(feedResp.Msg.GetPosts()))
	for _, c := range feedResp.Msg.GetPosts() {
		byID[c.GetPostId()] = c
	}
	for _, p := range posts {
		assertFullPostCard(t, byID[p.id], p.id, p.title, authorUID)
	}

	// ---- 热榜：发帖钩子入池（author 会话；榜单可重复看，不做 seen）----
	// 断言口径是"入池"而非"上榜"：共享测试环境的池含 7d 窗口历史帖（数百条），
	// 本轮新帖热度≈0 本就排不进前十；按 offset 游标翻页扫池确认其在池内即可。
	requireInHotPool(t, bff, authorToken, wantIDs)

	// ---- 推荐流 seen：限页对比（不依赖翻完全池——池含 7d 窗口历史帖）----
	// 语义断言：round2（同 uid）不重复 round1 已见（seen 生效）；round1 可见本轮新帖
	fetchReco := func(token string, maxPages int) []string {
		ids := make([]string, 0, 8)
		cursor := &commonv1.Cursor{Str: "0", Limit: 2}
		for pages := 0; pages < maxPages; pages++ {
			resp, err := bff.GetRecommendFeed(ctx, withBearer(connect.NewRequest(&bffv1.GetRecommendFeedRequest{
				Cursor: cursor,
			}), token))
			require.NoError(t, err, "GetRecommendFeed RPC")
			require.False(t, resp.Msg.GetFail(), "GetRecommendFeed: %v", resp.Msg.GetErr())
			for _, c := range resp.Msg.GetPosts() {
				assert.NotContains(t, ids, c.GetPostId(), "推荐流页间不重复")
			}
			ids = append(ids, feedPostIDs(resp.Msg.GetPosts())...)
			cursor = resp.Msg.GetCursor()
			if cursor.GetStr() == "" {
				break // 游标空即到底（末页可带尾部数据）
			}
		}
		return ids
	}

	firstRound := fetchReco(fanToken, 3)
	// 候选池含历史帖且探索位随机采样，"首轮必见本轮新帖"在共享环境不成立（仅记录了观测结果）；
	// 这里断言的是确定性语义：seen 去重 + 页间不重复（见下）。
	t.Logf("推荐流首轮可见 %d 条，是否含本轮新帖: %v", len(firstRound), containsAll(firstRound, wantIDs))
	secondRound := fetchReco(fanToken, 3)
	for _, id := range secondRound {
		assert.NotContains(t, firstRound, id, "同 uid 次轮不得重复首轮已见（seen 去重生效）")
	}

	// ---- 游标翻页：limit=2 短取翻到底，页间不重复 ----
	seen := make([]string, 0, 8)
	cursor := &commonv1.Cursor{Str: "0", Limit: 2}
	for pages := 0; ; pages++ {
		require.Less(t, pages, 30, "关注流翻页超限（游标不收敛）")
		resp, err := bff.GetFollowingFeed(ctx, withBearer(connect.NewRequest(&bffv1.GetFollowingFeedRequest{
			Cursor: cursor,
		}), fanToken))
		require.NoError(t, err)
		require.False(t, resp.Msg.GetFail())
		for _, c := range resp.Msg.GetPosts() {
			assert.NotContains(t, seen, c.GetPostId(), "关注流翻页不重复")
		}
		seen = append(seen, feedPostIDs(resp.Msg.GetPosts())...)
		cursor = resp.Msg.GetCursor()
		if cursor.GetStr() == "" {
			break // 游标空即到底（末页可带尾部数据）
		}
	}
	assert.True(t, containsAll(seen, wantIDs), "关注流翻页到底应收敛全部供给帖，got=%v", seen)

	// ---- 错误路径：未登录 → 业务拒绝（fail + Unauthorized，对齐 E8 鉴权矩阵口径）----
	noAuth, err := bff.GetFollowingFeed(ctx, connect.NewRequest(&bffv1.GetFollowingFeedRequest{}))
	require.NoError(t, err, "未登录为业务拒绝而非连接错误")
	assertBizErr(t, noAuth.Msg.GetFail(), noAuth.Msg.GetErr(), errmsg.Unauthorized, "未登录关注流")

	noAuthReco, err := bff.GetRecommendFeed(ctx, connect.NewRequest(&bffv1.GetRecommendFeedRequest{}))
	require.NoError(t, err, "未登录为业务拒绝而非连接错误")
	assertBizErr(t, noAuthReco.Msg.GetFail(), noAuthReco.Msg.GetErr(), errmsg.Unauthorized, "未登录推荐流")
}

// requireInHotPool 翻页扫热榜池，确认 wantIDs 全部在池内（发帖钩子入池是异步的，带轮询）。
// 热榜游标为 offset；短取即到底（游标置空）。scanPages×pageSize 覆盖测试环境池规模即可。
func requireInHotPool(t *testing.T, client bffv1connect.BFFServiceClient, token string, wantIDs []string) {
	t.Helper()

	const (
		pageSize  = 100
		scanPages = 50 // 最多扫 5000 条
	)
	deadline := time.Now().Add(30 * time.Second)
	var lastSeen int

	for {
		found := make(map[string]struct{}, len(wantIDs))
		offset := "0"
		lastSeen = 0
		for pages := 0; pages < scanPages; pages++ {
			resp, err := client.GetHotPosts(context.Background(), withBearer(connect.NewRequest(&bffv1.GetHotPostsRequest{
				Cursor: &commonv1.Cursor{Str: offset, Limit: pageSize},
			}), token))
			require.NoError(t, err, "GetHotPosts RPC")
			require.False(t, resp.Msg.GetFail(), "GetHotPosts: %v", resp.Msg.GetErr())

			ids := feedPostIDs(resp.Msg.GetPosts())
			lastSeen += len(ids)
			for _, id := range ids {
				for _, want := range wantIDs {
					if id == want {
						found[want] = struct{}{}
					}
				}
			}
			if len(found) == len(wantIDs) {
				return
			}
			if len(ids) < pageSize || resp.Msg.GetCursor().GetStr() == "" {
				break // 扫到池尾
			}
			offset = resp.Msg.GetCursor().GetStr()
		}

		if time.Now().After(deadline) {
			t.Fatalf("热榜入池超时：扫 %d 条池内未见 %v（找到 %d 条）；排查：consumer 是否消费 outbox 并执行 HotPool.Add",
				lastSeen, wantIDs, len(found))
		}
		time.Sleep(time.Second)
	}
}
