// Package e2e 对外部署的 auth 服务做黑盒端到端测试：
// 注册 → 登录 → 换发 access/refresh token → 轮换失效 → 登出。
//
// 前置条件：auth 服务已部署（须已接入 jwt 服务端拦截器，见 cmd/auth/main.go），PostgreSQL 可用。
// 运行：
//
//	go test ./test/e2e/... -count=1          # 默认 http://localhost:8081
//	AUTH_E2E_ADDR=http://host:8081 go test ./test/e2e/...
//	go test -short ./...                     # -short 跳过
package e2e

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "rednote/gen/go/auth/v1"
	"rednote/gen/go/auth/v1/authv1connect"
	commonv1 "rednote/gen/go/common/v1"
	"rednote/packages/errmsg"
)

const defaultAuthAddr = "http://localhost:8081"

// 测试账号密码：满足服务端校验（8-32 位，含大小写字母与数字）
const e2ePassword = "E2ePassw0rd!2026"

func authAddr() string {
	if v := os.Getenv("AUTH_E2E_ADDR"); v != "" {
		return v
	}
	return defaultAuthAddr
}

// e2eEmail 生成唯一的测试邮箱（时间戳保证不重复，账号留在测试库中不清理）
func e2eEmail() string {
	return fmt.Sprintf("e2e-auth-%d@test.local", time.Now().UnixNano())
}

// newAuthClient 创建 connect 客户端，并探测服务可达性（不可达则跳过测试）
func newAuthClient(t *testing.T) authv1connect.AuthServiceClient {
	t.Helper()

	resp, err := http.Get(authAddr() + "/auth.v1.AuthService/")
	if err != nil {
		t.Skipf("e2e: auth 服务不可达 (%s)，跳过: %v", authAddr(), err)
	}
	_ = resp.Body.Close()

	return authv1connect.NewAuthServiceClient(http.DefaultClient, authAddr())
}

// setBearer 把 Bearer token 写入请求头（token 为空表示匿名请求）。
// 必须在发送 RPC 之前调用。
func setBearer(req connect.AnyRequest, token string) {
	if token != "" {
		req.Header().Set("Authorization", "Bearer "+token)
	}
}

// assertBizErr 断言业务失败及错误码
func assertBizErr(t *testing.T, fail bool, err *commonv1.Error, want *commonv1.Error, msg string) {
	t.Helper()
	require.True(t, fail, "%s 应业务失败", msg)
	require.NotNil(t, err, "%s 应返回错误信息", msg)
	assert.Equal(t, want.GetCode(), err.GetCode(), "%s 错误码不符: want=%s got=%s",
		msg, want.GetMsg(), err.GetMsg())
}

