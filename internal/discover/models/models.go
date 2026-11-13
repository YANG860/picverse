package discover

// FanoutTimeline 收件箱行（fanout_timelines，复合主键 (uid, post_id)）：
// 一名粉丝收件箱里的一条帖子引用——推拉结合的 push 路径（名单外作者发帖扇出粉丝收件箱，
// 名单内大V由 pull 现拉不写扩散；判据 = 发件箱缓存存在性，见 doc/internal/discover/discover.md）。
// 游标口径与 pull 路径共享 (post_created_at, post_id)
// （即帖子的 (created_at, id)），2 路归并按 PostID 去重（宁重不漏）。
// 写入（扇出消费）与读取（关注流 push 路径）见 internal/discover/v1/repo.go。
type FanoutTimeline struct {
	UID           int64 `gorm:"column:uid;primaryKey;autoIncrement:false;comment:收件人(粉丝)ID(雪花)"`
	PostID        int64 `gorm:"column:post_id;primaryKey;autoIncrement:false;comment:帖子ID(雪花)"`
	AuthorID      int64 `gorm:"column:author_id;not null;default:0;comment:作者ID(冗余：排查/运维辅助)"`
	PostCreatedAt int64 `gorm:"column:post_created_at;not null;default:0;comment:帖子创建时刻(unix毫秒)，游标分量"`
	CreatedAt     int64 `gorm:"column:created_at;autoCreateTime:mili;comment:创建时间(unix毫秒)"`
	UpdatedAt     int64 `gorm:"column:updated_at;autoUpdateTime:mili;comment:更新时间(unix毫秒)"`
}

// TableName 指定表名
func (FanoutTimeline) TableName() string {
	return "fanout_timelines"
}

// HotCandidate 热池候选（posts 表：PUBLISHED + 时间窗 + 统计列）：
// 热度分算分输入（统计列即互动汇总事实，无需互动事件链路；见 doc/internal/discover/plan_recommend_hot.md）。
type HotCandidate struct {
	PostID         int64 `gorm:"column:id"`
	AuthorID       int64 `gorm:"column:author_id"`
	LikeCount      int64 `gorm:"column:like_count"`
	FavouriteCount int64 `gorm:"column:favourite_count"`
	CommentCount   int64 `gorm:"column:comment_count"`
	ViewCount      int64 `gorm:"column:view_count"`
	CreatedAt      int64 `gorm:"column:created_at"` // unix 毫秒
}
