package v1

import (
	"context"
	"fmt"
	"time"

	mediav1 "rednote/gen/go/media/v1"
	"rednote/gen/query"
	models "rednote/internal/media/models"

	"gorm.io/gen"
	"gorm.io/gorm"
)

// MediaRepository 媒体资产存储库接口
type MediaRepository interface {
	// 使用事务
	WithTX(tx *gorm.DB) MediaRepository

	// CreateMediaAsset 创建媒体资产
	CreateMediaAsset(ctx context.Context, asset *models.MediaAsset) error
	// GetMediaAssets 根据ID批量获取媒体资产
	GetMediaAssets(ctx context.Context, ids []int64) ([]*models.MediaAsset, error)
	// ListMediaAssetsBefore 根据时间获取媒体资产
	ListMediaAssetsBefore(ctx context.Context, t int64, limit int64) ([]*models.MediaAsset, error)
	// DeleteMediaAssets 删除媒体资产
	DeleteMediaAssets(ctx context.Context, ids []int64) (gen.ResultInfo, error)
	// SetMediaAssetsDeleted 标记媒体资产为待删除
	SetMediaAssetsDeleted(ctx context.Context, ids []int64) (gen.ResultInfo, error)
	// SetMediaAssetStatus 更新媒体资产状态
	SetMediaAssetStatus(ctx context.Context, id int64, from, to mediav1.MediaAssetStatus) (gen.ResultInfo, error)
	// ModifyMediaAssetRefCount 更新引用计数
	ModifyMediaAssetRefCount(ctx context.Context, ids []int64, deltas []int64) error

	// GetUnusedMedia 获取未使用的媒体资产
	GetUnusedMedia(ctx context.Context, limit int64) ([]*models.MediaAsset, error)
	// SetMediaAssetBanned 封禁媒体
	SetMediaAssetBanned(ctx context.Context, id int64) (gen.ResultInfo, error)

	// CreateMediaFile 创建媒体文件
	CreateMediaFile(ctx context.Context, file *models.MediaFile) error
	// GetMediaFiles 根据资产ID和文件类型批量获取媒体文件
	GetMediaFiles(ctx context.Context, ids []int64, types []mediav1.MediaFileType) ([]*models.MediaFile, error)
	// DeleteMediaFiles 删除一组资产下的媒体文件
	DeleteMediaFiles(ctx context.Context, id int64) (gen.ResultInfo, error)
}

var _ MediaRepository = (*MediaRepoImpl)(nil)

type MediaRepoImpl struct {
	DB *gorm.DB
	q  *query.Query
}

func NewMediaRepo(db *gorm.DB) *MediaRepoImpl {
	return &MediaRepoImpl{
		DB: db,
		q:  query.Use(db),
	}
}

func (r *MediaRepoImpl) WithTX(tx *gorm.DB) MediaRepository {
	return &MediaRepoImpl{DB: tx, q: query.Use(tx)}
}

func (r *MediaRepoImpl) CreateMediaAsset(ctx context.Context, asset *models.MediaAsset) error {
	return r.q.MediaAsset.WithContext(ctx).Create(asset)
}

// DeleteMediaAssets 删除媒体资产
func (r *MediaRepoImpl) DeleteMediaAssets(ctx context.Context, ids []int64) (gen.ResultInfo, error) {
	info, err := r.q.MediaAsset.WithContext(ctx).Where(r.q.MediaAsset.ID.In(ids...)).Delete()
	return info, err
}

func (r *MediaRepoImpl) SetMediaAssetsDeleted(ctx context.Context, ids []int64) (gen.ResultInfo, error) {
	info, err := r.q.MediaAsset.WithContext(ctx).
		Where(r.q.MediaAsset.ID.In(ids...)).
		Update(r.q.MediaAsset.Status,
			int32(mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_DELETED),
		)
	return info, err
}

func (r *MediaRepoImpl) GetMediaAssets(ctx context.Context, ids []int64) ([]*models.MediaAsset, error) {
	var assets []*models.MediaAsset
	assets, err := r.q.MediaAsset.WithContext(ctx).Where(r.q.MediaAsset.ID.In(ids...)).Find()
	return assets, err
}

func (r *MediaRepoImpl) ListMediaAssetsBefore(ctx context.Context, t int64, limit int64) ([]*models.MediaAsset, error) {
	var assets []*models.MediaAsset
	assets, err := r.q.MediaAsset.WithContext(ctx).Where(r.q.MediaAsset.CreatedAt.Lt(t)).
		Order(r.q.MediaAsset.CreatedAt.Desc()).
		Limit(int(limit)).
		Find()
	return assets, err
}

