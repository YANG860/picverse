# test — 集成测试工具

依赖真实 PostgreSQL 的测试基础设施。

## 目录

| 路径 | 说明 |
| --- | --- |
| `testdb/db.go` | 连接/建库/迁移/清空表 |
| `testdb/dbinit/` | 一次性准备测试库的命令行入口 |
| `testsnowflake/` | 测试用雪花节点初始化 |

## 准备测试库

```bash
go run ./test/testdb/dbinit
# 输出：
# ✔ 数据库就绪: postgres://postgres:***@106.53.172.186:5432/test?sslmode=disable
# ✔ 迁移完成 (sql/*.sql)
# ✔ 业务表已清空
```

该命令会：建库（不存在时）→ 执行 `sql/*.sql` 迁移 → `TRUNCATE` 全部业务表。

默认连接 `106.53.172.186:5432/test`，可用环境变量覆盖：

```bash
TEST_PG_URL='postgres://user:pass@host:5432/test?sslmode=disable' go run ./test/testdb/dbinit
```

## 在测试中使用

```go
func TestXxx(t *testing.T) {
 db := testdb.Setup(t) // 迁移 + 清空表 + 注册测试结束清理
 repo := v1.NewMediaRepo(db)
 // ...
}
```

- `testdb.Setup(t)` 每个用例都是干净的空表，用例之间互不污染。
- 表清空范围为 `media_assets`、`media_files`、`out_box_entries`（`RESTART IDENTITY CASCADE`）。
- `testing.Short()` 模式下自动跳过（`go test -short` 不需要数据库）。

## 运行

```bash
go test ./internal/media/v1/... -count=1
go test -short ./...              # 跳过依赖真实数据库的用例
```

真实数据库用例单个约 3-5 秒（远程 RTT 为主），全量约 50 秒。
