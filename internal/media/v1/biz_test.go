package v1

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	commonv1 "rednote/gen/go/common/v1"
	mediav1 "rednote/gen/go/media/v1"
	models "rednote/internal/media/models"
	"rednote/packages/defaults"
	"rednote/packages/errmsg"
	"rednote/packages/utils"
	"rednote/test/testdb"
	"rednote/test/testmq"
	"rednote/test/testoss"
	"rednote/test/testsnowflake"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

// newUsecase 构造 biz 层测试环境：真实 repo + testdb 克隆库 + testmq/testoss 内存模拟。
// seedAsset / seedFile / oldTS 复用 repo_test.go 中的同包 helper。
func newUsecase(t *testing.T) (*MediaUsecaseImpl, *gorm.DB, *testmq.Client, *testoss.Client) {
	t.Helper()
	testsnowflake.InitTestSnoflake(1)
	db := testdb.Setup(t)
	mqc := testmq.New()
	s3 := testoss.New()
	return NewMediaUsecase(NewMediaRepo(db), mqc, db, s3), db, mqc, s3
}

// ---------- UploadMedia ----------

func TestMediaUsecase_UploadMedia(t *testing.T) {
	ctx := context.Background()

	t.Run("格式不支持被拒绝", func(t *testing.T) {
		uc, _, mqc, _ := newUsecase(t)
		resp, err := uc.UploadMedia(ctx, &mediav1.UploadMediaRequest{
			UploaderId: "3001",
			MediaMeta:  &mediav1.MediaMetaData{Format: mediav1.MediaFormat_MEDIA_FORMAT_WEBP},
		})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidFormat, resp.GetErr())
		assert.Nil(t, resp.GetAsset(), "被拒绝的请求不应返回资产")
		assert.Zero(t, mqc.Count(), "被拒绝的请求不应发消息")
	})

	t.Run("非法 uploader 被拒绝", func(t *testing.T) {
		uc, _, mqc, _ := newUsecase(t)
		resp, err := uc.UploadMedia(ctx, &mediav1.UploadMediaRequest{
			UploaderId: "not-a-number",
			MediaMeta:  &mediav1.MediaMetaData{Format: mediav1.MediaFormat_MEDIA_FORMAT_PNG},
		})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetErr())
		assert.Nil(t, resp.GetAsset(), "被拒绝的请求不应返回资产")
		assert.Zero(t, mqc.Count())
	})

	t.Run("上传成功：资产落库 + 签名URL + 延时消息", func(t *testing.T) {
		uc, db, mqc, s3 := newUsecase(t)
		resp, err := uc.UploadMedia(ctx, &mediav1.UploadMediaRequest{
			UploaderId: "3001",
			MediaMeta:  &mediav1.MediaMetaData{Format: mediav1.MediaFormat_MEDIA_FORMAT_PNG},
		})
		require.NoError(t, err)
		require.False(t, resp.GetFail())
		assert.Nil(t, resp.GetErr())

		// 响应：资产 + 含对象键的签名 URL + 过期时间
		require.NotNil(t, resp.GetAsset())
		id, err := strconv.ParseInt(resp.GetAsset().GetBase().GetId(), 10, 64)
		require.NoError(t, err)
		key := utils.BuildKey(id, mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN, mediav1.MediaFormat_MEDIA_FORMAT_PNG)
		assert.Contains(t, resp.GetSignedUrl(), key, "签名 URL 应包含对象键")
		assert.Greater(t, resp.GetExpireAt(), time.Now().UnixMilli(), "过期时间应在未来")

		// 响应：资产字段完整
		assert.Equal(t, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_UPLOADING, resp.GetAsset().GetStatus())
		assert.Equal(t, "3001", resp.GetAsset().GetUploaderId())
		assert.Zero(t, resp.GetAsset().GetRefCount(), "新建资产引用计数应为 0")
		assert.NotZero(t, resp.GetAsset().GetBase().GetCreatedAt())

		// DB：资产已落库（UPLOADING）
		var got models.MediaAsset
		require.NoError(t, db.Where("id = ?", id).First(&got).Error)
		assert.Equal(t, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_UPLOADING, got.Status)
		assert.Equal(t, int64(3001), got.UploaderID)

		// S3：预签名调用已发生
		require.NotEmpty(t, s3.Ops())

		// MQ：处理消息（UploadMedia）已入列——需求变更：立即投递（原延时 15s 等待改为消费端退避重试）
		msgs := mqc.SentBySubject(defaults.MediaMessageStream)
		require.Len(t, msgs, 1)
		assert.LessOrEqual(t, msgs[0].SendAt, time.Now().UnixMilli(), "应为立即消息")

		var mediaMsg mediav1.MediaMessage
		require.NoError(t, proto.Unmarshal(msgs[0].PayLoad, &mediaMsg))
		up := mediaMsg.GetUploadMedia()
		require.NotNil(t, up)
		assert.Equal(t, key, up.GetKey())
		assert.Equal(t, s3.Bucket(), up.GetBucket())
	})

	t.Run("S3 失败时事务整体回滚", func(t *testing.T) {
		uc, db, mqc, s3 := newUsecase(t)
		s3.SetError(errors.New("s3 down"))

		_, err := uc.UploadMedia(ctx, &mediav1.UploadMediaRequest{
			UploaderId: "3001",
			MediaMeta:  &mediav1.MediaMetaData{Format: mediav1.MediaFormat_MEDIA_FORMAT_JPG},
		})
		require.Error(t, err)

		var cnt int64
		require.NoError(t, db.Model(&models.MediaAsset{}).Count(&cnt).Error)
		assert.Zero(t, cnt, "事务应回滚，不残留资产")
		assert.Zero(t, mqc.Count())
	})

	t.Run("MQ 失败时事务整体回滚", func(t *testing.T) {
		uc, db, mqc, _ := newUsecase(t)
		mqc.SetSendError(errors.New("mq down"))

		_, err := uc.UploadMedia(ctx, &mediav1.UploadMediaRequest{
			UploaderId: "3001",
			MediaMeta:  &mediav1.MediaMetaData{Format: mediav1.MediaFormat_MEDIA_FORMAT_JPG},
		})
		require.Error(t, err)

		var cnt int64
		require.NoError(t, db.Model(&models.MediaAsset{}).Count(&cnt).Error)
		assert.Zero(t, cnt, "消息发送失败应随事务回滚")
	})
}

