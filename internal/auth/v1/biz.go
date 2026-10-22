package v1

import (
	"context"
	"errors"
	"fmt"
	authv1 "rednote/gen/go/auth/v1"
	auth "rednote/internal/auth/models"
	"rednote/packages/defaults"
	"rednote/packages/errmsg"
	"rednote/packages/jwt"
	"rednote/packages/utils"

	"github.com/bwmarrin/snowflake"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type AuthUsecase interface {
	Register(context.Context, *authv1.RegisterRequest) (*authv1.RegisterResponse, error)
	Login(context.Context, *authv1.LoginRequest) (*authv1.LoginResponse, error)
	GetRefreshToken(context.Context, *authv1.GetRefreshTokenRequest) (*authv1.GetRefreshTokenResponse, error)
	GetAccessToken(context.Context, *authv1.GetAccessTokenRequest) (*authv1.GetAccessTokenResponse, error)
	Logout(context.Context, *authv1.LogoutRequest) (*authv1.LogoutResponse, error)
}

type AuthUsecaseImpl struct {
	Repo    AuthRepository
	Manager jwt.Manager
	DB      *gorm.DB
}

func NewAuthUsecaseImpl(repo AuthRepository, manager jwt.Manager, db *gorm.DB) *AuthUsecaseImpl {
	return &AuthUsecaseImpl{
		Repo:    repo,
		Manager: manager,
		DB:      db,
	}
}

func (u *AuthUsecaseImpl) Register(ctx context.Context, req *authv1.RegisterRequest) (*authv1.RegisterResponse, error) {

	if !utils.ValidateEmail(req.Credential.GetEmail()) {
		return &authv1.RegisterResponse{Fail: true, Err: errmsg.InvalidEmail}, nil
	}
	if !utils.ValidatePassword(req.Credential.GetPassword()) {
		return &authv1.RegisterResponse{Fail: true, Err: errmsg.InvalidPassword}, nil
	}
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Credential.GetPassword()), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	if req.GetRole() == authv1.Role_ROLE_UNSPECIFIED {
		return &authv1.RegisterResponse{Fail: true, Err: errmsg.InvalidArgument}, nil
	}

	credential := &auth.UserCredential{
		Email:        req.Credential.GetEmail(),
		PasswordHash: string(hashedPassword),
		Role:         req.GetRole(),
	}

	err = u.Repo.CreateCredential(ctx, credential)
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return &authv1.RegisterResponse{Fail: true, Err: errmsg.DuplicateEmail}, nil
	}

	if err != nil {
		return nil, fmt.Errorf("failed to register: %w", err)
	}

	return &authv1.RegisterResponse{
		AssignedId: snowflake.ParseInt64(credential.ID).String(),
	}, nil
}

func (u *AuthUsecaseImpl) Login(ctx context.Context, req *authv1.LoginRequest) (*authv1.LoginResponse, error) {
	if !utils.ValidateEmail(req.Credential.GetEmail()) {
		return &authv1.LoginResponse{Fail: true, Err: errmsg.IncorrectCredential}, nil
	}
	if !utils.ValidatePassword(req.Credential.GetPassword()) {
		return &authv1.LoginResponse{Fail: true, Err: errmsg.IncorrectCredential}, nil
	}

	credential, err := u.Repo.GetCredentialByEmail(ctx, req.Credential.GetEmail())
	if err != nil {
		return nil, fmt.Errorf("failed to get credential: %w", err)
	}
	if credential == nil {
		return &authv1.LoginResponse{Fail: true, Err: errmsg.IncorrectCredential}, nil
	}

	// 参数顺序为 (hash, 明文密码)
	err = bcrypt.CompareHashAndPassword(
		[]byte(credential.PasswordHash),
		[]byte(req.Credential.GetPassword()),
	)
	if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		return &authv1.LoginResponse{Fail: true, Err: errmsg.IncorrectCredential}, nil
	} else if err != nil {
		return nil, fmt.Errorf("failed to compare password: %w", err)
	}

	refreshClaim := jwt.NewClaim(
		defaults.JwtRefreshToken,
		credential.Role,
		credential.ID,
	)
	accessClaim := jwt.NewClaim(
		defaults.JwtAccessToken,
		credential.Role,
		credential.ID,
	)
	refreshToken, err := u.Manager.Generate(refreshClaim)
	if err != nil {
		return nil, fmt.Errorf("failed to generate refresh token: %w", err)
	}
	accessToken, err := u.Manager.Generate(accessClaim)
	if err != nil {
		return nil, fmt.Errorf("failed to generate access token: %w", err)
	}

	switch req.Platform {
	case authv1.Platform_PLATFORM_MOBILE:
		_, err := u.Repo.SetRefreshTokenIDMobile(ctx, credential.ID, refreshClaim.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to update tokenID for mobile platform: %w", err)
		}
	case authv1.Platform_PLATFORM_WEB:
		_, err := u.Repo.SetRefreshTokenIDWeb(ctx, credential.ID, refreshClaim.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to update tokenID for web platform: %w", err)
		}
	default:
		return &authv1.LoginResponse{Fail: true, Err: errmsg.InvalidArgument}, nil
	}

	return &authv1.LoginResponse{
		AccessToken:  &authv1.AccessToken{Value: accessToken},
		RefreshToken: &authv1.RefreshToken{Value: refreshToken},
	}, nil
}

