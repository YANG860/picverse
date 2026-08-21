package db

import (
	"rednote/config"
	"rednote/packages/common/logger"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func NewPG(cfg *config.Config, lf *logger.LoggerFactory) *gorm.DB {
	log := lf.GetLoggerOf("gorm")

	db, err := gorm.Open(postgres.Open(cfg.Database.PG.URL), &gorm.Config{
		Logger: NewSlogAdapter(log),
		
	})

	if err != nil {
		panic(err)
	}

	return db
}
