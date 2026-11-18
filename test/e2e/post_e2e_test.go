// post 服务黑盒端到端测试：CreateDraft → UpdateDraft → UpdateDraftMedias → PublishDraft → 可见性 → 删除/封禁。
//
// 前置条件：post / media 服务 + consumer + poller 已部署（依赖 PostgreSQL / NATS JetStream / MinIO）。
// 发布用例必须用真上传的 ACTIVE 媒体：audit_post 会校验帖内媒体资产 ACTIVE，否则回退 DRAFT。
// 运行：
//
//	go test ./test/e2e/... -run TestPostServiceE2E -count=1          # 默认 http://localhost:8082
//	POST_E2E_ADDR=http://host:8082 go test ./test/e2e/... -run TestPostServiceE2E
//	go test -short ./...                                             # -short 跳过
//
// 注：e2e 产生的数据（帖子草稿/软删记录、outbox 消息）留在服务端数据库中不清理。
package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"

	commonv1 "rednote/gen/go/common/v1"
	mediav1 "rednote/gen/go/media/v1"
	postv1 "rednote/gen/go/post/v1"
	"rednote/gen/go/post/v1/postv1connect"
	"rednote/packages/errmsg"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const defaultPostAddr = "http://localhost:8082"

func postAddr() string { return e2eAddr("POST_E2E_ADDR", defaultPostAddr) }

// newPostClient 创建 connect 客户端，并探测服务可达性（不可达则跳过测试）
func newPostClient(t *testing.T) postv1connect.PostServiceClient {
	t.Helper()

	addr := postAddr()
	mustReachable(t, addr, "/post.v1.PostService/")
	return postv1connect.NewPostServiceClient(http.DefaultClient, addr)
}

