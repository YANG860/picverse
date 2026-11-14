package v1

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	commonv1 "rednote/gen/go/common/v1"
	mediav1 "rednote/gen/go/media/v1"
	postv1 "rednote/gen/go/post/v1"
	common "rednote/internal/common/models"
	models "rednote/internal/post/models"
	"rednote/packages/defaults"
	"rednote/packages/errmsg"
	"rednote/test/testdb"
	"rednote/test/testmq"
	"rednote/test/testsnowflake"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// newUsecase 构造 biz 层测试环境：真实 repo + testdb 克隆库 + testmq 内存模拟。
// seedPost / seedPostMedia / newMediaFile 复用 repo_test.go 中的同包 helper。
func newUsecase(t *testing.T) (*PostUsecaseImpl, *gorm.DB, *testmq.Client) {
	t.Helper()
	testsnowflake.InitTestSnoflake(4)
	db := testdb.Setup(t)
	mqc := testmq.New()
	return NewPostUsecase(NewPostRepo(db), mqc), db, mqc
}

// mediaProto 构造完整媒体 proto（写入口入参）
func mediaProto(id, assetID int64) *mediav1.MediaFile {
	return &mediav1.MediaFile{
		Base:    &commonv1.BaseModel{Id: strconv.FormatInt(id, 10)},
		AssetId: strconv.FormatInt(assetID, 10),
		Type:    mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN,
		ObjKey:  "post/obj",
		Bucket:  "rednote",
		Meta: &mediav1.MediaMetaData{
			Sha_256Sum: "abc",
			SizeBytes:  1024,
			Format:     mediav1.MediaFormat_MEDIA_FORMAT_WEBP,
			Height:     100,
			Width:      200,
		},
	}
}

// assertSeededPost 断言 seedPost 造出的帖子经 ToProto 后的全字段
func assertSeededPost(t *testing.T, p *postv1.Post, id int64, status postv1.PostStatus) {
	t.Helper()
	require.NotNil(t, p)
	assert.Equal(t, strconv.FormatInt(id, 10), p.GetPostId())
	assert.Equal(t, strconv.FormatInt(authorID1, 10), p.GetAuthorId())
	assert.Equal(t, "标题", p.GetTitle())
	assert.Equal(t, "正文", p.GetFullText())
	assert.Equal(t, status, p.GetStatus())
	assert.NotEmpty(t, p.GetCreatedAt())
	assert.NotEmpty(t, p.GetUpdatedAt())
	require.NotNil(t, p.GetPostStat())
	assert.Equal(t, int64(1), p.GetPostStat().GetLikeCount())
	assert.Equal(t, int64(2), p.GetPostStat().GetFavouriteCount())
	assert.Equal(t, int64(3), p.GetPostStat().GetCommentCount())
	assert.Equal(t, int64(4), p.GetPostStat().GetViewCount())
	require.NotNil(t, p.GetMetadata())
	assert.JSONEq(t, `{"k":"v"}`, p.GetMetadata().GetPayload())
}

// assertMediaProto 断言媒体 proto 全字段（对应 newMediaFile/mediaProto 的构造值）
func assertMediaProto(t *testing.T, m *mediav1.MediaFile, wantID, wantAssetID int64) {
	t.Helper()
	require.NotNil(t, m)
	assert.Equal(t, strconv.FormatInt(wantID, 10), m.GetBase().GetId())
	assert.Equal(t, strconv.FormatInt(wantAssetID, 10), m.GetAssetId())
	assert.Equal(t, mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN, m.GetType())
	assert.Equal(t, "post/obj", m.GetObjKey())
	assert.Equal(t, "rednote", m.GetBucket())
	assert.Equal(t, "abc", m.GetMeta().GetSha_256Sum())
	assert.Equal(t, int64(1024), m.GetMeta().GetSizeBytes())
	assert.Equal(t, mediav1.MediaFormat_MEDIA_FORMAT_WEBP, m.GetMeta().GetFormat())
	assert.Equal(t, int64(100), m.GetMeta().GetHeight())
	assert.Equal(t, int64(200), m.GetMeta().GetWidth())
}

// mustParseID 将 proto 中的字符串 ID 解析为 int64
func mustParseID(t *testing.T, id string) int64 {
	t.Helper()
	parsed, err := strconv.ParseInt(id, 10, 64)
	require.NoError(t, err)
	return parsed
}

// decodePostMessage 解码 MQ 消息体为 PostMessage
func decodePostMessage(t *testing.T, payload []byte) *postv1.PostMessage {
	t.Helper()
	m := &postv1.PostMessage{}
	require.NoError(t, proto.Unmarshal(payload, m))
	return m
}

// ---------- CreateDraft ----------

func TestPostUsecase_CreateDraft(t *testing.T) {
	ctx := context.Background()
	uid := strconv.FormatInt(authorID1, 10)

	t.Run("非法 uid 被拒绝", func(t *testing.T) {
		uc, db, mqc := newUsecase(t)
		resp, err := uc.CreateDraft(ctx, &postv1.CreateDraftRequest{
			Uid: "bad", Title: "标题", FullText: "正文", Medias: []*mediav1.MediaFile{mediaProto(4001, 6001)},
		})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetErr())
		assert.Nil(t, resp.GetDraft(), "失败响应不应携带草稿")

		var cnt int64
		require.NoError(t, db.Model(&models.PostDetail{}).Count(&cnt).Error)
		assert.Zero(t, cnt)
		assert.Zero(t, mqc.Count())
	})

	t.Run("标题为空被拒绝", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		resp, err := uc.CreateDraft(ctx, &postv1.CreateDraftRequest{
			Uid: uid, Title: "", FullText: "正文", Medias: []*mediav1.MediaFile{mediaProto(4001, 6001)},
		})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidTitle, resp.GetErr())
		assert.Nil(t, resp.GetDraft())
	})

	t.Run("超长标题被拒绝（101 rune）", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		resp, err := uc.CreateDraft(ctx, &postv1.CreateDraftRequest{
			Uid: uid, Title: strings.Repeat("好", 101), FullText: "正文", Medias: []*mediav1.MediaFile{mediaProto(4001, 6001)},
		})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidTitle, resp.GetErr())
	})

	t.Run("正文为空被拒绝", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		resp, err := uc.CreateDraft(ctx, &postv1.CreateDraftRequest{
			Uid: uid, Title: "标题", FullText: "", Medias: []*mediav1.MediaFile{mediaProto(4001, 6001)},
		})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidContent, resp.GetErr())
	})

	t.Run("超长正文被拒绝（1001 rune）", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		resp, err := uc.CreateDraft(ctx, &postv1.CreateDraftRequest{
			Uid: uid, Title: "标题", FullText: strings.Repeat("字", 1001), Medias: []*mediav1.MediaFile{mediaProto(4001, 6001)},
		})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidContent, resp.GetErr())
	})

	t.Run("非法 metadata 被拒绝", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		resp, err := uc.CreateDraft(ctx, &postv1.CreateDraftRequest{
			Uid: uid, Title: "标题", FullText: "正文", Medias: []*mediav1.MediaFile{mediaProto(4001, 6001)},
			Metadata: &commonv1.JsonStr{Payload: "not-json"},
		})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidArgument, resp.GetErr())
	})

	t.Run("媒体为空被拒绝", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		resp, err := uc.CreateDraft(ctx, &postv1.CreateDraftRequest{Uid: uid, Title: "标题", FullText: "正文"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidMedia, resp.GetErr())
	})

	t.Run("媒体缺对象键被拒绝", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		bad := mediaProto(4001, 6001)
		bad.ObjKey = ""
		resp, err := uc.CreateDraft(ctx, &postv1.CreateDraftRequest{
			Uid: uid, Title: "标题", FullText: "正文", Medias: []*mediav1.MediaFile{bad},
		})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidMedia, resp.GetErr())
	})

	t.Run("媒体 proto 不完整被拒绝", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		bad := mediaProto(4001, 6001)
		bad.Base = nil // MediaFileFromProto 失败
		resp, err := uc.CreateDraft(ctx, &postv1.CreateDraftRequest{
			Uid: uid, Title: "标题", FullText: "正文", Medias: []*mediav1.MediaFile{bad},
		})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidMedia, resp.GetErr())
	})

	t.Run("创建成功：全字段落库 + 全字段响应", func(t *testing.T) {
		uc, db, mqc := newUsecase(t)
		resp, err := uc.CreateDraft(ctx, &postv1.CreateDraftRequest{
			Uid: uid, Title: "标题", FullText: "正文",
			Medias:   []*mediav1.MediaFile{mediaProto(4001, 6001), mediaProto(4002, 6002)},
			Metadata: &commonv1.JsonStr{Payload: `{"theme":"dark"}`},
		})
		require.NoError(t, err)
		require.False(t, resp.GetFail())
		assert.Nil(t, resp.GetErr())

		// 响应：草稿全字段
		draft := resp.GetDraft()
		require.NotNil(t, draft)
		parsedID, err := strconv.ParseInt(draft.GetPostId(), 10, 64)
		require.NoError(t, err)
		assert.Equal(t, authorID1, mustParseID(t, draft.GetAuthorId()))
		assert.Equal(t, "标题", draft.GetTitle())
		assert.Equal(t, "正文", draft.GetFullText())
		assert.Equal(t, postv1.PostStatus_POST_STATUS_DRAFT, draft.GetStatus())
		assert.NotEmpty(t, draft.GetCreatedAt())
		assert.NotEmpty(t, draft.GetUpdatedAt())
		require.NotNil(t, draft.GetPostStat())
		assert.Zero(t, draft.GetPostStat().GetLikeCount(), "新建草稿统计应全 0")
		assert.Zero(t, draft.GetPostStat().GetFavouriteCount())
		assert.Zero(t, draft.GetPostStat().GetCommentCount())
		assert.Zero(t, draft.GetPostStat().GetViewCount())
		require.NotNil(t, draft.GetMetadata())
		assert.JSONEq(t, `{"theme":"dark"}`, draft.GetMetadata().GetPayload())

		// 响应：媒体全字段且按传入顺序（首张即封面）
		require.Len(t, draft.GetMedias(), 2)
		assertMediaProto(t, draft.GetMedias()[0], 4001, 6001)
		assertMediaProto(t, draft.GetMedias()[1], 4002, 6002)

		// DB：posts 与 post_medias 均落库，sort_order 从 0 递增
		var got models.PostDetail
		require.NoError(t, db.Where("id = ?", parsedID).First(&got).Error)
		assert.Equal(t, authorID1, got.AuthorID)
		assert.Equal(t, datatypes.JSONMap{"theme": "dark"}, got.MetaData)

		var rows []models.PostMedia
		require.NoError(t, db.Where("post_id = ?", parsedID).Order("sort_order").Find(&rows).Error)
		require.Len(t, rows, 2)
		assert.Equal(t, int32(0), rows[0].SortOrder, "首张即封面")
		assert.Equal(t, int32(1), rows[1].SortOrder)

		assert.Zero(t, mqc.Count(), "创建草稿不应发消息")
	})

	t.Run("边界长度标题/正文成功（100/1000 rune）", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		resp, err := uc.CreateDraft(ctx, &postv1.CreateDraftRequest{
			Uid: uid, Title: strings.Repeat("好", 100), FullText: strings.Repeat("字", 1000),
			Medias: []*mediav1.MediaFile{mediaProto(4001, 6001)},
		})
		require.NoError(t, err)
		assert.False(t, resp.GetFail(), "边界长度应合法")
	})

	t.Run("metadata 空值等价 {}", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		resp, err := uc.CreateDraft(ctx, &postv1.CreateDraftRequest{
			Uid: uid, Title: "标题", FullText: "正文", Medias: []*mediav1.MediaFile{mediaProto(4001, 6001)},
			Metadata: &commonv1.JsonStr{},
		})
		require.NoError(t, err)
		require.False(t, resp.GetFail())
		require.NotNil(t, resp.GetDraft().GetMetadata())
		assert.JSONEq(t, "{}", resp.GetDraft().GetMetadata().GetPayload())

		var got models.PostDetail
		require.NoError(t, db.Where("id = ?", mustParseID(t, resp.GetDraft().GetPostId())).First(&got).Error)
		assert.Empty(t, got.MetaData, "空 metadata 应落库为 {}")
	})
}

