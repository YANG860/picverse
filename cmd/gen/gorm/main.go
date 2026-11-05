package main

import (
	"path/filepath"
	"runtime"

	"gorm.io/gen"

	auth "rednote/internal/auth/models"
	common "rednote/internal/common/models"
	media "rednote/internal/media/models"
	post "rednote/internal/post/models"
	user "rednote/internal/user/models"
)

// outPath 生成目录（gen/query）的绝对路径：
// 用 runtime.Caller 锚定本文件所在目录（cmd/gen/gorm），不依赖工作目录，
// 任意位置 go run ./cmd/gen/gorm 均可跑（此前 OutPath 为相对路径，换目录即写错位置）
func outPath() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "../../../gen/query")
}

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
		OutPath:       outPath(),
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
		&common.ProcessedMessage{},
		&auth.UserCredential{},
		&post.PostDetail{},
		&post.PostMedia{},
		&post.PostLike{},
		&post.PostFavourite{},
		&user.UserProfile{},
		&user.UserFollow{},
	)

	// 生成代码
	g.Execute()
}
