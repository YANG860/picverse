package v1

import (
	"context"
	"errors"
	"testing"
	"time"

	mediav1 "rednote/gen/go/media/v1"
	common "rednote/internal/common/models"
	models "rednote/internal/media/models"
	"rednote/test/testdb"
	"rednote/test/testsnowflake"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// 固定测试 ID，避免依赖雪花生成结果，便于断言
const (
	assetID1 int64 = 1001
	assetID2 int64 = 1002
	assetID3 int64 = 1003
)

// oldTS 返回 16 分钟前的时间戳（毫秒），用于命中 GetUnusedMedia 的 15 分钟窗口
func oldTS() int64 { return time.Now().Add(-16 * time.Minute).UnixMilli() }

func newRepo(t *testing.T) (*MediaRepoImpl, *gorm.DB) {
	t.Helper()
	testsnowflake.InitTestSnoflake(1)
	db := testdb.Setup(t)
	return NewMediaRepo(db), db
}

func seedAsset(t *testing.T, db *gorm.DB, id int64, status mediav1.MediaAssetStatus, refCount, createdAt int64) *models.MediaAsset {
	t.Helper()
	a := &models.MediaAsset{
		BaseModel:  common.BaseModel{ID: id, CreatedAt: createdAt, UpdatedAt: createdAt},
		Status:     status,
		RefCount:   refCount,
		UploaderID: 8888,
	}
	require.NoError(t, db.Create(a).Error)
	return a
}

func seedFile(t *testing.T, db *gorm.DB, id, assetID int64, ftype mediav1.MediaFileType) *models.MediaFile {
	t.Helper()
	f := &models.MediaFile{
		BaseModel:     common.BaseModel{ID: id},
		AssetID:       assetID,
		Type:          ftype,
		ObjKey:        "media/obj",
		Bucket:        "rednote",
		Sha256Sum:     "abc",
		SizeBytes:     1024,
		Format:        mediav1.MediaFormat_MEDIA_FORMAT_WEBP,
		Height:        100,
		Width:         200,
		MediaMetaData: datatypes.JSONMap{"k": "v"},
	}
	require.NoError(t, db.Create(f).Error)
	return f
}

// ---------- MediaAsset ----------

func TestCreateMediaAsset(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	asset := &models.MediaAsset{
		Status:     mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_UPLOADING,
		RefCount:   0,
		UploaderID: 7777,
	}
	require.NoError(t, repo.CreateMediaAsset(ctx, asset))

	// BeforeCreate 钩子应填充雪花 ID，autoCreateTime 填充毫秒时间戳
	assert.NotZero(t, asset.ID, "创建后应自动填充雪花 ID")
	assert.NotZero(t, asset.CreatedAt, "创建后应自动填充 created_at")

	var got models.MediaAsset
	require.NoError(t, db.Where("id = ?", asset.ID).First(&got).Error)
	assert.Equal(t, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_UPLOADING, got.Status)
	assert.Equal(t, int64(7777), got.UploaderID)
}

func TestGetMediaAssets(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	seedAsset(t, db, assetID1, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, 1, time.Now().UnixMilli())
	seedAsset(t, db, assetID2, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, 2, time.Now().UnixMilli())

	t.Run("批量命中", func(t *testing.T) {
		assets, err := repo.GetMediaAssets(ctx, []int64{assetID1, assetID2})
		require.NoError(t, err)
		require.Len(t, assets, 2)
		got := map[int64]int64{}
		for _, a := range assets {
			got[a.ID] = a.RefCount
		}
		assert.Equal(t, map[int64]int64{assetID1: 1, assetID2: 2}, got)
	})

	t.Run("部分不存在只返回命中的", func(t *testing.T) {
		assets, err := repo.GetMediaAssets(ctx, []int64{assetID1, 999999})
		require.NoError(t, err)
		require.Len(t, assets, 1)
		assert.Equal(t, assetID1, assets[0].ID)
	})

	t.Run("空入参返回空集", func(t *testing.T) {
		assets, err := repo.GetMediaAssets(ctx, []int64{})
		require.NoError(t, err)
		assert.Empty(t, assets)
	})
}

func TestListMediaAssetsBefore(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	base := time.Now().Add(-time.Hour).UnixMilli()
	seedAsset(t, db, assetID1, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, 0, base-2000) // 最旧
	seedAsset(t, db, assetID2, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, 0, base-1000)
	seedAsset(t, db, assetID3, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, 0, base+1000) // 超出阈值

	t.Run("按 created_at 降序且严格小于阈值", func(t *testing.T) {
		assets, err := repo.ListMediaAssetsBefore(ctx, base, 10)
		require.NoError(t, err)
		require.Len(t, assets, 2)
		assert.Equal(t, []int64{assetID2, assetID1}, []int64{assets[0].ID, assets[1].ID},
			"应按 created_at DESC 排序且不包含等于/晚于阈值的记录")
	})

	t.Run("limit 生效", func(t *testing.T) {
		assets, err := repo.ListMediaAssetsBefore(ctx, base, 1)
		require.NoError(t, err)
		require.Len(t, assets, 1)
		assert.Equal(t, assetID2, assets[0].ID)
	})
}

func TestDeleteMediaAssets(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	now := time.Now().UnixMilli()
	seedAsset(t, db, assetID1, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, 0, now)
	seedAsset(t, db, assetID2, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, 0, now)

	info, err := repo.DeleteMediaAssets(ctx, []int64{assetID1})
	require.NoError(t, err)
	assert.Equal(t, int64(1), info.RowsAffected)

	var count int64
	require.NoError(t, db.Model(&models.MediaAsset{}).Count(&count).Error)
	assert.Equal(t, int64(1), count, "仅应删除指定 ID")
}

func TestSetMediaAssetsDeleted(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	now := time.Now().UnixMilli()
	seedAsset(t, db, assetID1, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, 0, now)
	seedAsset(t, db, assetID2, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_FAILED, 0, now)

	info, err := repo.SetMediaAssetsDeleted(ctx, []int64{assetID1, assetID2})
	require.NoError(t, err)
	assert.Equal(t, int64(2), info.RowsAffected)

	var statuses []int32
	require.NoError(t, db.Model(&models.MediaAsset{}).Order("id").Pluck("status", &statuses).Error)
	assert.Equal(t, []int32{
		int32(mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_DELETED),
		int32(mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_DELETED),
	}, statuses)
}

func TestSetMediaAssetStatus_CAS(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	seedAsset(t, db, assetID1, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, 0, time.Now().UnixMilli())

	t.Run("from 匹配时更新成功", func(t *testing.T) {
		info, err := repo.SetMediaAssetStatus(ctx, assetID1,
			mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE,
			mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_DELETED)
		require.NoError(t, err)
		assert.Equal(t, int64(1), info.RowsAffected)

		var got models.MediaAsset
		require.NoError(t, db.Where("id = ?", assetID1).First(&got).Error)
		assert.Equal(t, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_DELETED, got.Status)
	})

	t.Run("from 不匹配时不更新（CAS 语义）", func(t *testing.T) {
		info, err := repo.SetMediaAssetStatus(ctx, assetID1,
			mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE,
			mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_BANNED)
		require.NoError(t, err)
		assert.Equal(t, int64(0), info.RowsAffected)

		var got models.MediaAsset
		require.NoError(t, db.Where("id = ?", assetID1).First(&got).Error)
		assert.Equal(t, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_DELETED, got.Status, "状态不应被改变")
	})
}

func TestModifyMediaAssetRefCount(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	now := time.Now().UnixMilli()
	seedAsset(t, db, assetID1, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, 10, now)
	seedAsset(t, db, assetID2, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, 20, now)

	t.Run("按对应关系增减引用计数", func(t *testing.T) {
		require.NoError(t, repo.ModifyMediaAssetRefCount(ctx,
			[]int64{assetID1, assetID2}, []int64{-10, 5}))

		var a1, a2 models.MediaAsset
		require.NoError(t, db.Where("id = ?", assetID1).First(&a1).Error)
		require.NoError(t, db.Where("id = ?", assetID2).First(&a2).Error)
		assert.Equal(t, int64(0), a1.RefCount)
		assert.Equal(t, int64(25), a2.RefCount)
	})

	t.Run("中途失败时整体回滚（原子性）", func(t *testing.T) {
		// 临时加约束：ref_count 不允许为负，用于制造第二次更新失败
		require.NoError(t, db.Exec("ALTER TABLE media_assets ADD CONSTRAINT chk_test_ref_count CHECK (ref_count >= 0)").Error)
		t.Cleanup(func() {
			db.Exec("ALTER TABLE media_assets DROP CONSTRAINT IF EXISTS chk_test_ref_count")
		})

		// assetID1 先 +3（成功），assetID2 再 -100（违反约束）
		err := repo.ModifyMediaAssetRefCount(ctx, []int64{assetID1, assetID2}, []int64{3, -100})
		require.Error(t, err, "违反约束的更新应返回错误")

		var a1, a2 models.MediaAsset
		require.NoError(t, db.Where("id = ?", assetID1).First(&a1).Error)
		require.NoError(t, db.Where("id = ?", assetID2).First(&a2).Error)
		assert.Equal(t, int64(0), a1.RefCount, "第一条更新应随事务回滚")
		assert.Equal(t, int64(25), a2.RefCount, "第二条更新应失败且不改动")
	})
}

func TestGetUnusedMedia(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	old := oldTS()
	fresh := time.Now().UnixMilli()

	seedAsset(t, db, assetID1, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_FAILED, 0, old)  // 命中：FAILED + 无引用 + 超过15分钟
	seedAsset(t, db, assetID2, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, 0, old)  // 命中：ACTIVE + 无引用 + 超过15分钟
	seedAsset(t, db, assetID3, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, 5, old)  // 排除：仍有引用
	seedAsset(t, db, 1004, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, 0, fresh)    // 排除：未超过15分钟
	seedAsset(t, db, 1005, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_PROCESSING, 0, old)  // 排除：状态不符
	seedAsset(t, db, 1006, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_FAILED, 0, old-5000) // 命中
	seedAsset(t, db, 1007, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_UNSPECIFIED, 0, old) // 排除：状态不符

	t.Run("只返回 FAILED/ACTIVE 且无引用且超时的记录", func(t *testing.T) {
		assets, err := repo.GetUnusedMedia(ctx, 100)
		require.NoError(t, err)
		ids := make([]int64, 0, len(assets))
		for _, a := range assets {
			ids = append(ids, a.ID)
		}
		assert.ElementsMatch(t, []int64{assetID1, assetID2, 1006}, ids)
	})

	t.Run("limit 生效", func(t *testing.T) {
		assets, err := repo.GetUnusedMedia(ctx, 1)
		require.NoError(t, err)
		assert.Len(t, assets, 1)
	})
}

func TestSetMediaAssetBanned(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	seedAsset(t, db, assetID1, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, 3, time.Now().UnixMilli())

	info, err := repo.SetMediaAssetBanned(ctx, assetID1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), info.RowsAffected)

	var got models.MediaAsset
	require.NoError(t, db.Where("id = ?", assetID1).First(&got).Error)
	assert.Equal(t, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_BANNED, got.Status)
	assert.Equal(t, int64(3), got.RefCount, "封禁不应改动引用计数")
}

