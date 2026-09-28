package db

import (
	"rednote/config"
	"rednote/packages/logger"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func NewPG(cfg *config.Config, lf logger.LoggerFactory) (*gorm.DB, func()) {
	log := lf.GetLoggerOf("gorm")

	db, err := gorm.Open(postgres.Open(cfg.Database.PG.URL), &gorm.Config{
		Logger: NewSlogAdapter(log),
		// 把驱动层错误翻译为 gorm.ErrDuplicatedKey 等语义化错误，
		// 供业务层 errors.Is 判断（如注册时的邮箱重复）
		TranslateError: true,
	})

	if err != nil {
		panic(err)
	}

	sqlConn, err := db.DB()
	if err != nil {
		panic(err)
	}

	return db, func() {
		_ = sqlConn.Close()
	}
}
