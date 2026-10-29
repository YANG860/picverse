package v1

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	authv1 "rednote/gen/go/auth/v1"
	"rednote/gen/go/auth/v1/authv1connect"
	bffv1 "rednote/gen/go/bff/v1"
	commonv1 "rednote/gen/go/common/v1"
	mediav1 "rednote/gen/go/media/v1"
	"rednote/packages/defaults"
	"rednote/packages/errmsg"
	"rednote/packages/jwt"

	"connectrpc.com/connect"
	jwtlib "github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAuthClient 按预设响应并记录请求（auth 域被测方法）
type fakeAuthClient struct {
	authv1connect.AuthServiceClient

	register    *authv1.RegisterResponse
	registerErr *commonv1.Error
	login       *authv1.LoginResponse
	loginErr    *commonv1.Error
	access      *authv1.GetAccessTokenResponse
	accessErr   *commonv1.Error
	refresh     *authv1.GetRefreshTokenResponse
	refreshErr  *commonv1.Error
	logoutErr   *commonv1.Error

	registerRPCErr error
	loginRPCErr    error
	accessRPCErr   error
	refreshRPCErr  error
	logoutRPCErr   error

	deleteAccountErr    *commonv1.Error
	deleteAccountRPCErr error

	gotRegister      *authv1.RegisterRequest
	gotLogin         *authv1.LoginRequest
	gotAccess        *authv1.GetAccessTokenRequest
	gotRefresh       *authv1.GetRefreshTokenRequest
	gotLogout        *authv1.LogoutRequest
	gotDeleteAccount *authv1.DeleteAccountRequest
}

func (f *fakeAuthClient) Register(_ context.Context, req *connect.Request[authv1.RegisterRequest]) (*connect.Response[authv1.RegisterResponse], error) {
	f.gotRegister = req.Msg
	if f.registerRPCErr != nil {
		return nil, f.registerRPCErr
	}
	return connect.NewResponse(&authv1.RegisterResponse{Fail: f.registerErr != nil, Err: f.registerErr, AssignedId: f.register.GetAssignedId()}), nil
}

func (f *fakeAuthClient) Login(_ context.Context, req *connect.Request[authv1.LoginRequest]) (*connect.Response[authv1.LoginResponse], error) {
	f.gotLogin = req.Msg
	if f.loginRPCErr != nil {
		return nil, f.loginRPCErr
	}
	return connect.NewResponse(&authv1.LoginResponse{
		Fail: f.loginErr != nil, Err: f.loginErr,
		AccessToken: f.login.GetAccessToken(), RefreshToken: f.login.GetRefreshToken(),
	}), nil
}

func (f *fakeAuthClient) GetAccessToken(_ context.Context, req *connect.Request[authv1.GetAccessTokenRequest]) (*connect.Response[authv1.GetAccessTokenResponse], error) {
	f.gotAccess = req.Msg
	if f.accessRPCErr != nil {
		return nil, f.accessRPCErr
	}
	return connect.NewResponse(&authv1.GetAccessTokenResponse{Fail: f.accessErr != nil, Err: f.accessErr, Token: f.access.GetToken()}), nil
}

func (f *fakeAuthClient) GetRefreshToken(_ context.Context, req *connect.Request[authv1.GetRefreshTokenRequest]) (*connect.Response[authv1.GetRefreshTokenResponse], error) {
	f.gotRefresh = req.Msg
	if f.refreshRPCErr != nil {
		return nil, f.refreshRPCErr
	}
	return connect.NewResponse(&authv1.GetRefreshTokenResponse{Fail: f.refreshErr != nil, Err: f.refreshErr, Token: f.refresh.GetToken()}), nil
}

func (f *fakeAuthClient) Logout(_ context.Context, req *connect.Request[authv1.LogoutRequest]) (*connect.Response[authv1.LogoutResponse], error) {
	f.gotLogout = req.Msg
	if f.logoutRPCErr != nil {
		return nil, f.logoutRPCErr
	}
	return connect.NewResponse(&authv1.LogoutResponse{Fail: f.logoutErr != nil, Err: f.logoutErr}), nil
}