// ---------- 事务 ----------

func TestWithTX(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	t.Run("提交后数据可见", func(t *testing.T) {
		err := db.Transaction(func(tx *gorm.DB) error {
			return repo.WithTX(tx).CreateMediaAsset(ctx, &models.MediaAsset{
				BaseModel:  common.BaseModel{ID: assetID1},
				Status:     mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE,
				UploaderID: 1,
			})
		})
		require.NoError(t, err)

		var count int64
		require.NoError(t, db.Model(&models.MediaAsset{}).Where("id = ?", assetID1).Count(&count).Error)
		assert.Equal(t, int64(1), count)
	})

	t.Run("回滚后数据不可见", func(t *testing.T) {
		sentinel := errors.New("force rollback")
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := repo.WithTX(tx).CreateMediaAsset(ctx, &models.MediaAsset{
				BaseModel:  common.BaseModel{ID: assetID2},
				Status:     mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE,
				UploaderID: 1,
			}); err != nil {
				return err
			}
			return sentinel
		})
		require.ErrorIs(t, err, sentinel)

		var count int64
		require.NoError(t, db.Model(&models.MediaAsset{}).Where("id = ?", assetID2).Count(&count).Error)
		assert.Equal(t, int64(0), count, "回滚后不应存在记录")
	})
}