// ---------- UpdateDraft ----------

func TestPostUsecase_UpdateDraft(t *testing.T) {
	ctx := context.Background()
	uid := strconv.FormatInt(authorID1, 10)
	postID := strconv.FormatInt(postID1, 10)

	// draftReq 造一个带 2 个媒体的草稿请求
	draftReq := func() *postv1.CreateDraftRequest {
		return &postv1.CreateDraftRequest{
			Uid: uid, Title: "标题", FullText: "正文",
			Medias:   []*mediav1.MediaFile{mediaProto(4001, 6001), mediaProto(4002, 6002)},
			Metadata: &commonv1.JsonStr{Payload: `{"k":"v"}`},
		}
	}

	t.Run("无任何更新字段被拒绝", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		resp, err := uc.UpdateDraft(ctx, &postv1.UpdateDraftRequest{Uid: uid, PostId: postID})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidArgument, resp.GetErr())
		assert.Nil(t, resp.GetPost())
	})

	t.Run("非法 uid / post_id 被拒绝", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		respBadUID, err := uc.UpdateDraft(ctx, &postv1.UpdateDraftRequest{Uid: "bad", PostId: postID, Title: wrapperspb.String("新")})
		require.NoError(t, err)
		assert.True(t, respBadUID.GetFail())
		assert.Equal(t, errmsg.InvalidID, respBadUID.GetErr())

		respBadID, err := uc.UpdateDraft(ctx, &postv1.UpdateDraftRequest{Uid: uid, PostId: "bad", Title: wrapperspb.String("新")})
		require.NoError(t, err)
		assert.True(t, respBadID.GetFail())
		assert.Equal(t, errmsg.InvalidID, respBadID.GetErr())
	})

	t.Run("非法标题被拒绝", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		resp, err := uc.UpdateDraft(ctx, &postv1.UpdateDraftRequest{Uid: uid, PostId: postID, Title: wrapperspb.String("")})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidTitle, resp.GetErr())
	})

	t.Run("超长正文被拒绝", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		resp, err := uc.UpdateDraft(ctx, &postv1.UpdateDraftRequest{
			Uid: uid, PostId: postID, FullText: wrapperspb.String(strings.Repeat("字", 1001)),
		})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidContent, resp.GetErr())
	})

	t.Run("非法 metadata 被拒绝", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		resp, err := uc.UpdateDraft(ctx, &postv1.UpdateDraftRequest{
			Uid: uid, PostId: postID, Metadata: &commonv1.JsonStr{Payload: "not-json"},
		})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidArgument, resp.GetErr())
	})

	t.Run("帖子不存在返回 NotFound", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		resp, err := uc.UpdateDraft(ctx, &postv1.UpdateDraftRequest{Uid: uid, PostId: "999999", Title: wrapperspb.String("新")})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.NotFound, resp.GetErr())
		assert.Nil(t, resp.GetPost())
	})

	t.Run("非作者返回 Forbidden", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DRAFT, time.Now().UnixMilli())

		resp, err := uc.UpdateDraft(ctx, &postv1.UpdateDraftRequest{
			Uid: strconv.FormatInt(authorID2, 10), PostId: postID, Title: wrapperspb.String("新"),
		})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.Forbidden, resp.GetErr())
	})

	t.Run("非草稿返回 NotEditable", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, time.Now().UnixMilli())

		resp, err := uc.UpdateDraft(ctx, &postv1.UpdateDraftRequest{Uid: uid, PostId: postID, Title: wrapperspb.String("新")})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.NotEditable, resp.GetErr())

		var got models.PostDetail
		require.NoError(t, db.Where("id = ?", postID1).First(&got).Error)
		assert.Equal(t, "标题", got.Title, "不可编辑时不应有任何修改")
	})

	t.Run("已软删返回 NotEditable", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DELETED, time.Now().UnixMilli())

		resp, err := uc.UpdateDraft(ctx, &postv1.UpdateDraftRequest{Uid: uid, PostId: postID, Title: wrapperspb.String("新")})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.NotEditable, resp.GetErr())
	})

	t.Run("只更新标题：其余字段不动 + 响应全字段", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		require.NoError(t, uc.Repo.CreatePost(ctx, &models.PostDetail{
			BaseModel: common.BaseModel{ID: postID1}, AuthorID: authorID1,
			Title: "旧标题", FullText: "旧正文", Status: postv1.PostStatus_POST_STATUS_DRAFT,
			MetaData: datatypes.JSONMap{"k": "v"},
		}))

		resp, err := uc.UpdateDraft(ctx, &postv1.UpdateDraftRequest{Uid: uid, PostId: postID, Title: wrapperspb.String("新标题")})
		require.NoError(t, err)
		require.False(t, resp.GetFail())

		// 响应：全字段回读（含未在本接口修改的统计与状态）
		got := resp.GetPost()
		require.NotNil(t, got)
		assert.Equal(t, "新标题", got.GetTitle())
		assert.Equal(t, "旧正文", got.GetFullText(), "未传入字段不应被修改")
		assert.Equal(t, postv1.PostStatus_POST_STATUS_DRAFT, got.GetStatus())
		assert.Equal(t, strconv.FormatInt(postID1, 10), got.GetPostId())
		assert.Equal(t, uid, got.GetAuthorId())
		assert.NotEmpty(t, got.GetCreatedAt())
		assert.NotEmpty(t, got.GetUpdatedAt())
		require.NotNil(t, got.GetPostStat())
		assert.Zero(t, got.GetPostStat().GetLikeCount())
		require.NotNil(t, got.GetMetadata())
		assert.JSONEq(t, `{"k":"v"}`, got.GetMetadata().GetPayload())

		// DB：全字段核对
		var dbGot models.PostDetail
		require.NoError(t, db.Where("id = ?", postID1).First(&dbGot).Error)
		assert.Equal(t, "新标题", dbGot.Title)
		assert.Equal(t, "旧正文", dbGot.FullText)
		assert.Equal(t, datatypes.JSONMap{"k": "v"}, dbGot.MetaData)
	})

	t.Run("组合更新（正文+metadata 清空为 {}）", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		require.NoError(t, uc.Repo.CreatePost(ctx, &models.PostDetail{
			BaseModel: common.BaseModel{ID: postID1}, AuthorID: authorID1,
			Title: "旧标题", FullText: "旧正文", Status: postv1.PostStatus_POST_STATUS_DRAFT,
			MetaData: datatypes.JSONMap{"k": "v"},
		}))

		resp, err := uc.UpdateDraft(ctx, &postv1.UpdateDraftRequest{
			Uid: uid, PostId: postID,
			FullText: wrapperspb.String("新正文"),
			Metadata: &commonv1.JsonStr{Payload: "{}"},
		})
		require.NoError(t, err)
		require.False(t, resp.GetFail())

		var got models.PostDetail
		require.NoError(t, db.Where("id = ?", postID1).First(&got).Error)
		assert.Equal(t, "新正文", got.FullText)
		assert.Empty(t, got.MetaData, "metadata 应被清空为 {}")
		assert.Equal(t, "旧标题", got.Title, "未传入字段不应被修改")
	})

	// 状态机：创建草稿 → 发布（AUDITING）后不可再编辑
	t.Run("发布后编辑被拒（状态机）", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		createResp, err := uc.CreateDraft(ctx, draftReq())
		require.NoError(t, err)
		require.False(t, createResp.GetFail())
		pid := createResp.GetDraft().GetPostId()

		pubResp, err := uc.PublishDraft(ctx, &postv1.PublishDraftRequest{Uid: uid, PostId: pid})
		require.NoError(t, err)
		require.False(t, pubResp.GetFail())

		resp, err := uc.UpdateDraft(ctx, &postv1.UpdateDraftRequest{Uid: uid, PostId: pid, Title: wrapperspb.String("新")})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.NotEditable, resp.GetErr())
	})
}

