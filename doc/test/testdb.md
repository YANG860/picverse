# testdb — 模板库克隆测试库

代码：`test/testdb/db.go`。

**templateDB 方案**：维护一个已完成全量迁移的模板库 `test_template`，每个用例用
`testdb.Setup(t)` 通过 `CREATE DATABASE … TEMPLATE` 克隆独立库，用例结束自动 `DROP`。
用例间零共享、互不污染，天然支持 `t.Parallel()`；`go test -short` 下自动跳过。

连接串默认 `postgres://…@106.53.172.186:5432/test?sslmode=disable`（tencent2），
用环境变量 **`TEST_PG_URL`** 覆盖（串中库名仅定位服务器，实际测试库按用例克隆）。

## 在测试中使用

```go
func TestXxx(t *testing.T) {
    db := testdb.Setup(t) // 从模板克隆独立库 + 注册测试结束清理（DROP）
    repo := v1.NewUserRepo(db)
    // ...
}
```

## 准备模板库

模板库由 `scripts/dbinit` 重建（DROP → CREATE `test_template` → 执行 `sql/*.sql`）：

```bash
go run ./scripts/dbinit
```

## 同步已有库的表结构

`scripts/dbsync` 把 `sql/*.sql` 幂等应用到已有库（不建库、不 DROP、不动数据）：

```bash
go run ./scripts/dbsync              # 同步连接串中的库（默认 test）
go run ./scripts/dbsync -db <name>   # 同步同一服务器上的指定库
```

与 dbinit 的区别：dbinit 重建模板库（DROP + CREATE + 迁移）；dbsync 只做增量同步。

## 残留库排查

测试进程崩溃可能残留 `test_<pid>_<seq>_<随机串>` 命名的库：

```sql
SELECT datname FROM pg_database WHERE datname LIKE 'test_%' AND datname <> 'test_template';
DROP DATABASE test_<pid>_<seq>_<随机串>;  -- 确认后逐个删除
```
