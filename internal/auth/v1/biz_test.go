package v1

import (
	"context"
	"strconv"
	"testing"
	"time"

	"rednote/config"
	authv1 "rednote/gen/go/auth/v1"
	auth "rednote/internal/auth/models"
	"rednote/packages/defaults"
	"rednote/packages/errmsg"
	"rednote/packages/jwt"

	jwtlib "github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// biz 测试专用 ID 区间，避免与 repo_test.go 的 userID* 混淆
const (
	bizUID1 int64 = 2101
	bizUID2 int64 = 2102
	bizUID3 int64 = 2103
)

// testPassword 合法密码样本：8~32 位且含大小写字母与数字
const testPassword = "Passw0rd!"

// newUsecase 构造 auth biz 测试环境：真实 repo + testdb 克隆库 + 真实 JWT 管理器。
// seedCredential / newRepo 复用 repo_test.go 中的同包 helper。
func newUsecase(t *testing.T) (*AuthUsecaseImpl, *gorm.DB, jwt.Manager) {
	t.Helper()
	repo, db := newRepo(t)
	mgr := jwt.NewManager(&config.Config{
		JWT: config.JWTConfig{
			Secret:        "auth-biz-test-secret",
			Issuer:        "test",
			AccessExpire:  time.Hour,
			RefreshExpire: time.Hour,
		},
	})
	return NewAuthUsecaseImpl(repo, mgr, db), db, mgr
}

// seedUser 造一条凭据（明文密码经 bcrypt 哈希后落库），返回记录
func seedUser(t *testing.T, db *gorm.DB, id int64, email, password string, role authv1.Role) *auth.UserCredential {
	t.Helper()
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	return seedCredential(t, db, id, email, string(hashed), role)
}

// mustToken 用指定 claims 签发会话凭据串（模拟任意来源的 refresh token）
func mustToken(t *testing.T, mgr jwt.Manager, claim *jwt.Claims) string {
	t.Helper()
	token, err := mgr.Generate(claim)
	require.NoError(t, err)
	return token
}

// jwtClaimID 构造只带 jti 的 RegisteredClaims（模拟任意来源的 token 载荷）
func jwtClaimID(id string) jwtlib.RegisteredClaims {
	return jwtlib.RegisteredClaims{ID: id}
}

// mustLogin 登录并返回 (accessToken, refreshToken, refreshClaims)，
// 同时把 refresh jti 写回凭据，模拟真实的登录态
func mustLogin(t *testing.T, uc *AuthUsecaseImpl, uid int64, platform authv1.Platform) (string, string, *jwt.Claims) {
	t.Helper()
	resp, err := uc.Login(context.Background(), &authv1.LoginRequest{
		Credential: &authv1.Credential{Email: emailOf(uid), Password: testPassword},
		Platform:   platform,
	})
	require.NoError(t, err)
	require.False(t, resp.GetFail(), "登录准备失败: %v", resp.GetErr())

	// 反解 refresh token 载荷，得到本次登录的 jti
	claims, err := uc.Manager.Parse(resp.GetRefreshToken().GetValue())
	require.NoError(t, err)

	return resp.GetAccessToken().GetValue(), resp.GetRefreshToken().GetValue(), claims
}

// emailOf 与 seedUser 使用同一套邮箱规则
func emailOf(uid int64) string { return "user" + strconv.FormatInt(uid, 10) + "@example.com" }

// ---------- Register ----------

func TestAuthUsecase_Register(t *testing.T) {
	ctx := context.Background()

	t.Run("非法邮箱被拒绝", func(t *testing.T) {
		// Arrange
		uc, db, _ := newUsecase(t)

		// Act
		resp, err := uc.Register(ctx, &authv1.RegisterRequest{
			Credential: &authv1.Credential{Email: "not-an-email", Password: testPassword},
			Role:       authv1.Role_ROLE_USER,
		})

		// Assert
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidEmail, resp.GetErr())
		assert.Empty(t, resp.GetAssignedId())
		assertNoCredential(t, db, "not-an-email")
	})

	t.Run("非法密码被拒绝", func(t *testing.T) {
		uc, db, _ := newUsecase(t)

		resp, err := uc.Register(ctx, &authv1.RegisterRequest{
			Credential: &authv1.Credential{Email: emailOf(bizUID1), Password: "weak"},
			Role:       authv1.Role_ROLE_USER,
		})

		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidPassword, resp.GetErr())
		assert.Empty(t, resp.GetAssignedId())
		assertNoCredential(t, db, emailOf(bizUID1))
	})

	t.Run("角色未指定被拒绝", func(t *testing.T) {
		uc, db, _ := newUsecase(t)

		resp, err := uc.Register(ctx, &authv1.RegisterRequest{
			Credential: &authv1.Credential{Email: emailOf(bizUID1), Password: testPassword},
			Role:       authv1.Role_ROLE_UNSPECIFIED,
		})

		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidArgument, resp.GetErr())
		assert.Empty(t, resp.GetAssignedId())
		assertNoCredential(t, db, emailOf(bizUID1))
	})

	t.Run("合法请求注册成功并以哈希落库", func(t *testing.T) {
		// Arrange
		uc, db, _ := newUsecase(t)
		email := emailOf(bizUID2)

		// Act
		resp, err := uc.Register(ctx, &authv1.RegisterRequest{
			Credential: &authv1.Credential{Email: email, Password: testPassword},
			Role:       authv1.Role_ROLE_ADMIN,
		})

		// Assert
		require.NoError(t, err)
		require.False(t, resp.GetFail())
		assert.Nil(t, resp.GetErr())
		assert.NotEmpty(t, resp.GetAssignedId(), "应返回分配的用户 ID")

		var got auth.UserCredential
		require.NoError(t, db.Where("email = ?", email).First(&got).Error)
		assert.Equal(t, resp.GetAssignedId(), strconv.FormatInt(got.ID, 10), "assigned_id 应与落库主键一致")
		assert.Equal(t, authv1.Role_ROLE_ADMIN, got.Role)
		assert.NotEmpty(t, got.PasswordHash)
		assert.NotEqual(t, testPassword, got.PasswordHash, "密码不得明文落库")
		assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(got.PasswordHash), []byte(testPassword)),
			"落库哈希应能校验原密码")
		assert.NotZero(t, got.CreatedAt)
		assert.Empty(t, got.RefreshTokenIDWeb, "注册不应产生登录态")
		assert.Empty(t, got.RefreshTokenIDMobile, "注册不应产生登录态")
	})

	t.Run("重复邮箱返回 DuplicateEmail", func(t *testing.T) {
		// Arrange
		uc, db, _ := newUsecase(t)
		email := emailOf(bizUID3)
		seedUser(t, db, bizUID3, email, testPassword, authv1.Role_ROLE_USER)

		// Act
		resp, err := uc.Register(ctx, &authv1.RegisterRequest{
			Credential: &authv1.Credential{Email: email, Password: testPassword},
			Role:       authv1.Role_ROLE_USER,
		})

		// Assert
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.DuplicateEmail, resp.GetErr())
		assert.Empty(t, resp.GetAssignedId())

		var cnt int64
		require.NoError(t, db.Model(&auth.UserCredential{}).Where("email = ?", email).Count(&cnt).Error)
		assert.Equal(t, int64(1), cnt, "不应产生第二条凭据")
	})
}

