package trace

import (
	"context"
	"net/http"

	"connectrpc.com/connect"

	"rednote/packages/defaults"
)

// traceInterceptor 同时覆盖服务端与客户端两种角色，运行时通过 req.Spec().IsClient 区分：
//   - 服务端：从请求头提取 trace_id 注入 context，并回写到响应头（便于调用方记录）；
//     请求未携带时跳过，不注入。
//   - 客户端：发送前从 context 取 trace_id 写入请求头；没有则跳过。
type traceInterceptor struct{}

// NewServerInterceptor 服务端拦截器：自动从请求头提取 traceID 注入 ctx
func NewServerInterceptor() connect.Interceptor {
	return traceInterceptor{}
}

// NewClientInterceptor 客户端拦截器：发送请求时自动附带 traceID
func NewClientInterceptor() connect.Interceptor {
	return traceInterceptor{}
}

func (traceInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if req.Spec().IsClient {
			// 客户端：出站前把 ctx 中的 traceID 写入请求头
			attachToHeader(ctx, req.Header())
			return next(ctx, req)
		}
		// 服务端：入站提取 traceID 注入 ctx，并回写到响应头
		traceID := extractOrNone(req.Header())
		ctx = WithTraceID(ctx, traceID)
		resp, err := next(ctx, req)
		if resp != nil {
			attachToHeader(ctx, resp.Header())
		}
		return resp, err
	}
}

func (traceInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		// 流式请求头在首次发送前均可写入
		attachToHeader(ctx, conn.RequestHeader())
		return conn
	}
}

func (traceInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		traceID := extractOrNone(conn.RequestHeader())
		ctx = WithTraceID(ctx, traceID)
		attachToHeader(ctx, conn.ResponseHeader())
		return next(ctx, conn)
	}
}

// extractOrNone 从请求头读取 traceID，不存在则忽略
func extractOrNone(h http.Header) string {
	if id := h.Get(defaults.TraceIDKey); id != "" {
		return id
	}
	return ""
}

// attachToHeader 把 ctx 中的 traceID 写入请求头；ctx 没有跳过
func attachToHeader(ctx context.Context, h http.Header) {
	traceID := FromContext(ctx)
	if traceID == "" {
		return
	}
	h.Set(defaults.TraceIDKey, traceID)
}
