package meili

import (
	"fmt"
	"strconv"

	ms "github.com/meilisearch/meilisearch-go"
)

// PostIndexDoc posts 搜索索引文档（schema 见 doc/design/discover.md §5.1）：
// 主键 post_id；仅 PUBLISHED 入列；created_at 为 unix 毫秒（"最新"排序键）。
// 纯值类型（不依赖业务 proto），入库调用方以 map[string]any 提供字段后经 NewPostIndexDoc 组装。
type PostIndexDoc struct {
	PostID         string `json:"post_id"`
	Title          string `json:"title"`
	FullText       string `json:"full_text"`
	AuthorID       string `json:"author_id"`
	CreatedAt      int64  `json:"created_at"`
	LikeCount      int64  `json:"like_count"`
	FavouriteCount int64  `json:"favourite_count"`
	CommentCount   int64  `json:"comment_count"`
	ViewCount      int64  `json:"view_count"`
}

// PostsIndexPrimaryKey posts 索引主键字段名（与 PostIndexDoc.PostID 的 json tag 一致）
const PostsIndexPrimaryKey = "post_id"

// PostsIndexSettings posts 索引设置：
// searchableAttributes 限定检索面（标题+正文）；sortableAttributes 支撑"最新"模式排序
// （created_at 主序 + post_id 同毫秒 tiebreaker，二者都必须声明才能进 sort，见 doc/design/discover.md §5.2）
func PostsIndexSettings() *ms.Settings {
	return &ms.Settings{
		SearchableAttributes: []string{"title", "full_text"},
		SortableAttributes:   []string{"created_at", "post_id"},
	}
}

// NewPostIndexDoc 从字段集合组装 posts 索引文档（postID 为消息侧 canonical ID，与索引主键一致）。
// fields 约定键：title / full_text / author_id / created_at（unix 毫秒字符串，全局约定）/
// like_count / favourite_count / comment_count / view_count（int64）。
// created_at 非法时降级 0 并返回 error 供调用方 Warn 记录（不阻塞索引链路）。
func NewPostIndexDoc(postID string, fields map[string]any) (PostIndexDoc, error) {
	doc := PostIndexDoc{
		PostID:         postID,
		Title:          strOf(fields["title"]),
		FullText:       strOf(fields["full_text"]),
		AuthorID:       strOf(fields["author_id"]),
		LikeCount:      intOf(fields["like_count"]),
		FavouriteCount: intOf(fields["favourite_count"]),
		CommentCount:   intOf(fields["comment_count"]),
		ViewCount:      intOf(fields["view_count"]),
	}

	var err error
	doc.CreatedAt, err = strconv.ParseInt(strOf(fields["created_at"]), 10, 64)
	if err != nil {
		doc.CreatedAt = 0
		err = fmt.Errorf("invalid created_at %v: %w", fields["created_at"], err)
	}
	return doc, err
}

// strOf 字段取字符串（非 string 类型降级空串）
func strOf(v any) string {
	s, _ := v.(string)
	return s
}

// intOf 字段取整数（支持 int / int64 / int32，其余降级 0）
func intOf(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case int32:
		return int64(n)
	}
	return 0
}