// assertNoCredential 断言指定邮箱未落库
func assertNoCredential(t *testing.T, db *gorm.DB, email string) {
	t.Helper()
	var cnt int64
	require.NoError(t, db.Model(&auth.UserCredential{}).Where("email = ?", email).Count(&cnt).Error)
	assert.Zero(t, cnt, "被拒绝的注册不应落库")
}

// ---------- Login ----------

func TestAuthUsecase_Login(t *testing.T) {
	ctx := context.Background()

	t.Run("非法邮箱返回 IncorrectCredential", func(t *testing.T) {
		uc, _, _ := newUsecase(t)

		resp, err := uc.Login(ctx, &authv1.LoginRequest{
			Credential: &authv1.Credential{Email: "bad", Password: testPassword},
			Platform:   authv1.Platform_PLATFORM_WEB,
		})

		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.IncorrectCredential, resp.GetErr())
		assert.Nil(t, resp.GetAccessToken())
		assert.Nil(t, resp.GetRefreshToken())
	})

	t.Run("密码格式非法返回 IncorrectCredential", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		email := emailOf(bizUID1)
		seedUser(t, db, bizUID1, email, testPassword, authv1.Role_ROLE_USER)

		resp, err := uc.Login(ctx, &authv1.LoginRequest{
			Credential: &authv1.Credential{Email: email, Password: "short"},
			Platform:   authv1.Platform_PLATFORM_WEB,
		})

		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.IncorrectCredential, resp.GetErr())
	})

	t.Run("邮箱不存在返回 IncorrectCredential", func(t *testing.T) {
		uc, _, _ := newUsecase(t)

		resp, err := uc.Login(ctx, &authv1.LoginRequest{
			Credential: &authv1.Credential{Email: emailOf(999999), Password: testPassword},
			Platform:   authv1.Platform_PLATFORM_WEB,
		})

		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.IncorrectCredential, resp.GetErr())
	})

	t.Run("密码错误返回 IncorrectCredential", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		email := emailOf(bizUID1)
		seedUser(t, db, bizUID1, email, testPassword, authv1.Role_ROLE_USER)

		resp, err := uc.Login(ctx, &authv1.LoginRequest{
			Credential: &authv1.Credential{Email: email, Password: "Passw0rd!!"},
			Platform:   authv1.Platform_PLATFORM_WEB,
		})

		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.IncorrectCredential, resp.GetErr())
	})

	t.Run("平台未指定返回 InvalidArgument 且不建立登录态", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		email := emailOf(bizUID1)
		seedUser(t, db, bizUID1, email, testPassword, authv1.Role_ROLE_USER)

		resp, err := uc.Login(ctx, &authv1.LoginRequest{
			Credential: &authv1.Credential{Email: email, Password: testPassword},
			Platform:   authv1.Platform_PLATFORM_UNSPECIFIED,
		})

		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidArgument, resp.GetErr())

		var got auth.UserCredential
		require.NoError(t, db.Where("email = ?", email).First(&got).Error)
		assert.Empty(t, got.RefreshTokenIDWeb, "平台非法不应写入登录态")
		assert.Empty(t, got.RefreshTokenIDMobile, "平台非法不应写入登录态")
	})

	t.Run("Web 登录成功：返回双 token 并记录 web 登录态", func(t *testing.T) {
		// Arrange
		uc, db, mgr := newUsecase(t)
		email := emailOf(bizUID2)
		seedUser(t, db, bizUID2, email, testPassword, authv1.Role_ROLE_ADMIN)

		// Act
		resp, err := uc.Login(ctx, &authv1.LoginRequest{
			Credential: &authv1.Credential{Email: email, Password: testPassword},
			Platform:   authv1.Platform_PLATFORM_WEB,
		})

		// Assert
		require.NoError(t, err)
		require.False(t, resp.GetFail())
		assert.Nil(t, resp.GetErr())
		require.NotNil(t, resp.GetAccessToken())
		require.NotNil(t, resp.GetRefreshToken())
		assert.NotEmpty(t, resp.GetAccessToken().GetValue())
		assert.NotEmpty(t, resp.GetRefreshToken().GetValue())

		accessClaims, err := mgr.Parse(resp.GetAccessToken().GetValue())
		require.NoError(t, err, "签发的 access token 必须可被校验")
		assert.Equal(t, defaults.JwtAccessToken, accessClaims.Type)
		assert.Equal(t, strconv.FormatInt(bizUID2, 10), accessClaims.UID)
		assert.Equal(t, authv1.Role_ROLE_ADMIN.String(), accessClaims.Role)

		refreshClaims, err := mgr.Parse(resp.GetRefreshToken().GetValue())
		require.NoError(t, err, "签发的 refresh token 必须可被校验")
		assert.Equal(t, defaults.JwtRefreshToken, refreshClaims.Type)

		var got auth.UserCredential
		require.NoError(t, db.Where("email = ?", email).First(&got).Error)
		assert.Equal(t, refreshClaims.ID, got.RefreshTokenIDWeb, "web 登录态应记录本次 refresh jti")
		assert.Empty(t, got.RefreshTokenIDMobile, "web 登录不应影响 mobile 登录态")
	})

	t.Run("Mobile 登录成功：仅记录 mobile 登录态", func(t *testing.T) {
		uc, db, mgr := newUsecase(t)
		email := emailOf(bizUID3)
		seedUser(t, db, bizUID3, email, testPassword, authv1.Role_ROLE_USER)

		resp, err := uc.Login(ctx, &authv1.LoginRequest{
			Credential: &authv1.Credential{Email: email, Password: testPassword},
			Platform:   authv1.Platform_PLATFORM_MOBILE,
		})

		require.NoError(t, err)
		require.False(t, resp.GetFail())

		refreshClaims, err := mgr.Parse(resp.GetRefreshToken().GetValue())
		require.NoError(t, err)

		var got auth.UserCredential
		require.NoError(t, db.Where("email = ?", email).First(&got).Error)
		assert.Equal(t, refreshClaims.ID, got.RefreshTokenIDMobile, "mobile 登录态应记录本次 refresh jti")
		assert.Empty(t, got.RefreshTokenIDWeb, "mobile 登录不应影响 web 登录态")
	})
}

