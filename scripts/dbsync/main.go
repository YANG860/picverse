// 表结构同步脚本
// Command dbsync 把 sql/*.sql 中的全部迁移脚本幂等地应用到目标数据库。
// 所有脚本均为 IF NOT EXISTS 语句，重复执行无副作用，不会删除/修改已有数据。
//
// 与 dbinit 的区别：dbinit 重建模板库 test_template（DROP + CREATE + 迁移）；
// dbsync 只对已有库做增量同步（不建库、不 DROP），用于 sql 变更后手动同步
// test 库（或其他环境库）的表结构。
//
// 用法：
//
//	go run ./scripts/dbsync                 # 同步连接串中的库（默认 test）
//	go run ./scripts/dbsync -db <name>      # 同步同一服务器上的指定库
//	TEST_PG_URL=postgres://user:pass@host:5432/test?sslmode=disable go run ./scripts/dbsync
package main

import (
	"flag"
	"fmt"
	"os"

	"rednote/test/testdb"
)

func main() {
	var target string
	flag.StringVar(&target, "db", "", "目标数据库名（默认取连接串中的库名）")
	flag.Parse()

	if err := run(target); err != nil {
		fmt.Fprintln(os.Stderr, "dbsync 失败:", err)
		os.Exit(1)
	}
}

func run(target string) error {
	if target == "" {
		name, err := testdb.DSNDatabaseName()
		if err != nil {
			return err
		}
		target = name
	}

	db, err := testdb.OpenDB(target)
	if err != nil {
		return fmt.Errorf("连接目标库 %s 失败（库不存在时请先创建）: %w", target, err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	if err := testdb.MigrateDB(db); err != nil {
		return err
	}
	fmt.Println("✔ 表结构已同步:", target, "(sql/*.sql 幂等应用)")
	return nil
}
