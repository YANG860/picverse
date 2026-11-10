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

## 分级与记录约定

### 分级标准

| 级别 | 语义 | 典型场景 |
| --- | --- | --- |
| `Error` | 需人工关注 | RPC/DB 故障、数据一致性风险（回滚失败、计数漂移）、panic、消息处理失败 |
| `Warn` | 可恢复或降级，需关注但不阻塞 | 降级容错成功继续服务（列表项缺失跳过、作者置空）、Ack/Nak 失败（将重投）、副作用失败待闲置回收兑底 |
| `Info` | 关键业务动作与消息审计 | 消息收发、状态流转、服务启停、计数变更、删帖回收 |
| `Debug` | 低价值细节 | 仅 GORM SQL 轨迹（`gorm trace`）使用，业务代码不使用 |

### 字段规范

- msg 用英文短句（如 `post.CreateDraft failed`），关键值走结构化 key-value。
- 错误统一用 `error` 字段（不用 `err`）；可枚举业务错误用 `err_code`。
- 消息链路统一携带 `id`（MQMessage ID）/ `type`（payload 类型）/ `trace_id` / `send_at`，同一次失败只打一条 Error（不重复记录 Nak 等衍生动作）。
- 带业务定位字段（`uid` / `post_id` / `asset_id` 等），保证失败日志可直接定位到实体。

### 覆盖要求

- 错误路径必记：每个失败分支（含 best-effort 回滚/卸载失败）都要有可操作上下文，**禁止吞错**（含 `_ = msg.Ack()` 这类丢弃错误）。
- 降级必记：任何容错跳过/降级路径用 `Warn` 并注明降级行为（如 `降级跳过` / `降级置空`）。
- **不记**：业务错误码透传（面客 `fail/err` 已含语义）、正常业务态的缺省（如“未关注”关系降级）、纯转发层（service handler）。
- 噪音控制：空转轮询（count=0）不打；每条消息的日志收敛到“收发审计 + handled（含 type）”两条内。

## GORM 日志

`packages/db.NewSlogAdapter` 把 `slog.Logger` 适配为 GORM 的 `logger.Interface`，SQL 执行细节（语句、行数、耗时、错误）以 `Debug` 级别输出（`gorm trace`）。详见 [packages/db/db.md](packages/db/db.md)。

## 可观测

服务侧输出 JSON 结构化日志（trace_id 关联）；日志的采集/存储/展示不在本仓库内。