// ---------- GetMediaAssets ----------

func TestMediaUsecase_GetMediaAssets(t *testing.T) {
	ctx := context.Background()

	t.Run("混合结果：命中 / 非法ID / 未找到", func(t *testing.T) {
		uc, db, _, _ := newUsecase(t)
		seedAsset(t, db, assetID1, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, 0, time.Now().UnixMilli())

		resp, err := uc.GetMediaAssets(ctx, &mediav1.GetMediaAssetsRequest{
			Ids: []string{strconv.FormatInt(assetID1, 10), "bad", "999999"},
		})
		require.NoError(t, err)
		require.Len(t, resp.GetResults(), 3)

		// 非法 ID 结果先返回，其后按有效 ID 顺序
		assert.True(t, resp.GetResults()[0].GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetResults()[0].GetErr())
		assert.Equal(t, "bad", resp.GetResults()[0].GetId(), "失败项应回填请求串")
		assert.Nil(t, resp.GetResults()[0].GetAsset())

		assert.False(t, resp.GetResults()[1].GetFail())
		assert.Equal(t, strconv.FormatInt(assetID1, 10), resp.GetResults()[1].GetId(), "成功项应回填 id")
		assert.Equal(t, assetID1, mustParseID(t, resp.GetResults()[1].GetAsset().GetBase().GetId()))

		assert.True(t, resp.GetResults()[2].GetFail())
		assert.Equal(t, errmsg.NotFound, resp.GetResults()[2].GetErr())
		assert.Equal(t, "999999", resp.GetResults()[2].GetId(), "NotFound 项必须回填 id（回归）")
		assert.Nil(t, resp.GetResults()[2].GetAsset())
	})

	// 回归：NotFound / 非法 ID 结果曾漏回填 id，调用方无法定位失败项
	t.Run("全部未命中时每项都回填请求 id（回归）", func(t *testing.T) {
		// Arrange
		uc, _, _, _ := newUsecase(t)

		// Act
		resp, err := uc.GetMediaAssets(ctx, &mediav1.GetMediaAssetsRequest{
			Ids: []string{"111111", "bad-id", "222222"},
		})

		// Assert：非法 ID 项先返回，其余按有效 ID 顺序，每项都携带对应请求 id
		require.NoError(t, err)
		require.Len(t, resp.GetResults(), 3)

		for i, wantID := range []string{"bad-id", "111111", "222222"} {
			got := resp.GetResults()[i]
			assert.True(t, got.GetFail())
			assert.Equal(t, wantID, got.GetId(), "每个结果都必须回填对应请求 id")
			assert.Nil(t, got.GetAsset())
		}
		assert.Equal(t, errmsg.InvalidID, resp.GetResults()[0].GetErr())
		assert.Equal(t, errmsg.NotFound, resp.GetResults()[1].GetErr())
		assert.Equal(t, errmsg.NotFound, resp.GetResults()[2].GetErr())
	})

	t.Run("空入参返回空结果", func(t *testing.T) {
		uc, _, _, _ := newUsecase(t)
		resp, err := uc.GetMediaAssets(ctx, &mediav1.GetMediaAssetsRequest{})
		require.NoError(t, err)
		assert.Empty(t, resp.GetResults())
	})
}

