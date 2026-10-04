# test — 测试工具


## 目录

| 路径 | 说明 |
| --- | --- |
| `testdb/db.go` | 模板库维护/按用例克隆测试库/迁移 |
| `testsnowflake/` | 测试用雪花节点初始化 |
| `e2e/` | 部署后的服务做黑盒端到端测试（media / auth） |
| `../scripts/dbinit/` | 重建模板库的命令行入口 |
| `../scripts/dbsync/` | 把 sql/*.sql 幂等同步到指定库的命令行入口 |

## 测试数据库

`testdb/` 提供了用于**单元测试** 自动克隆模板和清理的临时数据库 相互隔离 支持并发

端到端测试请**手动准备测试数据库**  不进行自动清理  用于自行排查

## 准备模板库

模板库 `test_template` 保存已执行完全量迁移的 schema，由 `scripts/dbinit` 重建：

```bash
go run ./scripts/dbinit
# 输出：
# ✔ 模板库就绪: test_template ( postgres://postgres:***@106.53.172.186:5432/test?sslmode=disable )
# ✔ 迁移完成 (sql/*.sql)
```

该命令会：DROP 旧模板 → CREATE `test_template` → 执行 `sql/*.sql` 迁移。

默认连接 `106.53.172.186:5432`，可用环境变量覆盖（连接串中的库名不参与测试，仅定位服务器）：

```bash
TEST_PG_URL='postgres://user:pass@host:5432/test?sslmode=disable' go run ./scripts/dbinit
```

> **注意**：`sql/*.sql` 迁移变更后必须重跑本命令，否则集成测试将运行在旧 schema 上。

## 同步已有库的表结构

`scripts/dbsync` 把 `sql/*.sql` 幂等应用到已有库（不建库、不 DROP，不动数据），
用于 sql 变更后手动同步 test 库或其他环境库：

```bash
go run ./scripts/dbsync              # 同步连接串中的库（默认 test）
go run ./scripts/dbsync -db <name>   # 同步同一服务器上的指定库
```

与 `dbinit` 的区别：dbinit 重建模板库（DROP + CREATE + 迁移）；dbsync 只对已有库做增量同步。

## 在测试中使用

```go
func TestXxx(t *testing.T) {
 db := testdb.Setup(t) // 从模板克隆独立库 + 注册测试结束清理（DROP）
 repo := v1.NewMediaRepo(db)
 // ...
}
```

- `testdb.Setup(t)` 为每个用例克隆一个独立数据库（`CREATE DATABASE ... TEMPLATE`），schema 与模板一致且表为空。
- 用例之间零共享、互不污染，天然支持 `t.Parallel()`。
- 测试结束自动 `DROP` 该用例的库；`testing.Short()` 模式下自动跳过（`go test -short` 不需要数据库）。
- 若模板库不存在，`Setup` 会直接失败并提示先运行 `go run ./scripts/dbinit`。

## 残留库排查

测试进程崩溃可能残留 `test_<pid>_<seq>_<随机串>` 命名的库，可按前缀手动清理：

```sql
-- 先查看
SELECT datname FROM pg_database WHERE datname LIKE 'test_%' AND datname <> 'test_template';
-- 确认后逐个删除
DROP DATABASE test_<pid>_<seq>_<随机串>;
```

## 运行

```bash
go test ./internal/media/v1/... -count=1
go test -short ./...              # 跳过依赖真实数据库的用例
```

## E2E 测试

`e2e/` 对已部署的服务做黑盒测试，不依赖内部实现，仅通过 connect RPC 访问。
服务不可达时自动跳过（而非失败），便于本地/CI 按需运行。

| 测试 | 覆盖 | 默认地址 | 环境变量 |
| --- | --- | --- | --- |
| `TestMediaServiceE2E` | 上传 → 预签名直传 MinIO → 异步处理 → 变体 → 分页 → 封禁 | `http://localhost:8080` | `MEDIA_E2E_ADDR`、`MEDIA_E2E_S3_*` |
| `TestAuthServiceE2E` | 注册 → 登录 → 换发 token → 轮换失效 → 登出 | `http://localhost:8081` | `AUTH_E2E_ADDR` |

前置：对应服务已部署（auth 须已接入 jwt 服务端拦截器，见 `cmd/auth/main.go`）；
auth e2e 需 PostgreSQL 中 `jwt.secret` 等配置与服务端一致。

```bash
# 例：部署 auth 后运行
AUTH_E2E_ADDR=http://localhost:8081 go test ./test/e2e/ -run TestAuthServiceE2E -count=1
```

注：e2e 测试产生的数据（media 资产、auth 测试账号）留在服务端数据库中不清理。