// GetRefreshToken 换发 refresh token：会话凭据由请求体直传（refresh token 串），
// auth 自行验签后取 uid/jti 做轮换校验，不再依赖 ctx 透传的 claims
func (u *AuthUsecaseImpl) GetRefreshToken(ctx context.Context, req *authv1.GetRefreshTokenRequest) (*authv1.GetRefreshTokenResponse, error) {
	claim, err := u.Manager.Parse(req.GetRefreshToken())
	if err != nil {
		// 验签失败（伪造/过期/空串）一律按未认证处理，不泄露具体原因
		return &authv1.GetRefreshTokenResponse{Fail: true, Err: errmsg.Unauthorized}, nil
	}

	if claim.Type != defaults.JwtRefreshToken {
		return &authv1.GetRefreshTokenResponse{Fail: true, Err: errmsg.InvalidArgument}, nil
	}

	uid, err := snowflake.ParseString(claim.UID)
	if err != nil {
		return &authv1.GetRefreshTokenResponse{Fail: true, Err: errmsg.InvalidArgument}, nil
	}

	credential, err := u.Repo.GetCredentialByUID(ctx, uid.Int64())
	if err != nil {
		return nil, fmt.Errorf("failed to get credential: %w", err)
	}

	if credential == nil {
		return &authv1.GetRefreshTokenResponse{Fail: true, Err: errmsg.Unauthorized}, nil
	}

	newClaim := jwt.NewClaim(
		defaults.JwtRefreshToken,
		credential.Role,
		credential.ID,
	)
	token, err := u.Manager.Generate(newClaim)
	if err != nil {
		return nil, fmt.Errorf("failed to generate refresh token: %w", err)
	}

	// 对比各个平台有效TokenID
	platform := matchPlatform(credential, claim)
	switch platform {
	case authv1.Platform_PLATFORM_MOBILE:
		_, err := u.Repo.SetRefreshTokenIDMobile(ctx, credential.ID, newClaim.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to update refresh token ID for mobile platform: %w", err)
		}
	case authv1.Platform_PLATFORM_WEB:
		_, err := u.Repo.SetRefreshTokenIDWeb(ctx, credential.ID, newClaim.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to update refresh token ID for web platform: %w", err)
		}
	default:
		// 未命中 失效的TokenID
		return &authv1.GetRefreshTokenResponse{Fail: true, Err: errmsg.Unauthorized}, nil
	}

	return &authv1.GetRefreshTokenResponse{
		Token: &authv1.RefreshToken{Value: token},
	}, nil
}

// GetAccessToken 换发 access token：会话凭据由请求体直传（refresh token 串），
// auth 验签后比对 jti 确认登录态仍有效（不轮换 refresh 登录态）
func (u *AuthUsecaseImpl) GetAccessToken(ctx context.Context, req *authv1.GetAccessTokenRequest) (*authv1.GetAccessTokenResponse, error) {
	claim, err := u.Manager.Parse(req.GetRefreshToken())
	if err != nil {
		// 验签失败（伪造/过期/空串）一律按未认证处理，不泄露具体原因
		return &authv1.GetAccessTokenResponse{Fail: true, Err: errmsg.Unauthorized}, nil
	}

	if claim.Type != defaults.JwtRefreshToken {
		return &authv1.GetAccessTokenResponse{Fail: true, Err: errmsg.InvalidArgument}, nil
	}
	uid, err := snowflake.ParseString(claim.UID)
	if err != nil {
		return &authv1.GetAccessTokenResponse{Fail: true, Err: errmsg.InvalidArgument}, nil
	}

	credential, err := u.Repo.GetCredentialByUID(ctx, uid.Int64())
	if err != nil {
		return nil, fmt.Errorf("failed to get credential: %w", err)
	}
	if credential == nil {
		return &authv1.GetAccessTokenResponse{Fail: true, Err: errmsg.Unauthorized}, nil
	}

	newClaim := jwt.NewClaim(
		defaults.JwtAccessToken,
		credential.Role,
		uid.Int64(),
	)
	token, err := u.Manager.Generate(newClaim)
	if err != nil {
		return nil, fmt.Errorf("failed to generate access token: %w", err)
	}

	platform := matchPlatform(credential, claim)
	switch platform {
	case authv1.Platform_PLATFORM_MOBILE, authv1.Platform_PLATFORM_WEB:
		return &authv1.GetAccessTokenResponse{Token: &authv1.AccessToken{Value: token}}, nil
	default:
		return &authv1.GetAccessTokenResponse{Fail: true, Err: errmsg.Unauthorized}, nil
	}
}

func matchPlatform(credential *auth.UserCredential, claim *jwt.Claims) authv1.Platform {
	if credential.RefreshTokenIDMobile == claim.ID {
		return authv1.Platform_PLATFORM_MOBILE
	}
	if credential.RefreshTokenIDWeb == claim.ID {
		return authv1.Platform_PLATFORM_WEB
	}
	return authv1.Platform_PLATFORM_UNSPECIFIED
}

func (u *AuthUsecaseImpl) Logout(ctx context.Context, req *authv1.LogoutRequest) (*authv1.LogoutResponse, error) {
	// 身份由内部 RPC 请求体直传（BFF 从会话解析 uid 后注入，内网互信），
	// 不接收面客自报的 UID——越权防护由 BFF 会话鉴权保证
	uid, err := snowflake.ParseString(req.GetUid())
	if err != nil {
		return &authv1.LogoutResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}
	placeholder := utils.GenID().String()

	// 通过 WithTX 把两个平台的失效更新绑定在同一事务内，保证原子性
	err = u.DB.Transaction(func(tx *gorm.DB) error {
		repo := u.Repo.WithTX(tx)
		if _, err := repo.SetRefreshTokenIDMobile(ctx, uid.Int64(), placeholder); err != nil {
			return fmt.Errorf("failed to invalidate mobile refresh token ID: %w", err)
		}

		if _, err := repo.SetRefreshTokenIDWeb(ctx, uid.Int64(), placeholder); err != nil {
			return fmt.Errorf("failed to invalidate web refresh token ID: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to logout: %w", err)
	}

	return &authv1.LogoutResponse{}, nil
}