// ---------- GetRefreshToken ----------

func TestAuthUsecase_GetRefreshToken(t *testing.T) {
	t.Run("凭证缺失或伪造返回 Unauthorized", func(t *testing.T) {
		uc, _, _ := newUsecase(t)

		resp, err := uc.GetRefreshToken(context.Background(), &authv1.GetRefreshTokenRequest{RefreshToken: "forged-token"})

		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.Unauthorized, resp.GetErr())
		assert.Nil(t, resp.GetToken())
	})

	t.Run("携带 access token 返回 InvalidArgument", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedUser(t, db, bizUID1, emailOf(bizUID1), testPassword, authv1.Role_ROLE_USER)
		access, _, _ := mustLogin(t, uc, bizUID1, authv1.Platform_PLATFORM_WEB)

		resp, err := uc.GetRefreshToken(context.Background(), &authv1.GetRefreshTokenRequest{RefreshToken: access})

		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidArgument, resp.GetErr())
	})

	t.Run("UID 非法返回 InvalidArgument", func(t *testing.T) {
		uc, _, mgr := newUsecase(t)

		token := mustToken(t, mgr, &jwt.Claims{
			Type: defaults.JwtRefreshToken, UID: "not-a-number", Role: authv1.Role_ROLE_USER.String(),
		})
		resp, err := uc.GetRefreshToken(context.Background(), &authv1.GetRefreshTokenRequest{RefreshToken: token})

		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidArgument, resp.GetErr())
	})

	t.Run("用户不存在返回 Unauthorized", func(t *testing.T) {
		uc, _, mgr := newUsecase(t)

		token := mustToken(t, mgr, &jwt.Claims{
			Type: defaults.JwtRefreshToken, UID: "999999", Role: authv1.Role_ROLE_USER.String(),
		})
		resp, err := uc.GetRefreshToken(context.Background(), &authv1.GetRefreshTokenRequest{RefreshToken: token})

		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.Unauthorized, resp.GetErr())
	})

	t.Run("jti 与登录态不符返回 Unauthorized", func(t *testing.T) {
		uc, db, mgr := newUsecase(t)
		seedUser(t, db, bizUID1, emailOf(bizUID1), testPassword, authv1.Role_ROLE_USER)

		token := mustToken(t, mgr, &jwt.Claims{
			RegisteredClaims: jwtClaimID("never-issued"),
			Type:             defaults.JwtRefreshToken,
			UID:              strconv.FormatInt(bizUID1, 10),
			Role:             authv1.Role_ROLE_USER.String(),
		})
		resp, err := uc.GetRefreshToken(context.Background(), &authv1.GetRefreshTokenRequest{RefreshToken: token})

		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.Unauthorized, resp.GetErr())
	})

	t.Run("刷新成功并轮换 jti（旧 token 立即失效）", func(t *testing.T) {
		// Arrange：登录建立 web 登录态
		uc, db, mgr := newUsecase(t)
		seedUser(t, db, bizUID1, emailOf(bizUID1), testPassword, authv1.Role_ROLE_USER)
		_, oldToken, oldClaims := mustLogin(t, uc, bizUID1, authv1.Platform_PLATFORM_WEB)

		// Act：用旧 refresh token 刷新
		resp, err := uc.GetRefreshToken(context.Background(), &authv1.GetRefreshTokenRequest{RefreshToken: oldToken})

		// Assert
		require.NoError(t, err)
		require.False(t, resp.GetFail())
		assert.Nil(t, resp.GetErr())
		require.NotNil(t, resp.GetToken())

		newClaims, err := mgr.Parse(resp.GetToken().GetValue())
		require.NoError(t, err)
		assert.Equal(t, defaults.JwtRefreshToken, newClaims.Type)
		assert.Equal(t, strconv.FormatInt(bizUID1, 10), newClaims.UID)
		assert.NotEqual(t, oldClaims.ID, newClaims.ID, "刷新必须轮换 jti")

		var got auth.UserCredential
		require.NoError(t, db.Where("id = ?", bizUID1).First(&got).Error)
		assert.Equal(t, newClaims.ID, got.RefreshTokenIDWeb, "登录态应指向新的 jti")

		// 旧 jti 已被轮换，重放旧 token 必须被拒绝（防令牌重放）
		replay, err := uc.GetRefreshToken(context.Background(), &authv1.GetRefreshTokenRequest{RefreshToken: oldToken})
		require.NoError(t, err)
		assert.True(t, replay.GetFail())
		assert.Equal(t, errmsg.Unauthorized, replay.GetErr())
	})
}

