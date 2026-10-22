package v1

import (
	"context"
	"log/slog"

	authv1 "rednote/gen/go/auth/v1"
	bffv1 "rednote/gen/go/bff/v1"
	commonv1 "rednote/gen/go/common/v1"
	mediav1 "rednote/gen/go/media/v1"
	userv1 "rednote/gen/go/user/v1"
	"rednote/packages/errmsg"
	"rednote/packages/jwt"
	"rednote/packages/logger"

	"connectrpc.com/connect"
	"golang.org/x/sync/errgroup"
)

// BFFUsecase 面客聚合业务接口（与 bff.v1 BFFService 一一对应）。
// 聚合视图在 BFF 组装、媒体资产引用计数由 BFF 维护。
type BFFUsecase interface {
	// ==================== 认证 ====================
	Register(ctx context.Context, req *bffv1.RegisterRequest) (*bffv1.RegisterResponse, error)
	Login(ctx context.Context, req *bffv1.LoginRequest) (*bffv1.LoginResponse, error)

	// ==================== 用户 ====================

	// ==================== 媒体 ====================

	// ==================== 帖子 ====================

	// ==================== 信息流 ====================

}

// BFFUsecaseImpl BFFUsecase 实现
type BFFUsecaseImpl struct {
	Repo *DownstreamRepo
	log  *slog.Logger
}

// NewBFFUsecase 创建 BFF 聚合业务
func NewBFFUsecase(repo *DownstreamRepo, lf logger.LoggerFactory) *BFFUsecaseImpl {
	return &BFFUsecaseImpl{
		Repo: repo,
		log:  lf.GetLoggerOf("bff"),
	}
}

// GetAndRefMediaFile 获取、校验并引用媒体文件（挂载点 +1）：
// 获取——按 (assetID, ftype) 查目标变体文件；检查——资产存在且状态 ACTIVE；
// 引用——ModifyMediaAssetRefCount +1，失败则整体失败，不产生“挂载成功但计数漂移”的静默状态。
// 资产归属校验由调用方按场景自行比对 uploader_id（注册时尚无会话，天然跳过）。
//
// 返回约定：业务可枚举错误（InvalidID/NotFound/状态不可用）走 errCode 且 err 为 nil；
// 连接层/内部错误走 err 且 errCode 为 nil，由调用方决定包装为 InternalError 或上抛。
func (u *BFFUsecaseImpl) GetAndRefMediaFile(ctx context.Context, assetID string, ftype mediav1.MediaFileType) (*mediav1.MediaFile, *commonv1.Error) {
	if assetID == "" {
		return nil, errmsg.InvalidID
	}
	if ftype == mediav1.MediaFileType_MEDIA_FILE_TYPE_UNSPECIFIED {
		return nil, errmsg.InvalidArgument
	}

	// 资产元数据与文件元数据互不依赖，并行获取
	var assetResp *connect.Response[mediav1.GetMediaAssetResponse]
	var fileResp *connect.Response[mediav1.GetMediaAssetFileResponse]
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		var err error
		assetResp, err = u.Repo.Media.GetMediaAsset(gctx, connect.NewRequest(&mediav1.GetMediaAssetRequest{Id: assetID}))
		return err
	})
	g.Go(func() error {
		var err error
		fileResp, err = u.Repo.Media.GetMediaAssetFile(gctx, connect.NewRequest(&mediav1.GetMediaAssetFileRequest{Id: assetID, Type: ftype}))
		return err
	})
	if err := g.Wait(); err != nil {
		u.log.Error("media.GetMediaAsset/GetMediaAssetFile failed", "asset_id", assetID, "type", ftype, "error", err)
		return nil, errmsg.InternalError
	}

	// 检查：资产必须存在且状态可用（存在性错误透传，状态不可用归为 InvalidMedia）
	if assetResp.Msg.GetFail() {
		return nil, assetResp.Msg.GetErr()
	}
	if assetResp.Msg.GetAsset().GetStatus() != mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE {
		return nil, errmsg.InvalidMedia
	}
	if fileResp.Msg.GetFail() {
		return nil, fileResp.Msg.GetErr()
	}

	// 引用：挂载 +1
	refResp, err := u.Repo.Media.ModifyMediaAssetRefCount(ctx, connect.NewRequest(&mediav1.ModifyMediaAssetRefCountRequest{
		Queries: []*mediav1.ModifyMediaAssetRefCountQuery{{Id: assetID, Delta: 1}},
	}))
	if err != nil {
		u.log.Error("media.ModifyMediaAssetRefCount failed", "asset_id", assetID, "delta", 1, "error", err)
		return nil, errmsg.InternalError
	}
	if refResp.Msg.GetFail() {
		return nil, refResp.Msg.GetErr()
	}

	return fileResp.Msg.GetFile(), nil
}

// DeRefMediaFile 解引用媒体资产（卸载点 -1）：供 UpdateDraftMedias 的 old_asset_ids、
// UpdateMyAvatar 的 old_asset_id 等卸载点调用；旧资产 ID 是下游中间产物，不外泄给客户端。
// 返回业务错误码（下游 fail 透传）；连接层错误记日志后归为 InternalError。
func (u *BFFUsecaseImpl) DeRefMediaFile(ctx context.Context, assetID string) *commonv1.Error {
	if assetID == "" {
		return errmsg.InvalidID
	}

	refResp, err := u.Repo.Media.ModifyMediaAssetRefCount(ctx, connect.NewRequest(&mediav1.ModifyMediaAssetRefCountRequest{
		Queries: []*mediav1.ModifyMediaAssetRefCountQuery{{Id: assetID, Delta: -1}},
	}))
	if err != nil {
		u.log.Error("media.ModifyMediaAssetRefCount failed", "asset_id", assetID, "delta", -1, "error", err)
		return errmsg.InternalError
	}
	if refResp.Msg.GetFail() {
		return refResp.Msg.GetErr()
	}
	return nil
}

