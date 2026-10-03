# db — 数据库连接

代码：`packages/db/`（`pg.go`、`sqlite.go`、`gorm_logger.go`）。

封装 GORM 连接创建，统一开启**语义化错误翻译**并接入 slog 日志。

## 连接

| 函数 | 用途 |
| --- | --- |
| `NewPG(cfg, lf)` | PostgreSQL 连接（生产），返回 `(*gorm.DB, cleanup)` |
| `NewSQLite(dbPath, lf)` | SQLite 连接（本地测试），`dbPath` 如 `":memory:"` / `"./test.db"` |
| `NewSQLiteMemory(lf)` | 内存 SQLite（单元测试） |

- 两者都用 `NewSlogAdapter` 接入 GORM 日志。
- **`TranslateError: true`**：把驱动层错误翻译为 `gorm.ErrDuplicatedKey`、`gorm.ErrRecordNotFound` 等语义化错误，供业务层 `errors.Is` 判断（如注册时的邮箱重复、查询未命中）。
- 连接失败直接 panic（fail-fast）。
- `NewPG` 返回的 cleanup 关闭底层 `sql.DB`。

## GORM 日志适配（slogAdapter）

`NewSlogAdapter(l *slog.Logger)` 把 `slog.Logger` 适配为 `gormlogger.Interface`：

- `Info` / `Warn` / `Error` — 直接映射到对应 slog 级别。
- `Trace` — 记录 SQL 执行细节（`gorm trace`：`sql` / `rows` / `elapsed` / `err`），级别 `Debug`。
- `LogMode` — 无操作（返回自身），级别由 slog handler 控制。

## 使用

```go
lf, cleanupLog := logger.NewLoggerFactory(cfg)
db, cleanupDB := db.NewPG(cfg, lf)
defer cleanupDB()
defer cleanupLog()
```

与 [test/testdb](../../test/test.md) 配合可为单元测试克隆独立的临时数据库。