// ---------- UpdateDraftMedias ----------

func TestPostUsecase_UpdateDraftMedias(t *testing.T) {
	ctx := context.Background()
	uid := strconv.FormatInt(authorID1, 10)
	postID := strconv.FormatInt(postID1, 10)

	t.Run("非法 uid / post_id 被拒绝", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		respBadUID, err := uc.UpdateDraftMedias(ctx, &postv1.UpdateDraftMediasRequest{
			Uid: "bad", PostId: postID, Medias: []*mediav1.MediaFile{mediaProto(4001, 6001)},
		})
		require.NoError(t, err)
		assert.True(t, respBadUID.GetFail())
		assert.Equal(t, errmsg.InvalidID, respBadUID.GetErr())

		respBadID, err := uc.UpdateDraftMedias(ctx, &postv1.UpdateDraftMediasRequest{
			Uid: uid, PostId: "bad", Medias: []*mediav1.MediaFile{mediaProto(4001, 6001)},
		})
		require.NoError(t, err)
		assert.True(t, respBadID.GetFail())
		assert.Equal(t, errmsg.InvalidID, respBadID.GetErr())
	})

	t.Run("媒体为空被拒绝（不支持清空）", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		resp, err := uc.UpdateDraftMedias(ctx, &postv1.UpdateDraftMediasRequest{Uid: uid, PostId: postID})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidMedia, resp.GetErr())
	})

	t.Run("非法媒体被拒绝", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		bad := mediaProto(4001, 6001)
		bad.Base = nil
		resp, err := uc.UpdateDraftMedias(ctx, &postv1.UpdateDraftMediasRequest{
			Uid: uid, PostId: postID, Medias: []*mediav1.MediaFile{bad},
		})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidMedia, resp.GetErr())
	})

	t.Run("帖子不存在返回 NotFound", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		resp, err := uc.UpdateDraftMedias(ctx, &postv1.UpdateDraftMediasRequest{
			Uid: uid, PostId: "999999", Medias: []*mediav1.MediaFile{mediaProto(4001, 6001)},
		})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.NotFound, resp.GetErr())
	})

	t.Run("非作者返回 Forbidden", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DRAFT, time.Now().UnixMilli())

		resp, err := uc.UpdateDraftMedias(ctx, &postv1.UpdateDraftMediasRequest{
			Uid: strconv.FormatInt(authorID2, 10), PostId: postID, Medias: []*mediav1.MediaFile{mediaProto(4001, 6001)},
		})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.Forbidden, resp.GetErr())
	})

	t.Run("非草稿返回 NotEditable", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, time.Now().UnixMilli())

		resp, err := uc.UpdateDraftMedias(ctx, &postv1.UpdateDraftMediasRequest{
			Uid: uid, PostId: postID, Medias: []*mediav1.MediaFile{mediaProto(4001, 6001)},
		})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.NotEditable, resp.GetErr())
	})

	t.Run("全量替换成功：重建顺序 + old_asset_ids + 全字段响应", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DRAFT, time.Now().UnixMilli())
		seedPostMedia(t, db, 5001, postID1, 4001, 6001, 0) // 保留项（asset 6001）
		seedPostMedia(t, db, 5002, postID1, 4002, 6002, 1) // 被替换（asset 6002）

		resp, err := uc.UpdateDraftMedias(ctx, &postv1.UpdateDraftMediasRequest{
			Uid: uid, PostId: postID,
			Medias: []*mediav1.MediaFile{mediaProto(4003, 6003), mediaProto(4001, 6001)},
		})
		require.NoError(t, err)
		require.False(t, resp.GetFail())

		// old_asset_ids：被替换的旧媒体所属资产（定稿语义：含未变更资产，BFF 先减后加）
		assert.ElementsMatch(t, []string{"6001", "6002"}, resp.GetOldAssetIds())

		// 响应：媒体全字段且按新顺序（首张即封面）
		require.NotNil(t, resp.GetPost())
		require.Len(t, resp.GetPost().GetMedias(), 2)
		assertMediaProto(t, resp.GetPost().GetMedias()[0], 4003, 6003)
		assertMediaProto(t, resp.GetPost().GetMedias()[1], 4001, 6001)

		// DB：旧关联已删、新关联 sort_order 从 0 重建
		var rows []models.PostMedia
		require.NoError(t, db.Where("post_id = ?", postID1).Order("sort_order").Find(&rows).Error)
		require.Len(t, rows, 2, "全量替换后应只有新关联")
		assert.Equal(t, int32(0), rows[0].SortOrder)
		assert.Equal(t, int64(4003), rows[0].Media.ID)
		assert.Equal(t, int32(1), rows[1].SortOrder)
		assert.Equal(t, int64(4001), rows[1].Media.ID)
	})

	t.Run("首次设置媒体时 old_asset_ids 为空", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DRAFT, time.Now().UnixMilli())

		resp, err := uc.UpdateDraftMedias(ctx, &postv1.UpdateDraftMediasRequest{
			Uid: uid, PostId: postID, Medias: []*mediav1.MediaFile{mediaProto(4001, 6001)},
		})
		require.NoError(t, err)
		require.False(t, resp.GetFail())
		assert.Empty(t, resp.GetOldAssetIds(), "首次设置媒体时无旧资产")
	})
}

// ---------- PublishDraft ----------

func TestPostUsecase_PublishDraft(t *testing.T) {
	ctx := context.Background()
	uid := strconv.FormatInt(authorID1, 10)
	postID := strconv.FormatInt(postID1, 10)

	t.Run("非法 uid / post_id 被拒绝", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		respBadUID, err := uc.PublishDraft(ctx, &postv1.PublishDraftRequest{Uid: "bad", PostId: postID})
		require.NoError(t, err)
		assert.True(t, respBadUID.GetFail())
		assert.Equal(t, errmsg.InvalidID, respBadUID.GetErr())

		respBadID, err := uc.PublishDraft(ctx, &postv1.PublishDraftRequest{Uid: uid, PostId: "bad"})
		require.NoError(t, err)
		assert.True(t, respBadID.GetFail())
		assert.Equal(t, errmsg.InvalidID, respBadID.GetErr())
	})

	t.Run("帖子不存在返回 NotFound", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		resp, err := uc.PublishDraft(ctx, &postv1.PublishDraftRequest{Uid: uid, PostId: "999999"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.NotFound, resp.GetErr())
	})

	t.Run("非作者返回 Forbidden", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DRAFT, time.Now().UnixMilli())

		resp, err := uc.PublishDraft(ctx, &postv1.PublishDraftRequest{Uid: strconv.FormatInt(authorID2, 10), PostId: postID})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.Forbidden, resp.GetErr())
	})

	// 回归（P0-2）：非 DRAFT 发布失败时不得发审核消息
	t.Run("非草稿发布被拒且不发消息（回归）", func(t *testing.T) {
		uc, db, mqc := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, time.Now().UnixMilli())

		resp, err := uc.PublishDraft(ctx, &postv1.PublishDraftRequest{Uid: uid, PostId: postID})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.NotEditable, resp.GetErr())
		assert.Zero(t, mqc.Count(), "状态未流转时不得发审核消息")

		var got models.PostDetail
		require.NoError(t, db.Where("id = ?", postID1).First(&got).Error)
		assert.Equal(t, postv1.PostStatus_POST_STATUS_PUBLISHED, got.Status, "状态不应被改变")
	})

	t.Run("发布成功：DRAFT→AUDITING + AuditPost 消息", func(t *testing.T) {
		uc, db, mqc := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DRAFT, time.Now().UnixMilli())

		resp, err := uc.PublishDraft(ctx, &postv1.PublishDraftRequest{Uid: uid, PostId: postID})
		require.NoError(t, err)
		require.False(t, resp.GetFail())

		var got models.PostDetail
		require.NoError(t, db.Where("id = ?", postID1).First(&got).Error)
		assert.Equal(t, postv1.PostStatus_POST_STATUS_AUDITING, got.Status)

		msgs := mqc.SentBySubject(defaults.PostMessageStream)
		require.Len(t, msgs, 1)
		msg := decodePostMessage(t, msgs[0].PayLoad)
		require.NotNil(t, msg.GetAuditPost())
		assert.Equal(t, postID, msg.GetAuditPost().GetPostId())
		assert.Equal(t, uid, msg.GetAuditPost().GetAuthorId())
	})

	t.Run("MQ 失败时状态流转整体回滚", func(t *testing.T) {
		uc, db, mqc := newUsecase(t)
		mqc.SetSendError(errors.New("mq down"))
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DRAFT, time.Now().UnixMilli())

		_, err := uc.PublishDraft(ctx, &postv1.PublishDraftRequest{Uid: uid, PostId: postID})
		require.Error(t, err)

		var got models.PostDetail
		require.NoError(t, db.Where("id = ?", postID1).First(&got).Error)
		assert.Equal(t, postv1.PostStatus_POST_STATUS_DRAFT, got.Status, "消息发送失败应随事务回滚")
	})
}

// ---------- GetPostsByIDs ----------