func (f *fakeAuthClient) DeleteAccount(_ context.Context, req *connect.Request[authv1.DeleteAccountRequest]) (*connect.Response[authv1.DeleteAccountResponse], error) {
	f.gotDeleteAccount = req.Msg
	if f.deleteAccountRPCErr != nil {
		return nil, f.deleteAccountRPCErr
	}
	return connect.NewResponse(&authv1.DeleteAccountResponse{Fail: f.deleteAccountErr != nil, Err: f.deleteAccountErr}), nil
}

func newBFFForAuthTest(auth *fakeAuthClient, user *fakeUserClient, media *fakeMediaClient) *BFFUsecaseImpl {
	return &BFFUsecaseImpl{
		Repo: &DownstreamRepo{Auth: auth, User: user, Media: media},
		MQ:   &fakeMQClient{},
		log:  slog.Default(),
	}
}

// refreshCtx 构造带 refresh token 会话的 ctx（token 串 + claims）
func refreshCtx(token string, expiring bool) context.Context {
	now := time.Now()
	claims := &jwt.Claims{
		Type: defaults.JwtRefreshToken,
		UID:  "u1",
	}
	if expiring {
		// 剩余 1 分钟 / 总寿命 60 分钟：远不足一半，触发轮换
		claims.IssuedAt = jwtlib.NewNumericDate(now.Add(-59 * time.Minute))
		claims.ExpiresAt = jwtlib.NewNumericDate(now.Add(1 * time.Minute))
	} else {
		// 剩余 59 分钟 / 总寿命 60 分钟：超过一半，不轮换
		claims.IssuedAt = jwtlib.NewNumericDate(now.Add(-1 * time.Minute))
		claims.ExpiresAt = jwtlib.NewNumericDate(now.Add(59 * time.Minute))
	}
	return jwt.WithToken(jwt.WithClaims(context.Background(), claims), token)
}

