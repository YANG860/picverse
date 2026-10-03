# utils — 工具集

代码：`packages/utils/`（`snowflake.go`、`s3.go`、`worker_pool.go`）。

## 雪花 ID（snowflake.go）

- `Node` — 全局雪花节点，包 `init()` 时用 `config.Global.SnowFlake.ID` 初始化（失败 panic）。
- `InitSnowflake(machineID)` — 初始化节点，`machineID` 范围 0–1023。
- `GenID()` — 生成雪花 ID（`snowflake.ID`）。

ID 是全局基础约定，DB 主键、消息 ID、`jti` 等都用它生成。

## S3 对象键（s3.go）

| 函数 | 说明 |
| --- | --- |
| `BuildKey(id, type, format)` | 生成对象键 `media/<id>/<type>.<ext>` |
| `ContentTypeOf(format)` | 格式 → MIME 类型（`image/jpeg` / `image/png` / `image/webp`） |

`type` 决定 `<type>` 段（`origin`/`216`/`540`/`1080`），`format` 决定扩展名（`.jpg`/`.png`/`.webp`）。示例：`media/123/origin.png`、`media/123/540.webp`。

## 协程池（worker_pool.go）

通用 `WorkerPool`，支持任务提交、优雅关闭、上下文取消与 panic 恢复。

- `NewWorkerPool(ctx, capacity, opts...)` — 创建并启动；`capacity <= 0` 用 `defaults.WorkerPoolSize`。
- 选项：`WithBufferSize`、`WithPanicHandler`、`WithLogger`。
- `AddWork(task)` — 提交无返回值任务，池已关闭返回 `context.Canceled`。
- `Close()` 等待已提交任务完成；`CloseNow()` 立即关闭；`Len()` 返回待处理数。
- 泛型 `Submit[T](pool, task)` — 提交带返回值任务，返回 `ResultFuture[T]`（`Wait` / `IsDone`）。
- worker panic 被 `safeExecute` 捕获（默认记录 slog.Error + 堆栈），不会拖垮整个池。
