// 建库脚本程序
// Command dbinit 准备集成测试用的 PostgreSQL 库：
// 建库（不存在时）→ 执行 sql/*.sql 迁移 → 清空业务表。
//
// 用法：
//
//	go run ./test/testdb/dbinit
//	TEST_PG_URL=postgres://user:pass@host:5432/test?sslmode=disable go run ./test/testdb/dbinit
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
	if err := testdb.EnsureDatabase(); err != nil {
		return fmt.Errorf("建库: %w", err)
	}
	fmt.Println("✔ 数据库就绪:", testdb.DSN())

	db, err := testdb.Open()
	if err != nil {
		return fmt.Errorf("连接测试库: %w", err)
	}

	if err := testdb.MigrateDB(db); err != nil {
		return fmt.Errorf("迁移: %w", err)
	}
	fmt.Println("✔ 迁移完成 (sql/*.sql)")

	if err := testdb.TruncateDB(db); err != nil {
		return fmt.Errorf("清空表: %w", err)
	}
	fmt.Println("✔ 业务表已清空")

	sqlDB, err := db.DB()
	if err == nil {
		defer func() { _ = sqlDB.Close() }()
	}
	return nil
}
