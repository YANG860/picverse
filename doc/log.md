# 日志体系

## 概述

日志基于标准库 `log/slog`，默认以 **JSON** 格式输出。核心实现位于 `packages/logger`：

- `LoggerFactory`：创建/管理带名字的 logger，统一输出目标。
- `LogWriter`：多路输出（标准输出 + 文件），文件侧带缓冲。
- `TraceHandler`：slog.Handler 包装层，从 context 提取 `trace_id` 自动附加到每条日志。

## LoggerFactory

`logger.NewLoggerFactory(cfg)` 返回 `*LoggerFactoryImpl`（实现 `logger.LoggerFactory` 接口）：

| 方法 | 说明 |
| --- | --- |
| `GetLoggerOf(name)` | 获取带 `name` 字段的子 logger，各模块用它区分日志来源（如 `auth` / `media_consumer` / `poller`） |
| `Default()` | 获取根 logger |
| `GetWriter()` | 获取底层 `io.Writer`（`LogWriter`） |
| `Close()` | 关闭底层 writer，冲刷缓冲 |

所有日志为 JSON 编码（`slog.NewJSONHandler`），外层套 `TraceHandler`。

## 输出目标（LogWriter）

由 `config.LoggerConfig` 控制，可同时输出到标准输出与文件：

| 配置项 | 默认 | 说明 |
| --- | --- | --- |
| `std` | — | 是否输出到标准输出 |
| `file` / `out` | — | 是否写文件及文件路径（自动创建目录） |
| `rotate` | — | 是否启用轮转（lumberjack） |
| `maxsize` | 100 | 单文件最大 MB |
| `maxbackups` | 7 | 保留旧文件数 |
| `maxage` | 28 | 保留天数 |
| `compress` | false | 是否压缩旧文件 |
| `buffersize` | 262144 (256KB) | 文件写缓冲大小，`0` 使用默认 |

- 文件写入经 `bufio.Writer` 缓冲，`Close()` 时冲刷并关闭。
- 轮转使用 `gopkg.in/natefinch/lumberjack.v2`，参数非法时回退到 `packages/defaults` 中的默认值（`RotateMaxSize/Backups/Age`、`BufferSize`）。
- 两路 writer 均关闭时，回退到标准输出。

## trace_id 关联

`TraceHandler.Handle` 从 `context` 读取 `trace.FromContext(ctx)`，非空时为记录追加 `trace_id` 字段。配合 `packages/trace` 的 ConnectRPC 拦截器，可将一次 RPC 调用贯穿的所有日志、以及下游 MQ 消息串联起来。

详见 [packages/trace/trace.md](packages/trace/trace.md)。

## GORM 日志

`packages/db.NewSlogAdapter` 把 `slog.Logger` 适配为 GORM 的 `logger.Interface`，SQL 执行细节（语句、行数、耗时、错误）以 `Debug` 级别输出（`gorm trace`）。详见 [packages/db/db.md](packages/db/db.md)。

## 目标可观测栈

项目规划的日志服务为 **Loki + Promtail + Grafana**：服务侧输出 JSON 结构化日志 → Promtail 采集 → Loki 存储/索引 → Grafana 查询与告警。当前仓库内已落地的是"结构化 JSON 日志输出"这一侧，采集/存储/展示的部署配置暂不在本仓库内。
