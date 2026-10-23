// Package rpc 下游 connectrpc 客户端构造：统一挂服务发现传输层与拦截器，
// wire 装配后注入各服务的 DownstreamRepo / consumer 等下游调用方。
package rpc

import (
	"net/http"

	authv1connect "rednote/gen/go/auth/v1/authv1connect"
	mediav1connect "rednote/gen/go/media/v1/mediav1connect"
	postv1connect "rednote/gen/go/post/v1/postv1connect"
	userv1connect "rednote/gen/go/user/v1/userv1connect"
	"rednote/packages/jwt"
	"rednote/packages/trace"

	"connectrpc.com/connect"
)

// newHTTPClient 构造走服务发现传输层的 http.Client。
// base URL 的 host 仅作占位（传输层按服务名解析重写），scheme 决定明文/TLS。
func newHTTPClient(provider EndpointProvider, svc ServiceName) *http.Client {
	return &http.Client{Transport: NewTransport(provider, NewRoundRobin(), svc, nil)}
}

// clientOptions 下游调用统一拦截器：
//   - trace：透传 traceID，串联跨服务调用链；
//   - jwt：把会话原始 token 以 Authorization 头透传给下游
//     （RefreshTokens/Logout 等接口依赖下游从请求头自行解析会话）。
func clientOptions() connect.ClientOption {
	return connect.WithInterceptors(trace.NewClientInterceptor(), jwt.NewClientInterceptor())
}

// NewAuthClient 构造 auth 服务客户端
func NewAuthClient(provider EndpointProvider) authv1connect.AuthServiceClient {
	return authv1connect.NewAuthServiceClient(
		newHTTPClient(provider, ServiceAuth), "http://auth", clientOptions(),
	)
}

// NewUserClient 构造 user 服务客户端
func NewUserClient(provider EndpointProvider) userv1connect.UserServiceClient {
	return userv1connect.NewUserServiceClient(
		newHTTPClient(provider, ServiceUser), "http://user", clientOptions(),
	)
}

// NewMediaClient 构造 media 服务客户端
func NewMediaClient(provider EndpointProvider) mediav1connect.MediaServiceClient {
	return mediav1connect.NewMediaServiceClient(
		newHTTPClient(provider, ServiceMedia), "http://media", clientOptions(),
	)
}

// NewPostClient 构造 post 服务客户端
func NewPostClient(provider EndpointProvider) postv1connect.PostServiceClient {
	return postv1connect.NewPostServiceClient(
		newHTTPClient(provider, ServicePost), "http://post", clientOptions(),
	)
}