// ---------- GetAccessToken ----------

func TestAuthUsecase_GetAccessToken(t *testing.T) {
	t.Run("凭证缺失或伪造返回 Unauthorized", func(t *testing.T) {
		uc, _, _ := newUsecase(t)

		resp, err := uc.GetAccessToken(context.Background(), &authv1.GetAccessTokenRequest{RefreshToken: "forged-token"})

		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.Unauthorized, resp.GetErr())
		assert.Nil(t, resp.GetToken())
	})

	t.Run("携带 access token 返回 InvalidArgument", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedUser(t, db, bizUID1, emailOf(bizUID1), testPassword, authv1.Role_ROLE_USER)
		access, _, _ := mustLogin(t, uc, bizUID1, authv1.Platform_PLATFORM_WEB)

		resp, err := uc.GetAccessToken(context.Background(), &authv1.GetAccessTokenRequest{RefreshToken: access})

		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidArgument, resp.GetErr())
	})

	t.Run("UID 非法返回 InvalidArgument", func(t *testing.T) {
		uc, _, mgr := newUsecase(t)

		token := mustToken(t, mgr, &jwt.Claims{
			Type: defaults.JwtRefreshToken, UID: "not-a-number",
		})
		resp, err := uc.GetAccessToken(context.Background(), &authv1.GetAccessTokenRequest{RefreshToken: token})

		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidArgument, resp.GetErr())
	})

	t.Run("用户不存在返回 Unauthorized", func(t *testing.T) {
		uc, _, mgr := newUsecase(t)

		token := mustToken(t, mgr, &jwt.Claims{
			Type: defaults.JwtRefreshToken, UID: "999999",
		})
		resp, err := uc.GetAccessToken(context.Background(), &authv1.GetAccessTokenRequest{RefreshToken: token})

		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.Unauthorized, resp.GetErr())
	})

	t.Run("jti 与登录态不符返回 Unauthorized", func(t *testing.T) {
		uc, db, mgr := newUsecase(t)
		seedUser(t, db, bizUID1, emailOf(bizUID1), testPassword, authv1.Role_ROLE_USER)

		token := mustToken(t, mgr, &jwt.Claims{
			RegisteredClaims: jwtClaimID("never-issued"),
			Type:             defaults.JwtRefreshToken,
			UID:              strconv.FormatInt(bizUID1, 10),
		})
		resp, err := uc.GetAccessToken(context.Background(), &authv1.GetAccessTokenRequest{RefreshToken: token})

		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.Unauthorized, resp.GetErr())
	})

	t.Run("刷新 access 成功且不轮换 refresh 登录态", func(t *testing.T) {
		// Arrange
		uc, db, mgr := newUsecase(t)
		seedUser(t, db, bizUID2, emailOf(bizUID2), testPassword, authv1.Role_ROLE_ADMIN)
		_, refresh, refreshClaims := mustLogin(t, uc, bizUID2, authv1.Platform_PLATFORM_WEB)

		// Act
		resp, err := uc.GetAccessToken(context.Background(), &authv1.GetAccessTokenRequest{RefreshToken: refresh})

		// Assert
		require.NoError(t, err)
		require.False(t, resp.GetFail())
		assert.Nil(t, resp.GetErr())
		require.NotNil(t, resp.GetToken())

		accessClaims, err := mgr.Parse(resp.GetToken().GetValue())
		require.NoError(t, err)
		assert.Equal(t, defaults.JwtAccessToken, accessClaims.Type)
		assert.Equal(t, strconv.FormatInt(bizUID2, 10), accessClaims.UID)
		assert.Equal(t, authv1.Role_ROLE_ADMIN.String(), accessClaims.Role)

		var got auth.UserCredential
		require.NoError(t, db.Where("id = ?", bizUID2).First(&got).Error)
		assert.Equal(t, refreshClaims.ID, got.RefreshTokenIDWeb, "换 access token 不应轮换 refresh jti")

		// refresh token 仍可继续使用
		assert.NotEmpty(t, refresh)
	})
}

