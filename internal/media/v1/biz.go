package v1

import (
	"context"
	"fmt"
	"strconv"
	"time"

	commonv1 "rednote/gen/go/common/v1"
	mediav1 "rednote/gen/go/media/v1"
	models "rednote/internal/media/models"
	"rednote/packages/defaults"
	"rednote/packages/errmsg"
	mq "rednote/packages/mq"
	"rednote/packages/oss"
	"rednote/packages/utils"

	"github.com/bwmarrin/snowflake"
	"gorm.io/gen"
	"gorm.io/gorm"
)

// MediaUsecase 媒体业务逻辑接口
type MediaUsecase interface {
	// UploadMedia 上传媒体文件
	UploadMedia(ctx context.Context, req *mediav1.UploadMediaRequest) (*mediav1.UploadMediaResponse, error)
	// GetMediaAssets 获取媒体资产列表
	GetMediaAssets(ctx context.Context, req *mediav1.GetMediaAssetsRequest) (*mediav1.GetMediaAssetsResponse, error)
	// GetMediaAssetFiles 获取媒体资产下的文件列表
	GetMediaAssetFiles(ctx context.Context, req *mediav1.GetMediaAssetFilesRequest) (*mediav1.GetMediaAssetFilesResponse, error)
	// ListMediaAssetsBefore 列出指定时间之前的媒体资产（游标分页）
	ListMediaAssetsBefore(ctx context.Context, req *mediav1.ListMediaAssetsBeforeRequest) (*mediav1.ListMediaAssetsBeforeResponse, error)
	// BanMedia 封禁媒体
	BanMedia(ctx context.Context, req *mediav1.BanMediaRequest) (*mediav1.BanMediaResponse, error)
	// ModifyMediaAssetRefCount 修改媒体资产引用计数
	ModifyMediaAssetRefCount(ctx context.Context, req *mediav1.ModifyMediaAssetRefCountRequest) (*mediav1.ModifyMediaAssetRefCountResponse, error)
	// DeleteUnusedMedia 删除未使用的媒体资产
	DeleteUnusedMedia(ctx context.Context, req *mediav1.DeleteUnusedMediaRequest) (*mediav1.DeleteUnusedMediaResponse, error)
}

// MediaUsecaseImpl 媒体业务逻辑实现
type MediaUsecaseImpl struct {
	Repo MediaRepository
	MQ   *mq.JetStreamClient
	DB   *gorm.DB
	S3   oss.S3Repo
}

var _ MediaUsecase = (*MediaUsecaseImpl)(nil)

// NewMediaUsecase 创建 MediaUsecase 实例
func NewMediaUsecase(repo MediaRepository, mq *mq.JetStreamClient, db *gorm.DB, s3 oss.S3Repo) *MediaUsecaseImpl {
	return &MediaUsecaseImpl{
		Repo: repo,
		MQ:   mq,
		DB:   db,
		S3:   s3,
	}
}