// ---------- GetMediaAsset ----------

func TestMediaUsecase_GetMediaAsset(t *testing.T) {
	ctx := context.Background()

	t.Run("命中返回资产", func(t *testing.T) {
		uc, db, _, _ := newUsecase(t)
		seedAsset(t, db, assetID1, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, 0, time.Now().UnixMilli())

		resp, err := uc.GetMediaAsset(ctx, &mediav1.GetMediaAssetRequest{Id: strconv.FormatInt(assetID1, 10)})
		require.NoError(t, err)
		require.False(t, resp.GetFail())
		assert.Nil(t, resp.GetErr())
		assert.Equal(t, assetID1, mustParseID(t, resp.GetAsset().GetBase().GetId()))
		assert.Equal(t, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, resp.GetAsset().GetStatus())
	})

	t.Run("非法 ID 返回 InvalidID", func(t *testing.T) {
		uc, _, _, _ := newUsecase(t)
		resp, err := uc.GetMediaAsset(ctx, &mediav1.GetMediaAssetRequest{Id: "bad"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetErr())
		assert.Nil(t, resp.GetAsset())
	})

	t.Run("未找到返回 NotFound", func(t *testing.T) {
		uc, _, _, _ := newUsecase(t)
		resp, err := uc.GetMediaAsset(ctx, &mediav1.GetMediaAssetRequest{Id: "999999"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.NotFound, resp.GetErr())
		assert.Nil(t, resp.GetAsset())
	})
}

// ---------- GetMediaAssetFiles ----------

func TestMediaUsecase_GetMediaAssetFiles(t *testing.T) {
	ctx := context.Background()

	t.Run("按 (asset_id, type) 配对命中，未命中返回 NotFound", func(t *testing.T) {
		uc, db, _, _ := newUsecase(t)
		now := time.Now().UnixMilli()
		seedAsset(t, db, assetID1, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, 0, now)
		seedFile(t, db, 4001, assetID1, mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN)
		seedFile(t, db, 4002, assetID1, mediav1.MediaFileType_MEDIA_FILE_TYPE_540)

		resp, err := uc.GetMediaAssetFiles(ctx, &mediav1.GetMediaAssetFilesRequest{
			Queries: []*mediav1.MediaAssetFileQuery{
				{Id: "bad", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN},
				{Id: strconv.FormatInt(assetID1, 10), Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN},
				{Id: strconv.FormatInt(assetID1, 10), Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_1080}, // 无此变体
			},
		})
		require.NoError(t, err)
		require.Len(t, resp.GetFiles(), 3)

		// 非法 query 结果先返回，其后按有效 query 顺序；每项都携带原 query
		assert.True(t, resp.GetFiles()[0].GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetFiles()[0].GetErr())
		assert.Equal(t, "bad", resp.GetFiles()[0].GetQuery().GetId())
		assert.Equal(t, mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN, resp.GetFiles()[0].GetQuery().GetType())

		assert.False(t, resp.GetFiles()[1].GetFail())
		assert.Equal(t, int64(4001), mustParseID(t, resp.GetFiles()[1].GetFile().GetBase().GetId()))
		assert.Equal(t, strconv.FormatInt(assetID1, 10), resp.GetFiles()[1].GetQuery().GetId())
		assert.Equal(t, mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN, resp.GetFiles()[1].GetQuery().GetType())

		assert.True(t, resp.GetFiles()[2].GetFail())
		assert.Equal(t, errmsg.NotFound, resp.GetFiles()[2].GetErr())
		assert.Equal(t, strconv.FormatInt(assetID1, 10), resp.GetFiles()[2].GetQuery().GetId())
		assert.Equal(t, mediav1.MediaFileType_MEDIA_FILE_TYPE_1080, resp.GetFiles()[2].GetQuery().GetType())
		assert.Nil(t, resp.GetFiles()[2].GetFile())
	})
}

// ---------- GetMediaAssetFile ----------

func TestMediaUsecase_GetMediaAssetFile(t *testing.T) {
	ctx := context.Background()

	t.Run("按 (id, type) 命中返回文件", func(t *testing.T) {
		uc, db, _, _ := newUsecase(t)
		seedAsset(t, db, assetID1, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, 0, time.Now().UnixMilli())
		seedFile(t, db, 4001, assetID1, mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN)

		resp, err := uc.GetMediaAssetFile(ctx, &mediav1.GetMediaAssetFileRequest{
			Id:   strconv.FormatInt(assetID1, 10),
			Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN,
		})
		require.NoError(t, err)
		require.False(t, resp.GetFail())
		assert.Nil(t, resp.GetErr())
		assert.Equal(t, int64(4001), mustParseID(t, resp.GetFile().GetBase().GetId()))
		assert.Equal(t, mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN, resp.GetFile().GetType())
	})

	t.Run("无此变体返回 NotFound", func(t *testing.T) {
		uc, db, _, _ := newUsecase(t)
		seedAsset(t, db, assetID1, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, 0, time.Now().UnixMilli())
		seedFile(t, db, 4001, assetID1, mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN)

		resp, err := uc.GetMediaAssetFile(ctx, &mediav1.GetMediaAssetFileRequest{
			Id:   strconv.FormatInt(assetID1, 10),
			Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_216, // 无此变体
		})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.NotFound, resp.GetErr())
		assert.Nil(t, resp.GetFile())
	})

	t.Run("非法 ID 返回 InvalidID", func(t *testing.T) {
		uc, _, _, _ := newUsecase(t)
		resp, err := uc.GetMediaAssetFile(ctx, &mediav1.GetMediaAssetFileRequest{
			Id:   "bad",
			Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN,
		})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetErr())
		assert.Nil(t, resp.GetFile())
	})
}

