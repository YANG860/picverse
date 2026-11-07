# utils — 工具集

代码：`packages/utils/`（`snowflake.go`、`s3.go`、`worker_pool.go`、`json.go`、`validate.go`）。

## 雪花 ID（snowflake.go）

- `Node` — 全局雪花节点，包 `init()` 时用 `config.Global.SnowFlake.ID` 初始化（失败 panic）。
- `InitSnowflake(machineID)` — 初始化节点，`machineID` 范围 0–1023。
- `GenID()` — 生成雪花 ID（`snowflake.ID`）。

ID 是全局基础约定，DB 主键、消息 ID、`jti` 等都用它生成。

## 游标（cursor.go）

`common.v1.Cursor` 的编码/解析工具，两种游标语义：

| 函数 | 说明 |
| --- | --- |
| `ParseTimeCursor(cursor)` / `NewTimeCursor(stamp, limit)` | 时间游标：`str` 编码末条 `created_at` 毫秒；非法串回退当前时间（首页），limit 越界回退默认 20（上限 1000） |
| `ParseOffsetCursor(cursor)` / `NewOffsetCursor(nextOffset, limit)` | offset 游标（搜索/推荐流）：`str` 编码偏移量；非法/负数回退 0（首页），limit 口径同上 |

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

## JSON（json.go）

- `UnmarshalJSON(data)` — 解析 JSON 对象为 `datatypes.JSONMap`。空数据（未提供/空串）返回空 map，语义上 ≡ `{}`；仅非法 JSON 返回错误。

## 校验（validate.go）

结构/格式校验，媒体内容审核由审核服务负责，此处只做结构校验。

| 函数 | 说明 |
| --- | --- |
| `ValidateName(name)` | 昵称：trim 后非空、1~64 rune、不含控制字符 |
| `ValidateBio(bio)` | 简介：≤512 rune、放行 `\n\r\t`，其余控制字符拒绝；可为空 |
| `ValidateEmail(email)` | RFC 5322 邮箱格式正则 |
| `ValidatePassword(pw)` | 8~32 位，须含大写、小写、数字 |
| `ValidateMedia(file)` | 媒体文件结构：非空 `ObjKey`（TODO 细化） |
| `ValidateTitle(title)` | 标题非空 ≤100（TODO 细化） |
| `ValidateContent(content)` | 正文非空 ≤1000（TODO 细化） |
| `ValidateImage(data, meta)` | 图片数据与元数据一致性：字节数、SHA256、实际解码格式、尺寸上限（`defaults.MaxImageWidth/Height`）与元数据宽高一致；返回具体错误 |

常量 `NameMaxLen = 64` / `BioMaxLen = 512`（rune 计数）与 `user_profiles` 列宽 `VARCHAR(64)/VARCHAR(512)` 对齐，避免落库时截断/报错。
