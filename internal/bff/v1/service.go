package v1

import (
	"context"
	"log/slog"

	bffv1 "rednote/gen/go/bff/v1"
	"rednote/gen/go/bff/v1/bffv1connect"
	commonv1 "rednote/gen/go/common/v1"
	"rednote/packages/errmsg"
	"rednote/packages/logger"
	"rednote/packages/ratelimit"

	"connectrpc.com/connect"
)

// BFFServiceHandler 实现 bffv1connect.BFFServiceHandler 接口（面客聚合网关）
type BFFServiceHandler struct {
	Biz     BFFUsecase
	Limiter *ratelimit.Limiter // 入口限流（nil/未配置时全放行）
	log     *slog.Logger
}

var _ bffv1connect.BFFServiceHandler = &BFFServiceHandler{}

// NewBFFServiceHandler 创建 BFFServiceHandler
func NewBFFServiceHandler(lf logger.LoggerFactory, biz BFFUsecase, limiter *ratelimit.Limiter) *BFFServiceHandler {
	return &BFFServiceHandler{
		Biz:     biz,
		Limiter: limiter,
		log:     lf.GetLoggerOf("bff"),
	}
}

// admit 入口限流（server.rate_limit，0 = 关闭）：溢出返回 InternalError 业务码
// （面客不区分内部状态，限流不引入独立面客码），由 handler 薄壳构造 fail 响应——
// 不产生连接层 error（BFF 例外规则）。限流键为客户端 IP
func (s *BFFServiceHandler) admit(req interface{ Peer() connect.Peer }) *commonv1.Error {
	if s.Limiter.Allow(ratelimit.Host(req.Peer().Addr)) {
		return nil
	}
	s.log.Warn("rate limit exceeded，入口限流拒绝", "addr", req.Peer().Addr)
	return errmsg.InternalError
}

// --- ConnectRPC 方法实现 ---