// TestBFFRegister 注册编排：auth.Register → 挂默认头像 +1 → user.CreateUser（能力仅“开户”，头像个性化走 UploadMedia → UpdateMyAvatar）
func TestBFFRegister(t *testing.T) {
	avatarFile := &mediav1.MediaFile{Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_216, AssetId: defaults.AvatarAssetID}

	t.Run("成功编排：角色固定普通用户 + uid 传递 + 挂默认头像", func(t *testing.T) {
		auth := &fakeAuthClient{register: &authv1.RegisterResponse{AssignedId: "9001"}}
		user := &fakeUserClient{createUserErr: nil}
		media := &fakeMediaClient{
			asset: &mediav1.MediaAsset{UploaderId: "u0", Status: mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE},
			file:  avatarFile,
		}
		u := newBFFForAuthTest(auth, user, media)

		resp, err := u.Register(context.Background(), &bffv1.RegisterRequest{
			Email: "a@b.c", Password: "pw", Name: "alice",
		})
		require.NoError(t, err)
		require.False(t, resp.Fail)
		assert.Nil(t, resp.Err, "成功响应无错误字段")

		require.NotNil(t, auth.gotRegister)
		assert.Equal(t, "a@b.c", auth.gotRegister.GetCredential().GetEmail())
		assert.Equal(t, "pw", auth.gotRegister.GetCredential().GetPassword())
		assert.Equal(t, authv1.Role_ROLE_USER, auth.gotRegister.GetRole(), "角色由 BFF 固定，不暴露给客户端")

		require.NotNil(t, user.gotCreateUser)
		assert.Equal(t, "9001", user.gotCreateUser.GetUid(), "auth 分配的 ID 注入 CreateUser")
		assert.Equal(t, "alice", user.gotCreateUser.GetName())
		assert.Equal(t, avatarFile, user.gotCreateUser.GetAvatar(), "216 变体文件传给 CreateUser")
		assert.Equal(t, []refCall{{id: defaults.AvatarAssetID, delta: 1}}, media.refCalls, "默认头像挂载点 +1")
	})

	t.Run("auth 业务失败透传", func(t *testing.T) {
		auth := &fakeAuthClient{registerErr: errmsg.DuplicateEmail}
		u := newBFFForAuthTest(auth, &fakeUserClient{}, &fakeMediaClient{})

		resp, err := u.Register(context.Background(), &bffv1.RegisterRequest{Email: "a@b.c", Password: "pw"})
		require.NoError(t, err)
		assert.True(t, resp.Fail)
		assert.Equal(t, errmsg.DuplicateEmail.Code, resp.Err.GetCode())
	})

	t.Run("auth 连接层错误归为 InternalError", func(t *testing.T) {
		auth := &fakeAuthClient{registerRPCErr: errors.New("dial fail")}
		u := newBFFForAuthTest(auth, &fakeUserClient{}, &fakeMediaClient{})

		resp, err := u.Register(context.Background(), &bffv1.RegisterRequest{Email: "a@b.c", Password: "pw"})
		require.NoError(t, err)
		assert.True(t, resp.Fail)
		assert.Equal(t, errmsg.InternalError.Code, resp.Err.GetCode())
	})

	t.Run("默认头像资产不可用（未预置）：回滚账户（无计数副作用）", func(t *testing.T) {
		auth := &fakeAuthClient{register: &authv1.RegisterResponse{AssignedId: "9001"}}
		media := &fakeMediaClient{assetErr: errmsg.NotFound}
		u := newBFFForAuthTest(auth, &fakeUserClient{}, media)

		resp, err := u.Register(context.Background(), &bffv1.RegisterRequest{Email: "a@b.c", Password: "pw"})
		require.NoError(t, err)
		assert.True(t, resp.Fail)
		assert.Equal(t, errmsg.NotFound.Code, resp.Err.GetCode())
		require.NotNil(t, auth.gotDeleteAccount, "挂载失败回滚已创建的账户")
		assert.Equal(t, "9001", auth.gotDeleteAccount.GetUid())
		assert.Empty(t, derefSent(t, u), "挂载未成功不得发卸载消息")
	})

	t.Run("CreateUser 失败：回滚计数与账户", func(t *testing.T) {
		auth := &fakeAuthClient{register: &authv1.RegisterResponse{AssignedId: "9001"}}
		user := &fakeUserClient{createUserErr: errmsg.InvalidName}
		media := &fakeMediaClient{
			asset: &mediav1.MediaAsset{Status: mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE},
			file:  avatarFile,
		}
		u := newBFFForAuthTest(auth, user, media)

		resp, err := u.Register(context.Background(), &bffv1.RegisterRequest{Email: "a@b.c", Password: "pw"})
		require.NoError(t, err)
		assert.True(t, resp.Fail)
		assert.Equal(t, errmsg.InvalidName.Code, resp.Err.GetCode())
		assert.Equal(t, []string{defaults.AvatarAssetID}, derefSent(t, u), "回滚默认头像挂载计数")
		require.NotNil(t, auth.gotDeleteAccount)
		assert.Equal(t, "9001", auth.gotDeleteAccount.GetUid(), "回滚已创建的账户")
	})

	t.Run("CreateUser 连接层错误：回滚计数与账户，归为 InternalError", func(t *testing.T) {
		auth := &fakeAuthClient{register: &authv1.RegisterResponse{AssignedId: "9001"}}
		user := &fakeUserClient{createUserRPCErr: errors.New("dial fail")}
		media := &fakeMediaClient{
			asset: &mediav1.MediaAsset{Status: mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE},
			file:  avatarFile,
		}
		u := newBFFForAuthTest(auth, user, media)

		resp, err := u.Register(context.Background(), &bffv1.RegisterRequest{Email: "a@b.c", Password: "pw"})
		require.NoError(t, err)
		assert.Equal(t, errmsg.InternalError.Code, resp.Err.GetCode())
		assert.Equal(t, []string{defaults.AvatarAssetID}, derefSent(t, u))
		require.NotNil(t, auth.gotDeleteAccount)
	})

	t.Run("回滚失败仅记日志，不影响主错误返回", func(t *testing.T) {
		auth := &fakeAuthClient{register: &authv1.RegisterResponse{AssignedId: "9001"}, deleteAccountRPCErr: errors.New("dial fail")}
		user := &fakeUserClient{createUserErr: errmsg.InvalidName}
		media := &fakeMediaClient{
			asset: &mediav1.MediaAsset{Status: mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE},
			file:  avatarFile,
		}
		u := newBFFForAuthTest(auth, user, media)

		resp, err := u.Register(context.Background(), &bffv1.RegisterRequest{Email: "a@b.c", Password: "pw"})
		require.NoError(t, err)
		assert.True(t, resp.Fail)
		assert.Equal(t, errmsg.InvalidName.Code, resp.Err.GetCode(), "主错误不受回滚失败影响")
		assert.Equal(t, []string{defaults.AvatarAssetID}, derefSent(t, u), "计数回滚仍尝试执行")
	})
}

