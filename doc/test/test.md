# test — 测试体系



## 测试类型

| 类型 | 位置 | 依赖 | 说明 |
| --- | --- | --- | --- |
| 单元/集成测试 | `internal/*/v1/*_test.go`、`packages/*/…_test.go` | 真实 PostgreSQL（克隆库） | 存储层、拦截器等 |
| E2E 黑盒测试 | `test/e2e/` | 已部署的服务 | 仅通过 ConnectRPC 访问，服务不可达时自动跳过 |

## 测试数据库（testdb）

`test/testdb` 采用 **templateDB 方案**：

- 维护一个已完成全量迁移的模板库 `test_template`。
- 每个用例用 `testdb.Setup(t)` 通过 `CREATE DATABASE … TEMPLATE` 克隆独立库，用例结束自动 `DROP`。
- 用例间零共享、互不污染，天然支持 `t.Parallel()`。
- `testing.Short()`（`go test -short`）下自动跳过依赖真实数据库的用例。

连接串默认 `postgres://…@106.53.172.186:5432/test?sslmode=disable`，用环境变量 **`TEST_PG_URL`** 覆盖（串中库名仅定位服务器，实际测试库按用例克隆）。

## 维护脚本

| 脚本 | 作用 |
| --- | --- |
| `scripts/dbinit` | 重建模板库（DROP → CREATE `test_template` → 执行 `sql/*.sql`） |
| `scripts/dbsync` | 把 `sql/*.sql` 幂等同步到已有库（不建库、不 DROP、不动数据） |

> **`sql/*.sql` 变更后必须重跑 `go run ./scripts/dbinit`**，否则测试将运行在旧 schema 上。

## 运行

```bash
go run ./scripts/dbinit                # 准备模板库
go test ./internal/media/v1/... -count=1
go test -short ./...                   # 跳过依赖真实数据库的用例

# E2E（服务已部署）
AUTH_E2E_ADDR=http://localhost:8081 go test ./test/e2e/ -run TestAuthServiceE2E -count=1
```

## 其他

- `test/testsnowflake/` — 测试用雪花节点初始化。
- E2E 产生的数据（media 资产、auth 测试账号）留在服务端数据库中不清理。
- 测试进程崩溃可能残留 `test_<pid>_<seq>_<随机>` 命名的克隆库，排查/清理见 `test/README.md`。