func (u *MediaUsecaseImpl) UploadMedia(ctx context.Context, req *mediav1.UploadMediaRequest) (*mediav1.UploadMediaResponse, error) {

	// 文件类型过滤
	format := req.GetMediaMeta().GetFormat()
	if format != mediav1.MediaFormat_MEDIA_FORMAT_JPG &&
		format != mediav1.MediaFormat_MEDIA_FORMAT_PNG {
		return &mediav1.UploadMediaResponse{Fail: true, Err: errmsg.InvalidFormat}, nil
	}

	uploader, err := snowflake.ParseString(req.UploaderId)
	if err != nil {
		return &mediav1.UploadMediaResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	resp := &mediav1.UploadMediaResponse{}
	err = u.DB.Transaction(func(tx *gorm.DB) error {

		// 创建媒体资产文件
		asset := &models.MediaAsset{
			Status:     mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_UPLOADING,
			UploaderID: uploader.Int64(),
		}
		err = u.Repo.WithTX(tx).CreateMediaAsset(ctx, asset)
		if err != nil {
			return err
		}

		// 获取签名url
		key := utils.BuildKey(asset.ID, mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN, format)
		result, err := u.S3.PresignedPutURL(ctx, key,
			defaults.PutUrlExpiry,
			utils.ContentTypeOf(format),
		)
		if err != nil {
			return err
		}
		resp.Asset = asset.ToProto()
		resp.SignedUrl = result.URL
		resp.ExpireAt = result.ExpiresAt.UnixMilli()

		// 创建延时消息
		uploadMsg := mediav1.MediaMessage{
			Payload: &mediav1.MediaMessage_UploadMedia{
				UploadMedia: &mediav1.UploadMedia{
					AssetId: snowflake.ParseInt64(asset.ID).String(),
					Bucket:  u.S3.Bucket(),
					Key:     key,

					Meta:       req.GetMediaMeta(),
					UploaderId: uploader.String(),
				},
			},
		}

		// 创建发送延时消息
		mqMsg, err := mq.NewMQMessage(defaults.MediaMessageStream, nil, &uploadMsg)
		if err != nil {
			return err
		}
		mqMsg.WithDelay(defaults.UploadMsgDelay)

		err = u.MQ.SendTX(ctx, mqMsg, tx)
		if err != nil {
			return err
		}
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to upload media: %w", err)
	}

	return resp, nil
}

func (u *MediaUsecaseImpl) GetMediaAssets(ctx context.Context, req *mediav1.GetMediaAssetsRequest) (*mediav1.GetMediaAssetsResponse, error) {

	results := make([]*mediav1.MediaAssetResult, 0, len(req.Ids))
	ids := make([]int64, 0, len(req.Ids))
	for _, v := range req.Ids {
		id, err := snowflake.ParseString(v)
		if err != nil {
			results = append(results, &mediav1.MediaAssetResult{
				Fail: true,
				Err:  errmsg.InvalidID,
			})
		} else {
			ids = append(ids, id.Int64())
		}

	}

	assets, err := u.Repo.GetMediaAssets(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("failed to get media assets: %w", err)
	}

	for _, id := range ids {
		found := false
		for _, asset := range assets {
			if asset.ID == id {
				// 正确返回
				results = append(results, &mediav1.MediaAssetResult{
					Asset: asset.ToProto(),
				})
				found = true
				break
			}
		}
		if !found {
			// 无结果
			results = append(results, &mediav1.MediaAssetResult{
				Fail: true,
				Err:  errmsg.NotFound,
			})
		}
	}

	return &mediav1.GetMediaAssetsResponse{
		Results: results,
	}, nil
}

func (u *MediaUsecaseImpl) GetMediaAssetFiles(ctx context.Context, req *mediav1.GetMediaAssetFilesRequest) (*mediav1.GetMediaAssetFilesResponse, error) {
	results := make([]*mediav1.MediaAssetFileResult, 0, len(req.Queries))
	ids := make([]int64, 0, len(req.Queries))
	types := make([]mediav1.MediaFileType, 0, len(req.Queries))

	for _, query := range req.Queries {
		id, err := snowflake.ParseString(query.Id)
		if err != nil {
			results = append(results, &mediav1.MediaAssetFileResult{
				Fail:  true,
				Err:   errmsg.InvalidID,
				Query: query,
			})
		} else {
			ids = append(ids, id.Int64())
			types = append(types, query.Type)
		}
	}

	files, err := u.Repo.GetMediaFiles(ctx, ids, types)
	if err != nil {
		return nil, fmt.Errorf("failed to get media files: %w", err)
	}

	for i := 0; i < len(ids); i++ {
		found := false
		for _, file := range files {
			// 入参 id 为媒体资产ID，故与 file.AssetID 配对比较
			if ids[i] == file.AssetID && types[i] == file.Type {
				results = append(results, &mediav1.MediaAssetFileResult{
					File: file.ToProto(),
					Query: &mediav1.MediaAssetFileQuery{
						Id:   snowflake.ParseInt64(ids[i]).String(),
						Type: types[i],
					},
				})
				found = true
				break
			}
		}
		if !found {
			results = append(results, &mediav1.MediaAssetFileResult{
				Fail: true,
				Err:  errmsg.NotFound,
				Query: &mediav1.MediaAssetFileQuery{
					Id:   snowflake.ParseInt64(ids[i]).String(),
					Type: types[i],
				},
			})
		}
	}

	return &mediav1.GetMediaAssetFilesResponse{
		Files: results,
	}, nil
}

func (u *MediaUsecaseImpl) ListMediaAssetsBefore(ctx context.Context, req *mediav1.ListMediaAssetsBeforeRequest) (*mediav1.ListMediaAssetsBeforeResponse, error) {

	stamp, err := strconv.ParseInt(req.GetCursor().GetStr(), 10, 64)
	if err != nil {
		stamp = time.Now().UnixMilli()
	}

	limit := req.GetCursor().GetLimit()
	if limit <= 0 {
		limit = defaults.CursorLimit
	}

	assets, err := u.Repo.ListMediaAssetsBefore(ctx, stamp, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list media assets: %w", err)
	}

	var results []*mediav1.MediaAsset
	for _, v := range assets {
		results = append(results, v.ToProto())
	}

	if len(results) == 0 {
		return &mediav1.ListMediaAssetsBeforeResponse{Fail: true, Err: errmsg.NotFound}, nil
	}

	stamp = results[len(results)-1].GetBase().GetCreatedAt()
	cursor := &commonv1.Cursor{
		Str:   strconv.FormatInt(stamp, 10),
		Limit: limit,
	}

	return &mediav1.ListMediaAssetsBeforeResponse{
		Assets: results,
		Cursor: cursor,
	}, nil
}

func (u *MediaUsecaseImpl) BanMedia(ctx context.Context, req *mediav1.BanMediaRequest) (*mediav1.BanMediaResponse, error) {
	assetID, err := snowflake.ParseString(req.GetAssetId())
	if err != nil {
		return &mediav1.BanMediaResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	_, err = u.Repo.SetMediaAssetBanned(ctx, assetID.Int64())
	if err != nil {
		return nil, fmt.Errorf("failed to ban media: %w", err)
	}
	return &mediav1.BanMediaResponse{}, nil
}

func (u *MediaUsecaseImpl) ModifyMediaAssetRefCount(ctx context.Context, req *mediav1.ModifyMediaAssetRefCountRequest) (*mediav1.ModifyMediaAssetRefCountResponse, error) {
	ids := make([]int64, 0, len(req.GetQueries()))
	deltas := make([]int64, 0, len(req.GetQueries()))

	for _, query := range req.GetQueries() {
		id, err := snowflake.ParseString(query.GetId())
		if err != nil {
			return &mediav1.ModifyMediaAssetRefCountResponse{Fail: true, Err: errmsg.InvalidID}, nil
		}
		ids = append(ids, id.Int64())
		deltas = append(deltas, query.GetDelta())
	}

	err := u.Repo.ModifyMediaAssetRefCount(ctx, ids, deltas)
	if err != nil {
		return nil, fmt.Errorf("failed to modify media asset ref count: %w", err)
	}
	return &mediav1.ModifyMediaAssetRefCountResponse{}, nil
}

func (u *MediaUsecaseImpl) DeleteUnusedMedia(ctx context.Context, req *mediav1.DeleteUnusedMediaRequest) (*mediav1.DeleteUnusedMediaResponse, error) {
	limit := max(req.GetLimit(), 0)

	var info gen.ResultInfo
	var ids []int64
	err := u.DB.Transaction(func(tx *gorm.DB) error {
		assets, err := u.Repo.GetUnusedMedia(ctx, limit)
		if err != nil {
			return err
		}

		ids = make([]int64, 0, len(assets))
		for _, asset := range assets {
			ids = append(ids, asset.ID)
		}

		info, err = u.Repo.SetMediaAssetsDeleted(ctx, ids)
		if err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to delete unused media: %w", err)
	}

	for _, v := range ids {
		deleteMsg := &mediav1.DeleteMedia{
			AssetId: snowflake.ParseInt64(v).String(),
		}
		mqMsg, _ := mq.NewMQMessage(defaults.MediaMessageStream, nil, &mediav1.MediaMessage{Payload: &mediav1.MediaMessage_DeleteMedia{DeleteMedia: deleteMsg}})
		// 事务已提交后的尽力而为投递，失败不影响删除结果
		_ = u.MQ.Send(ctx, mqMsg)
	}

	return &mediav1.DeleteUnusedMediaResponse{
		RowsAffected: info.RowsAffected,
	}, nil
}
