package discover

// BigVAuthor 大V快照行（discover_bigv_authors，uid 主键）：
// DemotedAt 为 NULL（Go 侧 nil）= 现任大V；降级行保留宽限期供 pull 兜底（宁重不漏）。
// 表维护（晋升/降级/清理）在发布帖子时判定（P1，随 fanout 链路落地），读侧见 internal/discover/v1/repo.go。
type BigVAuthor struct {
	UID          int64  `gorm:"column:uid;primaryKey;autoIncrement:false;comment:用户ID(雪花)"`
	BecameBigVAt int64  `gorm:"column:became_bigv_at;not null;default:0;comment:本次晋升时刻(unix毫秒)"`
	DemotedAt    *int64 `gorm:"column:demoted_at;comment:降级时刻(unix毫秒);NULL=现任大V"`
	Followers    int64  `gorm:"column:followers;not null;default:0;comment:判定时粉丝数快照"`
	CreatedAt    int64  `gorm:"column:created_at;autoCreateTime:mili;comment:创建时间(unix毫秒)"`
	UpdatedAt    int64  `gorm:"column:updated_at;autoUpdateTime:mili;comment:更新时间(unix毫秒)"`
}

// TableName 指定表名
func (BigVAuthor) TableName() string {
	return "discover_bigv_authors"
}

// FanoutTimeline 收件箱行（fanout_timelines，复合主键 (uid, post_id)）：
// 一名粉丝收件箱里的一条帖子引用——推拉结合的 push 路径（名单外作者发帖扇出粉丝收件箱，
// 名单内大V由 pull 现拉不写扩散）。游标口径与 pull 路径共享 (post_created_at, post_id)
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