// TestPostServiceE2E post 草稿生命周期黑盒测试。
// 步骤之间有依赖（同一帖子的状态流转链），因此在一个测试内按序执行：
//
//	建草稿 → 部分更新 → 全量换媒体 → 可见性（作者/他人）→ 发布 → 状态守卫
//	→ 内部查询 → 统计同步 → 删除（软删幂等）→ 封禁 → 错误路径
func TestPostServiceE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: -short 模式跳过依赖外部部署服务的测试")
	}

	client := newPostClient(t)
	ctx := context.Background()

	now := time.Now().UnixNano()
	authorUID := e2eID(now)
	otherUID := e2eID(now + 1)

	// 媒体必须真实上传并处理到 ACTIVE（~15s，并发）：伪造 asset_id 会在发布审核被判不可用而回退 DRAFT
	uploads := activeMediaFiles(t, 2, now+4)
	mediaA, mediaB := uploads[0], uploads[1]
	assetA, assetB := mediaA.GetAssetId(), mediaB.GetAssetId()

	// ---- 1. 创建草稿 ----
	create, err := client.CreateDraft(ctx, connect.NewRequest(&postv1.CreateDraftRequest{
		Uid:      authorUID,
		Title:    "e2e draft title",
		FullText: "e2e draft full text",
		Medias:   []*mediav1.MediaFile{mediaA},
		Metadata: &commonv1.JsonStr{Payload: `{"topic":"e2e"}`},
	}))
	require.NoError(t, err, "CreateDraft RPC")
	require.False(t, create.Msg.GetFail(), "创建草稿业务失败: %v", create.Msg.GetErr())
	assert.Nil(t, create.Msg.GetErr(), "成功响应不应携带错误码")

	// 全字段检测：CreateDraft 响应的 Post 每个字段都应与请求一致
	draft := create.Msg.GetDraft()
	require.NotNil(t, draft, "应返回草稿")
	postID := draft.GetPostId()
	require.NotEmpty(t, postID, "草稿应返回 post_id")
	assert.Equal(t, "e2e draft title", draft.GetTitle())
	assert.Equal(t, "e2e draft full text", draft.GetFullText())
	assert.Equal(t, authorUID, draft.GetAuthorId())
	assert.Equal(t, postv1.PostStatus_POST_STATUS_DRAFT, draft.GetStatus(), "新建应为 DRAFT")
	assert.Contains(t, draft.GetMetadata().GetPayload(), "e2e", "metadata 应落库")
	assert.NotEmpty(t, draft.GetCreatedAt(), "应返回创建时间")
	assert.NotEmpty(t, draft.GetUpdatedAt(), "应返回更新时间")
	require.NotNil(t, draft.GetPostStat(), "统计字段应存在")
	assert.Zero(t, draft.GetPostStat().GetLikeCount())
	assert.Zero(t, draft.GetPostStat().GetFavouriteCount())
	assert.Zero(t, draft.GetPostStat().GetCommentCount())
	assert.Zero(t, draft.GetPostStat().GetViewCount())

	// 全字段检测：首张即封面，媒体关联字段完整落库
	require.Len(t, draft.GetMedias(), 1)
	gotMedia := draft.GetMedias()[0]
	assert.Equal(t, mediaA.GetObjKey(), gotMedia.GetObjKey(), "首张即封面，媒体应落库")
	assert.Equal(t, assetA, gotMedia.GetAssetId())
	assert.Equal(t, mediaA.GetBase().GetId(), gotMedia.GetBase().GetId())
	assert.Equal(t, mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN, gotMedia.GetType())
	assert.Equal(t, mediaA.GetBucket(), gotMedia.GetBucket())
	assert.Equal(t, mediav1.MediaFormat_MEDIA_FORMAT_JPG, gotMedia.GetMeta().GetFormat())
	assert.Equal(t, mediaA.GetMeta().GetSizeBytes(), gotMedia.GetMeta().GetSizeBytes())
	t.Logf("创建草稿 post_id=%s author=%s", postID, authorUID)

	// ---- 2. 部分更新：只改 title，未传的 full_text 不变 ----
	upd, err := client.UpdateDraft(ctx, connect.NewRequest(&postv1.UpdateDraftRequest{
		Uid:    authorUID,
		PostId: postID,
		Title:  wrapperspb.String("e2e draft title v2"),
	}))
	require.NoError(t, err, "UpdateDraft RPC")
	require.False(t, upd.Msg.GetFail(), "更新草稿业务失败: %v", upd.Msg.GetErr())
	assert.Nil(t, upd.Msg.GetErr(), "成功响应不应携带错误码")
	assert.Equal(t, postID, upd.Msg.GetPost().GetPostId(), "应返回同一帖子")
	assert.Equal(t, "e2e draft title v2", upd.Msg.GetPost().GetTitle(), "title 应被更新")
	assert.Equal(t, "e2e draft full text", upd.Msg.GetPost().GetFullText(), "未传的 full_text 不应被修改")
	assert.Contains(t, upd.Msg.GetPost().GetMetadata().GetPayload(), "e2e", "未传的 metadata 不应被修改")

	// ---- 3. 全量换媒体：返回旧资产 ID 供 BFF 调整引用计数 ----
	medias, err := client.UpdateDraftMedias(ctx, connect.NewRequest(&postv1.UpdateDraftMediasRequest{
		Uid:    authorUID,
		PostId: postID,
		Medias: []*mediav1.MediaFile{mediaB},
	}))
	require.NoError(t, err, "UpdateDraftMedias RPC")
	require.False(t, medias.Msg.GetFail(), "更新媒体业务失败: %v", medias.Msg.GetErr())
	require.Len(t, medias.Msg.GetPost().GetMedias(), 1)
	assert.Equal(t, mediaB.GetObjKey(), medias.Msg.GetPost().GetMedias()[0].GetObjKey(), "媒体应被全量替换")
	assert.Equal(t, []string{assetA}, medias.Msg.GetOldAssetIds(), "应返回被替换的旧资产 ID")

	// ---- 4/5. 可见性：DRAFT 仅作者可见 ----
	authorView, err := client.ViewPost(ctx, connect.NewRequest(&postv1.ViewPostRequest{
		ViewerId: authorUID, PostId: postID,
	}))
	require.NoError(t, err, "作者 ViewPost RPC")
	require.False(t, authorView.Msg.GetFail(), "作者应可见自己的草稿: %v", authorView.Msg.GetErr())
	assert.Nil(t, authorView.Msg.GetErr(), "成功响应不应携带错误码")

	// 全字段检测：ViewPost 响应的 Post 应携带完整可展示数据
	viewed := authorView.Msg.GetPost()
	require.NotNil(t, viewed)
	assert.Equal(t, postID, viewed.GetPostId())
	assert.Equal(t, "e2e draft title v2", viewed.GetTitle())
	assert.Equal(t, "e2e draft full text", viewed.GetFullText())
	assert.Equal(t, authorUID, viewed.GetAuthorId())
	assert.Equal(t, postv1.PostStatus_POST_STATUS_DRAFT, viewed.GetStatus())
	require.Len(t, viewed.GetMedias(), 1)
	assert.Equal(t, mediaB.GetObjKey(), viewed.GetMedias()[0].GetObjKey(), "应展示替换后的媒体")
	assert.Equal(t, assetB, viewed.GetMedias()[0].GetAssetId())

	otherView, err := client.ViewPost(ctx, connect.NewRequest(&postv1.ViewPostRequest{
		ViewerId: otherUID, PostId: postID,
	}))
	require.NoError(t, err, "他人 ViewPost RPC")
	assertBizErr(t, otherView.Msg.GetFail(), otherView.Msg.GetErr(), errmsg.Forbidden, "他人看草稿")

	// ---- 6/7. 作者列表含草稿，他人列表只见已发布 ----
	mine, err := client.ViewPostByAuthor(ctx, connect.NewRequest(&postv1.ViewPostByAuthorRequest{
		ViewerId: authorUID, AuthorId: authorUID,
	}))
	require.NoError(t, err, "ViewPostByAuthor(作者) RPC")
	require.Len(t, mine.Msg.GetPosts(), 1, "作者列表应含自己的草稿")

	// 全字段检测：PostBrief 展示字段完整
	brief := mine.Msg.GetPosts()[0]
	assert.Equal(t, postID, brief.GetPostId())
	assert.Equal(t, "e2e draft title v2", brief.GetTitle())
	assert.Equal(t, authorUID, brief.GetAuthorId())
	assert.Equal(t, mediaB.GetObjKey(), brief.GetCover().GetObjKey(), "cover 应为首张媒体")
	assert.NotEmpty(t, brief.GetCreatedAt())
	require.NotNil(t, brief.GetStat(), "统计字段应存在")
	assert.Zero(t, brief.GetStat().GetLikeCount())
	assert.Zero(t, brief.GetStat().GetFavouriteCount())
	assert.Zero(t, brief.GetStat().GetCommentCount())
	assert.Zero(t, brief.GetStat().GetViewCount())
	// 分页口径：只有满页才返回游标（半页/空页返回空游标，调用方按空列表终止翻页）
	assert.Nil(t, mine.Msg.GetCursor(), "默认页大小 20、仅 1 条 → 半页不返回游标")
	fullPage, err := client.ViewPostByAuthor(ctx, connect.NewRequest(&postv1.ViewPostByAuthorRequest{
		ViewerId: authorUID, AuthorId: authorUID, Cursor: &commonv1.Cursor{Limit: 1},
	}))
	require.NoError(t, err, "ViewPostByAuthor(满页) RPC")
	require.Len(t, fullPage.Msg.GetPosts(), 1)
	require.NotNil(t, fullPage.Msg.GetCursor(), "满页应返回游标")
	nextPage, err := client.ViewPostByAuthor(ctx, connect.NewRequest(&postv1.ViewPostByAuthorRequest{
		ViewerId: authorUID, AuthorId: authorUID, Cursor: fullPage.Msg.GetCursor(),
	}))
	require.NoError(t, err, "ViewPostByAuthor(续翻) RPC")
	assert.Empty(t, nextPage.Msg.GetPosts(), "续翻到底应返回空页")
	assert.Nil(t, nextPage.Msg.GetCursor(), "空页返回空游标")

	theirs, err := client.ViewPostByAuthor(ctx, connect.NewRequest(&postv1.ViewPostByAuthorRequest{
		ViewerId: otherUID, AuthorId: authorUID,
	}))
	require.NoError(t, err, "ViewPostByAuthor(他人) RPC")
	assert.Empty(t, theirs.Msg.GetPosts(), "他人列表不应含未发布帖子")
	assert.Nil(t, theirs.Msg.GetCursor(), "空页应返回空游标")

	// ---- 8. 状态查询（无鉴权）----
	st, err := client.GetPostStatus(ctx, connect.NewRequest(&postv1.GetPostStatusRequest{PostId: postID}))
	require.NoError(t, err, "GetPostStatus RPC")
	require.False(t, st.Msg.GetFail())
	assert.Equal(t, postv1.PostStatus_POST_STATUS_DRAFT, st.Msg.GetStatus())

	// ---- 9. 发布：DRAFT → AUDITING，异步审核链路（当前占位实现自动通过）→ PUBLISHED ----
	pub, err := client.PublishDraft(ctx, connect.NewRequest(&postv1.PublishDraftRequest{
		Uid: authorUID, PostId: postID,
	}))
	require.NoError(t, err, "PublishDraft RPC")
	require.False(t, pub.Msg.GetFail(), "发布业务失败: %v", pub.Msg.GetErr())
	assert.Nil(t, pub.Msg.GetErr(), "成功响应不应携带错误码")

	// 状态流转经异步链路（poller 投递 AuditPost → post consumer 校验媒体 ACTIVE → PUBLISHED）；
	// 中间态 AUDITING 不可稳定观测，以轮询等终态代替即时断言。
	// 观测状态只记变化：testify 的 msgAndArgs 在调用时求值，把变量塞进失败信息永远只会打印零值。
	var lastStatus postv1.PostStatus
	require.Eventually(t, func() bool {
		got, err := client.GetPostStatus(ctx, connect.NewRequest(&postv1.GetPostStatusRequest{PostId: postID}))
		if err != nil || got.Msg.GetFail() {
			return false
		}
		if st := got.Msg.GetStatus(); st != lastStatus {
			lastStatus = st
			t.Logf("发布后状态 → %s", st)
		}
		return got.Msg.GetStatus() == postv1.PostStatus_POST_STATUS_PUBLISHED
	}, 60*time.Second, time.Second, "发布后应经审核链路到达 PUBLISHED（最近一次观测状态见上方日志）")

	// ---- 10. 状态守卫：非 DRAFT 不可再发布/编辑 ----
	pubAgain, err := client.PublishDraft(ctx, connect.NewRequest(&postv1.PublishDraftRequest{
		Uid: authorUID, PostId: postID,
	}))
	require.NoError(t, err, "重复发布 RPC")
	assertBizErr(t, pubAgain.Msg.GetFail(), pubAgain.Msg.GetErr(), errmsg.NotEditable, "重复发布")

	updAfter, err := client.UpdateDraft(ctx, connect.NewRequest(&postv1.UpdateDraftRequest{
		Uid:    authorUID,
		PostId: postID,
		Title:  wrapperspb.String("e2e draft title v3"),
	}))
	require.NoError(t, err, "审核中更新 RPC")
	assertBizErr(t, updAfter.Msg.GetFail(), updAfter.Msg.GetErr(), errmsg.NotEditable, "已发布帖子不可再编辑")

	// ---- 11. 已发布：对外公开可见 ----
	otherView2, err := client.ViewPost(ctx, connect.NewRequest(&postv1.ViewPostRequest{
		ViewerId: otherUID, PostId: postID,
	}))
	require.NoError(t, err, "已发布他人 ViewPost RPC")
	require.False(t, otherView2.Msg.GetFail(), "已发布帖子应对他人可见: %v", otherView2.Msg.GetErr())
	assert.Equal(t, postID, otherView2.Msg.GetPost().GetPostId())
	assert.Equal(t, "e2e draft title v2", otherView2.Msg.GetPost().GetTitle())

	authorView2, err := client.ViewPost(ctx, connect.NewRequest(&postv1.ViewPostRequest{
		ViewerId: authorUID, PostId: postID,
	}))
	require.NoError(t, err, "已发布作者 ViewPost RPC")
	require.False(t, authorView2.Msg.GetFail(), "作者应可见已发布的帖子: %v", authorView2.Msg.GetErr())

	// ---- 12. 内部接口：按 ID 批量获取（返回任意状态，逐项回填可对账）----
	batch, err := client.GetPostsByIDs(ctx, connect.NewRequest(&postv1.GetPostsByIDsRequest{
		PostIds: []string{postID, "bad", e2eID(now + 200)},
	}))
	require.NoError(t, err, "GetPostsByIDs RPC")
	require.Len(t, batch.Msg.GetResults(), 3)

	// 逐项语义：非法 ID 先返回且原样回填，未命中逐条返回不静默丢弃
	assert.Equal(t, "bad", batch.Msg.GetResults()[0].GetPostId(), "非法 ID 应原样回填")
	assert.True(t, batch.Msg.GetResults()[0].GetFail())
	assert.Equal(t, errmsg.InvalidID, batch.Msg.GetResults()[0].GetErr())

	// 全字段检测：内部接口返回完整帖子数据（含非公开状态）
	got := batch.Msg.GetResults()[1]
	assert.Equal(t, postID, got.GetPostId(), "应原样回填请求 ID")
	assert.False(t, got.GetFail())
	assert.Nil(t, got.GetErr())
	gotPost := got.GetPost()
	require.NotNil(t, gotPost)
	assert.Equal(t, postID, gotPost.GetPostId())
	assert.Equal(t, "e2e draft title v2", gotPost.GetTitle())
	assert.Equal(t, "e2e draft full text", gotPost.GetFullText())
	assert.Equal(t, authorUID, gotPost.GetAuthorId())
	assert.Equal(t, postv1.PostStatus_POST_STATUS_PUBLISHED, gotPost.GetStatus())

	assert.Equal(t, e2eID(now+200), batch.Msg.GetResults()[2].GetPostId(), "未命中应回填规范化 ID")
	assert.True(t, batch.Msg.GetResults()[2].GetFail())
	assert.Equal(t, errmsg.NotFound, batch.Msg.GetResults()[2].GetErr())

	// ---- 13. 统计同步：仅统计字段，不改状态 ----
	sync, err := client.SyncPostStat(ctx, connect.NewRequest(&postv1.SyncPostStatRequest{
		PostId:    postID,
		ViewCount: wrapperspb.Int64(5),
	}))
	require.NoError(t, err, "SyncPostStat RPC")
	require.False(t, sync.Msg.GetFail(), "统计同步业务失败: %v", sync.Msg.GetErr())

	// 计数列走短 TTL 读缓存、写路径不失效（陈旧 ≤ cache.stat_ttl，默认 1m ±10%）：
	// 面客读取需等缓存过期收敛，这里验证“最终一致”而不是假设写后立即读到新值。
	var lastView int64 = -1
	require.Eventually(t, func() bool {
		got, err := client.ViewPost(ctx, connect.NewRequest(&postv1.ViewPostRequest{
			ViewerId: authorUID, PostId: postID,
		}))
		if err != nil || got.Msg.GetFail() {
			return false
		}
		view := got.Msg.GetPost().GetPostStat().GetViewCount()
		if view != lastView {
			lastView = view
			t.Logf("view_count → %d（等待统计缓存收敛，上界 stat_ttl）", view)
		}
		return view == 5
	}, 90*time.Second, 2*time.Second, "统计缓存应在 ≤ stat_ttl 内收敛到同步后的新值（最近一次观测见上方日志）")

	authorView3, err := client.ViewPost(ctx, connect.NewRequest(&postv1.ViewPostRequest{
		ViewerId: authorUID, PostId: postID,
	}))
	require.NoError(t, err)
	stat := authorView3.Msg.GetPost().GetPostStat()
	assert.Equal(t, int64(5), stat.GetViewCount(), "浏览数应被同步")
	assert.Zero(t, stat.GetLikeCount(), "未传字段不应被修改")
	assert.Zero(t, stat.GetFavouriteCount(), "未传字段不应被修改")
	assert.Zero(t, stat.GetCommentCount(), "未传字段不应被修改")
	assert.Equal(t, postv1.PostStatus_POST_STATUS_PUBLISHED, authorView3.Msg.GetPost().GetStatus(), "统计同步不应改状态")

	// ---- 14. 删除（软删 + 幂等）----
	create2, err := client.CreateDraft(ctx, connect.NewRequest(&postv1.CreateDraftRequest{
		Uid:      authorUID,
		Title:    "e2e draft to delete",
		FullText: "e2e draft to delete content",
		// 该帖只走删除回收（不发布），无需真上传 ACTIVE 媒体
		Medias: []*mediav1.MediaFile{e2eMediaFile(e2eID(now+6), e2eID(now+7), "e2e/obj-c.jpg")},
	}))
	require.NoError(t, err, "CreateDraft(待删) RPC")
	require.False(t, create2.Msg.GetFail())
	post2ID := create2.Msg.GetDraft().GetPostId()
	require.NotEmpty(t, post2ID)

	delOther, err := client.DeletePost(ctx, connect.NewRequest(&postv1.DeletePostRequest{
		Uid: otherUID, PostId: post2ID,
	}))
	require.NoError(t, err, "他人删除 RPC")
	assertBizErr(t, delOther.Msg.GetFail(), delOther.Msg.GetErr(), errmsg.Forbidden, "他人删除帖子")

	del, err := client.DeletePost(ctx, connect.NewRequest(&postv1.DeletePostRequest{
		Uid: authorUID, PostId: post2ID,
	}))
	require.NoError(t, err, "DeletePost RPC")
	require.False(t, del.Msg.GetFail(), "删除业务失败: %v", del.Msg.GetErr())

	delAgain, err := client.DeletePost(ctx, connect.NewRequest(&postv1.DeletePostRequest{
		Uid: authorUID, PostId: post2ID,
	}))
	require.NoError(t, err, "重复删除 RPC")
	assert.False(t, delAgain.Msg.GetFail(), "重复删除应幂等成功")

	delView, err := client.ViewPost(ctx, connect.NewRequest(&postv1.ViewPostRequest{
		ViewerId: authorUID, PostId: post2ID,
	}))
	require.NoError(t, err, "已删除 ViewPost RPC")
	assertBizErr(t, delView.Msg.GetFail(), delView.Msg.GetErr(), errmsg.NotFound, "已删除帖子应不可见")

	// ---- 15. 封禁（内部管理）----
	ban, err := client.BanPost(ctx, connect.NewRequest(&postv1.BanPostRequest{PostId: postID}))
	require.NoError(t, err, "BanPost RPC")
	require.False(t, ban.Msg.GetFail(), "封禁业务失败: %v", ban.Msg.GetErr())

	banView, err := client.ViewPost(ctx, connect.NewRequest(&postv1.ViewPostRequest{
		ViewerId: authorUID, PostId: postID,
	}))
	require.NoError(t, err, "封禁后 ViewPost RPC")
	assertBizErr(t, banView.Msg.GetFail(), banView.Msg.GetErr(), errmsg.Banned, "封禁帖子应显式返回 Banned")

	// ---- 16. 错误路径 ----
	badID, err := client.GetPostStatus(ctx, connect.NewRequest(&postv1.GetPostStatusRequest{PostId: "bad"}))
	require.NoError(t, err, "非法 ID RPC")
	assertBizErr(t, badID.Msg.GetFail(), badID.Msg.GetErr(), errmsg.InvalidID, "非法 post_id")

	missing, err := client.GetPostStatus(ctx, connect.NewRequest(&postv1.GetPostStatusRequest{
		PostId: e2eID(now + 100),
	}))
	require.NoError(t, err, "不存在帖子 RPC")
	assertBizErr(t, missing.Msg.GetFail(), missing.Msg.GetErr(), errmsg.NotFound, "不存在的帖子")

	_, err = client.CreateDraft(ctx, connect.NewRequest(&postv1.CreateDraftRequest{
		Uid: authorUID, Title: "no media", FullText: "no media content",
	}))
	require.Error(t, err, "空媒体列表应被传输层拒绝")
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "空媒体列表应返回 InvalidArgument")
}
