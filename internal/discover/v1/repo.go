package v1

import (
	"context"
	"fmt"

	postv1 "rednote/gen/go/post/v1"
	"rednote/gen/query"
	models "rednote/internal/discover/models"
	"rednote/packages/defaults"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// FanoutRepo 收件箱（fanout_timelines）读写仓库接口（推拉结合的 push 路径，gorm 实现见 fanoutRepo）。
// 一行 = 一名粉丝收件箱里的一条帖子引用；游标口径与 pull 路径共享
// (post_created_at, post_id)（即帖子的 (created_at, id)），2 路归并按 post_id 去重。
// 写入方为扇出消费（普通作者发帖 → 扇出粉丝收件箱），读取方为关注流 push 路径。
// 不做删帖级联删除：已删帖行留在收件箱，由下游（BFF 回查组装时过滤）与前端自行过滤，
// 行随保留期清理（按 post_created_at 时间窗）回收。接口便于测试替身/mock。
type FanoutRepo interface {
	// WriteInbox 扇出写扩散：为 followerUIDs 各写一行收件箱。
	// 幂等可安全重投（复合主键 (uid, post_id) ON CONFLICT DO NOTHING）；
	// postCreatedAt 为帖子创建时刻（unix毫秒），作为游标分量写入。
	// 分批插入防单语句过大；空粉丝列表为 no-op
	WriteInbox(ctx context.Context, postID, authorID, postCreatedAt int64, followerUIDs []int64) error
	// ReadInbox 收件箱 keyset 分页：返回严格早于游标 (beforeTS, beforeID) 的至多 limit 行，
	// 按 (post_created_at, post_id) 倒序（新帖在前）；首页游标传 (0, 0)。
	// 游标为帖子的 (created_at, id)，与 pull 路径共享同一游标空间
	ReadInbox(ctx context.Context, uid, beforeTS, beforeID int64, limit int) ([]models.FanoutTimeline, error)
	// CleanupExpired 保留期清理：删除 post_created_at 严格早于 cutoffMillis 的行，返回删除总数。
	// 按批循环（ctid 批删，批间独立提交防长锁）；无过期行即返回，ctx 取消即中止。
	// 多实例并发安全（删除幂等，重复执行只是空转）；不做删帖级联删除，已删帖行同样随保留期回收
	CleanupExpired(ctx context.Context, cutoffMillis int64, batch int) (int64, error)
}

// fanoutRepo FanoutRepo 的实现（gorm gen 查询见 gen/query；
// CleanupExpired 的 ctid 批删为唯一保留的原生 SQL：ctid 是 pg 系统列不在 gen 模型，
// 且 DELETE 无 LIMIT，ctid 子查询批删是防长锁的刻意优化）
type fanoutRepo struct {
	db *gorm.DB
	q  *query.Query
}

// NewFanoutRepo 创建收件箱仓库
func NewFanoutRepo(db *gorm.DB) FanoutRepo {
	return &fanoutRepo{db: db, q: query.Use(db)}
}

// HotRepo 热池候选查询仓库接口（维护任务算分输入，gorm gen 查询见 gen/query）。
// 候选 = posts 表统计列（互动汇总事实，同事务维护）+ 时间窗 + 状态过滤
type HotRepo interface {
	// ListHotCandidates 窗口候选：status=PUBLISHED 且 created_at 严格晚于 cutoffMillis 的帖子
	// （含四统计列，按 id 升序稳定输出）；候选有界（窗口 + 发帖量），单次全量返回
	ListHotCandidates(ctx context.Context, cutoffMillis int64) ([]models.HotCandidate, error)
}

// hotRepo HotRepo 的 gorm gen 实现
type hotRepo struct {
	q *query.Query
}

// NewHotRepo 创建热池候选仓库
func NewHotRepo(db *gorm.DB) HotRepo {
	return &hotRepo{q: query.Use(db)}
}

// ListHotCandidates 窗口候选查询（PUBLISHED + 时间窗，含统计列）
func (r *hotRepo) ListHotCandidates(ctx context.Context, cutoffMillis int64) ([]models.HotCandidate, error) {
	rows, err := r.q.PostDetail.WithContext(ctx).
		Select(
			r.q.PostDetail.ID,
			r.q.PostDetail.AuthorID,
			r.q.PostDetail.LikeCount,
			r.q.PostDetail.FavouriteCount,
			r.q.PostDetail.CommentCount,
			r.q.PostDetail.ViewCount,
			r.q.PostDetail.CreatedAt,
		).
		Where(
			r.q.PostDetail.Status.Eq(int32(postv1.PostStatus_POST_STATUS_PUBLISHED)),
			r.q.PostDetail.CreatedAt.Gt(cutoffMillis),
		).
		Order(r.q.PostDetail.ID).
		Find()
	if err != nil {
		return nil, fmt.Errorf("hot candidates: %w", err)
	}

	out := make([]models.HotCandidate, 0, len(rows))
	for _, p := range rows {
		out = append(out, models.HotCandidate{
			PostID:         p.ID,
			AuthorID:       p.AuthorID,
			LikeCount:      p.LikeCount,
			FavouriteCount: p.FavouriteCount,
			CommentCount:   p.CommentCount,
			ViewCount:      p.ViewCount,
			CreatedAt:      p.CreatedAt,
		})
	}
	return out, nil
}

func (r *fanoutRepo) WriteInbox(ctx context.Context, postID, authorID, postCreatedAt int64, followerUIDs []int64) error {
	if len(followerUIDs) == 0 {
		return nil
	}
	rows := make([]*models.FanoutTimeline, 0, len(followerUIDs))
	for _, uid := range followerUIDs {
		rows = append(rows, &models.FanoutTimeline{UID: uid, PostID: postID, AuthorID: authorID, PostCreatedAt: postCreatedAt})
	}
	// 分批插入防单语句过大（复合主键 (uid, post_id) ON CONFLICT DO NOTHING 幂等可重投）
	for start := 0; start < len(rows); start += 500 {
		end := min(start+500, len(rows))
		if err := r.q.FanoutTimeline.WithContext(ctx).
			Clauses(clause.OnConflict{DoNothing: true}).
			Create(rows[start:end]...); err != nil {
			return fmt.Errorf("fanout write: %w", err)
		}
	}
	return nil
}

func (r *fanoutRepo) ReadInbox(ctx context.Context, uid, beforeTS, beforeID int64, limit int) ([]models.FanoutTimeline, error) {
	if limit <= 0 || limit > defaults.MaxLimit {
		limit = defaults.CursorLimit
	}
	do := r.q.FanoutTimeline.WithContext(ctx)
	do = do.Where(r.q.FanoutTimeline.UID.Eq(uid))
	if beforeTS > 0 || beforeID > 0 {
		// keyset 元组过滤：严格早于游标 (beforeTS, beforeID)。
		// gen 分组：内层用独立新链（mk() 每次 WithContext 新链）合成 (a OR (b AND c)) 子句，
		// 不可自引用展开后的 do（共享 statement 会污染条件）
		ft := r.q.FanoutTimeline
		mk := func() query.IFanoutTimelineDo { return ft.WithContext(ctx) }
		do = do.Where(
			mk().Where(ft.PostCreatedAt.Lt(beforeTS)).
				Or(mk().Where(ft.PostCreatedAt.Eq(beforeTS)).Where(ft.PostID.Lt(beforeID))),
		)
	}
	rows, err := do.
		Order(r.q.FanoutTimeline.PostCreatedAt.Desc(), r.q.FanoutTimeline.PostID.Desc()).
		Limit(limit).
		Find()
	if err != nil {
		return nil, fmt.Errorf("fanout read: %w", err)
	}
	out := make([]models.FanoutTimeline, 0, len(rows))
	for _, p := range rows {
		out = append(out, *p)
	}
	return out, nil
}

func (r *fanoutRepo) CleanupExpired(ctx context.Context, cutoffMillis int64, batch int) (int64, error) {
	if batch <= 0 {
		batch = defaults.FanoutCleanupBatch
	}
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, fmt.Errorf("fanout cleanup: %w", err)
		}
		res := r.db.WithContext(ctx).Exec(`
			DELETE FROM fanout_timelines
			WHERE ctid IN (
				SELECT ctid FROM fanout_timelines WHERE post_created_at < ? LIMIT ?
			)`, cutoffMillis, batch)
		if res.Error != nil {
			return total, fmt.Errorf("fanout cleanup: %w", res.Error)
		}
		total += res.RowsAffected
		if res.RowsAffected < int64(batch) {
			return total, nil
		}
	}
}
