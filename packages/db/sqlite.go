package db

import (
	"rednote/packages/logger"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// NewSQLite 创建 SQLite 数据库连接（用于本地测试）
// dbPath: 数据库文件路径，例如 ":memory:" 或 "./test.db"
func NewSQLite(dbPath string, lf logger.LoggerFactory) *gorm.DB {
	log := lf.GetLoggerOf("gorm")

	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: NewSlogAdapter(log),
		// 与 PG 连接保持一致：翻译驱动错误为 gorm.ErrDuplicatedKey 等语义化错误
		TranslateError: true,
	})

	if err != nil {
		panic(err)
	}

	return db
}

// NewSQLiteMemory 创建内存 SQLite 数据库（用于单元测试）
func NewSQLiteMemory(lf logger.LoggerFactory) *gorm.DB {
	return NewSQLite(":memory:", lf)
}