// ==================== 认证 ====================

// Register 注册账户并自动登录签发双令牌（auth.Register 只返回 assigned_id，不签发令牌）。
// 流程：注册账户 → 检查头像资源（缺省用默认头像）→ 维护媒体引用（+1）→ 创建用户 → 登录账号。
// 注意：auth 注册成功后无回滚接口，后续步骤失败仅返回错误（账户已创建，头像计数可能已 +1）。
func (u *BFFUsecaseImpl) Register(ctx context.Context, req *bffv1.RegisterRequest) (*bffv1.RegisterResponse, error) {
	// 1. 注册账户：角色由 BFF 固定 ROLE_USER，不暴露给客户端
	resp, err := u.Repo.Auth.Register(ctx, connect.NewRequest(&authv1.RegisterRequest{
		Credential: &authv1.Credential{
			Email:    req.GetEmail(),
			Password: req.GetPassword(),
		},
		Role: authv1.Role_ROLE_USER,
	}))

	if err != nil {
		u.log.Error("failed to register user", "error", err)
		return &bffv1.RegisterResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.Fail {
		return &bffv1.RegisterResponse{Fail: resp.Msg.Fail, Err: resp.Msg.Err}, nil
	}

	uid := resp.Msg.GetAssignedId()
	// 2. 检查头像资源：用户提供则校验资产存在且状态可用，缺省兜底默认头像
	avatar_id := req.GetAvatarAssetId()
	avatarFile, errCode := u.GetAndRefMediaFile(ctx, avatar_id, mediav1.MediaFileType_MEDIA_FILE_TYPE_216)
	if errCode != nil {
		return &bffv1.RegisterResponse{Fail: true, Err: errCode}, nil
	}

	// 3. 创建用户
	userResp, err := u.Repo.User.CreateUser(ctx, connect.NewRequest(&userv1.CreateUserRequest{
		Uid:    uid,
		Name:   req.GetName(),
		Avatar: avatarFile,
	}))

	if err != nil {
		u.log.Error("user.CreateUser failed", "uid", uid, "error", err)
		return &bffv1.RegisterResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if userResp.Msg.Fail {
		return &bffv1.RegisterResponse{Fail: true, Err: userResp.Msg.Err}, nil
	}

	return &bffv1.RegisterResponse{}, nil
}

// Login 密码登录并签发令牌（透传 platform）
func (u *BFFUsecaseImpl) Login(ctx context.Context, req *bffv1.LoginRequest) (*bffv1.LoginResponse, error) {

	loginResp, err := u.Repo.Auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{
		Credential: &authv1.Credential{
			Email:    req.GetEmail(),
			Password: req.GetPassword(),
		},
		//TODO: 后续追加通过请求或UA判断平台
		Platform: authv1.Platform_PLATFORM_WEB,
	}))

	if err != nil {
		return &bffv1.LoginResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if loginResp.Msg.Fail {
		return &bffv1.LoginResponse{Fail: loginResp.Msg.Fail, Err: loginResp.Msg.Err}, nil
	}

	return &bffv1.LoginResponse{AccessToken: loginResp.Msg.AccessToken, RefreshToken: loginResp.Msg.RefreshToken}, nil
}

// RefreshTokens 刷新双令牌：会话凭据（refresh token 串）由请求体直传内部 RPC，
// auth 验签 + jti 轮换校验，不再经 ClientInterceptor 透传请求头。
// 顺序固定：先换 access（不轮换 jti），再轮换 refresh——GetRefreshToken 成功即轮换 jti、
// 旧 token 立即失效，反过来会让第二次调用拿旧 token 被拒。
func (u *BFFUsecaseImpl) RefreshTokens(ctx context.Context, req *bffv1.RefreshTokensRequest) (*bffv1.RefreshTokensResponse, error) {
	token := jwt.TokenFromContext(ctx)
	if token == "" {
		return &bffv1.RefreshTokensResponse{Fail: true, Err: errmsg.Unauthorized}, nil
	}

	access, err := u.Repo.Auth.GetAccessToken(ctx, connect.NewRequest(&authv1.GetAccessTokenRequest{RefreshToken: token}))
	if err != nil {
		u.log.Error("auth.GetAccessToken failed", "error", err)
		return nil, err
	}
	if access.Msg.Fail {
		return &bffv1.RefreshTokensResponse{Fail: access.Msg.Fail, Err: access.Msg.Err}, nil
	}

	refresh, err := u.Repo.Auth.GetRefreshToken(ctx, connect.NewRequest(&authv1.GetRefreshTokenRequest{RefreshToken: token}))
	if err != nil {
		u.log.Error("auth.GetRefreshToken failed", "error", err)
		return nil, err
	}
	if refresh.Msg.Fail {
		return &bffv1.RefreshTokensResponse{Fail: refresh.Msg.Fail, Err: refresh.Msg.Err}, nil
	}

	return &bffv1.RefreshTokensResponse{
		Fail:         refresh.Msg.Fail,
		Err:          refresh.Msg.Err,
		AccessToken:  access.Msg.Token,
		RefreshToken: refresh.Msg.Token,
	}, nil
}
