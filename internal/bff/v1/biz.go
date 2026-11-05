package v1

import (
	"context"
	"log/slog"
	bffv1 "rednote/gen/go/bff/v1"
	"rednote/packages/logger"
	"rednote/packages/mq"
)

// BFFUsecase 面客聚合业务接口（与 bff.v1 BFFService 一一对应）。
// 聚合视图在 BFF 组装、媒体资产引用计数由 BFF 维护。
type BFFUsecase interface {
	// ==================== 认证 ====================
	Register(ctx context.Context, req *bffv1.RegisterRequest) (*bffv1.RegisterResponse, error)
	Login(ctx context.Context, req *bffv1.LoginRequest) (*bffv1.LoginResponse, error)
	RefreshTokens(ctx context.Context, req *bffv1.RefreshTokensRequest) (*bffv1.RefreshTokensResponse, error)
	Logout(ctx context.Context, req *bffv1.LogoutRequest) (*bffv1.LogoutResponse, error)
	ChangePassword(ctx context.Context, req *bffv1.ChangePasswordRequest) (*bffv1.ChangePasswordResponse, error)
	// ==================== 用户 ====================
	GetMyProfile(ctx context.Context, req *bffv1.GetMyProfileRequest) (*bffv1.GetMyProfileResponse, error)
	GetUserProfile(ctx context.Context, req *bffv1.GetUserProfileRequest) (*bffv1.GetUserProfileResponse, error)
	UpdateMyProfile(ctx context.Context, req *bffv1.UpdateMyProfileRequest) (*bffv1.UpdateMyProfileResponse, error)
	UpdateMyAvatar(ctx context.Context, req *bffv1.UpdateMyAvatarRequest) (*bffv1.UpdateMyAvatarResponse, error)
	Follow(ctx context.Context, req *bffv1.FollowRequest) (*bffv1.FollowResponse, error)
	Unfollow(ctx context.Context, req *bffv1.UnfollowRequest) (*bffv1.UnfollowResponse, error)
	GetFollowingList(ctx context.Context, req *bffv1.GetFollowingListRequest) (*bffv1.GetFollowingListResponse, error)
	GetFollowerList(ctx context.Context, req *bffv1.GetFollowerListRequest) (*bffv1.GetFollowerListResponse, error)
	GetFollowRelation(ctx context.Context, req *bffv1.GetFollowRelationRequest) (*bffv1.GetFollowRelationResponse, error)

	// ==================== 媒体 ====================
	UploadMedia(ctx context.Context, req *bffv1.UploadMediaRequest) (*bffv1.UploadMediaResponse, error)
	// GetMediaAssetStatus 批量查询媒体资产状态（供前端上传后轮询 UPLOADING→ACTIVE）
	GetMediaAssetStatus(ctx context.Context, req *bffv1.GetMediaAssetStatusRequest) (*bffv1.GetMediaAssetStatusResponse, error)

	// ==================== 帖子 ====================
	// CreateDraft 创建草稿（媒体归属校验 + 引用计数挂载由 BFF 编排）
	CreateDraft(ctx context.Context, req *bffv1.CreateDraftRequest) (*bffv1.CreateDraftResponse, error)
	UpdateDraft(ctx context.Context, req *bffv1.UpdateDraftRequest) (*bffv1.UpdateDraftResponse, error)
	UpdateDraftMedias(ctx context.Context, req *bffv1.UpdateDraftMediasRequest) (*bffv1.UpdateDraftMediasResponse, error)
	PublishDraft(ctx context.Context, req *bffv1.PublishDraftRequest) (*bffv1.PublishDraftResponse, error)
	DeletePost(ctx context.Context, req *bffv1.DeletePostRequest) (*bffv1.DeletePostResponse, error)
	GetPost(ctx context.Context, req *bffv1.GetPostRequest) (*bffv1.GetPostResponse, error)
	GetMyPosts(ctx context.Context, req *bffv1.GetMyPostsRequest) (*bffv1.GetMyPostsResponse, error)
	GetUserPosts(ctx context.Context, req *bffv1.GetUserPostsRequest) (*bffv1.GetUserPostsResponse, error)

	// ==================== 信息流 ====================
	// TODO: GetFollowingFeed（关注流）待实现，设计见 doc/internal/bff/bff.md（限制关注数全量归并 + 时间游标）

}

// BFFUsecaseImpl BFFUsecase 实现
type BFFUsecaseImpl struct {
	Repo *DownstreamRepo
	MQ   mq.MQClient
	log  *slog.Logger
}

// NewBFFUsecase 创建 BFF 聚合业务
func NewBFFUsecase(repo *DownstreamRepo, mqClient mq.MQClient, lf logger.LoggerFactory) *BFFUsecaseImpl {
	return &BFFUsecaseImpl{
		Repo: repo,
		MQ:   mqClient,
		log:  lf.GetLoggerOf("bff"),
	}
}
