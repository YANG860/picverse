// Package rpc 下游 connectrpc 客户端构造：统一挂服务发现传输层与拦截器，
// wire 装配后注入各服务的 DownstreamRepo / consumer 等下游调用方。
package rpc

import (
	"net/http"
	"time"

	authv1connect "rednote/gen/go/auth/v1/authv1connect"
	discoverv1connect "rednote/gen/go/discover/v1/discoverv1connect"
	mediav1connect "rednote/gen/go/media/v1/mediav1connect"
	postv1connect "rednote/gen/go/post/v1/postv1connect"
	userv1connect "rednote/gen/go/user/v1/userv1connect"
	"rednote/packages/defaults"
	"rednote/packages/jwt"
	"rednote/packages/trace"

	"connectrpc.com/connect"
)

// newHTTPClient 构造走服务发现传输层的 http.Client。
// base URL 的 host 仅作占位（传输层按服务名解析重写），scheme 决定明文/TLS。
func newHTTPClient(provider EndpointProvider, svc ServiceName) *http.Client {
	// 传输层兜底：响应头等待上限（防对端接收后不响应的黑洞绕过拦截器调用超时，
	// 如黑洞连接）；主约束是 clientOptions 注入的调用超时，此处仅最后防线
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.ResponseHeaderTimeout = defaults.RPCResponseHeaderTimeout
	return &http.Client{Transport: NewTransport(provider, NewRoundRobin(), svc, nil, WithBaseTransport(base))}
}

// clientOptions 下游调用统一拦截器：
//   - bulkhead：per-service 在途并发上限（<=0 用默认 256；满舱快速失败，见 bulkhead.go）；
//   - timeout：单次调用超时（<=0 用默认 3s；只收紧不放宽，见 timeout.go）；
//   - trace：透传 traceID，串联跨服务调用链；
//   - jwt：把会话原始 token 以 Authorization 头透传给下游
//     （RefreshTokens/Logout 等接口依赖下游从请求头自行解析会话）。
func clientOptions(timeout time.Duration, maxInflight int64) connect.ClientOption {
	if timeout <= 0 {
		timeout = defaults.RPCDefaultTimeout
	}
	if maxInflight <= 0 {
		maxInflight = defaults.RPCMaxInflight
	}
	return connect.WithInterceptors(
		NewBulkheadInterceptor(maxInflight),
		NewTimeoutInterceptor(timeout),
		trace.NewClientInterceptor(),
		jwt.NewClientInterceptor(),
	)
}

// NewAuthClient 构造 auth 服务客户端
func NewAuthClient(provider EndpointProvider, timeout time.Duration, maxInflight int64) authv1connect.AuthServiceClient {
	return authv1connect.NewAuthServiceClient(
		newHTTPClient(provider, ServiceAuth), "http://auth", clientOptions(timeout, maxInflight),
	)
}

// NewUserClient 构造 user 服务客户端
func NewUserClient(provider EndpointProvider, timeout time.Duration, maxInflight int64) userv1connect.UserServiceClient {
	return userv1connect.NewUserServiceClient(
		newHTTPClient(provider, ServiceUser), "http://user", clientOptions(timeout, maxInflight),
	)
}

// NewMediaClient 构造 media 服务客户端
func NewMediaClient(provider EndpointProvider, timeout time.Duration, maxInflight int64) mediav1connect.MediaServiceClient {
	return mediav1connect.NewMediaServiceClient(
		newHTTPClient(provider, ServiceMedia), "http://media", clientOptions(timeout, maxInflight),
	)
}

// NewPostClient 构造 post 服务客户端
func NewPostClient(provider EndpointProvider, timeout time.Duration, maxInflight int64) postv1connect.PostServiceClient {
	return postv1connect.NewPostServiceClient(
		newHTTPClient(provider, ServicePost), "http://post", clientOptions(timeout, maxInflight),
	)
}

// NewDiscoverClient 构造 discover（搜推）服务客户端
func NewDiscoverClient(provider EndpointProvider, timeout time.Duration, maxInflight int64) discoverv1connect.DiscoverServiceClient {
	return discoverv1connect.NewDiscoverServiceClient(
		newHTTPClient(provider, ServiceDiscover), "http://discover", clientOptions(timeout, maxInflight),
	)
}