func TestPostUsecase_GetPostsByIDs(t *testing.T) {
	ctx := context.Background()

	t.Run("混合结果：命中 / 非法ID / 未命中（回归）", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, time.Now().UnixMilli())

		resp, err := uc.GetPostsByIDs(ctx, &postv1.GetPostsByIDsRequest{
			PostIds: []string{"bad", strconv.FormatInt(postID1, 10), "999999"},
		})
		require.NoError(t, err)
		require.Len(t, resp.GetResults(), 3)

		// 非法 ID：回填请求串
		assert.True(t, resp.GetResults()[0].GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetResults()[0].GetErr())
		assert.Equal(t, "bad", resp.GetResults()[0].GetPostId())
		assert.Nil(t, resp.GetResults()[0].GetPost())

		// 命中：全字段
		assert.False(t, resp.GetResults()[1].GetFail())
		assert.Equal(t, strconv.FormatInt(postID1, 10), resp.GetResults()[1].GetPostId())
		assertSeededPost(t, resp.GetResults()[1].GetPost(), postID1, postv1.PostStatus_POST_STATUS_PUBLISHED)

		// 回归（P1-2）：ID 合法但未命中必须逐条 NotFound 且回填 post_id，不得静默丢弃
		assert.True(t, resp.GetResults()[2].GetFail())
		assert.Equal(t, errmsg.NotFound, resp.GetResults()[2].GetErr())
		assert.Equal(t, "999999", resp.GetResults()[2].GetPostId())
		assert.Nil(t, resp.GetResults()[2].GetPost())
	})

	t.Run("空入参返回空结果", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		resp, err := uc.GetPostsByIDs(ctx, &postv1.GetPostsByIDsRequest{})
		require.NoError(t, err)
		assert.Empty(t, resp.GetResults())
	})
}

// ---------- ViewPostsByIDs ----------

func TestPostUsecase_ViewPostsByIDs(t *testing.T) {
	ctx := context.Background()

	t.Run("混合结果：仅 PUBLISHED 出 brief，其余逐条 fail（key=post_id 定位，不保证序）", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, time.Now().UnixMilli())
		seedPost(t, db, postID2, authorID1, postv1.PostStatus_POST_STATUS_DELETED, time.Now().UnixMilli())

		resp, err := uc.ViewPostsByIDs(ctx, &postv1.ViewPostsByIDsRequest{
			PostIds: []string{"bad", strconv.FormatInt(postID1, 10), strconv.FormatInt(postID2, 10), "999999"},
		})
		require.NoError(t, err)
		require.Len(t, resp.GetResults(), 4)

		byID := make(map[string]*postv1.PostBriefResult, len(resp.GetResults()))
		for _, r := range resp.GetResults() {
			byID[r.GetPostId()] = r
		}

		// PUBLISHED：出 brief（全字段经 ToBrief）
		ok := byID[strconv.FormatInt(postID1, 10)]
		require.NotNil(t, ok)
		assert.False(t, ok.GetFail())
		require.NotNil(t, ok.GetPost())
		assert.Equal(t, strconv.FormatInt(postID1, 10), ok.GetPost().GetPostId())

		// 非 PUBLISHED 与不存在统一 NotFound（不暴露状态存在性）；非法雪花 id InvalidID——均 key 定位
		assert.True(t, byID[strconv.FormatInt(postID2, 10)].GetFail())
		assert.Equal(t, errmsg.NotFound, byID[strconv.FormatInt(postID2, 10)].GetErr())
		assert.True(t, byID["999999"].GetFail(), "合法 id 未命中")
		assert.Equal(t, errmsg.NotFound, byID["999999"].GetErr())
		assert.True(t, byID["bad"].GetFail())
		assert.Equal(t, errmsg.InvalidID, byID["bad"].GetErr())
		assert.Equal(t, "bad", byID["bad"].GetPostId(), "非法 id 回填请求串")
	})

	t.Run("空入参返回空结果", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		resp, err := uc.ViewPostsByIDs(ctx, &postv1.ViewPostsByIDsRequest{})
		require.NoError(t, err)
		assert.Empty(t, resp.GetResults())
	})
}

// ---------- ViewPostByAuthor ----------

func TestPostUsecase_ViewPostByAuthor(t *testing.T) {
	ctx := context.Background()
	base := time.Now().Add(-time.Hour).UnixMilli()
	self := strconv.FormatInt(authorID1, 10)
	other := strconv.FormatInt(authorID2, 10)
	author := strconv.FormatInt(authorID1, 10)

	t.Run("非法 viewer / author 被拒绝", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		respBadViewer, err := uc.ViewPostByAuthor(ctx, &postv1.ViewPostByAuthorRequest{ViewerId: "bad", AuthorId: author})
		require.NoError(t, err)
		assert.True(t, respBadViewer.GetFail())
		assert.Equal(t, errmsg.InvalidID, respBadViewer.GetErr())

		respBadAuthor, err := uc.ViewPostByAuthor(ctx, &postv1.ViewPostByAuthorRequest{ViewerId: self, AuthorId: "bad"})
		require.NoError(t, err)
		assert.True(t, respBadAuthor.GetFail())
		assert.Equal(t, errmsg.InvalidID, respBadAuthor.GetErr())
	})

	t.Run("作者自己可见草稿/审核中/已发布，排除封禁/已删", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DRAFT, base-5000)
		seedPost(t, db, postID2, authorID1, postv1.PostStatus_POST_STATUS_AUDITING, base-4000)
		seedPost(t, db, postID3, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, base-3000)
		seedPost(t, db, postID4, authorID1, postv1.PostStatus_POST_STATUS_BANNED, base-2000)
		seedPost(t, db, 2005, authorID1, postv1.PostStatus_POST_STATUS_DELETED, base-1000)

		resp, err := uc.ViewPostByAuthor(ctx, &postv1.ViewPostByAuthorRequest{ViewerId: self, AuthorId: author})
		require.NoError(t, err)
		require.False(t, resp.GetFail())

		ids := make([]string, 0, len(resp.GetPosts()))
		for _, b := range resp.GetPosts() {
			ids = append(ids, b.GetPostId())
		}
		assert.Equal(t, []string{
			strconv.FormatInt(postID3, 10),
			strconv.FormatInt(postID2, 10),
			strconv.FormatInt(postID1, 10),
		}, ids, "应按 created_at DESC 且排除封禁/已删")
	})

	t.Run("他人仅见已发布", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DRAFT, base-5000)
		seedPost(t, db, postID3, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, base-3000)

		resp, err := uc.ViewPostByAuthor(ctx, &postv1.ViewPostByAuthorRequest{ViewerId: other, AuthorId: author})
		require.NoError(t, err)
		require.Len(t, resp.GetPosts(), 1)
		assert.Equal(t, strconv.FormatInt(postID3, 10), resp.GetPosts()[0].GetPostId())
	})

	t.Run("游标翻页 + brief 全字段", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, base-5000)
		seedPost(t, db, postID2, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, base-4000)
		seedPost(t, db, postID3, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, base-3000)
		seedPostMedia(t, db, 5001, postID3, 4001, 6001, 0) // 封面
		seedPostMedia(t, db, 5002, postID3, 4002, 6002, 1)

		// 第一页
		page1, err := uc.ViewPostByAuthor(ctx, &postv1.ViewPostByAuthorRequest{
			ViewerId: self, AuthorId: author, Cursor: &commonv1.Cursor{Str: strconv.FormatInt(base, 10), Limit: 2},
		})
		require.NoError(t, err)
		require.Len(t, page1.GetPosts(), 2)

		// brief 全字段（含封面只带 sort_order=0 的关联）
		b := page1.GetPosts()[0]
		assert.Equal(t, strconv.FormatInt(postID3, 10), b.GetPostId())
		assert.Equal(t, "标题", b.GetTitle())
		assert.Equal(t, self, b.GetAuthorId())
		assert.Equal(t, strconv.FormatInt(base-3000, 10), b.GetCreatedAt())
		require.NotNil(t, b.GetStat())
		assert.Equal(t, int64(1), b.GetStat().GetLikeCount())
		assert.Equal(t, int64(2), b.GetStat().GetFavouriteCount())
		assert.Equal(t, int64(3), b.GetStat().GetCommentCount())
		assert.Equal(t, int64(4), b.GetStat().GetViewCount())
		assertMediaProto(t, b.GetCover(), 4001, 6001)

		// 游标 = 最后一条 created_at；第二页接着翻
		require.NotNil(t, page1.GetCursor())
		assert.Equal(t, strconv.FormatInt(base-4000, 10), page1.GetCursor().GetStr())

		page2, err := uc.ViewPostByAuthor(ctx, &postv1.ViewPostByAuthorRequest{
			ViewerId: self, AuthorId: author, Cursor: page1.GetCursor(),
		})
		require.NoError(t, err)
		require.Len(t, page2.GetPosts(), 1)
		assert.Equal(t, strconv.FormatInt(postID1, 10), page2.GetPosts()[0].GetPostId())
	})

	// 回归（P2-5）：空页返回空列表 + 空游标，不再以 NotFound 表达翻页到底
	t.Run("翻页到底返回空列表 + 空游标（回归）", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, base-5000)

		last, err := uc.ViewPostByAuthor(ctx, &postv1.ViewPostByAuthorRequest{
			ViewerId: self, AuthorId: author, Cursor: &commonv1.Cursor{Str: strconv.FormatInt(base, 10), Limit: 10},
		})
		require.NoError(t, err)
		require.Len(t, last.GetPosts(), 1)

		end, err := uc.ViewPostByAuthor(ctx, &postv1.ViewPostByAuthorRequest{
			ViewerId: self, AuthorId: author, Cursor: last.GetCursor(),
		})
		require.NoError(t, err)
		assert.False(t, end.GetFail())
		assert.Empty(t, end.GetPosts(), "翻页到底应返回空列表")
		assert.Nil(t, end.GetCursor(), "翻页到底应返回空游标")
		assert.Nil(t, end.GetErr())
	})

	t.Run("无内容返回空列表（非 NotFound）", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		resp, err := uc.ViewPostByAuthor(ctx, &postv1.ViewPostByAuthorRequest{ViewerId: self, AuthorId: author})
		require.NoError(t, err)
		assert.False(t, resp.GetFail())
		assert.Empty(t, resp.GetPosts())
		assert.Nil(t, resp.GetCursor())
	})
}

// ---------- ViewPost ----------

