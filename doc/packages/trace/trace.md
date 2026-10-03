# trace — 链路追踪

代码：`packages/trace/`（`trace.go`、`interceptor.go`）。

在 ConnectRPC 请求、context、MQ 消息之间透传 `trace_id`，用于串联一次调用的全链路日志。

## context 传递

| 函数 | 说明 |
| --- | --- |
| `WithTraceID(ctx, id)` | 附加 trace_id（空串不注入） |
| `FromContext(ctx)` | 读取 trace_id，无则返回空串 |

## 拦截器

`traceInterceptor` 同时覆盖服务端与客户端，通过 `req.Spec().IsClient` 区分：

- **服务端**：从请求头提取 `trace_id` 注入 ctx，并回写到响应头（便于调用方记录）；请求未携带则跳过。
- **客户端**：发送前从 ctx 取 `trace_id` 写入请求头；没有则跳过。

支持一元（`WrapUnary`）与流式（`WrapStreamingClient` / `WrapStreamingHandler`）。

入口：
- `NewServerInterceptor()` — 服务端拦截器
- `NewClientInterceptor()` — 客户端拦截器

## 关联链路

- **RPC 层**：请求头 `trace_id` ⇄ context ⇄ 响应头。
- **日志层**：`packages/logger.TraceHandler` 从 ctx 提取 `trace_id` 自动附加到每条日志。
- **MQ 层**：消息头 `trace_id`（`packages/defaults.TraceIDKey`）随消息传递，consumer 消费时还原到 ctx。

传输键统一为 `packages/defaults.TraceIDKey`（`"trace_id"`）。
