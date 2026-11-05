package v1

import (
	"context"
	"log/slog"

	bffv1 "rednote/gen/go/bff/v1"
	"rednote/gen/go/bff/v1/bffv1connect"
	"rednote/packages/logger"

	"connectrpc.com/connect"
)

// BFFServiceHandler 实现 bffv1connect.BFFServiceHandler 接口（面客聚合网关）
type BFFServiceHandler struct {
	Biz BFFUsecase
	log *slog.Logger
}

var _ bffv1connect.BFFServiceHandler = &BFFServiceHandler{}

// NewBFFServiceHandler 创建 BFFServiceHandler
func NewBFFServiceHandler(lf logger.LoggerFactory, biz BFFUsecase) *BFFServiceHandler {
	return &BFFServiceHandler{
		Biz: biz,
		log: lf.GetLoggerOf("bff"),
	}
}

// --- ConnectRPC 方法实现 ---

// Register 注册（成功不签发令牌，需另行 Login）
func (s *BFFServiceHandler) Register(ctx context.Context, req *connect.Request[bffv1.RegisterRequest]) (*connect.Response[bffv1.RegisterResponse], error) {
	resp, err := s.Biz.Register(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// Login 密码登录并签发令牌
func (s *BFFServiceHandler) Login(ctx context.Context, req *connect.Request[bffv1.LoginRequest]) (*connect.Response[bffv1.LoginResponse], error) {
	resp, err := s.Biz.Login(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

func (s *BFFServiceHandler) RefreshTokens(ctx context.Context, req *connect.Request[bffv1.RefreshTokensRequest]) (*connect.Response[bffv1.RefreshTokensResponse], error) {
	resp, err := s.Biz.RefreshTokens(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

func (s *BFFServiceHandler) Logout(ctx context.Context, req *connect.Request[bffv1.LogoutRequest]) (*connect.Response[bffv1.LogoutResponse], error) {
	resp, err := s.Biz.Logout(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// ChangePassword 修改密码（uid 来自会话；成功后全部设备下线，需重新登录）
func (s *BFFServiceHandler) ChangePassword(ctx context.Context, req *connect.Request[bffv1.ChangePasswordRequest]) (*connect.Response[bffv1.ChangePasswordResponse], error) {
	resp, err := s.Biz.ChangePassword(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

func (s *BFFServiceHandler) UploadMedia(ctx context.Context, req *connect.Request[bffv1.UploadMediaRequest]) (*connect.Response[bffv1.UploadMediaResponse], error) {
	resp, err := s.Biz.UploadMedia(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// GetMediaAssetStatus 批量查询媒体资产状态
func (s *BFFServiceHandler) GetMediaAssetStatus(ctx context.Context, req *connect.Request[bffv1.GetMediaAssetStatusRequest]) (*connect.Response[bffv1.GetMediaAssetStatusResponse], error) {
	resp, err := s.Biz.GetMediaAssetStatus(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// --- 帖子 ---

// CreateDraft 创建草稿（媒体归属校验 + 引用计数由 BFF 维护）
func (s *BFFServiceHandler) CreateDraft(ctx context.Context, req *connect.Request[bffv1.CreateDraftRequest]) (*connect.Response[bffv1.CreateDraftResponse], error) {
	resp, err := s.Biz.CreateDraft(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// UpdateDraft 更新草稿（文本字段部分更新）
func (s *BFFServiceHandler) UpdateDraft(ctx context.Context, req *connect.Request[bffv1.UpdateDraftRequest]) (*connect.Response[bffv1.UpdateDraftResponse], error) {
	resp, err := s.Biz.UpdateDraft(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// UpdateDraftMedias 更新草稿媒体（全量替换，旧资产卸载不外泄）
func (s *BFFServiceHandler) UpdateDraftMedias(ctx context.Context, req *connect.Request[bffv1.UpdateDraftMediasRequest]) (*connect.Response[bffv1.UpdateDraftMediasResponse], error) {
	resp, err := s.Biz.UpdateDraftMedias(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// PublishDraft 发布草稿（DRAFT → AUDITING）
func (s *BFFServiceHandler) PublishDraft(ctx context.Context, req *connect.Request[bffv1.PublishDraftRequest]) (*connect.Response[bffv1.PublishDraftResponse], error) {
	resp, err := s.Biz.PublishDraft(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// DeletePost 删除帖子（软删）
func (s *BFFServiceHandler) DeletePost(ctx context.Context, req *connect.Request[bffv1.DeletePostRequest]) (*connect.Response[bffv1.DeletePostResponse], error) {
	resp, err := s.Biz.DeletePost(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// --- 帖子查询 ---

// GetPost 帖子详情（聚合作者与关注关系）
func (s *BFFServiceHandler) GetPost(ctx context.Context, req *connect.Request[bffv1.GetPostRequest]) (*connect.Response[bffv1.GetPostResponse], error) {
	resp, err := s.Biz.GetPost(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// GetMyPosts 我的帖子列表（含草稿/审核中，游标分页）
func (s *BFFServiceHandler) GetMyPosts(ctx context.Context, req *connect.Request[bffv1.GetMyPostsRequest]) (*connect.Response[bffv1.GetMyPostsResponse], error) {
	resp, err := s.Biz.GetMyPosts(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// GetUserPosts 他人主页的帖子列表（仅已发布，游标分页）
func (s *BFFServiceHandler) GetUserPosts(ctx context.Context, req *connect.Request[bffv1.GetUserPostsRequest]) (*connect.Response[bffv1.GetUserPostsResponse], error) {
	resp, err := s.Biz.GetUserPosts(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// --- 用户 ---

// GetMyProfile 我的主页资料
func (s *BFFServiceHandler) GetMyProfile(ctx context.Context, req *connect.Request[bffv1.GetMyProfileRequest]) (*connect.Response[bffv1.GetMyProfileResponse], error) {
	resp, err := s.Biz.GetMyProfile(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// GetUserProfile 他人主页资料（含关注关系）
func (s *BFFServiceHandler) GetUserProfile(ctx context.Context, req *connect.Request[bffv1.GetUserProfileRequest]) (*connect.Response[bffv1.GetUserProfileResponse], error) {
	resp, err := s.Biz.GetUserProfile(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// UpdateMyProfile 更新我的资料（部分更新）
func (s *BFFServiceHandler) UpdateMyProfile(ctx context.Context, req *connect.Request[bffv1.UpdateMyProfileRequest]) (*connect.Response[bffv1.UpdateMyProfileResponse], error) {
	resp, err := s.Biz.UpdateMyProfile(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// UpdateMyAvatar 更新我的头像（引用计数由 BFF 维护）
func (s *BFFServiceHandler) UpdateMyAvatar(ctx context.Context, req *connect.Request[bffv1.UpdateMyAvatarRequest]) (*connect.Response[bffv1.UpdateMyAvatarResponse], error) {
	resp, err := s.Biz.UpdateMyAvatar(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// Follow 关注目标用户
func (s *BFFServiceHandler) Follow(ctx context.Context, req *connect.Request[bffv1.FollowRequest]) (*connect.Response[bffv1.FollowResponse], error) {
	resp, err := s.Biz.Follow(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// Unfollow 取消关注目标用户
func (s *BFFServiceHandler) Unfollow(ctx context.Context, req *connect.Request[bffv1.UnfollowRequest]) (*connect.Response[bffv1.UnfollowResponse], error) {
	resp, err := s.Biz.Unfollow(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// GetFollowingList 我的关注列表（游标分页）
func (s *BFFServiceHandler) GetFollowingList(ctx context.Context, req *connect.Request[bffv1.GetFollowingListRequest]) (*connect.Response[bffv1.GetFollowingListResponse], error) {
	resp, err := s.Biz.GetFollowingList(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// GetFollowerList 我的粉丝列表（游标分页）
func (s *BFFServiceHandler) GetFollowerList(ctx context.Context, req *connect.Request[bffv1.GetFollowerListRequest]) (*connect.Response[bffv1.GetFollowerListResponse], error) {
	resp, err := s.Biz.GetFollowerList(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// GetFollowRelation 我与目标用户的关注关系
func (s *BFFServiceHandler) GetFollowRelation(ctx context.Context, req *connect.Request[bffv1.GetFollowRelationRequest]) (*connect.Response[bffv1.GetFollowRelationResponse], error) {
	resp, err := s.Biz.GetFollowRelation(ctx, req.Msg)
	return connect.NewResponse(resp), err
}