func TestPostUsecase_ViewPost(t *testing.T) {
	ctx := context.Background()
	self := strconv.FormatInt(authorID1, 10)
	other := strconv.FormatInt(authorID2, 10)
	postID := strconv.FormatInt(postID1, 10)

	t.Run("非法 viewer / post_id 被拒绝", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		respBadViewer, err := uc.ViewPost(ctx, &postv1.ViewPostRequest{ViewerId: "bad", PostId: postID})
		require.NoError(t, err)
		assert.True(t, respBadViewer.GetFail())
		assert.Equal(t, errmsg.InvalidID, respBadViewer.GetErr())

		respBadID, err := uc.ViewPost(ctx, &postv1.ViewPostRequest{ViewerId: self, PostId: "bad"})
		require.NoError(t, err)
		assert.True(t, respBadID.GetFail())
		assert.Equal(t, errmsg.InvalidID, respBadID.GetErr())
	})

	t.Run("帖子不存在返回 NotFound", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		resp, err := uc.ViewPost(ctx, &postv1.ViewPostRequest{ViewerId: self, PostId: "999999"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.NotFound, resp.GetErr())
	})

	t.Run("已发布他人可见（全字段）", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, time.Now().UnixMilli())
		seedPostMedia(t, db, 5001, postID1, 4001, 6001, 0)

		resp, err := uc.ViewPost(ctx, &postv1.ViewPostRequest{ViewerId: other, PostId: postID})
		require.NoError(t, err)
		require.False(t, resp.GetFail())
		assertSeededPost(t, resp.GetPost(), postID1, postv1.PostStatus_POST_STATUS_PUBLISHED)
		require.Len(t, resp.GetPost().GetMedias(), 1)
		assertMediaProto(t, resp.GetPost().GetMedias()[0], 4001, 6001)
	})

	t.Run("草稿仅作者可见", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DRAFT, time.Now().UnixMilli())

		// 他人：Forbidden
		respOther, err := uc.ViewPost(ctx, &postv1.ViewPostRequest{ViewerId: other, PostId: postID})
		require.NoError(t, err)
		assert.True(t, respOther.GetFail())
		assert.Equal(t, errmsg.Forbidden, respOther.GetErr())
		assert.Nil(t, respOther.GetPost())

		// 作者：可见
		respSelf, err := uc.ViewPost(ctx, &postv1.ViewPostRequest{ViewerId: self, PostId: postID})
		require.NoError(t, err)
		assert.False(t, respSelf.GetFail())
		assertSeededPost(t, respSelf.GetPost(), postID1, postv1.PostStatus_POST_STATUS_DRAFT)
	})

	t.Run("审核中仅作者可见", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_AUDITING, time.Now().UnixMilli())

		respOther, err := uc.ViewPost(ctx, &postv1.ViewPostRequest{ViewerId: other, PostId: postID})
		require.NoError(t, err)
		assert.True(t, respOther.GetFail())
		assert.Equal(t, errmsg.Forbidden, respOther.GetErr())

		respSelf, err := uc.ViewPost(ctx, &postv1.ViewPostRequest{ViewerId: self, PostId: postID})
		require.NoError(t, err)
		assert.False(t, respSelf.GetFail())
	})

	t.Run("已软删对外隐藏（NotFound）", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DELETED, time.Now().UnixMilli())

		resp, err := uc.ViewPost(ctx, &postv1.ViewPostRequest{ViewerId: self, PostId: postID})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.NotFound, resp.GetErr())
	})

	// 回归（P1-1）：封禁帖子应返回 Banned，而不是兜底 InternalError
	t.Run("封禁返回 Banned（回归）", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_BANNED, time.Now().UnixMilli())

		resp, err := uc.ViewPost(ctx, &postv1.ViewPostRequest{ViewerId: self, PostId: postID})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.Banned, resp.GetErr())
		assert.Nil(t, resp.GetPost())
	})
}

// ---------- GetPostStatus ----------

func TestPostUsecase_GetPostStatus(t *testing.T) {
	ctx := context.Background()
	postID := strconv.FormatInt(postID1, 10)

	t.Run("非法 post_id 被拒绝", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		resp, err := uc.GetPostStatus(ctx, &postv1.GetPostStatusRequest{PostId: "bad"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetErr())
	})

	t.Run("帖子不存在返回 NotFound", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		resp, err := uc.GetPostStatus(ctx, &postv1.GetPostStatusRequest{PostId: "999999"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.NotFound, resp.GetErr())
	})

	t.Run("命中返回状态", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_AUDITING, time.Now().UnixMilli())

		resp, err := uc.GetPostStatus(ctx, &postv1.GetPostStatusRequest{PostId: postID})
		require.NoError(t, err)
		require.False(t, resp.GetFail())
		assert.Equal(t, postv1.PostStatus_POST_STATUS_AUDITING, resp.GetStatus())
	})
}

// ---------- BanPost ----------

func TestPostUsecase_BanPost(t *testing.T) {
	ctx := context.Background()
	postID := strconv.FormatInt(postID1, 10)
	author := strconv.FormatInt(authorID1, 10)

	t.Run("非法 post_id 被拒绝", func(t *testing.T) {
		uc, _, mqc := newUsecase(t)
		resp, err := uc.BanPost(ctx, &postv1.BanPostRequest{PostId: "bad"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetErr())
		assert.Zero(t, mqc.Count())
	})

	t.Run("帖子不存在返回 NotFound", func(t *testing.T) {
		uc, _, mqc := newUsecase(t)
		resp, err := uc.BanPost(ctx, &postv1.BanPostRequest{PostId: "999999"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.NotFound, resp.GetErr())
		assert.Zero(t, mqc.Count())
	})

	t.Run("封禁成功：状态 BANNED + BanPost 消息（author_id 取自库）", func(t *testing.T) {
		uc, db, mqc := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, time.Now().UnixMilli())

		resp, err := uc.BanPost(ctx, &postv1.BanPostRequest{PostId: postID})
		require.NoError(t, err)
		require.False(t, resp.GetFail())

		var got models.PostDetail
		require.NoError(t, db.Where("id = ?", postID1).First(&got).Error)
		assert.Equal(t, postv1.PostStatus_POST_STATUS_BANNED, got.Status)

		msgs := mqc.SentBySubject(defaults.PostMessageStream)
		require.Len(t, msgs, 1)
		msg := decodePostMessage(t, msgs[0].PayLoad)
		require.NotNil(t, msg.GetBanPost())
		assert.Equal(t, postID, msg.GetBanPost().GetPostId())
		assert.Equal(t, author, msg.GetBanPost().GetAuthorId(), "author_id 只能来自库（请求不带 uid）")
	})

	t.Run("已封禁幂等成功且不重复发消息", func(t *testing.T) {
		uc, db, mqc := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_BANNED, time.Now().UnixMilli())

		resp, err := uc.BanPost(ctx, &postv1.BanPostRequest{PostId: postID})
		require.NoError(t, err)
		assert.False(t, resp.GetFail(), "重复封禁应幂等成功")
		assert.Zero(t, mqc.Count(), "0 行命中不得发消息")
	})

	// 定稿（P2-5）：封禁已删帖静默成功、不发消息
	t.Run("封禁已删帖静默成功不发消息", func(t *testing.T) {
		uc, db, mqc := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DELETED, time.Now().UnixMilli())

		resp, err := uc.BanPost(ctx, &postv1.BanPostRequest{PostId: postID})
		require.NoError(t, err)
		assert.False(t, resp.GetFail())
		assert.Zero(t, mqc.Count())

		var got models.PostDetail
		require.NoError(t, db.Where("id = ?", postID1).First(&got).Error)
		assert.Equal(t, postv1.PostStatus_POST_STATUS_DELETED, got.Status, "已删状态不应被覆盖")
	})
}

// ---------- DeletePost ----------

func TestPostUsecase_DeletePost(t *testing.T) {
	ctx := context.Background()
	uid := strconv.FormatInt(authorID1, 10)
	postID := strconv.FormatInt(postID1, 10)

	t.Run("非法 uid / post_id 被拒绝", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		respBadUID, err := uc.DeletePost(ctx, &postv1.DeletePostRequest{Uid: "bad", PostId: postID})
		require.NoError(t, err)
		assert.True(t, respBadUID.GetFail())
		assert.Equal(t, errmsg.InvalidID, respBadUID.GetErr())

		respBadID, err := uc.DeletePost(ctx, &postv1.DeletePostRequest{Uid: uid, PostId: "bad"})
		require.NoError(t, err)
		assert.True(t, respBadID.GetFail())
		assert.Equal(t, errmsg.InvalidID, respBadID.GetErr())
	})

	t.Run("帖子不存在返回 NotFound", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		resp, err := uc.DeletePost(ctx, &postv1.DeletePostRequest{Uid: uid, PostId: "999999"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.NotFound, resp.GetErr())
	})

	t.Run("非作者返回 Forbidden", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, time.Now().UnixMilli())

		resp, err := uc.DeletePost(ctx, &postv1.DeletePostRequest{Uid: strconv.FormatInt(authorID2, 10), PostId: postID})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.Forbidden, resp.GetErr())

		var got models.PostDetail
		require.NoError(t, db.Where("id = ?", postID1).First(&got).Error)
		assert.Equal(t, postv1.PostStatus_POST_STATUS_PUBLISHED, got.Status, "非作者不得改变状态")
	})

	t.Run("删除成功：软删 + DeletePost 消息", func(t *testing.T) {
		uc, db, mqc := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, time.Now().UnixMilli())

		resp, err := uc.DeletePost(ctx, &postv1.DeletePostRequest{Uid: uid, PostId: postID})
		require.NoError(t, err)
		require.False(t, resp.GetFail())

		var got models.PostDetail
		require.NoError(t, db.Where("id = ?", postID1).First(&got).Error)
		assert.Equal(t, postv1.PostStatus_POST_STATUS_DELETED, got.Status)

		msgs := mqc.SentBySubject(defaults.PostMessageStream)
		require.Len(t, msgs, 1)
		msg := decodePostMessage(t, msgs[0].PayLoad)
		require.NotNil(t, msg.GetDeletePost())
		assert.Equal(t, postID, msg.GetDeletePost().GetPostId())
		assert.Equal(t, uid, msg.GetDeletePost().GetAuthorId())
	})

	t.Run("已删除幂等成功且不重复发消息", func(t *testing.T) {
		uc, db, mqc := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DELETED, time.Now().UnixMilli())

		resp, err := uc.DeletePost(ctx, &postv1.DeletePostRequest{Uid: uid, PostId: postID})
		require.NoError(t, err)
		assert.False(t, resp.GetFail())
		assert.Zero(t, mqc.Count(), "0 行命中不得重复投递 DeletePost 消息")
	})
}

