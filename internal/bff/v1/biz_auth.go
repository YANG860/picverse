package v1

import (
	"context"
	authv1 "rednote/gen/go/auth/v1"
	bffv1 "rednote/gen/go/bff/v1"
	mediav1 "rednote/gen/go/media/v1"
	userv1 "rednote/gen/go/user/v1"
	"rednote/packages/defaults"
	"rednote/packages/errmsg"
	"rednote/packages/jwt"
	"time"

	"connectrpc.com/connect"
)

// ==================== 认证 ====================

// Register 注册账户（auth.Register 只返回 assigned_id，成功不签发令牌，注册后需另行 Login）。
// 能力范围仅“开户”（email/password/name）：注册前无 uid/无会话无法上传媒体，头像由 BFF 挂
// 默认头像资产（defaults.AvatarAssetID，引用 +1）；头像个性化由登录后的 UploadMedia → UpdateMyAvatar 完成。
// 流程：注册账户 → 挂默认头像（引用 +1）→ 创建用户。
// 一致性：auth.Register 成功后任何步骤失败，尽力回滚已产生的副作用——卸载头像计数（发卸载消息）
// + 删除 auth 账户（DeleteAccount），避免孤儿账户与计数漂移；回滚失败仅记 ERROR（best-effort）。
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
	// 2. 挂默认头像：固定系统资产，无归属校验问题（计数 +1 防止被闲置回收）
	avatarFile, errCode := u.GetAndRefMediaFile(ctx, defaults.AvatarAssetID, mediav1.MediaFileType_MEDIA_FILE_TYPE_216)
	if errCode != nil {
		u.rollbackRegister(ctx, uid, "")
		return &bffv1.RegisterResponse{Fail: true, Err: errCode}, nil
	}

	userResp, err := u.Repo.User.CreateUser(ctx, connect.NewRequest(&userv1.CreateUserRequest{
		Uid:    uid,
		Name:   req.GetName(),
		Avatar: avatarFile,
	}))

	if err != nil {
		u.log.Error("user.CreateUser failed", "uid", uid, "error", err)
		u.rollbackRegister(ctx, uid, defaults.AvatarAssetID)
		return &bffv1.RegisterResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if userResp.Msg.Fail {
		u.rollbackRegister(ctx, uid, defaults.AvatarAssetID)
		return &bffv1.RegisterResponse{Fail: true, Err: userResp.Msg.Err}, nil
	}

	return &bffv1.RegisterResponse{}, nil
}

// rollbackRegister 注册回滚（best-effort，逆序撤销已产生的副作用）：卸载头像计数（avatarID 非空时，
// 发卸载消息）→ 删除 auth 账户；任一回滚失败记 ERROR 日志，不改变调用方的主错误返回。
func (u *BFFUsecaseImpl) rollbackRegister(ctx context.Context, uid, avatarID string) {
	if avatarID != "" {
		if rb := u.DeRefMediaFile(ctx, avatarID); rb != nil {
			u.log.Error("rollback avatar ref count failed", "uid", uid, "asset_id", avatarID, "err_code", rb.GetCode())
		}
	}

	resp, err := u.Repo.Auth.DeleteAccount(ctx, connect.NewRequest(&authv1.DeleteAccountRequest{Uid: uid}))
	if err != nil {
		u.log.Error("auth.DeleteAccount failed", "uid", uid, "error", err)
		return
	}
	if resp.Msg.GetFail() {
		u.log.Error("auth.DeleteAccount failed", "uid", uid, "err_code", resp.Msg.GetErr().GetCode())
	}
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

// RefreshTokens 刷新令牌：始终换发 access token，refresh token 仅在临近过期时才轮换
// （判定见 expiringSoon）；响应的 refresh_token 总是回填当前有效值，客户端可无脑覆盖存储。
// 顺序固定：先换 access 再轮换 refresh——GetRefreshToken 成功即轮换 jti、旧 token 立即失效，
// 反过来第二次调用会拿旧 token 被拒。
func (u *BFFUsecaseImpl) RefreshTokens(ctx context.Context, req *bffv1.RefreshTokensRequest) (*bffv1.RefreshTokensResponse, error) {
	// 仅 refresh token 可刷新
	token := jwt.TokenFromContext(ctx)
	claims := jwt.ClaimsFromContext(ctx)
	if token == "" || claims == nil || claims.Type != defaults.JwtRefreshToken {
		return &bffv1.RefreshTokensResponse{Fail: true, Err: errmsg.Unauthorized}, nil
	}

	// 1. 换发 access token
	access, err := u.Repo.Auth.GetAccessToken(ctx, connect.NewRequest(&authv1.GetAccessTokenRequest{RefreshToken: token}))
	if err != nil {
		u.log.Error("auth.GetAccessToken failed", "error", err)
		return &bffv1.RefreshTokensResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if access.Msg.Fail {
		return &bffv1.RefreshTokensResponse{Fail: access.Msg.Fail, Err: access.Msg.Err}, nil
	}

	resp := &bffv1.RefreshTokensResponse{
		AccessToken:  access.Msg.Token,
		RefreshToken: &authv1.RefreshToken{Value: token},
	}

	if !expiringSoon(claims) {
		return resp, nil
	}

	refresh, err := u.Repo.Auth.GetRefreshToken(ctx, connect.NewRequest(&authv1.GetRefreshTokenRequest{RefreshToken: token}))
	if err != nil {
		u.log.Error("auth.GetRefreshToken failed", "error", err)
		return &bffv1.RefreshTokensResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if refresh.Msg.Fail {
		return &bffv1.RefreshTokensResponse{Fail: refresh.Msg.Fail, Err: refresh.Msg.Err}, nil
	}
	resp.RefreshToken = refresh.Msg.Token
	return resp, nil
}

// Logout 登出当前会话用户：失效该账号全部平台的 refresh 登录态。
func (u *BFFUsecaseImpl) Logout(ctx context.Context, req *bffv1.LogoutRequest) (*bffv1.LogoutResponse, error) {
	claims := jwt.ClaimsFromContext(ctx)
	if claims == nil {
		return &bffv1.LogoutResponse{Fail: true, Err: errmsg.Unauthorized}, nil
	}

	resp, err := u.Repo.Auth.Logout(ctx, connect.NewRequest(&authv1.LogoutRequest{Uid: claims.UID}))
	if err != nil {
		u.log.Error("auth.Logout failed", "uid", claims.UID, "error", err)
		return &bffv1.LogoutResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.Fail {
		return &bffv1.LogoutResponse{Fail: resp.Msg.Fail, Err: resp.Msg.Err}, nil
	}

	return &bffv1.LogoutResponse{}, nil
}

// expiringSoon 判断令牌是否临近过期：剩余寿命不足总寿命（exp - iat）的一半；缺 exp/iat 按临近过期保守轮换。
func expiringSoon(claims *jwt.Claims) bool {
	if claims.ExpiresAt == nil || claims.IssuedAt == nil {
		return true
	}
	lifetime := claims.ExpiresAt.Time.Sub(claims.IssuedAt.Time)
	return time.Until(claims.ExpiresAt.Time) < lifetime/2
}