func (r *MediaRepoImpl) SetMediaAssetStatus(ctx context.Context, id int64, from, to mediav1.MediaAssetStatus) (gen.ResultInfo, error) {
	info, err := r.q.MediaAsset.WithContext(ctx).Where(
		r.q.MediaAsset.ID.Eq(id),
		r.q.MediaAsset.Status.Eq(int32(from)),
	).Update(r.q.MediaAsset.Status, int32(to))

	return info, err
}

// 原子性地更新所有资产的引用计数
func (r *MediaRepoImpl) ModifyMediaAssetRefCount(ctx context.Context, ids []int64, deltas []int64) error {

	err := r.DB.Transaction(func(tx *gorm.DB) error {
		q := query.Use(tx)

		for i := 0; i < len(ids); i++ {
			_, err := q.WithContext(ctx).MediaAsset.
				Where(q.MediaAsset.ID.Eq(ids[i])).
				Update(q.MediaAsset.RefCount, q.MediaAsset.RefCount.Add(deltas[i]))
			if err != nil {
				return err
			}
		}

		return nil
	})

	return err
}

func (r *MediaRepoImpl) GetUnusedMedia(ctx context.Context, limit int64) ([]*models.MediaAsset, error) {
	var assets []*models.MediaAsset
	assets, err := r.q.MediaAsset.WithContext(ctx).Where(
		r.q.MediaAsset.RefCount.Lte(0),
		r.q.MediaAsset.Status.Eq(int32(mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_FAILED)),
		r.q.MediaAsset.CreatedAt.Lt(time.Now().Add(-15*time.Minute).UnixMilli()),
	).Or(
		r.q.MediaAsset.RefCount.Lte(0),
		r.q.MediaAsset.Status.Eq(int32(mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE)),
		r.q.MediaAsset.CreatedAt.Lt(time.Now().Add(-15*time.Minute).UnixMilli()),
	).Limit(int(limit)).Find()
	return assets, err
}

func (r *MediaRepoImpl) CreateMediaFile(ctx context.Context, file *models.MediaFile) error {
	err := r.q.MediaFile.WithContext(ctx).Create(file)
	return err
}

// 要求 ids, types 等长且一一对应；ids 为媒体资产ID(asset_id)，types 为文件类型。
// 一个资产下同一类型最多一个文件，因此返回结果可按 (asset_id, type) 与入参配对。
func (r *MediaRepoImpl) GetMediaFiles(ctx context.Context, ids []int64, types []mediav1.MediaFileType) ([]*models.MediaFile, error) {
	var files []*models.MediaFile

	if len(ids) != len(types) {
		return nil, fmt.Errorf("IDs and types length mismatch")
	}

	if len(ids) == 0 || len(types) == 0 {
		return nil, nil
	}

	q := r.q.MediaFile.WithContext(ctx).Where(
		r.q.MediaFile.AssetID.Eq(ids[0]),
		r.q.MediaFile.Type.Eq(int32(types[0])),
	)

	for i := 1; i < len(ids); i++ {
		q = q.Or(
			r.q.MediaFile.AssetID.Eq(ids[i]),
			r.q.MediaFile.Type.Eq(int32(types[i])),
		)
	}

	files, err := q.Find()
	return files, err
}

func (r *MediaRepoImpl) DeleteMediaFiles(ctx context.Context, assetID int64) (gen.ResultInfo, error) {
	info, err := r.q.MediaFile.WithContext(ctx).Where(
		r.q.MediaFile.AssetID.Eq(assetID),
	).Delete()
	return info, err
}

// SetMediaAssetBanned 封禁媒体；
// WHERE 带 status != DELETED 且 != BANNED 条件（对齐 PostRepoImpl.SetPostBanned）：
// 已封禁/已删除的资产不被修改（RowsAffected 为 0），上层据此跳过重复投递 BanMedia 消息，
// 且已删除状态不会被覆盖成 BANNED（PG 计匹配行，无守卫时值不变也计 1）
func (r *MediaRepoImpl) SetMediaAssetBanned(ctx context.Context, id int64) (gen.ResultInfo, error) {
	info, err := r.q.MediaAsset.WithContext(ctx).Where(
		r.q.MediaAsset.ID.Eq(id),
		r.q.MediaAsset.Status.Neq(int32(mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_DELETED)),
		r.q.MediaAsset.Status.Neq(int32(mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_BANNED)),
	).Update(r.q.MediaAsset.Status, int32(mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_BANNED))
	return info, err
}
