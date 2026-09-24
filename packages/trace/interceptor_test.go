package trace

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"rednote/packages/defaults"
)

const pingProcedure = "/trace.v1.PingService/Ping"

// echoHeader 用于把 handler 所在 ctx 的 traceID 回传给测试断言
const echoHeader = "x-echo-trace-id"

// newPingHandler 构造服务端探针：回显 ctx 中的 traceID
func newPingHandler() http.Handler {
	return connect.NewUnaryHandler(
		pingProcedure,
		func(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
			resp := connect.NewResponse(&emptypb.Empty{})
			resp.Header().Set(echoHeader, FromContext(ctx))
			return resp, nil
		},
		connect.WithInterceptors(NewServerInterceptor()),
	)
}

// 服务端：请求带 trace_id → 注入 ctx；不带 → 跳过不注入；响应头回写 trace_id
func TestServerInterceptor(t *testing.T) {
	probe := newPingHandler()

	t.Run("header_to_ctx", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, pingProcedure, nil)
		req.Header.Set("Content-Type", "application/proto")
		req.Header.Set(defaults.TraceIDKey, "trace-from-client")
		probe.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get(echoHeader); got != "trace-from-client" {
			t.Fatalf("ctx traceID = %q, want trace-from-client", got)
		}
		if got := rec.Header().Get(defaults.TraceIDKey); got != "trace-from-client" {
			t.Fatalf("response header trace_id = %q, want trace-from-client", got)
		}
	})

	t.Run("skip_when_missing", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, pingProcedure, nil)
		req.Header.Set("Content-Type", "application/proto")
		probe.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get(echoHeader); got != "" {
			t.Fatalf("ctx traceID should be empty when header missing, got %q", got)
		}
		if got := rec.Header().Get(defaults.TraceIDKey); got != "" {
			t.Fatalf("response header trace_id should be unset when header missing, got %q", got)
		}
	})
}

// 端到端：客户端拦截器从 ctx 取 traceID 写入请求头 → 服务端拦截器提取注入 ctx
func TestClientToServerTracePropagation(t *testing.T) {
	srv := httptest.NewServer(newPingHandler())
	defer srv.Close()

	client := connect.NewClient[emptypb.Empty, emptypb.Empty](
		srv.Client(),
		srv.URL+pingProcedure,
		connect.WithInterceptors(NewClientInterceptor()),
	)

	t.Run("ctx_to_header", func(t *testing.T) {
		resp, err := client.CallUnary(
			WithTraceID(context.Background(), "trace-in-ctx"),
			connect.NewRequest(&emptypb.Empty{}),
		)
		if err != nil {
			t.Fatalf("call failed: %v", err)
		}
		if got := resp.Header().Get(echoHeader); got != "trace-in-ctx" {
			t.Fatalf("server ctx traceID = %q, want trace-in-ctx", got)
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
		if got := resp.Header().Get(echoHeader); got != "" {
			t.Fatalf("server ctx traceID should be empty when ctx has none, got %q", got)
		}
		if got := resp.Header().Get(defaults.TraceIDKey); got != "" {
			t.Fatalf("response header trace_id should be unset when ctx has none, got %q", got)
		}
	})
}
