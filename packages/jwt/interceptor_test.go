package jwt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"rednote/config"
)

const echoProcedure = "/jwt.v1.EchoService/Echo"

// 回传头用于把 handler 所在 ctx 中的 token / claims 传回测试断言
const (
	echoTokenHeader  = "x-echo-token"
	echoUIDHeader    = "x-echo-uid"
	echoClaimsHeader = "x-echo-claims" // claims 是否存在："1" / ""
)

const testSecret = "test_secret"

// newTestManager 用测试密钥构造 Manager，避免依赖配置文件
func newTestManager(t *testing.T) *ManagerImpl {
	t.Helper()
	return NewManager(&config.Config{
		JWT: config.JWTConfig{
			Secret: testSecret,
			Issuer: "test",
			Expire: time.Hour,
		},
	})
}

// newEchoHandler 构造服务端探针：把 ctx 中的原始 token 与 claims 回传到响应头
func newEchoHandler(mgr Manager) http.Handler {
	return connect.NewUnaryHandler(
		echoProcedure,
		func(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
			resp := connect.NewResponse(&emptypb.Empty{})
			resp.Header().Set(echoTokenHeader, TokenFromContext(ctx))
			if claims := ClaimsFromContext(ctx); claims != nil {
				resp.Header().Set(echoClaimsHeader, "1")
				resp.Header().Set(echoUIDHeader, claims.UID)
			}
			return resp, nil
		},
		connect.WithInterceptors(NewServerInterceptor(mgr)),
	)
}

// signToken 用 Manager 签发一个指定 UID 的 token，失败直接终止测试
func signToken(t *testing.T, mgr Manager, uid string) string {
	t.Helper()
	token, err := mgr.Generate(&Claims{UID: uid})
	if err != nil {
		t.Fatalf("签发 token 失败: %v", err)
	}
	return token
}

func TestBearerToken(t *testing.T) {
	h := http.Header{}
	if got := bearerToken(h); got != "" {
		t.Fatalf("空请求头应返回空串, got %q", got)
	}
	h.Set(AuthorizationHeader, "Basic dXNlcjpwYXNz")
	if got := bearerToken(h); got != "" {
		t.Fatalf("非 Bearer 方案应返回空串, got %q", got)
	}
	h.Set(AuthorizationHeader, "Bearer  ")
	if got := bearerToken(h); got != "" {
		t.Fatalf("空 token 应返回空串, got %q", got)
	}
	h.Set(AuthorizationHeader, "Bearer abc.def.ghi")
	if got := bearerToken(h); got != "abc.def.ghi" {
		t.Fatalf("bearerToken = %q, want abc.def.ghi", got)
	}
	// 方案名大小写不敏感（RFC 7235）
	h.Set(AuthorizationHeader, "bearer abc.def.ghi")
	if got := bearerToken(h); got != "abc.def.ghi" {
		t.Fatalf("小写 bearer 应解析成功, got %q", got)
	}
}

// 服务端：Authorization 头有 token → 解析并把原始 token + claims 注入 ctx
func TestServerInterceptor(t *testing.T) {
	mgr := newTestManager(t)
	probe := newEchoHandler(mgr)
	token := signToken(t, mgr, "user-42")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, echoProcedure, nil)
	req.Header.Set("Content-Type", "application/proto")
	req.Header.Set(AuthorizationHeader, BearerPrefix+token)
	probe.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(echoTokenHeader); got != token {
		t.Fatalf("ctx 原始 token = %q, want 签发的 token", got)
	}
	if got := rec.Header().Get(echoClaimsHeader); got != "1" {
		t.Fatal("ctx 中应存在解析后的 claims")
	}
	if got := rec.Header().Get(echoUIDHeader); got != "user-42" {
		t.Fatalf("claims.UID = %q, want user-42", got)
	}
}

// 服务端：无 token / 无效 token → 跳过注入，不拦截请求
func TestServerInterceptorSkip(t *testing.T) {
	mgr := newTestManager(t)
	probe := newEchoHandler(mgr)

	t.Run("no_token", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, echoProcedure, nil)
		req.Header.Set("Content-Type", "application/proto")
		probe.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("无 token 不应拦截, status = %d", rec.Code)
		}
		if got := rec.Header().Get(echoTokenHeader); got != "" {
			t.Fatalf("ctx 原始 token 应为空, got %q", got)
		}
		if got := rec.Header().Get(echoClaimsHeader); got != "" {
			t.Fatal("ctx 中不应存在 claims")
		}
	})

	t.Run("invalid_token", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, echoProcedure, nil)
		req.Header.Set("Content-Type", "application/proto")
		req.Header.Set(AuthorizationHeader, BearerPrefix+"invalid.token.here")
		probe.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("无效 token 不应拦截, status = %d", rec.Code)
		}
		if got := rec.Header().Get(echoTokenHeader); got != "" {
			t.Fatalf("ctx 原始 token 应为空, got %q", got)
		}
		if got := rec.Header().Get(echoClaimsHeader); got != "" {
			t.Fatal("ctx 中不应存在 claims")
		}
	})

	t.Run("wrong_secret_token", func(t *testing.T) {
		other := NewManager(&config.Config{
			JWT: config.JWTConfig{Secret: "other_secret", Issuer: "test"},
		})
		badToken := signToken(t, other, "user-42")

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, echoProcedure, nil)
		req.Header.Set("Content-Type", "application/proto")
		req.Header.Set(AuthorizationHeader, BearerPrefix+badToken)
		probe.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("签名不符的 token 不应拦截, status = %d", rec.Code)
		}
		if got := rec.Header().Get(echoClaimsHeader); got != "" {
			t.Fatal("签名不符的 token 不应注入 claims")
		}
	})
}

// 端到端：客户端拦截器从 ctx 取原始 token 附加 Authorization 头 → 服务端解析注入 ctx
func TestClientToServerTokenPropagation(t *testing.T) {
	mgr := newTestManager(t)
	srv := httptest.NewServer(newEchoHandler(mgr))
	defer srv.Close()

	client := connect.NewClient[emptypb.Empty, emptypb.Empty](
		srv.Client(),
		srv.URL+echoProcedure,
		connect.WithInterceptors(NewClientInterceptor()),
	)

	t.Run("ctx_token_to_header", func(t *testing.T) {
		token := signToken(t, mgr, "user-42")
		resp, err := client.CallUnary(
			WithToken(context.Background(), token),
			connect.NewRequest(&emptypb.Empty{}),
		)
		if err != nil {
			t.Fatalf("call failed: %v", err)
		}
		if got := resp.Header().Get(echoTokenHeader); got != token {
			t.Fatal("服务端 ctx 应收到客户端 ctx 中的原始 token")
		}
		if got := resp.Header().Get(echoUIDHeader); got != "user-42" {
			t.Fatalf("服务端 claims.UID = %q, want user-42", got)
		}
	})

	t.Run("skip_when_ctx_empty", func(t *testing.T) {
		resp, err := client.CallUnary(
			context.Background(),
			connect.NewRequest(&emptypb.Empty{}),
		)
		if err != nil {
			t.Fatalf("call failed: %v", err)
		}
		if got := resp.Header().Get(echoTokenHeader); got != "" {
			t.Fatalf("ctx 无 token 时请求头不应携带 Authorization, 服务端收到 %q", got)
		}
		if got := resp.Header().Get(echoClaimsHeader); got != "" {
			t.Fatal("ctx 无 token 时服务端不应注入 claims")
		}
	})
}