// ---------- ListMediaAssetsBefore ----------

func TestMediaUsecase_ListMediaAssetsBefore(t *testing.T) {
	ctx := context.Background()

	t.Run("返回资产列表与游标", func(t *testing.T) {
		uc, db, _, _ := newUsecase(t)
		seedAsset(t, db, assetID1, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, 0, time.Now().Add(-time.Hour).UnixMilli())
		seedAsset(t, db, assetID2, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, 0, time.Now().Add(-time.Minute).UnixMilli())

		resp, err := uc.ListMediaAssetsBefore(ctx, &mediav1.ListMediaAssetsBeforeRequest{
			Cursor: &commonv1.Cursor{Str: strconv.FormatInt(time.Now().UnixMilli(), 10), Limit: 2},
		})
		require.NoError(t, err)
		require.False(t, resp.GetFail())
		require.Len(t, resp.GetAssets(), 2)
		require.NotNil(t, resp.GetCursor(), "满页（== limit）回 next 游标")
		assert.Equal(t,
			strconv.FormatInt(resp.GetAssets()[1].GetBase().GetCreatedAt(), 10),
			resp.GetCursor().GetStr(),
			"游标应为最后一条的 created_at")
	})

	t.Run("空结果回空列表+空游标（不以 NotFound 表达空页）", func(t *testing.T) {
		uc, _, _, _ := newUsecase(t)
		resp, err := uc.ListMediaAssetsBefore(ctx, &mediav1.ListMediaAssetsBeforeRequest{})
		require.NoError(t, err)
		assert.False(t, resp.GetFail())
		assert.Empty(t, resp.GetAssets())
		assert.Nil(t, resp.GetCursor(), "短取即到底：空页空游标")
	})
}

// ---------- BanMedia ----------