// ---------- SyncPostStat ----------

func TestPostUsecase_SyncPostStat(t *testing.T) {
	ctx := context.Background()
	postID := strconv.FormatInt(postID1, 10)

	t.Run("全部字段未设置被拒绝", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		resp, err := uc.SyncPostStat(ctx, &postv1.SyncPostStatRequest{PostId: postID})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidArgument, resp.GetErr())
	})

	t.Run("非法 post_id 被拒绝", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		resp, err := uc.SyncPostStat(ctx, &postv1.SyncPostStatRequest{PostId: "bad", LikeCount: wrapperspb.Int64(1)})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetErr())
	})

	t.Run("负数计数被拒绝", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, time.Now().UnixMilli())

		resp, err := uc.SyncPostStat(ctx, &postv1.SyncPostStatRequest{PostId: postID, LikeCount: wrapperspb.Int64(-1)})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidArgument, resp.GetErr())

		var got models.PostDetail
		require.NoError(t, db.Where("id = ?", postID1).First(&got).Error)
		assert.Equal(t, int64(1), got.LikeCount, "非法请求不应有部分写入")
	})

	// 回归（P1-4）：nil 字段不修改，设置则覆盖（含 0）
	t.Run("仅更新 like：nil 不修改 + set 0 覆盖（回归）", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, time.Now().UnixMilli())

		resp, err := uc.SyncPostStat(ctx, &postv1.SyncPostStatRequest{PostId: postID, LikeCount: wrapperspb.Int64(0)})
		require.NoError(t, err)
		require.False(t, resp.GetFail())

		var got models.PostDetail
		require.NoError(t, db.Where("id = ?", postID1).First(&got).Error)
		assert.Zero(t, got.LikeCount, "显式写 0 应覆盖旧值 1")
		assert.Equal(t, int64(2), got.FavouriteCount, "nil 字段不应被修改")
		assert.Equal(t, int64(3), got.CommentCount, "nil 字段不应被修改")
		assert.Equal(t, int64(4), got.ViewCount, "nil 字段不应被修改")
	})

	t.Run("全字段更新（DB 全字段核对）", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, time.Now().UnixMilli())

		resp, err := uc.SyncPostStat(ctx, &postv1.SyncPostStatRequest{
			PostId:         postID,
			LikeCount:      wrapperspb.Int64(11),
			FavouriteCount: wrapperspb.Int64(12),
			CommentCount:   wrapperspb.Int64(13),
			ViewCount:      wrapperspb.Int64(14),
		})
		require.NoError(t, err)
		require.False(t, resp.GetFail())

		var got models.PostDetail
		require.NoError(t, db.Where("id = ?", postID1).First(&got).Error)
		assert.Equal(t, int64(11), got.LikeCount)
		assert.Equal(t, int64(12), got.FavouriteCount)
		assert.Equal(t, int64(13), got.CommentCount)
		assert.Equal(t, int64(14), got.ViewCount)
		assert.Equal(t, "标题", got.Title, "仅统计字段，不改内容与状态")
		assert.Equal(t, postv1.PostStatus_POST_STATUS_PUBLISHED, got.Status)
	})

	t.Run("帖子不存在返回 NotFound", func(t *testing.T) {
		uc, _, _ := newUsecase(t)
		resp, err := uc.SyncPostStat(ctx, &postv1.SyncPostStatRequest{PostId: "999999", LikeCount: wrapperspb.Int64(5)})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.NotFound, resp.GetErr())
	})
}

// ---------- 互动（点赞/收藏） ----------

// TestPostUsecase_Interactions 幂等口径 + 强一致计数 + 状态裁决 + 收藏容量上限 + 判态列表
func TestPostUsecase_Interactions(t *testing.T) {
	uc, db, _ := newUsecase(t)
	ctx := context.Background()
	seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, 1000)
	seedPost(t, db, postID2, authorID1, postv1.PostStatus_POST_STATUS_DRAFT, 1000)
	const uid = "5001"
	postIDStr := strconv.FormatInt(postID1, 10)

	t.Run("点赞/取消幂等 + 计数强一致", func(t *testing.T) {
		r1, err := uc.LikePost(ctx, &postv1.LikePostRequest{Uid: uid, PostId: postIDStr})
		require.NoError(t, err)
		require.False(t, r1.GetFail(), "点赞失败: %v", r1.GetErr())
		assert.True(t, r1.GetLiked())
		assert.Equal(t, int64(2), r1.GetLikeCount(), "seed 1 + 1（明细为准）")

		// 重复点赞：幂等成功，计数不重复加（前端可重复动作收敛的契约基础）
		r2, err := uc.LikePost(ctx, &postv1.LikePostRequest{Uid: uid, PostId: postIDStr})
		require.NoError(t, err)
		require.False(t, r2.GetFail())
		assert.True(t, r2.GetLiked())
		assert.Equal(t, int64(2), r2.GetLikeCount())

		// 取消 + 重复取消
		r3, err := uc.UnlikePost(ctx, &postv1.UnlikePostRequest{Uid: uid, PostId: postIDStr})
		require.NoError(t, err)
		require.False(t, r3.GetFail())
		assert.False(t, r3.GetLiked())
		assert.Equal(t, int64(1), r3.GetLikeCount())

		r4, err := uc.UnlikePost(ctx, &postv1.UnlikePostRequest{Uid: uid, PostId: postIDStr})
		require.NoError(t, err)
		require.False(t, r4.GetFail())
		assert.Equal(t, int64(1), r4.GetLikeCount(), "未赞时取消不重复减")
	})

	t.Run("非 PUBLISHED 与不存在一律 NotFound（不泄露状态）", func(t *testing.T) {
		draft, err := uc.LikePost(ctx, &postv1.LikePostRequest{Uid: uid, PostId: strconv.FormatInt(postID2, 10)})
		require.NoError(t, err)
		assert.True(t, draft.GetFail())
		assert.Equal(t, errmsg.NotFound.Code, draft.GetErr().GetCode())

		missing, err := uc.FavouritePost(ctx, &postv1.FavouritePostRequest{Uid: uid, PostId: "999999"})
		require.NoError(t, err)
		assert.True(t, missing.GetFail())
		assert.Equal(t, errmsg.NotFound.Code, missing.GetErr().GetCode())
	})

	t.Run("非法 uid/post_id → InvalidID", func(t *testing.T) {
		bad, err := uc.LikePost(ctx, &postv1.LikePostRequest{Uid: "not-id", PostId: postIDStr})
		require.NoError(t, err)
		assert.True(t, bad.GetFail())
		assert.Equal(t, errmsg.InvalidID.Code, bad.GetErr().GetCode())
	})

	t.Run("收藏容量上限：超限回滚；已藏满额重复收藏幂等不报满", func(t *testing.T) {
		// 满容量用户 6001：批量种 FavouriteCap 条明细（占位帖子不需要存在——容量只数明细行）
		favs := make([]*models.PostFavourite, 0, defaults.FavouriteCap)
		for i := 0; i < defaults.FavouriteCap; i++ {
			favs = append(favs, &models.PostFavourite{
				BaseModel: common.BaseModel{ID: 92000 + int64(i)},
				PostID:    70000 + int64(i),
				UserID:    6001,
			})
		}
		require.NoError(t, db.CreateInBatches(favs, 500).Error)

		full, err := uc.FavouritePost(ctx, &postv1.FavouritePostRequest{Uid: "6001", PostId: postIDStr})
		require.NoError(t, err)
		assert.True(t, full.GetFail())
		assert.Equal(t, errmsg.FavouriteLimitExceeded.Code, full.GetErr().GetCode())
		cnt, cntErr := uc.Repo.CountFavouritesByUser(ctx, 6001)
		require.NoError(t, cntErr)
		assert.Equal(t, int64(defaults.FavouriteCap), cnt, "超限失败须回滚插入")

		// 已藏且满额：重复收藏幂等成功（不占容量、不报满）
		favs2 := make([]*models.PostFavourite, 0, defaults.FavouriteCap)
		for i := 0; i < defaults.FavouriteCap-1; i++ {
			favs2 = append(favs2, &models.PostFavourite{
				BaseModel: common.BaseModel{ID: 93000 + int64(i)},
				PostID:    71000 + int64(i),
				UserID:    6002,
			})
		}
		favs2 = append(favs2, &models.PostFavourite{
			BaseModel: common.BaseModel{ID: 93999}, PostID: postID1, UserID: 6002,
		})
		require.NoError(t, db.CreateInBatches(favs2, 500).Error)

		okResp, err := uc.FavouritePost(ctx, &postv1.FavouritePostRequest{Uid: "6002", PostId: postIDStr})
		require.NoError(t, err)
		require.False(t, okResp.GetFail(), "已藏重复收藏应幂等成功: %v", okResp.GetErr())
		assert.True(t, okResp.GetFavourited())
	})

	t.Run("收藏/取消全流程：计数 + 态位", func(t *testing.T) {
		const uid2 = "6003"
		f1, err := uc.FavouritePost(ctx, &postv1.FavouritePostRequest{Uid: uid2, PostId: postIDStr})
		require.NoError(t, err)
		require.False(t, f1.GetFail())
		assert.True(t, f1.GetFavourited())
		assert.Equal(t, int64(3), f1.GetFavouriteCount(), "seed 2 + 1")

		u1, err := uc.UnfavouritePost(ctx, &postv1.UnfavouritePostRequest{Uid: uid2, PostId: postIDStr})
		require.NoError(t, err)
		require.False(t, u1.GetFail())
		assert.False(t, u1.GetFavourited())
		assert.Equal(t, int64(2), u1.GetFavouriteCount())
	})

	t.Run("判态列表：空页语义 + 顺序 + 游标", func(t *testing.T) {
		const uid3 = "6004"
		empty, err := uc.ListLikedPostIDs(ctx, &postv1.ListLikedPostIDsRequest{Uid: uid3})
		require.NoError(t, err)
		require.False(t, empty.GetFail())
		assert.Empty(t, empty.GetPostIds())
		assert.Nil(t, empty.GetCursor(), "空页返回空游标")

		// 显式 created_at：两条同毫秒由 id 决序
		likeRows := []*models.PostLike{
			{BaseModel: common.BaseModel{ID: 94001, CreatedAt: 1000}, PostID: postID1, UserID: 6004},
			{BaseModel: common.BaseModel{ID: 94002, CreatedAt: 1000}, PostID: postID2, UserID: 6004},
		}
		require.NoError(t, db.Create(likeRows).Error)

		got, err := uc.ListLikedPostIDs(ctx, &postv1.ListLikedPostIDsRequest{
			Uid:    uid3,
			Cursor: &commonv1.Cursor{Str: "99999", Limit: 10},
		})
		require.NoError(t, err)
		require.False(t, got.GetFail())
		assert.Equal(t, []string{strconv.FormatInt(postID2, 10), postIDStr}, got.GetPostIds(),
			"created_at 并列按 id DESC（94002 > 94001）")
		require.NotNil(t, got.GetCursor())

		// 游标续翻：时间游标严格小于边界（与现有帖子列表翻页同口径），
		// 同毫秒并列行落页外为已知现状（判态场景可接受）
		next, err := uc.ListLikedPostIDs(ctx, &postv1.ListLikedPostIDsRequest{
			Uid:    uid3,
			Cursor: &commonv1.Cursor{Str: "1000", Limit: 10},
		})
		require.NoError(t, err)
		assert.Empty(t, next.GetPostIds())
	})
}

