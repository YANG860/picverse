// 建库脚本程序
// Command dbinit 重建集成测试用的模板库（templateDB 方案）：
// DROP 旧模板 → CREATE 模板库 → 执行 sql/*.sql 迁移。
//
// 迁移脚本（sql/*.sql）变更后必须重跑本命令，否则集成测试将运行在旧 schema 上。
//
// 用法：
//
//	go run ./scripts/dbinit
//	TEST_PG_URL=postgres://user:pass@host:5432/test?sslmode=disable go run ./scripts/dbinit
package main

import (
	"fmt"
	"os"

	"rednote/test/testdb"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "dbinit 失败:", err)
		os.Exit(1)
	}
}

func run() error {
	if err := testdb.EnsureTemplate(); err != nil {
		return fmt.Errorf("重建模板库: %w", err)
	}
	fmt.Println("✔ 模板库就绪:", testdb.TemplateDBName, "(", testdb.DSN(), ")")
	fmt.Println("✔ 迁移完成 (sql/*.sql)")
	return nil
}