// TestBFFLogin 登录：凭据透传 + 平台固定 WEB + 令牌透传
func TestBFFLogin(t *testing.T) {
	t.Run("成功：凭据与平台注入 + 令牌透传", func(t *testing.T) {
		auth := &fakeAuthClient{login: &authv1.LoginResponse{
			AccessToken: &authv1.AccessToken{Value: "at-1"}, RefreshToken: &authv1.RefreshToken{Value: "rt-1"},
		}}
		u := newBFFForAuthTest(auth, &fakeUserClient{}, &fakeMediaClient{})

		resp, err := u.Login(context.Background(), &bffv1.LoginRequest{Email: "a@b.c", Password: "pw"})
		require.NoError(t, err)
		require.False(t, resp.Fail)
		require.NotNil(t, auth.gotLogin)
		assert.Equal(t, "a@b.c", auth.gotLogin.GetCredential().GetEmail())
		assert.Equal(t, "pw", auth.gotLogin.GetCredential().GetPassword())
		assert.Equal(t, authv1.Platform_PLATFORM_WEB, auth.gotLogin.GetPlatform())
		assert.Equal(t, "at-1", resp.AccessToken.GetValue())
		assert.Equal(t, "rt-1", resp.RefreshToken.GetValue())
	})

	t.Run("业务失败透传", func(t *testing.T) {
		auth := &fakeAuthClient{loginErr: errmsg.IncorrectCredential}
		u := newBFFForAuthTest(auth, &fakeUserClient{}, &fakeMediaClient{})

		resp, err := u.Login(context.Background(), &bffv1.LoginRequest{Email: "a@b.c", Password: "pw"})
		require.NoError(t, err)
		assert.True(t, resp.Fail)
		assert.Equal(t, errmsg.IncorrectCredential.Code, resp.Err.GetCode())
	})

	t.Run("连接层错误归为 InternalError", func(t *testing.T) {
		auth := &fakeAuthClient{loginRPCErr: errors.New("dial fail")}
		u := newBFFForAuthTest(auth, &fakeUserClient{}, &fakeMediaClient{})

		resp, err := u.Login(context.Background(), &bffv1.LoginRequest{Email: "a@b.c", Password: "pw"})
		require.NoError(t, err)
		assert.True(t, resp.Fail)
		assert.Equal(t, errmsg.InternalError.Code, resp.Err.GetCode())
	})
}

