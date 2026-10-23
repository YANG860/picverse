package v1

import (
	"context"
	"log/slog"

	mediav1 "rednote/gen/go/media/v1"
	"rednote/gen/go/media/v1/mediav1connect"
	"rednote/packages/logger"

	"connectrpc.com/connect"
	"gorm.io/gorm"
)

// MediaServiceHandler 实现 mediav1connect.MediaServiceHandler 接口
type MediaServiceHandler struct {
	mediav1connect.UnimplementedMediaServiceHandler
	DB   *gorm.DB
	Biz  MediaUsecase
	Repo MediaRepository
	log  *slog.Logger
}

var _ mediav1connect.MediaServiceHandler = &MediaServiceHandler{}

// NewMediaServiceHandler 创建 MediaServiceHandler
func NewMediaServiceHandler(db *gorm.DB, lf logger.LoggerFactory, useCase MediaUsecase, repo MediaRepository) *MediaServiceHandler {
	return &MediaServiceHandler{
		DB:   db,
		log:  lf.GetLoggerOf("media"),
		Biz:  useCase,
		Repo: repo,
	}
}

// --- ConnectRPC 方法实现 ---

// UploadMedia 上传媒体文件
func (s *MediaServiceHandler) UploadMedia(ctx context.Context, req *connect.Request[mediav1.UploadMediaRequest]) (*connect.Response[mediav1.UploadMediaResponse], error) {
	resp, err := s.Biz.UploadMedia(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// GetMediaAssets 获取媒体资产列表
func (s *MediaServiceHandler) GetMediaAssets(ctx context.Context, req *connect.Request[mediav1.GetMediaAssetsRequest]) (*connect.Response[mediav1.GetMediaAssetsResponse], error) {
	resp, err := s.Biz.GetMediaAssets(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// GetMediaAsset 获取单个媒体资产元数据
func (s *MediaServiceHandler) GetMediaAsset(ctx context.Context, req *connect.Request[mediav1.GetMediaAssetRequest]) (*connect.Response[mediav1.GetMediaAssetResponse], error) {
	resp, err := s.Biz.GetMediaAsset(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// GetMediaAssetFiles 获取媒体资产文件列表
func (s *MediaServiceHandler) GetMediaAssetFiles(ctx context.Context, req *connect.Request[mediav1.GetMediaAssetFilesRequest]) (*connect.Response[mediav1.GetMediaAssetFilesResponse], error) {
	resp, err := s.Biz.GetMediaAssetFiles(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// GetMediaAssetFile 获取单个媒体资产文件
func (s *MediaServiceHandler) GetMediaAssetFile(ctx context.Context, req *connect.Request[mediav1.GetMediaAssetFileRequest]) (*connect.Response[mediav1.GetMediaAssetFileResponse], error) {
	resp, err := s.Biz.GetMediaAssetFile(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// SignMediaFileURLs 批量签发展示 GET URL
func (s *MediaServiceHandler) SignMediaFileURLs(ctx context.Context, req *connect.Request[mediav1.SignMediaFileURLsRequest]) (*connect.Response[mediav1.SignMediaFileURLsResponse], error) {
	resp, err := s.Biz.SignMediaFileURLs(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// ListMediaAssetsBefore 列出指定时间之前的媒体资产（游标分页）
func (s *MediaServiceHandler) ListMediaAssetsBefore(ctx context.Context, req *connect.Request[mediav1.ListMediaAssetsBeforeRequest]) (*connect.Response[mediav1.ListMediaAssetsBeforeResponse], error) {
	resp, err := s.Biz.ListMediaAssetsBefore(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// BanMedia 封禁媒体
func (s *MediaServiceHandler) BanMedia(ctx context.Context, req *connect.Request[mediav1.BanMediaRequest]) (*connect.Response[mediav1.BanMediaResponse], error) {
	resp, err := s.Biz.BanMedia(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// ModifyMediaAssetRefCount 修改媒体资产引用计数
func (s *MediaServiceHandler) ModifyMediaAssetRefCount(ctx context.Context, req *connect.Request[mediav1.ModifyMediaAssetRefCountRequest]) (*connect.Response[mediav1.ModifyMediaAssetRefCountResponse], error) {
	resp, err := s.Biz.ModifyMediaAssetRefCount(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// DeleteUnusedMedia 删除未使用的媒体资产
func (s *MediaServiceHandler) DeleteUnusedMedia(ctx context.Context, req *connect.Request[mediav1.DeleteUnusedMediaRequest]) (*connect.Response[mediav1.DeleteUnusedMediaResponse], error) {
	resp, err := s.Biz.DeleteUnusedMedia(ctx, req.Msg)
	return connect.NewResponse(resp), err
}