// ---------- 评论 ----------

// TestCommentCreateReply 发评/回复（同步链路：校验+审核+状态裁决）+ 计数强一致 + 楼中楼层级
func TestCommentCreateReply(t *testing.T) {
	u, db, _ := newUsecase(t)
	ctx := context.Background()
	seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, 1000)
	seedPost(t, db, postID2, authorID1, postv1.PostStatus_POST_STATUS_DRAFT, 1000)

	// 发一级评论（seedPost 起点 comment_count=3 → 4）
	resp, err := u.CreateComment(ctx, &postv1.CreateCommentRequest{
		Uid: "6001", PostId: "2001", Content: "好帖，收藏了",
	})
	require.NoError(t, err)
	assert.False(t, resp.GetFail())
	c := resp.GetComment()
	require.NotNil(t, c)
	assert.NotEmpty(t, c.GetCommentId())
	assert.Equal(t, c.GetCommentId(), c.GetRootId(), "一级评论楼层即自身")
	assert.Empty(t, c.GetParentId())
	assert.Empty(t, c.GetReplyToUserId())
	assert.Equal(t, "好帖，收藏了", c.GetContent())
	assert.NotEmpty(t, c.GetCreatedAt())
	assert.Zero(t, c.GetReplyCount())
	assert.Equal(t, int64(4), resp.GetCommentCount())

	// 落库哨兵口径：RootID=0 / ParentID=0 / NORMAL
	var row models.PostComment
	require.NoError(t, db.First(&row, mustParseID(t, c.GetCommentId())).Error)
	assert.Zero(t, row.RootID, "一级评论 RootID 哨兵 0")
	assert.Zero(t, row.ParentID)
	assert.Equal(t, postv1.CommentStatus_COMMENT_STATUS_NORMAL, row.Status)

	// 入参校验三态
	bad, err := u.CreateComment(ctx, &postv1.CreateCommentRequest{Uid: "x", PostId: "2001", Content: "a"})
	require.NoError(t, err)
	assert.True(t, bad.GetFail())
	assert.Equal(t, errmsg.InvalidID, bad.GetErr())
	bad, err = u.CreateComment(ctx, &postv1.CreateCommentRequest{Uid: "6001", PostId: "x", Content: "a"})
	require.NoError(t, err)
	assert.Equal(t, errmsg.InvalidID, bad.GetErr())
	bad, err = u.CreateComment(ctx, &postv1.CreateCommentRequest{Uid: "6001", PostId: "2001", Content: "   "})
	require.NoError(t, err)
	assert.Equal(t, errmsg.InvalidComment, bad.GetErr(), "空白内容非法")
	bad, err = u.CreateComment(ctx, &postv1.CreateCommentRequest{Uid: "6001", PostId: "2001", Content: strings.Repeat("啊", 501)})
	require.NoError(t, err)
	assert.Equal(t, errmsg.InvalidComment, bad.GetErr(), "超 500 rune 非法")
	bad, err = u.CreateComment(ctx, &postv1.CreateCommentRequest{Uid: "6001", PostId: "2001", Content: "a\x01b"})
	require.NoError(t, err)
	assert.Equal(t, errmsg.InvalidComment, bad.GetErr(), "控制字符非法")

	// 状态裁决：非 PUBLISHED 一律 NotFound（不泄露帖子状态）
	bad, err = u.CreateComment(ctx, &postv1.CreateCommentRequest{Uid: "6001", PostId: "2002", Content: "草稿评论"})
	require.NoError(t, err)
	assert.Equal(t, errmsg.NotFound, bad.GetErr())

	// 回复一级评论（挂楼层，reply_to=被回复者，comment_count 4→5）
	r1, err := u.ReplyComment(ctx, &postv1.ReplyCommentRequest{
		Uid: "6002", PostId: "2001", ParentId: c.GetCommentId(), Content: "同问",
	})
	require.NoError(t, err)
	assert.False(t, r1.GetFail())
	assert.Equal(t, c.GetCommentId(), r1.GetComment().GetRootId(), "回复挂 parent 所在楼层")
	assert.Equal(t, c.GetCommentId(), r1.GetComment().GetParentId())
	assert.Equal(t, "6001", r1.GetComment().GetReplyToUserId())
	assert.Equal(t, int64(5), r1.GetCommentCount())

	// 回复的回复（平级挂同一楼，root 不变）
	r2, err := u.ReplyComment(ctx, &postv1.ReplyCommentRequest{
		Uid: "6003", PostId: "2001", ParentId: r1.GetComment().GetCommentId(), Content: "回复你",
	})
	require.NoError(t, err)
	assert.False(t, r2.GetFail())
	assert.Equal(t, c.GetCommentId(), r2.GetComment().GetRootId(), "回复的回复仍挂同一楼")
	assert.Equal(t, r1.GetComment().GetCommentId(), r2.GetComment().GetParentId())
	assert.Equal(t, "6002", r2.GetComment().GetReplyToUserId())
	assert.Equal(t, int64(6), r2.GetCommentCount())

	// 楼层 reply_count 同步（2 条回复）
	require.NoError(t, db.First(&row, mustParseID(t, c.GetCommentId())).Error)
	assert.Equal(t, int64(2), row.ReplyCount)

	// parent 错误分支：不存在 / 已删 → NotFound
	badR, err := u.ReplyComment(ctx, &postv1.ReplyCommentRequest{
		Uid: "6002", PostId: "2001", ParentId: "999999", Content: "x",
	})
	require.NoError(t, err)
	assert.Equal(t, errmsg.NotFound, badR.GetErr())
	_, err = u.DeleteComment(ctx, &postv1.DeleteCommentRequest{Uid: "6003", CommentId: r2.GetComment().GetCommentId()})
	require.NoError(t, err)
	badR, err = u.ReplyComment(ctx, &postv1.ReplyCommentRequest{
		Uid: "6002", PostId: "2001", ParentId: r2.GetComment().GetCommentId(), Content: "x",
	})
	require.NoError(t, err)
	assert.Equal(t, errmsg.NotFound, badR.GetErr(), "parent 已删 → NotFound")

	// parent 跨帖 → NotFound
	seedPost(t, db, postID3, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, 1000)
	otherC, err := u.CreateComment(ctx, &postv1.CreateCommentRequest{Uid: "6001", PostId: "2003", Content: "他帖评论"})
	require.NoError(t, err)
	badR, err = u.ReplyComment(ctx, &postv1.ReplyCommentRequest{
		Uid: "6002", PostId: "2001", ParentId: otherC.GetComment().GetCommentId(), Content: "x",
	})
	require.NoError(t, err)
	assert.Equal(t, errmsg.NotFound, badR.GetErr(), "parent 跨帖 → NotFound")
}