// ---------- MediaFile ----------

func TestCreateMediaFile(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	file := &models.MediaFile{
		AssetID:       assetID1,
		Type:          mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN,
		ObjKey:        "media/origin.jpg",
		Bucket:        "rednote",
		Sha256Sum:     "deadbeef",
		SizeBytes:     2048,
		Format:        mediav1.MediaFormat_MEDIA_FORMAT_JPG,
		Height:        1080,
		Width:         1920,
		MediaMetaData: datatypes.JSONMap{"source": "test"},
	}
	require.NoError(t, repo.CreateMediaFile(ctx, file))
	assert.NotZero(t, file.ID)

	var got models.MediaFile
	require.NoError(t, db.Where("id = ?", file.ID).First(&got).Error)
	assert.Equal(t, assetID1, got.AssetID)
	assert.Equal(t, "media/origin.jpg", got.ObjKey)
	assert.Equal(t, "test", got.MediaMetaData["source"], "jsonb 元数据应完整往返")
}

func TestGetMediaFiles(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	// 入参 id 为资产ID(asset_id)，types 为文件类型
	fOrigin := seedFile(t, db, 2001, assetID1, mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN)
	f540 := seedFile(t, db, 2003, assetID1, mediav1.MediaFileType_MEDIA_FILE_TYPE_540)
	f1080 := seedFile(t, db, 2004, assetID1, mediav1.MediaFileType_MEDIA_FILE_TYPE_1080)
	fOtherOrigin := seedFile(t, db, 2005, assetID2, mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN)

	t.Run("(asset_id, type) 成对匹配", func(t *testing.T) {
		files, err := repo.GetMediaFiles(ctx,
			[]int64{assetID1, assetID1},
			[]mediav1.MediaFileType{mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN, mediav1.MediaFileType_MEDIA_FILE_TYPE_540})
		require.NoError(t, err)
		assert.ElementsMatch(t, []int64{fOrigin.ID, f540.ID}, idsOf(files))
	})

	t.Run("同一资产的不同类型按入参一一对应", func(t *testing.T) {
		files, err := repo.GetMediaFiles(ctx,
			[]int64{assetID1},
			[]mediav1.MediaFileType{mediav1.MediaFileType_MEDIA_FILE_TYPE_1080})
		require.NoError(t, err)
		assert.ElementsMatch(t, []int64{f1080.ID}, idsOf(files))
	})

	t.Run("跨资产查询按对匹配", func(t *testing.T) {
		files, err := repo.GetMediaFiles(ctx,
			[]int64{assetID1, assetID2},
			[]mediav1.MediaFileType{mediav1.MediaFileType_MEDIA_FILE_TYPE_1080, mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN})
		require.NoError(t, err)
		assert.ElementsMatch(t, []int64{f1080.ID, fOtherOrigin.ID}, idsOf(files))
	})

	t.Run("asset_id 与 type 错配时不返回任何记录", func(t *testing.T) {
		// assetID1 下没有 216 变体：若退化为 asset_id OR type，会错误返回 assetID1 的全部文件
		files, err := repo.GetMediaFiles(ctx,
			[]int64{assetID1},
			[]mediav1.MediaFileType{mediav1.MediaFileType_MEDIA_FILE_TYPE_216})
		require.NoError(t, err)
		assert.Empty(t, files, "asset_id 与 type 必须同时匹配")
	})

	t.Run("其他资产的同名类型不应被错误返回", func(t *testing.T) {
		files, err := repo.GetMediaFiles(ctx,
			[]int64{assetID1},
			[]mediav1.MediaFileType{mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN})
		require.NoError(t, err)
		require.Len(t, files, 1)
		assert.Equal(t, assetID1, files[0].AssetID)
		assert.NotEqual(t, fOtherOrigin.ID, files[0].ID)
	})

	t.Run("ids/types 长度不一致返回错误", func(t *testing.T) {
		_, err := repo.GetMediaFiles(ctx, []int64{assetID1}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mismatch")
	})

	t.Run("空入参安全返回空集", func(t *testing.T) {
		require.NotPanics(t, func() {
			files, err := repo.GetMediaFiles(ctx, []int64{}, []mediav1.MediaFileType{})
			require.NoError(t, err)
			assert.Empty(t, files)
		})
	})
}

func idsOf(files []*models.MediaFile) []int64 {
	ids := make([]int64, 0, len(files))
	for _, f := range files {
		ids = append(ids, f.ID)
	}
	return ids
}

func TestDeleteMediaFiles(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	seedFile(t, db, 2001, assetID1, mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN)
	seedFile(t, db, 2002, assetID1, mediav1.MediaFileType_MEDIA_FILE_TYPE_216)
	seedFile(t, db, 2003, assetID2, mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN)

	info, err := repo.DeleteMediaFiles(ctx, assetID1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), info.RowsAffected)

	var files []models.MediaFile
	require.NoError(t, db.Where("asset_id = ?", assetID2).Find(&files).Error)
	require.Len(t, files, 1)
	assert.Equal(t, assetID2, files[0].AssetID, "只应删除指定资产下的文件")

	var count int64
	require.NoError(t, db.Model(&models.MediaFile{}).Count(&count).Error)
	assert.Equal(t, int64(1), count, "总数应只剩 1 条")
}