func TestMediaUsecase_BanMedia(t *testing.T) {
	ctx := context.Background()

	t.Run("非法 asset id 被拒绝", func(t *testing.T) {
		uc, _, mqc, _ := newUsecase(t)
		resp, err := uc.BanMedia(ctx, &mediav1.BanMediaRequest{AssetId: "bad"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetErr())
		assert.Zero(t, mqc.Count())
	})

	t.Run("封禁成功：状态落库 + 消息发出", func(t *testing.T) {
		uc, db, mqc, _ := newUsecase(t)
		seedAsset(t, db, assetID1, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, 0, time.Now().UnixMilli())

		resp, err := uc.BanMedia(ctx, &mediav1.BanMediaRequest{AssetId: strconv.FormatInt(assetID1, 10)})
		require.NoError(t, err)
		assert.False(t, resp.GetFail())

		var got models.MediaAsset
		require.NoError(t, db.Where("id = ?", assetID1).First(&got).Error)
		assert.Equal(t, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_BANNED, got.Status)

		msgs := mqc.SentBySubject(defaults.MediaMessageStream)
		require.Len(t, msgs, 1)
		var mediaMsg mediav1.MediaMessage
		require.NoError(t, proto.Unmarshal(msgs[0].PayLoad, &mediaMsg))
		require.NotNil(t, mediaMsg.GetBanMedia())
		assert.Equal(t, strconv.FormatInt(assetID1, 10), mediaMsg.GetBanMedia().GetAssetId())
	})
}

// ---------- ModifyMediaAssetRefCount ----------

func TestMediaUsecase_ModifyMediaAssetRefCount(t *testing.T) {
	ctx := context.Background()

	t.Run("非法 id 被拒绝", func(t *testing.T) {
		uc, _, _, _ := newUsecase(t)
		resp, err := uc.ModifyMediaAssetRefCount(ctx, &mediav1.ModifyMediaAssetRefCountRequest{
			Queries: []*mediav1.ModifyMediaAssetRefCountQuery{{Id: "bad", Delta: 1}},
		})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetErr())
	})

	t.Run("计数修改成功", func(t *testing.T) {
		uc, db, _, _ := newUsecase(t)
		seedAsset(t, db, assetID1, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, 5, time.Now().UnixMilli())

		resp, err := uc.ModifyMediaAssetRefCount(ctx, &mediav1.ModifyMediaAssetRefCountRequest{
			Queries: []*mediav1.ModifyMediaAssetRefCountQuery{{Id: strconv.FormatInt(assetID1, 10), Delta: -2}},
		})
		require.NoError(t, err)
		assert.False(t, resp.GetFail())

		var got models.MediaAsset
		require.NoError(t, db.Where("id = ?", assetID1).First(&got).Error)
		assert.Equal(t, int64(3), got.RefCount)
	})
}

// ---------- DeleteUnusedMedia ----------

func TestMediaUsecase_DeleteUnusedMedia(t *testing.T) {
	ctx := context.Background()

	t.Run("删除未使用资产：状态变更 + 直发消息", func(t *testing.T) {
		uc, db, mqc, _ := newUsecase(t)
		seedAsset(t, db, assetID1, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_FAILED, 0, oldTS())
		seedFile(t, db, 4001, assetID1, mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN)

		resp, err := uc.DeleteUnusedMedia(ctx, &mediav1.DeleteUnusedMediaRequest{Limit: 10})
		require.NoError(t, err)
		assert.False(t, resp.GetFail())
		assert.Equal(t, int64(1), resp.GetRowsAffected())

		var got models.MediaAsset
		require.NoError(t, db.Where("id = ?", assetID1).First(&got).Error)
		assert.Equal(t, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_DELETED, got.Status)

		// 事务外尽力而为直发（非 SendTX）
		msgs := mqc.SentBySubject(defaults.MediaMessageStream)
		require.Len(t, msgs, 1)
		var mediaMsg mediav1.MediaMessage
		require.NoError(t, proto.Unmarshal(msgs[0].PayLoad, &mediaMsg))
		require.NotNil(t, mediaMsg.GetDeleteMedia())
		assert.Equal(t, strconv.FormatInt(assetID1, 10), mediaMsg.GetDeleteMedia().GetAssetId())
	})

	t.Run("MQ 发送失败不影响删除结果（尽力而为）", func(t *testing.T) {
		uc, db, mqc, _ := newUsecase(t)
		mqc.SetSendError(errors.New("mq down"))
		seedAsset(t, db, assetID1, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_FAILED, 0, oldTS())

		resp, err := uc.DeleteUnusedMedia(ctx, &mediav1.DeleteUnusedMediaRequest{Limit: 10})
		require.NoError(t, err)
		assert.Equal(t, int64(1), resp.GetRowsAffected())

		var got models.MediaAsset
		require.NoError(t, db.Where("id = ?", assetID1).First(&got).Error)
		assert.Equal(t, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_DELETED, got.Status, "状态变更不应受消息发送失败影响")
	})

	t.Run("无闲置资产时不动任何记录", func(t *testing.T) {
		uc, db, mqc, _ := newUsecase(t)
		seedAsset(t, db, assetID1, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, 3, oldTS()) // 有引用

		resp, err := uc.DeleteUnusedMedia(ctx, &mediav1.DeleteUnusedMediaRequest{Limit: 10})
		require.NoError(t, err)
		assert.Zero(t, resp.GetRowsAffected())
		assert.Zero(t, mqc.Count())

		var got models.MediaAsset
		require.NoError(t, db.Where("id = ?", assetID1).First(&got).Error)
		assert.Equal(t, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, got.Status)
	})
}