// ---------- Logout ----------

func TestAuthUsecase_Logout(t *testing.T) {
	t.Run("UID 为空返回 InvalidID", func(t *testing.T) {
		uc, _, _ := newUsecase(t)

		resp, err := uc.Logout(context.Background(), &authv1.LogoutRequest{})

		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetErr())
	})

	t.Run("UID 非法返回 InvalidID", func(t *testing.T) {
		uc, _, _ := newUsecase(t)

		resp, err := uc.Logout(context.Background(), &authv1.LogoutRequest{Uid: "not-a-number"})

		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetErr())
	})

	t.Run("登出后双端 refresh token 全部失效", func(t *testing.T) {
		// Arrange：web 与 mobile 各登录一次
		uc, db, _ := newUsecase(t)
		seedUser(t, db, bizUID1, emailOf(bizUID1), testPassword, authv1.Role_ROLE_USER)
		_, webToken, webClaims := mustLogin(t, uc, bizUID1, authv1.Platform_PLATFORM_WEB)
		_, mobileToken, mobileClaims := mustLogin(t, uc, bizUID1, authv1.Platform_PLATFORM_MOBILE)

		// Act：身份由请求体直传会话 uid
		resp, err := uc.Logout(context.Background(), &authv1.LogoutRequest{Uid: strconv.FormatInt(bizUID1, 10)})

		// Assert
		require.NoError(t, err)
		assert.False(t, resp.GetFail())
		assert.Nil(t, resp.GetErr())

		var got auth.UserCredential
		require.NoError(t, db.Where("id = ?", bizUID1).First(&got).Error)
		assert.NotEqual(t, webClaims.ID, got.RefreshTokenIDWeb, "web 登录态应被置为占位值")
		assert.NotEqual(t, mobileClaims.ID, got.RefreshTokenIDMobile, "mobile 登录态应被置为占位值")

		// 双端旧 refresh token 均无法再刷新
		webResp, err := uc.GetRefreshToken(context.Background(), &authv1.GetRefreshTokenRequest{RefreshToken: webToken})
		require.NoError(t, err)
		assert.True(t, webResp.GetFail())
		assert.Equal(t, errmsg.Unauthorized, webResp.GetErr())

		mobileResp, err := uc.GetRefreshToken(context.Background(), &authv1.GetRefreshTokenRequest{RefreshToken: mobileToken})
		require.NoError(t, err)
		assert.True(t, mobileResp.GetFail())
		assert.Equal(t, errmsg.Unauthorized, mobileResp.GetErr())
	})

	t.Run("重复登出幂等成功", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedUser(t, db, bizUID2, emailOf(bizUID2), testPassword, authv1.Role_ROLE_USER)
		mustLogin(t, uc, bizUID2, authv1.Platform_PLATFORM_WEB)

		logoutReq := &authv1.LogoutRequest{Uid: strconv.FormatInt(bizUID2, 10)}
		first, err := uc.Logout(context.Background(), logoutReq)
		require.NoError(t, err)
		require.False(t, first.GetFail())

		second, err := uc.Logout(context.Background(), logoutReq)
		require.NoError(t, err)
		assert.False(t, second.GetFail(), "重复登出应幂等成功")
		assert.Nil(t, second.GetErr())
	})
}

