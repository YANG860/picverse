# logger — 日志工厂

代码：`packages/logger/`（`logger.go`、`writer.go`、`trace_handler.go`）。

日志体系的实现细节见 [log.md](../../log.md)，本文聚焦包内 API。

## LoggerFactory

接口 `LoggerFactory`（实现 `LoggerFactoryImpl`）：

| 方法 | 说明 |
| --- | --- |
| `GetLoggerOf(name)` | 带 `name` 字段的子 logger |
| `Default()` | 根 logger |
| `GetWriter()` | 底层 `io.Writer`（`LogWriter`） |
| `Close()` | 关闭 writer，冲刷缓冲 |

`NewLoggerFactory(cfg)` 返回 `(*LoggerFactoryImpl, cleanup)`，内部构建 `JSONHandler` → `TraceHandler`。

## LogWriter

多路输出 writer：同时写标准输出与文件（文件侧带 `bufio` 缓冲）。

- `NewLogWriter(std, file, bufSize)` — 构造；两路均为空时回退标准输出。
- `Write` — 写 std + 文件缓冲。
- `Close` — 冲刷缓冲并关闭各路，返回最后一个错误。
- `buildWriter(cfg)` — 按 `config.LoggerConfig` 构建：`std` 开标准输出；`file`/`out` 开文件（自动建目录）；`rotate` 开 lumberjack 轮转（参数非法回退 `packages/defaults` 默认值）；否则普通追加文件。

## TraceHandler

`NewTraceHandler(h)` 包装任意 `slog.Handler`，`Handle` 时从 ctx 提取 `trace_id` 追加为字段，其余方法透传。实现 [trace](../trace/trace.md) 与日志的自动关联。