// mustParseID 将 proto 中的字符串 ID 解析为 int64
func mustParseID(t *testing.T, id string) int64 {
	t.Helper()
	parsed, err := strconv.ParseInt(id, 10, 64)
	require.NoError(t, err)
	return parsed
}

// ---------- SignMediaFileURLs ----------

// TestMediaUsecase_SignMediaFileURLs 批量签发展示 GET URL：逐项结果 + TTL 1 小时 + 纯读
func TestMediaUsecase_SignMediaFileURLs(t *testing.T) {
	newReq := func(refs ...*mediav1.MediaAssetFileQuery) *mediav1.SignMediaFileURLsRequest {
		return &mediav1.SignMediaFileURLsRequest{Refs: refs}
	}

	t.Run("成功：默认 TTL 24 小时（私有内容）", func(t *testing.T) {
		u, db, _, _ := newUsecase(t)
		asset := seedAsset(t, db, 9100, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, 0, oldTS())
		seedFile(t, db, 9101, asset.ID, mediav1.MediaFileType_MEDIA_FILE_TYPE_216)

		before := time.Now()
		resp, err := u.SignMediaFileURLs(context.Background(), newReq(
			&mediav1.MediaAssetFileQuery{Id: strconv.FormatInt(asset.ID, 10), Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_216},
		))
		require.NoError(t, err)
		require.False(t, resp.Fail)
		require.Len(t, resp.Urls, 1)

		got := resp.Urls[0]
		assert.False(t, got.GetFail())
		assert.Empty(t, got.GetErr().GetCode(), "成功项不得携带错误")
		assert.Equal(t, strconv.FormatInt(asset.ID, 10), got.GetAssetId(), "Key 回显")
		assert.Equal(t, mediav1.MediaFileType_MEDIA_FILE_TYPE_216, got.GetType(), "变体回显")
		assert.NotEmpty(t, got.GetSignedUrl())

		expire := time.UnixMilli(got.GetExpireAt())
		assert.WithinDuration(t, before.Add(24*time.Hour), expire, 2*time.Minute, "默认 TTL 24h（私有内容）")
	})

	t.Run("显式 TTL（公开内容长 TTL）", func(t *testing.T) {
		u, db, _, _ := newUsecase(t)
		asset := seedAsset(t, db, 9110, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, 0, oldTS())
		seedFile(t, db, 9111, asset.ID, mediav1.MediaFileType_MEDIA_FILE_TYPE_216)

		before := time.Now()
		resp, err := u.SignMediaFileURLs(context.Background(), &mediav1.SignMediaFileURLsRequest{
			Refs:       []*mediav1.MediaAssetFileQuery{{Id: strconv.FormatInt(asset.ID, 10), Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_216}},
			TtlSeconds: int64((7 * 24 * time.Hour) / time.Second), // 7d 上限边界
		})
		require.NoError(t, err)
		require.False(t, resp.Fail)
		require.Len(t, resp.Urls, 1)
		assert.False(t, resp.Urls[0].GetFail())

		expire := time.UnixMilli(resp.Urls[0].GetExpireAt())
		assert.WithinDuration(t, before.Add(7*24*time.Hour), expire, 2*time.Minute, "7d 上限边界应放行")
	})

	t.Run("TTL 超 7d 上限报 InvalidArgument", func(t *testing.T) {
		u, _, _, _ := newUsecase(t)
		resp, err := u.SignMediaFileURLs(context.Background(), &mediav1.SignMediaFileURLsRequest{
			Refs:       []*mediav1.MediaAssetFileQuery{{Id: "9100", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_216}},
			TtlSeconds: int64((8 * 24 * time.Hour) / time.Second),
		})
		require.NoError(t, err)
		assert.True(t, resp.Fail)
		assert.Equal(t, errmsg.InvalidArgument.Code, resp.Err.GetCode())
		assert.Empty(t, resp.Urls, "TTL 非法时不得签发任何 URL")
	})

	t.Run("非法 ID 逐项 InvalidID", func(t *testing.T) {
		u, _, _, _ := newUsecase(t)
		resp, err := u.SignMediaFileURLs(context.Background(), newReq(
			&mediav1.MediaAssetFileQuery{Id: "not-a-snowflake", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_216},
		))
		require.NoError(t, err)
		require.Len(t, resp.Urls, 1)
		assert.True(t, resp.Urls[0].GetFail())
		assert.Equal(t, errmsg.InvalidID.Code, resp.Urls[0].GetErr().GetCode())
		assert.Equal(t, "not-a-snowflake", resp.Urls[0].GetAssetId(), "非法 ID 也回显 Key 供对齐")
	})

	t.Run("查无文件逐项 NotFound", func(t *testing.T) {
		u, db, _, _ := newUsecase(t)
		asset := seedAsset(t, db, 9200, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, 0, oldTS())
		_ = asset
		resp, err := u.SignMediaFileURLs(context.Background(), newReq(
			&mediav1.MediaAssetFileQuery{Id: "9200", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_1080},
		))
		require.NoError(t, err)
		require.Len(t, resp.Urls, 1)
		assert.True(t, resp.Urls[0].GetFail())
		assert.Equal(t, errmsg.NotFound.Code, resp.Urls[0].GetErr().GetCode())
	})

	t.Run("混合批量逐项对齐", func(t *testing.T) {
		u, db, _, _ := newUsecase(t)
		asset := seedAsset(t, db, 9300, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, 0, oldTS())
		seedFile(t, db, 9301, asset.ID, mediav1.MediaFileType_MEDIA_FILE_TYPE_540)

		resp, err := u.SignMediaFileURLs(context.Background(), newReq(
			&mediav1.MediaAssetFileQuery{Id: strconv.FormatInt(asset.ID, 10), Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_540},
			&mediav1.MediaAssetFileQuery{Id: "bad-id", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_540},
			&mediav1.MediaAssetFileQuery{Id: "9300", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN},
		))
		require.NoError(t, err)
		require.Len(t, resp.Urls, 3, "结果与请求逐项对应")

		// 契约：不保证顺序，按 Key（asset_id + type）定位断言
		byKey := map[string]*mediav1.MediaFileURL{}
		for _, u := range resp.Urls {
			byKey[u.GetAssetId()+"/"+u.GetType().String()] = u
		}

		ok := byKey[strconv.FormatInt(asset.ID, 10)+"/"+mediav1.MediaFileType_MEDIA_FILE_TYPE_540.String()]
		require.NotNil(t, ok)
		assert.False(t, ok.GetFail())
		assert.NotEmpty(t, ok.GetSignedUrl())

		bad := byKey["bad-id/"+mediav1.MediaFileType_MEDIA_FILE_TYPE_540.String()]
		require.NotNil(t, bad)
		assert.True(t, bad.GetFail())
		assert.Equal(t, errmsg.InvalidID.Code, bad.GetErr().GetCode())

		nf := byKey["9300/"+mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN.String()]
		require.NotNil(t, nf)
		assert.True(t, nf.GetFail())
		assert.Equal(t, errmsg.NotFound.Code, nf.GetErr().GetCode())
	})

	t.Run("空 refs 返回空结果不 panic", func(t *testing.T) {
		u, _, _, _ := newUsecase(t)
		resp, err := u.SignMediaFileURLs(context.Background(), &mediav1.SignMediaFileURLsRequest{})
		require.NoError(t, err)
		require.False(t, resp.Fail)
		assert.Empty(t, resp.Urls, "空集合边界")
	})
}
