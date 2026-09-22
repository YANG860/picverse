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
