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

		// MQ：延时消息（UploadMedia）已入列
		msgs := mqc.SentBySubject(defaults.MediaMessageStream)
		require.Len(t, msgs, 1)
		assert.Greater(t, msgs[0].SendAt, time.Now().UnixMilli(), "应为延时消息")

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

		assert.False(t, resp.GetResults()[1].GetFail())
		assert.Equal(t, assetID1, mustParseID(t, resp.GetResults()[1].GetAsset().GetBase().GetId()))

		assert.True(t, resp.GetResults()[2].GetFail())
		assert.Equal(t, errmsg.NotFound, resp.GetResults()[2].GetErr())
	})

	t.Run("空入参返回空结果", func(t *testing.T) {
		uc, _, _, _ := newUsecase(t)
		resp, err := uc.GetMediaAssets(ctx, &mediav1.GetMediaAssetsRequest{})
		require.NoError(t, err)
		assert.Empty(t, resp.GetResults())
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

		// 非法 query 结果先返回，其后按有效 query 顺序
		assert.True(t, resp.GetFiles()[0].GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetFiles()[0].GetErr())

		assert.False(t, resp.GetFiles()[1].GetFail())
		assert.Equal(t, int64(4001), mustParseID(t, resp.GetFiles()[1].GetFile().GetBase().GetId()))

		assert.True(t, resp.GetFiles()[2].GetFail())
		assert.Equal(t, errmsg.NotFound, resp.GetFiles()[2].GetErr())
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
			Cursor: &commonv1.Cursor{Str: strconv.FormatInt(time.Now().UnixMilli(), 10), Limit: 10},
		})
		require.NoError(t, err)
		require.False(t, resp.GetFail())
		require.Len(t, resp.GetAssets(), 2)
		require.NotNil(t, resp.GetCursor(), "有数据时应返回游标")
		assert.Equal(t,
			strconv.FormatInt(resp.GetAssets()[1].GetBase().GetCreatedAt(), 10),
			resp.GetCursor().GetStr(),
			"游标应为最后一条的 created_at")
	})

	t.Run("无数据返回 NotFound", func(t *testing.T) {
		uc, _, _, _ := newUsecase(t)
		resp, err := uc.ListMediaAssetsBefore(ctx, &mediav1.ListMediaAssetsBeforeRequest{})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.NotFound, resp.GetErr())
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