// TestAuthUsecase_DeleteAccount 删除账户（注册回滚内部接口）：删行即失效全部登录态
func TestAuthUsecase_DeleteAccount(t *testing.T) {
	t.Run("UID 为空/非法返回 InvalidID", func(t *testing.T) {
		uc, _, _ := newUsecase(t)

		resp, err := uc.DeleteAccount(context.Background(), &authv1.DeleteAccountRequest{})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetErr())

		resp, err = uc.DeleteAccount(context.Background(), &authv1.DeleteAccountRequest{Uid: "not-a-number"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetErr())
	})

	t.Run("账户不存在返回 NotFound", func(t *testing.T) {
		uc, _, _ := newUsecase(t)

		resp, err := uc.DeleteAccount(context.Background(), &authv1.DeleteAccountRequest{Uid: strconv.FormatInt(bizUID1, 10)})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.NotFound, resp.GetErr())
	})

	t.Run("删除账户：行删除且登录态一并失效，重复删除 NotFound", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedUser(t, db, bizUID1, emailOf(bizUID1), testPassword, authv1.Role_ROLE_USER)
		_, webToken, _ := mustLogin(t, uc, bizUID1, authv1.Platform_PLATFORM_WEB)
		_, mobileToken, _ := mustLogin(t, uc, bizUID1, authv1.Platform_PLATFORM_MOBILE)

		resp, err := uc.DeleteAccount(context.Background(), &authv1.DeleteAccountRequest{Uid: strconv.FormatInt(bizUID1, 10)})
		require.NoError(t, err)
		assert.False(t, resp.GetFail())

		var count int64
		require.NoError(t, db.Model(&auth.UserCredential{}).Where("id = ?", bizUID1).Count(&count).Error)
		assert.Zero(t, count, "注册记录应被物理删除")

		webResp, err := uc.GetRefreshToken(context.Background(), &authv1.GetRefreshTokenRequest{RefreshToken: webToken})
		require.NoError(t, err)
		assert.True(t, webResp.GetFail(), "web 登录态随删行失效")

		mobileResp, err := uc.GetRefreshToken(context.Background(), &authv1.GetRefreshTokenRequest{RefreshToken: mobileToken})
		require.NoError(t, err)
		assert.True(t, mobileResp.GetFail(), "mobile 登录态随删行失效")

		again, err := uc.DeleteAccount(context.Background(), &authv1.DeleteAccountRequest{Uid: strconv.FormatInt(bizUID1, 10)})
		require.NoError(t, err)
		assert.True(t, again.GetFail())
		assert.Equal(t, errmsg.NotFound, again.GetErr())
	})
}