// TestAuthServiceE2E auth 全生命周期黑盒测试。
// 步骤之间有依赖（同一账号的 token 轮换链），因此在一个测试内按序执行：
//
//	注册 → 重复注册 → 登录失败路径 → 登录 → 换 access → 轮换 refresh
//	→ 旧 token 失效 → 轮换后仍可用 → 类型不符/伪造/无 token 拒绝 → 登出 → 登出后失效
func TestAuthServiceE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: -short 模式跳过依赖外部部署服务的测试")
	}

	client := newAuthClient(t)
	email := e2eEmail()

	// ---- 1. 注册 ----
	reg, err := client.Register(context.Background(),
		connect.NewRequest(&authv1.RegisterRequest{
			Credential: &authv1.Credential{Email: email, Password: e2ePassword},
			Role:       authv1.Role_ROLE_USER,
		}))
	require.NoError(t, err, "Register RPC")
	require.False(t, reg.Msg.GetFail(), "注册业务失败: %v", reg.Msg.GetErr())
	require.NotEmpty(t, reg.Msg.GetAssignedId(), "注册应返回分配的用户ID")
	t.Logf("注册成功 uid=%s email=%s", reg.Msg.GetAssignedId(), email)

	// ---- 2. 重复注册 → DuplicateEmail ----
	dup, err := client.Register(context.Background(),
		connect.NewRequest(&authv1.RegisterRequest{
			Credential: &authv1.Credential{Email: email, Password: e2ePassword},
			Role:       authv1.Role_ROLE_USER,
		}))
	require.NoError(t, err, "重复注册 RPC")
	assertBizErr(t, dup.Msg.GetFail(), dup.Msg.GetErr(), errmsg.DuplicateEmail, "重复注册")

	// ---- 3. 登录失败路径 ----
	badPw, err := client.Login(context.Background(),
		connect.NewRequest(&authv1.LoginRequest{
			Credential: &authv1.Credential{Email: email, Password: "WrongPass0rd!"},
			Platform:   authv1.Platform_PLATFORM_WEB,
		}))
	require.NoError(t, err, "错误密码登录 RPC")
	assertBizErr(t, badPw.Msg.GetFail(), badPw.Msg.GetErr(), errmsg.IncorrectCredential, "错误密码登录")

	unknown, err := client.Login(context.Background(),
		connect.NewRequest(&authv1.LoginRequest{
			Credential: &authv1.Credential{Email: "nobody-" + email, Password: e2ePassword},
			Platform:   authv1.Platform_PLATFORM_WEB,
		}))
	require.NoError(t, err, "未知邮箱登录 RPC")
	assertBizErr(t, unknown.Msg.GetFail(), unknown.Msg.GetErr(), errmsg.IncorrectCredential, "未知邮箱登录")

	// ---- 4. 登录（web）----
	login, err := client.Login(context.Background(),
		connect.NewRequest(&authv1.LoginRequest{
			Credential: &authv1.Credential{Email: email, Password: e2ePassword},
			Platform:   authv1.Platform_PLATFORM_WEB,
		}))
	require.NoError(t, err, "登录 RPC")
	require.False(t, login.Msg.GetFail(), "登录业务失败: %v", login.Msg.GetErr())
	refreshA := login.Msg.GetRefreshToken().GetValue()
	accessToken := login.Msg.GetAccessToken().GetValue()
	require.NotEmpty(t, refreshA, "登录应返回 refresh token")
	require.NotEmpty(t, accessToken, "登录应返回 access token")

	// ---- 5. 用 refresh token 换发 access token ----
	atReq := connect.NewRequest(&authv1.GetAccessTokenRequest{})
	setBearer(atReq, refreshA)
	atResp, err := client.GetAccessToken(context.Background(), atReq)
	require.NoError(t, err, "GetAccessToken RPC")
	require.False(t, atResp.Msg.GetFail(), "换发 access token 业务失败: %v", atResp.Msg.GetErr())
	require.NotEmpty(t, atResp.Msg.GetToken().GetValue(), "应返回新的 access token")

	// ---- 6. 轮换 refresh token：A → B ----
	rtReq := connect.NewRequest(&authv1.GetRefreshTokenRequest{})
	setBearer(rtReq, refreshA)
	rtResp, err := client.GetRefreshToken(context.Background(), rtReq)
	require.NoError(t, err, "GetRefreshToken RPC")
	require.False(t, rtResp.Msg.GetFail(), "轮换 refresh token 业务失败: %v", rtResp.Msg.GetErr())
	refreshB := rtResp.Msg.GetToken().GetValue()
	require.NotEmpty(t, refreshB, "轮换应返回新 refresh token")
	assert.NotEqual(t, refreshA, refreshB, "轮换出的 token 应与旧 token 不同")

	// ---- 7. 旧 refresh token 轮换后立即失效 ----
	staleReq := connect.NewRequest(&authv1.GetRefreshTokenRequest{})
	setBearer(staleReq, refreshA)
	stale, err := client.GetRefreshToken(context.Background(), staleReq)
	require.NoError(t, err, "旧 token 轮换 RPC")
	assertBizErr(t, stale.Msg.GetFail(), stale.Msg.GetErr(), errmsg.Unauthorized, "旧 refresh token 应失效")

	// ---- 8. 轮换后的新 token 仍可正常使用 ----
	at2Req := connect.NewRequest(&authv1.GetAccessTokenRequest{})
	setBearer(at2Req, refreshB)
	at2, err := client.GetAccessToken(context.Background(), at2Req)
	require.NoError(t, err, "新 token 换发 access RPC")
	require.False(t, at2.Msg.GetFail(), "轮换后换发 access 应成功: %v", at2.Msg.GetErr())

	rt2Req := connect.NewRequest(&authv1.GetRefreshTokenRequest{})
	setBearer(rt2Req, refreshB)
	rt2, err := client.GetRefreshToken(context.Background(), rt2Req)
	require.NoError(t, err, "新 token 再轮换 RPC")
	require.False(t, rt2.Msg.GetFail(), "轮换后再轮换应成功: %v", rt2.Msg.GetErr())
	refreshC := rt2.Msg.GetToken().GetValue()
	require.NotEmpty(t, refreshC, "再次轮换应返回新 refresh token")

	// ---- 9. token 类型不符：拿 access token 调 refresh 接口 → InvalidArgument ----
	wrongTypeReq := connect.NewRequest(&authv1.GetRefreshTokenRequest{})
	setBearer(wrongTypeReq, accessToken)
	wrongType, err := client.GetRefreshToken(context.Background(), wrongTypeReq)
	require.NoError(t, err, "类型不符 RPC")
	assertBizErr(t, wrongType.Msg.GetFail(), wrongType.Msg.GetErr(), errmsg.InvalidArgument,
		"access token 不应用于轮换 refresh token")

	// ---- 10. 伪造 token → Unauthorized（claims 为空）----
	forgedReq := connect.NewRequest(&authv1.GetRefreshTokenRequest{})
	setBearer(forgedReq, refreshA[:len(refreshA)/2]+"forged")
	forged, err := client.GetRefreshToken(context.Background(), forgedReq)
	require.NoError(t, err, "伪造 token RPC")
	assertBizErr(t, forged.Msg.GetFail(), forged.Msg.GetErr(), errmsg.Unauthorized, "伪造 token 应被拒绝")

	// ---- 11. 无 token → Unauthorized ----
	anon, err := client.GetRefreshToken(context.Background(),
		connect.NewRequest(&authv1.GetRefreshTokenRequest{}))
	require.NoError(t, err, "无 token RPC")
	assertBizErr(t, anon.Msg.GetFail(), anon.Msg.GetErr(), errmsg.Unauthorized, "无 token 应被拒绝")

	// ---- 12. 登出 ----
	outReq := connect.NewRequest(&authv1.LogoutRequest{})
	setBearer(outReq, refreshC)
	out, err := client.Logout(context.Background(), outReq)
	require.NoError(t, err, "Logout RPC")
	require.False(t, out.Msg.GetFail(), "登出业务失败: %v", out.Msg.GetErr())

	// ---- 13. 登出后 refresh token 全平台失效 ----
	afterOutReq := connect.NewRequest(&authv1.GetRefreshTokenRequest{})
	setBearer(afterOutReq, refreshC)
	afterOut, err := client.GetRefreshToken(context.Background(), afterOutReq)
	require.NoError(t, err, "登出后轮换 RPC")
	assertBizErr(t, afterOut.Msg.GetFail(), afterOut.Msg.GetErr(), errmsg.Unauthorized,
		"登出后 refresh token 应失效")
}