// TestBFFRefreshTokens 刷新令牌：按需轮换（剩余寿命 < 1/2 总寿命才轮换 refresh）
func TestBFFRefreshTokens(t *testing.T) {
	t.Run("会话缺失/非 refresh 一律 Unauthorized", func(t *testing.T) {
		u := newBFFForAuthTest(&fakeAuthClient{}, &fakeUserClient{}, &fakeMediaClient{})

		// 无 token 无 claims
		r, err := u.RefreshTokens(context.Background(), &bffv1.RefreshTokensRequest{})
		require.NoError(t, err)
		assert.Equal(t, errmsg.Unauthorized.Code, r.Err.GetCode())

		// 有 claims 无 token
		r, err = u.RefreshTokens(jwt.WithClaims(context.Background(), &jwt.Claims{Type: defaults.JwtRefreshToken}), &bffv1.RefreshTokensRequest{})
		require.NoError(t, err)
		assert.Equal(t, errmsg.Unauthorized.Code, r.Err.GetCode())

		// access token 不可用于刷新
		r, err = u.RefreshTokens(refreshCtxWithType("at-x", defaults.JwtAccessToken), &bffv1.RefreshTokensRequest{})
		require.NoError(t, err)
		assert.Equal(t, errmsg.Unauthorized.Code, r.Err.GetCode())
	})

	t.Run("未临近过期：只换 access，refresh 回填旧值不轮换", func(t *testing.T) {
		auth := &fakeAuthClient{access: &authv1.GetAccessTokenResponse{Token: &authv1.AccessToken{Value: "at-2"}}}
		u := newBFFForAuthTest(auth, &fakeUserClient{}, &fakeMediaClient{})

		resp, err := u.RefreshTokens(refreshCtx("rt-old", false), &bffv1.RefreshTokensRequest{})
		require.NoError(t, err)
		require.False(t, resp.Fail)
		assert.Equal(t, "at-2", resp.AccessToken.GetValue())
		assert.Equal(t, "rt-old", resp.RefreshToken.GetValue(), "未轮换回填旧值，客户端可无脑覆盖存储")
		assert.Equal(t, "rt-old", auth.gotAccess.GetRefreshToken(), "refresh token 串请求体直传")
		assert.Nil(t, auth.gotRefresh, "未临近过期不得轮换 refresh token")
	})

	t.Run("临近过期：先换 access 再轮换 refresh", func(t *testing.T) {
		auth := &fakeAuthClient{
			access:  &authv1.GetAccessTokenResponse{Token: &authv1.AccessToken{Value: "at-2"}},
			refresh: &authv1.GetRefreshTokenResponse{Token: &authv1.RefreshToken{Value: "rt-new"}},
		}
		u := newBFFForAuthTest(auth, &fakeUserClient{}, &fakeMediaClient{})

		resp, err := u.RefreshTokens(refreshCtx("rt-old", true), &bffv1.RefreshTokensRequest{})
		require.NoError(t, err)
		require.False(t, resp.Fail)
		assert.Equal(t, "at-2", resp.AccessToken.GetValue())
		assert.Equal(t, "rt-new", resp.RefreshToken.GetValue(), "轮换后回填新值")
		require.NotNil(t, auth.gotRefresh)
		assert.Equal(t, "rt-old", auth.gotRefresh.GetRefreshToken())
	})

	t.Run("GetAccessToken 失败透传", func(t *testing.T) {
		auth := &fakeAuthClient{accessErr: errmsg.Unauthorized}
		u := newBFFForAuthTest(auth, &fakeUserClient{}, &fakeMediaClient{})

		resp, err := u.RefreshTokens(refreshCtx("rt-old", false), &bffv1.RefreshTokensRequest{})
		require.NoError(t, err)
		assert.True(t, resp.Fail)
		assert.Equal(t, errmsg.Unauthorized.Code, resp.Err.GetCode())
	})

	t.Run("GetRefreshToken 连接层错误归为 InternalError", func(t *testing.T) {
		auth := &fakeAuthClient{
			access:        &authv1.GetAccessTokenResponse{Token: &authv1.AccessToken{Value: "at-2"}},
			refreshRPCErr: errors.New("dial fail"),
		}
		u := newBFFForAuthTest(auth, &fakeUserClient{}, &fakeMediaClient{})

		resp, err := u.RefreshTokens(refreshCtx("rt-old", true), &bffv1.RefreshTokensRequest{})
		require.NoError(t, err)
		assert.True(t, resp.Fail)
		assert.Equal(t, errmsg.InternalError.Code, resp.Err.GetCode())
	})
}

