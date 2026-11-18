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

	// Medias 帖子关联的媒体，foreign key 为 PostMedia.PostID，供 Preload/Joins 加载。
	// 顺序即展示顺序（按 PostMedia.SortOrder 升序加载），**只读字段**：禁止重排序、
	// 禁止改写元素字段或 MetaData。读取结果可能来自缓存或并发合并回源（跨请求共享），
	// 就地修改会污染其它并发请求（且顺序错乱不可复现）。
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

// PostStat 帖子统计：统计列子集（轻路径 GetPostStat 的返回，缓存层统计块的实体）
type PostStat struct {
	// ID 帖子ID
	ID int64 `json:"id"`
	// 点赞数
	LikeCount int64 `json:"like_count"`
	// 收藏数
	FavouriteCount int64 `json:"favourite_count"`
	// 评论数
	CommentCount int64 `json:"comment_count"`
	// 浏览数
	ViewCount int64 `json:"view_count"`
}

func (s *PostStat) ToProto() *postv1.PostStat {
	if s == nil {
		return nil
	}
	return &postv1.PostStat{
		LikeCount:      s.LikeCount,
		FavouriteCount: s.FavouriteCount,
		CommentCount:   s.CommentCount,
		ViewCount:      s.ViewCount,
	}
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

// PostLike 帖子点赞明细
// 一行 = 一个用户对一个帖子的点赞；明细为准，like_count = 该帖的行数
// 同事务维护 posts.like_count（强一致），唯一约束 uk_post_likes_post_user 幂等锚点

type PostLike struct {
	common.BaseModel

	// PostID 帖子ID
	PostID int64 `gorm:"type:bigint;not null;uniqueIndex:uk_post_likes_post_user,priority:1;index:idx_post_likes_user;comment:帖子ID" json:"post_id"`
	// UserID 点赞者ID
	UserID int64 `gorm:"type:bigint;not null;uniqueIndex:uk_post_likes_post_user,priority:2;comment:点赞者ID" json:"user_id"`
}

// TableName 指定表名
func (PostLike) TableName() string {
	return "post_likes"
}

// PostFavourite 帖子收藏明细
// 与 PostLike 同构；明细为准，favourite_count = 该帖的行数

type PostFavourite struct {
	common.BaseModel

	// PostID 帖子ID
	PostID int64 `gorm:"type:bigint;not null;uniqueIndex:uk_post_favourites_post_user,priority:1;index:idx_post_favourites_user;comment:帖子ID" json:"post_id"`
	// UserID 收藏者ID
	UserID int64 `gorm:"type:bigint;not null;uniqueIndex:uk_post_favourites_post_user,priority:2;comment:收藏者ID" json:"user_id"`
}

// TableName 指定表名
func (PostFavourite) TableName() string {
	return "post_favourites"
}

// PostComment 帖子评论（一级评论 + 楼内回复同表，两级楼中楼）
// 层级口径：一级评论 RootID=0（哨兵，ToProto 转为自身 id）、ParentID=0；
// 回复 RootID=楼层 id、ParentID=被回复评论 id（回复的回复也平级挂同一楼层）、ReplyToUserID=被回复者；
// 明细为准，comment_count = 该帖 status=NORMAL 的行数（一级 + 回复），同事务维护 posts.comment_count（强一致）；
// 软删 status=DELETED，删一级评论级联软删整楼，按实际删除行数回收计数
type PostComment struct {
	common.BaseModel

	// PostID 所属帖子ID
	PostID int64 `gorm:"type:bigint;not null;index:idx_post_comments_post_root,priority:1;comment:帖子ID" json:"post_id"`
	// UserID 评论者ID
	UserID int64 `gorm:"type:bigint;not null;index:idx_post_comments_user;comment:评论者ID" json:"user_id"`
	// RootID 楼层ID（一级评论=0，回复=顶层评论ID）
	RootID int64 `gorm:"type:bigint;not null;default:0;index:idx_post_comments_post_root,priority:2;index:idx_post_comments_root,priority:1;comment:楼层ID" json:"root_id"`
	// ParentID 被回复评论ID（一级评论=0）
	ParentID int64 `gorm:"type:bigint;not null;default:0;comment:被回复评论ID" json:"parent_id"`
	// ReplyToUserID 被回复者ID（一级评论=0）
	ReplyToUserID int64 `gorm:"type:bigint;not null;default:0;comment:被回复者ID" json:"reply_to_user_id"`
	// Content 评论内容（≤500 rune，utils.ValidateComment 校验）
	Content string `gorm:"type:varchar(512);not null;default:'';comment:评论内容" json:"content"`
	// Status 评论状态
	Status postv1.CommentStatus `gorm:"type:integer;not null;default:0;comment:评论状态" json:"status"`
	// ReplyCount 楼层回复数（仅一级评论有意义，回复恒 0）
	ReplyCount int64 `gorm:"type:bigint;not null;default:0;comment:楼层回复数" json:"reply_count"`
}

// TableName 指定表名
func (PostComment) TableName() string {
	return "post_comments"
}

// ToProto 转 proto：RootID 哨兵 0 转为楼层 id（一级评论 = 自身 id），ParentID/ReplyToUserID 0 转空串
func (c *PostComment) ToProto() *postv1.Comment {
	if c == nil {
		return nil
	}

	rootID := c.RootID
	if rootID == 0 {
		// 一级评论：楼层即自身
		rootID = c.ID
	}
	var parentID, replyToUserID string
	if c.ParentID != 0 {
		parentID = snowflake.ParseInt64(c.ParentID).String()
	}
	if c.ReplyToUserID != 0 {
		replyToUserID = snowflake.ParseInt64(c.ReplyToUserID).String()
	}

	return &postv1.Comment{
		CommentId:     snowflake.ParseInt64(c.ID).String(),
		PostId:        snowflake.ParseInt64(c.PostID).String(),
		RootId:        snowflake.ParseInt64(rootID).String(),
		ParentId:      parentID,
		UserId:        snowflake.ParseInt64(c.UserID).String(),
		ReplyToUserId: replyToUserID,
		Content:       c.Content,
		ReplyCount:    c.ReplyCount,
		CreatedAt:     strconv.FormatInt(c.CreatedAt, 10),
	}
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