// ---------- ChangePassword ----------

func TestAuthUsecase_ChangePassword(t *testing.T) {
	// newTestPassword 合法新密码样本（与 testPassword 不同）
	const newTestPassword = "NewPassw0rd!"

	changeReq := func(uid int64, oldPw, newPw string) *authv1.ChangePasswordRequest {
		return &authv1.ChangePasswordRequest{
			Uid:         strconv.FormatInt(uid, 10),
			OldPassword: oldPw,
			NewPassword: newPw,
		}
	}

	t.Run("UID 为空/非法返回 InvalidID", func(t *testing.T) {
		uc, _, _ := newUsecase(t)

		resp, err := uc.ChangePassword(context.Background(), &authv1.ChangePasswordRequest{OldPassword: testPassword, NewPassword: newTestPassword})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetErr())

		resp, err = uc.ChangePassword(context.Background(), &authv1.ChangePasswordRequest{Uid: "not-a-number", OldPassword: testPassword, NewPassword: newTestPassword})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetErr())
	})

	t.Run("新密码弱返回 InvalidPassword", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedUser(t, db, bizUID1, emailOf(bizUID1), testPassword, authv1.Role_ROLE_USER)

		resp, err := uc.ChangePassword(context.Background(), changeReq(bizUID1, testPassword, "weak"))
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidPassword, resp.GetErr())
	})

	t.Run("账户不存在返回 IncorrectCredential", func(t *testing.T) {
		uc, _, _ := newUsecase(t)

		resp, err := uc.ChangePassword(context.Background(), changeReq(bizUID1, testPassword, newTestPassword))
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.IncorrectCredential, resp.GetErr())
	})

	t.Run("旧密码错误返回 IncorrectCredential 且不改密", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedUser(t, db, bizUID1, emailOf(bizUID1), testPassword, authv1.Role_ROLE_USER)

		var before auth.UserCredential
		require.NoError(t, db.Where("id = ?", bizUID1).First(&before).Error)

		resp, err := uc.ChangePassword(context.Background(), changeReq(bizUID1, "WrongPassw0rd", newTestPassword))
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.IncorrectCredential, resp.GetErr())

		var after auth.UserCredential
		require.NoError(t, db.Where("id = ?", bizUID1).First(&after).Error)
		assert.Equal(t, before.PasswordHash, after.PasswordHash, "旧密码错误时不应改写哈希")
	})

	t.Run("改密成功：哈希更新且双端登录态失效", func(t *testing.T) {
		// Arrange：web 与 mobile 各登录一次
		uc, db, _ := newUsecase(t)
		seedUser(t, db, bizUID1, emailOf(bizUID1), testPassword, authv1.Role_ROLE_USER)
		_, webToken, webClaims := mustLogin(t, uc, bizUID1, authv1.Platform_PLATFORM_WEB)
		_, mobileToken, mobileClaims := mustLogin(t, uc, bizUID1, authv1.Platform_PLATFORM_MOBILE)

		// Act
		resp, err := uc.ChangePassword(context.Background(), changeReq(bizUID1, testPassword, newTestPassword))
		require.NoError(t, err)
		assert.False(t, resp.GetFail())
		assert.Nil(t, resp.GetErr())

		// Assert：哈希已换写为新密码
		var got auth.UserCredential
		require.NoError(t, db.Where("id = ?", bizUID1).First(&got).Error)
		assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(got.PasswordHash), []byte(newTestPassword)), "哈希应对应新密码")

		// 双端登录态被置为占位值
		assert.NotEqual(t, webClaims.ID, got.RefreshTokenIDWeb, "web 登录态应被置为占位值")
		assert.NotEqual(t, mobileClaims.ID, got.RefreshTokenIDMobile, "mobile 登录态应被置为占位值")

		// 双端旧 refresh token 均无法再刷新
		webResp, err := uc.GetRefreshToken(context.Background(), &authv1.GetRefreshTokenRequest{RefreshToken: webToken})
		require.NoError(t, err)
		assert.True(t, webResp.GetFail())
		assert.Equal(t, errmsg.Unauthorized, webResp.GetErr())

		mobileResp, err := uc.GetRefreshToken(context.Background(), &authv1.GetRefreshTokenRequest{RefreshToken: mobileToken})
		require.NoError(t, err)
		assert.True(t, mobileResp.GetFail())
		assert.Equal(t, errmsg.Unauthorized, mobileResp.GetErr())

		// 旧密码不可登录
		oldLogin, err := uc.Login(context.Background(), &authv1.LoginRequest{
			Credential: &authv1.Credential{Email: emailOf(bizUID1), Password: testPassword},
			Platform:   authv1.Platform_PLATFORM_WEB,
		})
		require.NoError(t, err)
		assert.True(t, oldLogin.GetFail())
		assert.Equal(t, errmsg.IncorrectCredential, oldLogin.GetErr())

		// 新密码可重新登录
		newLogin, err := uc.Login(context.Background(), &authv1.LoginRequest{
			Credential: &authv1.Credential{Email: emailOf(bizUID1), Password: newTestPassword},
			Platform:   authv1.Platform_PLATFORM_WEB,
		})
		require.NoError(t, err)
		assert.False(t, newLogin.GetFail(), "新密码应可登录: %v", newLogin.GetErr())
	})

	t.Run("新旧密码相同允许成功", func(t *testing.T) {
		uc, db, _ := newUsecase(t)
		seedUser(t, db, bizUID2, emailOf(bizUID2), testPassword, authv1.Role_ROLE_USER)

		resp, err := uc.ChangePassword(context.Background(), changeReq(bizUID2, testPassword, testPassword))
		require.NoError(t, err)
		assert.False(t, resp.GetFail())
		assert.Nil(t, resp.GetErr())

		// 哈希已重新生成（仍对应同一明文），原密码可继续登录
		login, err := uc.Login(context.Background(), &authv1.LoginRequest{
			Credential: &authv1.Credential{Email: emailOf(bizUID2), Password: testPassword},
			Platform:   authv1.Platform_PLATFORM_WEB,
		})
		require.NoError(t, err)
		assert.False(t, login.GetFail(), "相同密码改密后仍应可登录: %v", login.GetErr())
	})
}
