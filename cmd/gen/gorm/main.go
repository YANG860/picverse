package main

import (
	"gorm.io/gen"

	common "rednote/internal/common/models"
	media "rednote/internal/media/models"
)

func main() {
	/* // 从环境变量获取数据库连接信息
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		dsn = "postgres://postgres:postgres@localhost:5432/rednote?sslmode=disable"
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		panic("failed to connect database: " + err.Error())
	} */

	// 创建代码生成器
	g := gen.NewGenerator(gen.Config{
		OutPath:       "../../../gen/query",
		Mode:          gen.WithDefaultQuery | gen.WithQueryInterface,
		FieldNullable: true,
	})

	/* g.UseDB(db) */

	// 注册模型
	g.ApplyBasic(
		// Media 模块
		&media.MediaAsset{},
		&media.MediaFile{},
		&common.BaseModel{},
		&common.OutBoxEntry{},
	)

	// 生成代码
	g.Execute()
}
