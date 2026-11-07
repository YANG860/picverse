package v1

import (
	"context"
	"log/slog"

	postv1 "rednote/gen/go/post/v1"
	"rednote/gen/go/post/v1/postv1connect"
	"rednote/packages/logger"

	"connectrpc.com/connect"
	"gorm.io/gorm"
)

// PostServiceHandler 实现 postv1connect.PostServiceHandler 接口
type PostServiceHandler struct {
	DB   *gorm.DB
	Biz  PostUsecase
	Repo PostRepository
	log  *slog.Logger
}

var _ postv1connect.PostServiceHandler = &PostServiceHandler{}

// NewPostServiceHandler 创建 PostServiceHandler
func NewPostServiceHandler(db *gorm.DB, lf logger.LoggerFactory, useCase PostUsecase, repo PostRepository) *PostServiceHandler {
	return &PostServiceHandler{
		DB:   db,
		log:  lf.GetLoggerOf("post"),
		Biz:  useCase,
		Repo: repo,
	}
}

// --- ConnectRPC 方法实现 ---

// CreateDraft 创建草稿
func (s *PostServiceHandler) CreateDraft(ctx context.Context, req *connect.Request[postv1.CreateDraftRequest]) (*connect.Response[postv1.CreateDraftResponse], error) {
	resp, err := s.Biz.CreateDraft(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// UpdateDraft 更新草稿（文本字段部分更新）
func (s *PostServiceHandler) UpdateDraft(ctx context.Context, req *connect.Request[postv1.UpdateDraftRequest]) (*connect.Response[postv1.UpdateDraftResponse], error) {
	resp, err := s.Biz.UpdateDraft(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// UpdateDraftMedias 更新草稿媒体（全量替换，至少 1 张，不支持清空）
func (s *PostServiceHandler) UpdateDraftMedias(ctx context.Context, req *connect.Request[postv1.UpdateDraftMediasRequest]) (*connect.Response[postv1.UpdateDraftMediasResponse], error) {
	resp, err := s.Biz.UpdateDraftMedias(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// PublishDraft 发布草稿
func (s *PostServiceHandler) PublishDraft(ctx context.Context, req *connect.Request[postv1.PublishDraftRequest]) (*connect.Response[postv1.PublishDraftResponse], error) {
	resp, err := s.Biz.PublishDraft(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// GetPostsByIDs 按ID批量获取帖子
func (s *PostServiceHandler) GetPostsByIDs(ctx context.Context, req *connect.Request[postv1.GetPostsByIDsRequest]) (*connect.Response[postv1.GetPostsByIDsResponse], error) {
	resp, err := s.Biz.GetPostsByIDs(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// ViewPostByAuthor 按作者获取帖子列表
func (s *PostServiceHandler) ViewPostByAuthor(ctx context.Context, req *connect.Request[postv1.ViewPostByAuthorRequest]) (*connect.Response[postv1.ViewPostByAuthorResponse], error) {
	resp, err := s.Biz.ViewPostByAuthor(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// BanPost 封禁帖子
func (s *PostServiceHandler) BanPost(ctx context.Context, req *connect.Request[postv1.BanPostRequest]) (*connect.Response[postv1.BanPostResponse], error) {
	resp, err := s.Biz.BanPost(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// ViewPost 查看帖子
func (s *PostServiceHandler) ViewPost(ctx context.Context, req *connect.Request[postv1.ViewPostRequest]) (*connect.Response[postv1.ViewPostResponse], error) {
	resp, err := s.Biz.ViewPost(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

func (s *PostServiceHandler) GetPostStatus(ctx context.Context, req *connect.Request[postv1.GetPostStatusRequest]) (*connect.Response[postv1.GetPostStatusResponse], error) {
	resp, err := s.Biz.GetPostStatus(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

func (s *PostServiceHandler) DeletePost(ctx context.Context, req *connect.Request[postv1.DeletePostRequest]) (*connect.Response[postv1.DeletePostResponse], error) {
	resp, err := s.Biz.DeletePost(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// SyncPostStat 同步帖子统计信息（内部管理，异步链路调用；逐字段部分更新，nil 不修改）
func (s *PostServiceHandler) SyncPostStat(ctx context.Context, req *connect.Request[postv1.SyncPostStatRequest]) (*connect.Response[postv1.SyncPostStatResponse], error) {
	resp, err := s.Biz.SyncPostStat(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// ==================== 互动（点赞/收藏） ====================

func (s *PostServiceHandler) LikePost(ctx context.Context, req *connect.Request[postv1.LikePostRequest]) (*connect.Response[postv1.LikePostResponse], error) {
	resp, err := s.Biz.LikePost(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

func (s *PostServiceHandler) UnlikePost(ctx context.Context, req *connect.Request[postv1.UnlikePostRequest]) (*connect.Response[postv1.UnlikePostResponse], error) {
	resp, err := s.Biz.UnlikePost(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

func (s *PostServiceHandler) FavouritePost(ctx context.Context, req *connect.Request[postv1.FavouritePostRequest]) (*connect.Response[postv1.FavouritePostResponse], error) {
	resp, err := s.Biz.FavouritePost(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

func (s *PostServiceHandler) UnfavouritePost(ctx context.Context, req *connect.Request[postv1.UnfavouritePostRequest]) (*connect.Response[postv1.UnfavouritePostResponse], error) {
	resp, err := s.Biz.UnfavouritePost(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

func (s *PostServiceHandler) ListLikedPostIDs(ctx context.Context, req *connect.Request[postv1.ListLikedPostIDsRequest]) (*connect.Response[postv1.ListLikedPostIDsResponse], error) {
	resp, err := s.Biz.ListLikedPostIDs(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

func (s *PostServiceHandler) ListFavouritePostIDs(ctx context.Context, req *connect.Request[postv1.ListFavouritePostIDsRequest]) (*connect.Response[postv1.ListFavouritePostIDsResponse], error) {
	resp, err := s.Biz.ListFavouritePostIDs(ctx, req.Msg)
	return connect.NewResponse(resp), err
}