// TestBFFLogout 登出：uid 会话注入，连接层错误统一包装为 InternalError
func TestBFFLogout(t *testing.T) {
	t.Run("无会话返回 Unauthorized", func(t *testing.T) {
		u := newBFFForAuthTest(&fakeAuthClient{}, &fakeUserClient{}, &fakeMediaClient{})
		resp, err := u.Logout(context.Background(), &bffv1.LogoutRequest{})
		require.NoError(t, err)
		assert.True(t, resp.Fail)
		assert.Equal(t, errmsg.Unauthorized.Code, resp.Err.GetCode())
	})

	t.Run("成功：uid 来自会话", func(t *testing.T) {
		auth := &fakeAuthClient{}
		u := newBFFForAuthTest(auth, &fakeUserClient{}, &fakeMediaClient{})

		resp, err := u.Logout(ctxWithUID("u1"), &bffv1.LogoutRequest{})
		require.NoError(t, err)
		require.False(t, resp.Fail)
		require.NotNil(t, auth.gotLogout)
		assert.Equal(t, "u1", auth.gotLogout.GetUid(), "uid 必须来自会话而非请求体")
	})

	t.Run("重复登出幂等透传", func(t *testing.T) {
		auth := &fakeAuthClient{}
		u := newBFFForAuthTest(auth, &fakeUserClient{}, &fakeMediaClient{})

		for range 2 {
			resp, err := u.Logout(ctxWithUID("u1"), &bffv1.LogoutRequest{})
			require.NoError(t, err)
			require.False(t, resp.Fail, "重复登出幂等（下游语义），不报错")
		}
		assert.Equal(t, "u1", auth.gotLogout.GetUid(), "重复调用仍按会话注入")
	})

	t.Run("业务失败透传", func(t *testing.T) {
		auth := &fakeAuthClient{logoutErr: errmsg.NotFound}
		u := newBFFForAuthTest(auth, &fakeUserClient{}, &fakeMediaClient{})

		resp, err := u.Logout(ctxWithUID("u1"), &bffv1.LogoutRequest{})
		require.NoError(t, err)
		assert.True(t, resp.Fail)
		assert.Equal(t, errmsg.NotFound.Code, resp.Err.GetCode())
	})

	t.Run("连接层错误包装为 InternalError", func(t *testing.T) {
		auth := &fakeAuthClient{logoutRPCErr: errors.New("dial fail")}
		u := newBFFForAuthTest(auth, &fakeUserClient{}, &fakeMediaClient{})

		resp, err := u.Logout(ctxWithUID("u1"), &bffv1.LogoutRequest{})
		require.NoError(t, err, "BFF 统一错误语义：不向客户端返回原始连接层错误")
		assert.True(t, resp.Fail)
		assert.Equal(t, errmsg.InternalError.Code, resp.Err.GetCode())
	})
}

// TestExpiringSoon 临近过期判定边界：剩余寿命 < 1/2 总寿命（exp - iat）
func TestExpiringSoon(t *testing.T) {
	now := time.Now()

	t.Run("缺 exp/iat 保守按临近过期处理", func(t *testing.T) {
		assert.True(t, expiringSoon(&jwt.Claims{}))
	})

	t.Run("剩余超过一半不轮换", func(t *testing.T) {
		claims := &jwt.Claims{RegisteredClaims: jwtlib.RegisteredClaims{
			IssuedAt:  jwtlib.NewNumericDate(now.Add(-1 * time.Minute)),
			ExpiresAt: jwtlib.NewNumericDate(now.Add(59 * time.Minute)),
		}}
		assert.False(t, expiringSoon(claims))
	})

	t.Run("剩余不足一半轮换", func(t *testing.T) {
		claims := &jwt.Claims{RegisteredClaims: jwtlib.RegisteredClaims{
			IssuedAt:  jwtlib.NewNumericDate(now.Add(-59 * time.Minute)),
			ExpiresAt: jwtlib.NewNumericDate(now.Add(1 * time.Minute)),
		}}
		assert.True(t, expiringSoon(claims))
	})

	t.Run("已过期按临近过期处理", func(t *testing.T) {
		claims := &jwt.Claims{RegisteredClaims: jwtlib.RegisteredClaims{
			IssuedAt:  jwtlib.NewNumericDate(now.Add(-10 * time.Minute)),
			ExpiresAt: jwtlib.NewNumericDate(now.Add(-1 * time.Minute)),
		}}
		assert.True(t, expiringSoon(claims))
	})
}

// refreshCtxWithType 构造指定 token 类型的会话 ctx（Unauthorized 表驱动用）
func refreshCtxWithType(token, tokenType string) context.Context {
	return jwt.WithToken(jwt.WithClaims(context.Background(), &jwt.Claims{Type: tokenType, UID: "u1"}), token)
}