// TestCommentDelete 删评权限三态 + 计数按实删行数回收 + 删楼级联 + 幂等
func TestCommentDelete(t *testing.T) {
	u, db, _ := newUsecase(t)
	ctx := context.Background()
	seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, 1000)

	// root（6001）+ 两条回复（6002），楼层 reply_count=2、帖子 comment_count=5
	seedComment(t, db, 9601, postID1, 6001, 0, 0, postv1.CommentStatus_COMMENT_STATUS_NORMAL, 1000)
	seedComment(t, db, 9602, postID1, 6002, 9601, 9601, postv1.CommentStatus_COMMENT_STATUS_NORMAL, 2000)
	seedComment(t, db, 9603, postID1, 6002, 9601, 9602, postv1.CommentStatus_COMMENT_STATUS_NORMAL, 3000)
	require.NoError(t, db.Model(&models.PostComment{}).Where("id = ?", 9601).Update("reply_count", 2).Error)
	require.NoError(t, db.Model(&models.PostDetail{}).Where("id = ?", postID1).Update("comment_count", 5).Error)

	// 删回复（评论作者）：楼层 reply_count 2→1，帖子 comment_count 5→4
	resp, err := u.DeleteComment(ctx, &postv1.DeleteCommentRequest{Uid: "6002", CommentId: "9602"})
	require.NoError(t, err)
	assert.False(t, resp.GetFail())
	assert.True(t, resp.GetDeleted())
	assert.Equal(t, int64(4), resp.GetCommentCount())
	// GORM 惯例坑：First 的 dest 非零主键会并入 WHERE 条件，复用结构体跨主键查询前须清零
	var row models.PostComment
	require.NoError(t, db.First(&row, 9602).Error)
	assert.Equal(t, postv1.CommentStatus_COMMENT_STATUS_DELETED, row.Status)
	row = models.PostComment{}
	require.NoError(t, db.First(&row, 9601).Error)
	assert.Equal(t, int64(1), row.ReplyCount)

	// 幂等：重复删除成功且不重复减计数
	resp, err = u.DeleteComment(ctx, &postv1.DeleteCommentRequest{Uid: "6002", CommentId: "9602"})
	require.NoError(t, err)
	assert.False(t, resp.GetFail())
	assert.True(t, resp.GetDeleted())
	assert.Equal(t, int64(4), resp.GetCommentCount(), "重复删除不重复减计数")

	// 权限三态：他人 Forbidden；楼主（帖子作者）可删他人评论
	bad, err := u.DeleteComment(ctx, &postv1.DeleteCommentRequest{Uid: "6009", CommentId: "9603"})
	require.NoError(t, err)
	assert.True(t, bad.GetFail())
	assert.Equal(t, errmsg.Forbidden, bad.GetErr())
	resp, err = u.DeleteComment(ctx, &postv1.DeleteCommentRequest{Uid: "3001", CommentId: "9603"})
	require.NoError(t, err)
	assert.False(t, resp.GetFail())
	assert.Equal(t, int64(3), resp.GetCommentCount(), "楼主删评：计数 -1")
	row = models.PostComment{}
	require.NoError(t, db.First(&row, 9601).Error)
	assert.Zero(t, row.ReplyCount)

	// 删一级评论：级联软删整楼（1 层 + 2 回复 = 3 行），计数按实删行数 -3
	seedComment(t, db, 9611, postID1, 6001, 0, 0, postv1.CommentStatus_COMMENT_STATUS_NORMAL, 4000)
	seedComment(t, db, 9612, postID1, 6002, 9611, 9611, postv1.CommentStatus_COMMENT_STATUS_NORMAL, 5000)
	seedComment(t, db, 9613, postID1, 6002, 9611, 9612, postv1.CommentStatus_COMMENT_STATUS_NORMAL, 6000)
	require.NoError(t, db.Model(&models.PostComment{}).Where("id = ?", 9611).Update("reply_count", 2).Error)
	require.NoError(t, db.Model(&models.PostDetail{}).Where("id = ?", postID1).Update("comment_count", 10).Error)

	resp, err = u.DeleteComment(ctx, &postv1.DeleteCommentRequest{Uid: "6001", CommentId: "9611"})
	require.NoError(t, err)
	assert.False(t, resp.GetFail())
	assert.Equal(t, int64(7), resp.GetCommentCount(), "级联 1+2 行，计数 -3")
	var normal int64
	require.NoError(t, db.Model(&models.PostComment{}).
		Where("root_id = ? AND status = ?", 9611, postv1.CommentStatus_COMMENT_STATUS_NORMAL).Count(&normal).Error)
	assert.Zero(t, normal, "整楼级联软删")
	row = models.PostComment{}
	require.NoError(t, db.First(&row, 9611).Error)
	assert.Equal(t, postv1.CommentStatus_COMMENT_STATUS_DELETED, row.Status)

	// 幂等：重复删楼成功且计数不变
	resp, err = u.DeleteComment(ctx, &postv1.DeleteCommentRequest{Uid: "6001", CommentId: "9611"})
	require.NoError(t, err)
	assert.True(t, resp.GetDeleted())
	assert.Equal(t, int64(7), resp.GetCommentCount())

	// 不存在 → NotFound
	bad, err = u.DeleteComment(ctx, &postv1.DeleteCommentRequest{Uid: "6001", CommentId: "999999"})
	require.NoError(t, err)
	assert.Equal(t, errmsg.NotFound, bad.GetErr())
}

// TestCommentList 一级评论/楼内回复列表：过滤口径 + reply_count 下发 + 空页语义 + 状态裁决
func TestCommentList(t *testing.T) {
	u, db, _ := newUsecase(t)
	ctx := context.Background()
	seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, 1000)
	seedPost(t, db, postID2, authorID1, postv1.PostStatus_POST_STATUS_DRAFT, 1000)

	// 一楼（含 2 回复，其一软删）+ 二楼 + 软删楼 + 楼层下回复混入
	seedComment(t, db, 9701, postID1, 6001, 0, 0, postv1.CommentStatus_COMMENT_STATUS_NORMAL, 2000)
	seedComment(t, db, 9702, postID1, 6001, 0, 0, postv1.CommentStatus_COMMENT_STATUS_NORMAL, 1000)
	seedComment(t, db, 9703, postID1, 6001, 0, 0, postv1.CommentStatus_COMMENT_STATUS_DELETED, 5000)
	seedComment(t, db, 9711, postID1, 6002, 9701, 9701, postv1.CommentStatus_COMMENT_STATUS_NORMAL, 3000)
	// 构造 reply_to 指向被回复者（root 作者 6001），而非 seed 默认的评论者自身
	require.NoError(t, db.Model(&models.PostComment{}).Where("id = ?", 9711).Update("reply_to_user_id", 6001).Error)
	seedComment(t, db, 9712, postID1, 6002, 9701, 9701, postv1.CommentStatus_COMMENT_STATUS_DELETED, 4000)
	seedComment(t, db, 9721, postID1, 6002, 9702, 9702, postv1.CommentStatus_COMMENT_STATUS_NORMAL, 6000)
	require.NoError(t, db.Model(&models.PostComment{}).Where("id = ?", 9701).Update("reply_count", 2).Error)

	// 一级评论：仅 root_id=0 且 NORMAL，created_at DESC；reply_count 随列表下发
	resp, err := u.ListComments(ctx, &postv1.ListCommentsRequest{PostId: "2001"})
	require.NoError(t, err)
	assert.False(t, resp.GetFail())
	require.Len(t, resp.GetComments(), 2, "软删楼与回复不出现")
	assert.Equal(t, "9701", resp.GetComments()[0].GetCommentId())
	assert.Equal(t, "9702", resp.GetComments()[1].GetCommentId())
	assert.Equal(t, int64(2), resp.GetComments()[0].GetReplyCount(), "楼层回复数下发")
	require.Nil(t, resp.GetCursor(), "短取即到底：短页（< limit）带数据回空游标（与信息流口径一致）")

	// 游标翻页到底：短取即到底（末页带数据回空游标，空页同样回空游标）
	next, err := u.ListComments(ctx, &postv1.ListCommentsRequest{PostId: "2001", Cursor: &commonv1.Cursor{Str: "1500", Limit: 10}})
	require.NoError(t, err)
	assert.False(t, next.GetFail())
	require.Len(t, next.GetComments(), 1, "严格小于时间边界")
	assert.Equal(t, "9702", next.GetComments()[0].GetCommentId())
	last, err := u.ListComments(ctx, &postv1.ListCommentsRequest{PostId: "2001", Cursor: &commonv1.Cursor{Str: "1000", Limit: 10}})
	require.NoError(t, err)
	assert.Empty(t, last.GetComments())
	assert.Nil(t, last.GetCursor(), "空页 = 空列表 + 空游标（到底）")

	// 满页才回 next 游标（短取即到底的正向面）
	full, err := u.ListComments(ctx, &postv1.ListCommentsRequest{PostId: "2001", Cursor: &commonv1.Cursor{Limit: 1}})
	require.NoError(t, err)
	require.Len(t, full.GetComments(), 1, "满页（== limit）")
	require.NotNil(t, full.GetCursor(), "满页回 next 游标")

	// 状态裁决：非 PUBLISHED / 不存在 → NotFound
	bad, err := u.ListComments(ctx, &postv1.ListCommentsRequest{PostId: "2002"})
	require.NoError(t, err)
	assert.Equal(t, errmsg.NotFound, bad.GetErr())
	bad, err = u.ListComments(ctx, &postv1.ListCommentsRequest{PostId: "999999"})
	require.NoError(t, err)
	assert.Equal(t, errmsg.NotFound, bad.GetErr())

	// 楼内回复：仅本楼 NORMAL，平铺含回复的回复，reply_to 下发
	rr, err := u.ListReplies(ctx, &postv1.ListRepliesRequest{RootId: "9701"})
	require.NoError(t, err)
	assert.False(t, rr.GetFail())
	require.Len(t, rr.GetReplies(), 1, "软删回复不出现")
	assert.Equal(t, "9711", rr.GetReplies()[0].GetCommentId())
	assert.Equal(t, "9701", rr.GetReplies()[0].GetRootId())
	assert.Equal(t, "6001", rr.GetReplies()[0].GetReplyToUserId())

	// 楼层口径：回复 id 非楼层 / 不存在 / 楼层已删 / 帖子非 PUBLISHED → NotFound
	badR, err := u.ListReplies(ctx, &postv1.ListRepliesRequest{RootId: "9711"})
	require.NoError(t, err)
	assert.Equal(t, errmsg.NotFound, badR.GetErr(), "root_id 必须指向楼层")
	badR, err = u.ListReplies(ctx, &postv1.ListRepliesRequest{RootId: "999999"})
	require.NoError(t, err)
	assert.Equal(t, errmsg.NotFound, badR.GetErr())
	badR, err = u.ListReplies(ctx, &postv1.ListRepliesRequest{RootId: "9703"})
	require.NoError(t, err)
	assert.Equal(t, errmsg.NotFound, badR.GetErr(), "楼层已删 → NotFound")
	seedComment(t, db, 9731, postID2, 6001, 0, 0, postv1.CommentStatus_COMMENT_STATUS_NORMAL, 7000)
	badR, err = u.ListReplies(ctx, &postv1.ListRepliesRequest{RootId: "9731"})
	require.NoError(t, err)
	assert.Equal(t, errmsg.NotFound, badR.GetErr(), "帖子非 PUBLISHED → NotFound")
}