// Register 注册（成功不签发令牌，需另行 Login）
func (s *BFFServiceHandler) Register(ctx context.Context, req *connect.Request[bffv1.RegisterRequest]) (*connect.Response[bffv1.RegisterResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.RegisterResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.Register(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// Login 密码登录并签发令牌
func (s *BFFServiceHandler) Login(ctx context.Context, req *connect.Request[bffv1.LoginRequest]) (*connect.Response[bffv1.LoginResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.LoginResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.Login(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

func (s *BFFServiceHandler) RefreshTokens(ctx context.Context, req *connect.Request[bffv1.RefreshTokensRequest]) (*connect.Response[bffv1.RefreshTokensResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.RefreshTokensResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.RefreshTokens(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

func (s *BFFServiceHandler) Logout(ctx context.Context, req *connect.Request[bffv1.LogoutRequest]) (*connect.Response[bffv1.LogoutResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.LogoutResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.Logout(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// ChangePassword 修改密码（uid 来自会话；成功后全部设备下线，需重新登录）
func (s *BFFServiceHandler) ChangePassword(ctx context.Context, req *connect.Request[bffv1.ChangePasswordRequest]) (*connect.Response[bffv1.ChangePasswordResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.ChangePasswordResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.ChangePassword(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

func (s *BFFServiceHandler) UploadMedia(ctx context.Context, req *connect.Request[bffv1.UploadMediaRequest]) (*connect.Response[bffv1.UploadMediaResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.UploadMediaResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.UploadMedia(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// GetMediaAssetStatus 批量查询媒体资产状态
func (s *BFFServiceHandler) GetMediaAssetStatus(ctx context.Context, req *connect.Request[bffv1.GetMediaAssetStatusRequest]) (*connect.Response[bffv1.GetMediaAssetStatusResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.GetMediaAssetStatusResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.GetMediaAssetStatus(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// --- 帖子 ---

// CreateDraft 创建草稿（媒体归属校验 + 引用计数由 BFF 维护）
func (s *BFFServiceHandler) CreateDraft(ctx context.Context, req *connect.Request[bffv1.CreateDraftRequest]) (*connect.Response[bffv1.CreateDraftResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.CreateDraftResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.CreateDraft(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// UpdateDraft 更新草稿（文本字段部分更新）
func (s *BFFServiceHandler) UpdateDraft(ctx context.Context, req *connect.Request[bffv1.UpdateDraftRequest]) (*connect.Response[bffv1.UpdateDraftResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.UpdateDraftResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.UpdateDraft(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// UpdateDraftMedias 更新草稿媒体（全量替换，旧资产卸载不外泄）
func (s *BFFServiceHandler) UpdateDraftMedias(ctx context.Context, req *connect.Request[bffv1.UpdateDraftMediasRequest]) (*connect.Response[bffv1.UpdateDraftMediasResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.UpdateDraftMediasResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.UpdateDraftMedias(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// PublishDraft 发布草稿（DRAFT → AUDITING）
func (s *BFFServiceHandler) PublishDraft(ctx context.Context, req *connect.Request[bffv1.PublishDraftRequest]) (*connect.Response[bffv1.PublishDraftResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.PublishDraftResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.PublishDraft(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// DeletePost 删除帖子（软删）
func (s *BFFServiceHandler) DeletePost(ctx context.Context, req *connect.Request[bffv1.DeletePostRequest]) (*connect.Response[bffv1.DeletePostResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.DeletePostResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.DeletePost(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// --- 帖子查询 ---

// GetPost 帖子详情（聚合作者与关注关系）
func (s *BFFServiceHandler) GetPost(ctx context.Context, req *connect.Request[bffv1.GetPostRequest]) (*connect.Response[bffv1.GetPostResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.GetPostResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.GetPost(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// GetMyPosts 我的帖子列表（含草稿/审核中，游标分页）
func (s *BFFServiceHandler) GetMyPosts(ctx context.Context, req *connect.Request[bffv1.GetMyPostsRequest]) (*connect.Response[bffv1.GetMyPostsResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.GetMyPostsResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.GetMyPosts(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// GetUserPosts 他人主页的帖子列表（仅已发布，游标分页）
func (s *BFFServiceHandler) GetUserPosts(ctx context.Context, req *connect.Request[bffv1.GetUserPostsRequest]) (*connect.Response[bffv1.GetUserPostsResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.GetUserPostsResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.GetUserPosts(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// SearchPosts 帖子搜索
func (s *BFFServiceHandler) SearchPosts(ctx context.Context, req *connect.Request[bffv1.SearchPostsRequest]) (*connect.Response[bffv1.SearchPostsResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.SearchPostsResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.SearchPosts(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// --- 信息流 ---

// GetFollowingFeed 关注流
func (s *BFFServiceHandler) GetFollowingFeed(ctx context.Context, req *connect.Request[bffv1.GetFollowingFeedRequest]) (*connect.Response[bffv1.GetFollowingFeedResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.GetFollowingFeedResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.GetFollowingFeed(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// GetHotPosts 热榜
func (s *BFFServiceHandler) GetHotPosts(ctx context.Context, req *connect.Request[bffv1.GetHotPostsRequest]) (*connect.Response[bffv1.GetHotPostsResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.GetHotPostsResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.GetHotPosts(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// GetRecommendFeed 推荐流
func (s *BFFServiceHandler) GetRecommendFeed(ctx context.Context, req *connect.Request[bffv1.GetRecommendFeedRequest]) (*connect.Response[bffv1.GetRecommendFeedResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.GetRecommendFeedResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.GetRecommendFeed(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// --- 用户 ---

// GetMyProfile 我的主页资料
func (s *BFFServiceHandler) GetMyProfile(ctx context.Context, req *connect.Request[bffv1.GetMyProfileRequest]) (*connect.Response[bffv1.GetMyProfileResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.GetMyProfileResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.GetMyProfile(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// GetUserProfile 他人主页资料（含关注关系）
func (s *BFFServiceHandler) GetUserProfile(ctx context.Context, req *connect.Request[bffv1.GetUserProfileRequest]) (*connect.Response[bffv1.GetUserProfileResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.GetUserProfileResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.GetUserProfile(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// UpdateMyProfile 更新我的资料（部分更新）
func (s *BFFServiceHandler) UpdateMyProfile(ctx context.Context, req *connect.Request[bffv1.UpdateMyProfileRequest]) (*connect.Response[bffv1.UpdateMyProfileResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.UpdateMyProfileResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.UpdateMyProfile(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// UpdateMyAvatar 更新我的头像（引用计数由 BFF 维护）
func (s *BFFServiceHandler) UpdateMyAvatar(ctx context.Context, req *connect.Request[bffv1.UpdateMyAvatarRequest]) (*connect.Response[bffv1.UpdateMyAvatarResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.UpdateMyAvatarResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.UpdateMyAvatar(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// Follow 关注目标用户
func (s *BFFServiceHandler) Follow(ctx context.Context, req *connect.Request[bffv1.FollowRequest]) (*connect.Response[bffv1.FollowResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.FollowResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.Follow(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// Unfollow 取消关注目标用户
func (s *BFFServiceHandler) Unfollow(ctx context.Context, req *connect.Request[bffv1.UnfollowRequest]) (*connect.Response[bffv1.UnfollowResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.UnfollowResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.Unfollow(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// GetFollowingList 我的关注列表（游标分页）
func (s *BFFServiceHandler) GetFollowingList(ctx context.Context, req *connect.Request[bffv1.GetFollowingListRequest]) (*connect.Response[bffv1.GetFollowingListResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.GetFollowingListResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.GetFollowingList(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// GetFollowerList 我的粉丝列表（游标分页）
func (s *BFFServiceHandler) GetFollowerList(ctx context.Context, req *connect.Request[bffv1.GetFollowerListRequest]) (*connect.Response[bffv1.GetFollowerListResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.GetFollowerListResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.GetFollowerList(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// GetFollowRelation 我与目标用户的关注关系
func (s *BFFServiceHandler) GetFollowRelation(ctx context.Context, req *connect.Request[bffv1.GetFollowRelationRequest]) (*connect.Response[bffv1.GetFollowRelationResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.GetFollowRelationResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.GetFollowRelation(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// ==================== 互动（点赞/收藏） ====================

func (s *BFFServiceHandler) LikePost(ctx context.Context, req *connect.Request[bffv1.LikePostRequest]) (*connect.Response[bffv1.LikePostResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.LikePostResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.LikePost(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

func (s *BFFServiceHandler) UnlikePost(ctx context.Context, req *connect.Request[bffv1.UnlikePostRequest]) (*connect.Response[bffv1.UnlikePostResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.UnlikePostResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.UnlikePost(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

func (s *BFFServiceHandler) FavouritePost(ctx context.Context, req *connect.Request[bffv1.FavouritePostRequest]) (*connect.Response[bffv1.FavouritePostResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.FavouritePostResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.FavouritePost(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

func (s *BFFServiceHandler) UnfavouritePost(ctx context.Context, req *connect.Request[bffv1.UnfavouritePostRequest]) (*connect.Response[bffv1.UnfavouritePostResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.UnfavouritePostResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.UnfavouritePost(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

func (s *BFFServiceHandler) GetMyLikedPostIDs(ctx context.Context, req *connect.Request[bffv1.GetMyLikedPostIDsRequest]) (*connect.Response[bffv1.GetMyLikedPostIDsResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.GetMyLikedPostIDsResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.GetMyLikedPostIDs(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

func (s *BFFServiceHandler) GetMyFavouritePostIDs(ctx context.Context, req *connect.Request[bffv1.GetMyFavouritePostIDsRequest]) (*connect.Response[bffv1.GetMyFavouritePostIDsResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.GetMyFavouritePostIDsResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.GetMyFavouritePostIDs(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// ==================== 评论 ====================

func (s *BFFServiceHandler) AddComment(ctx context.Context, req *connect.Request[bffv1.AddCommentRequest]) (*connect.Response[bffv1.AddCommentResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.AddCommentResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.AddComment(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

func (s *BFFServiceHandler) ReplyComment(ctx context.Context, req *connect.Request[bffv1.ReplyCommentRequest]) (*connect.Response[bffv1.ReplyCommentResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.ReplyCommentResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.ReplyComment(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

func (s *BFFServiceHandler) DeleteComment(ctx context.Context, req *connect.Request[bffv1.DeleteCommentRequest]) (*connect.Response[bffv1.DeleteCommentResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.DeleteCommentResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.DeleteComment(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

func (s *BFFServiceHandler) GetComments(ctx context.Context, req *connect.Request[bffv1.GetCommentsRequest]) (*connect.Response[bffv1.GetCommentsResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.GetCommentsResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.GetComments(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

func (s *BFFServiceHandler) GetReplies(ctx context.Context, req *connect.Request[bffv1.GetRepliesRequest]) (*connect.Response[bffv1.GetRepliesResponse], error) {
	if errCode := s.admit(req); errCode != nil {
		return connect.NewResponse(&bffv1.GetRepliesResponse{Fail: true, Err: errCode}), nil
	}
	resp, err := s.Biz.GetReplies(ctx, req.Msg)
	return connect.NewResponse(resp), err
}
