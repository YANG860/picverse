package post

import (
	commonv1 "rednote/gen/go/common/v1"
	mediav1 "rednote/gen/go/media/v1"
	postv1 "rednote/gen/go/post/v1"
	common "rednote/internal/common/models"
	media "rednote/internal/media/models"
	"strconv"

	"github.com/bwmarrin/snowflake"
	"gorm.io/datatypes"
)

// PostDetail 帖子
// 草稿与已发布帖子同表，以 Status 区分
type PostDetail struct {
	common.BaseModel

	// 作者
	AuthorID int64 `gorm:"type:bigint;not null;index;comment:作者ID" json:"author_id"`

	// 内容
	Title    string `gorm:"type:varchar(256);not null;default:'';comment:标题" json:"title"`
	FullText string `gorm:"type:text;not null;default:'';comment:正文" json:"full_text"`

	// 状态
	Status postv1.PostStatus `gorm:"type:integer;not null;default:0;comment:帖子状态" json:"status"`

	// 统计
	LikeCount      int64 `gorm:"type:bigint;not null;default:0;comment:点赞数" json:"like_count"`
	FavouriteCount int64 `gorm:"type:bigint;not null;default:0;comment:收藏数" json:"favourite_count"`
	CommentCount   int64 `gorm:"type:bigint;not null;default:0;comment:评论数" json:"comment_count"`
	ViewCount      int64 `gorm:"type:bigint;not null;default:0;comment:浏览数" json:"view_count"`

	// 元数据
	MetaData datatypes.JSONMap `gorm:"type:jsonb;not null;default:'{}';comment:元数据" json:"meta_data"`

	// Medias 帖子关联的媒体，foreign key 为 PostMedia.PostID，供 Preload/Joins 加载
	Medias []*PostMedia `gorm:"foreignKey:PostID;references:ID"`
}

// TableName 指定表名
func (PostDetail) TableName() string {
	return "posts"
}

// PostStatUpdate 单条帖子统计更新入参（预留类型，当前未被引用；SyncPostStat 已改为逐字段更新）
// 用结构体切片替代 postIDs/stats 双平行切片，防止两组参数错位
type PostStatUpdate struct {
	PostID int64
	Stat   *postv1.PostStat
}

// PostMedia 帖子与媒体的关联
// 关联粒度：媒体文件（MediaFile 嵌入列，前缀 media_）
type PostMedia struct {
	common.BaseModel

	// 所属帖子ID
	PostID int64 `gorm:"type:bigint;not null;index;comment:帖子ID" json:"post_id"`

	// 关联媒体文件 嵌入
	Media *media.MediaFile `gorm:"embedded;embeddedPrefix:media_"`

	// 展示顺序
	SortOrder int32 `gorm:"type:integer;not null;default:0;comment:展示顺序"`
}

// TableName 指定表名
func (PostMedia) TableName() string {
	return "post_medias"
}

func (p *PostDetail) ToBrief() *postv1.PostBrief {
	if p == nil {
		return nil
	}

	var cover *media.MediaFile
	if len(p.Medias) > 0 {
		cover = p.Medias[0].Media
	}
	return &postv1.PostBrief{
		PostId: snowflake.ParseInt64(p.ID).String(),
		Title:  p.Title,
		// proto 契约字段：author_id/created_at 必须回填，调用方依赖它们定位与展示
		AuthorId:  snowflake.ParseInt64(p.AuthorID).String(),
		CreatedAt: strconv.FormatInt(p.CreatedAt, 10),
		Status:    p.Status,
		Stat: &postv1.PostStat{
			LikeCount:      p.LikeCount,
			FavouriteCount: p.FavouriteCount,
			CommentCount:   p.CommentCount,
			ViewCount:      p.ViewCount,
		},
		Cover: cover.ToProto(),
	}
}

func BuildPost(post *PostDetail, medias []*media.MediaFile) *postv1.Post {
	m := make([]*mediav1.MediaFile, 0, len(medias))
	for _, v := range medias {
		m = append(m, v.ToProto())
	}

	jsonData, _ := post.MetaData.MarshalJSON()

	return &postv1.Post{
		PostId:    snowflake.ParseInt64(post.ID).String(),
		CreatedAt: strconv.FormatInt(post.CreatedAt, 10),
		UpdatedAt: strconv.FormatInt(post.UpdatedAt, 10),
		Medias:    m,
		Title:     post.Title,
		FullText:  post.FullText,
		AuthorId:  snowflake.ParseInt64(post.AuthorID).String(),
		PostStat: &postv1.PostStat{
			LikeCount:      post.LikeCount,
			FavouriteCount: post.FavouriteCount,
			CommentCount:   post.CommentCount,
			ViewCount:      post.ViewCount,
		},
		Metadata: &commonv1.JsonStr{Payload: string(jsonData)},
		Status:   post.Status,
	}
}

func (p *PostDetail) ToProto() *postv1.Post {
	medias := make([]*mediav1.MediaFile, 0, len(p.Medias))
	for _, v := range p.Medias {
		medias = append(medias, v.Media.ToProto())
	}

	jsonData, _ := p.MetaData.MarshalJSON()

	return &postv1.Post{
		PostId:    snowflake.ParseInt64(p.ID).String(),
		CreatedAt: strconv.FormatInt(p.CreatedAt, 10),
		UpdatedAt: strconv.FormatInt(p.UpdatedAt, 10),
		Medias:    medias,
		Title:     p.Title,
		FullText:  p.FullText,
		AuthorId:  snowflake.ParseInt64(p.AuthorID).String(),
		PostStat: &postv1.PostStat{
			LikeCount:      p.LikeCount,
			FavouriteCount: p.FavouriteCount,
			CommentCount:   p.CommentCount,
			ViewCount:      p.ViewCount,
		},
		Metadata: &commonv1.JsonStr{Payload: string(jsonData)},
		Status:   p.Status,
	}
}
