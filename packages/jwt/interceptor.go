package jwt

import (
	"context"
	"net/http"
	"strings"

	"connectrpc.com/connect"
)

// token 传输约定：标准 Authorization 头，Bearer 认证方案
const (
	AuthorizationHeader = "Authorization"
	BearerPrefix        = "Bearer "
)

// ServerInterceptor 服务端拦截器：请求头带 token 时解析，
// 成功则把原始 token 与解析后的 claims 附加到 ctx；
// 无 token 或解析失败时跳过，不注入也不拦截（是否强制认证由业务层决定）。
type ServerInterceptor struct {
	m Manager
}

// NewServerInterceptor 服务端拦截器：header 有 token 时把原始 token 与 claims 附加到 ctx
func NewServerInterceptor(m Manager) *ServerInterceptor {
	return &ServerInterceptor{m: m}
}

func (i *ServerInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return next(i.inject(ctx, req.Header()), req)
	}
}

func (i *ServerInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *ServerInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return next(i.inject(ctx, conn.RequestHeader()), conn)
	}
}

// inject 从请求头提取 Bearer token 并解析，成功则把原始 token 与 claims 注入 ctx
func (i *ServerInterceptor) inject(ctx context.Context, h http.Header) context.Context {
	token := bearerToken(h)
	if token == "" {
		return ctx
	}
	claims, err := i.m.Parse(token)
	if err != nil {
		// 解析失败（无效/过期 token）时跳过注入，认证强制由业务层处理
		return ctx
	}
	return WithClaims(WithToken(ctx, token), claims)
}

// ClientInterceptor 客户端拦截器：ctx 有原始 token 时附加 Authorization 头，没有则跳过
type ClientInterceptor struct{}

// NewClientInterceptor 客户端拦截器：发送请求时 ctx 有原始 token 就附加到 header
func NewClientInterceptor() connect.Interceptor {
	return ClientInterceptor{}
}

func (ClientInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		attachAuthHeader(ctx, req.Header())
		return next(ctx, req)
	}
}

func (ClientInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		// 流式请求头在首次发送前均可写入
		attachAuthHeader(ctx, conn.RequestHeader())
		return conn
	}
}

func (ClientInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// attachAuthHeader 把 ctx 中的原始 token 以 Bearer 方案写入请求头；ctx 没有则跳过
func attachAuthHeader(ctx context.Context, h http.Header) {
	if token := TokenFromContext(ctx); token != "" {
		h.Set(AuthorizationHeader, BearerPrefix+token)
	}
}

// bearerToken 从请求头提取 Bearer token；格式不符或不存在返回空串
func bearerToken(h http.Header) string {
	raw := h.Get(AuthorizationHeader)
	if len(raw) <= len(BearerPrefix) || !strings.EqualFold(raw[:len(BearerPrefix)], BearerPrefix) {
		return ""
	}
	return strings.TrimSpace(raw[len(BearerPrefix):])
}
