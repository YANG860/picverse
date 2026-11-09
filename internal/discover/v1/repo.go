package v1

import (
	"context"
	"fmt"

	models "rednote/internal/discover/models"
	"rednote/packages/defaults"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// FanoutRepo 收件箱（fanout_timelines）读写仓库接口（推拉结合的 push 路径，gorm 实现见 fanoutRepo）。
// 一行 = 一名粉丝收件箱里的一条帖子引用；游标口径与 pull 路径共享
// (post_created_at, post_id)（即帖子的 (created_at, id)），2 路归并按 post_id 去重。
// 写入方为扇出消费（普通作者发帖 → 扇出粉丝收件箱），读取方为关注流 push 路径（P1）。
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

// fanoutRepo FanoutRepo 的 gorm 实现
type fanoutRepo struct {
	db *gorm.DB
}

// NewFanoutRepo 创建收件箱仓库
func NewFanoutRepo(db *gorm.DB) FanoutRepo {
	return &fanoutRepo{db: db}
}

func (r *fanoutRepo) WriteInbox(ctx context.Context, postID, authorID, postCreatedAt int64, followerUIDs []int64) error {
	if len(followerUIDs) == 0 {
		return nil
	}
	rows := make([]models.FanoutTimeline, 0, len(followerUIDs))
	for _, uid := range followerUIDs {
		rows = append(rows, models.FanoutTimeline{UID: uid, PostID: postID, AuthorID: authorID, PostCreatedAt: postCreatedAt})
	}
	if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(rows, 500).Error; err != nil {
		return fmt.Errorf("fanout write: %w", err)
	}
	return nil
}

func (r *fanoutRepo) ReadInbox(ctx context.Context, uid, beforeTS, beforeID int64, limit int) ([]models.FanoutTimeline, error) {
	if limit <= 0 || limit > defaults.MaxLimit {
		limit = defaults.CursorLimit
	}
	q := r.db.WithContext(ctx).Where("uid = ?", uid)
	if beforeTS > 0 || beforeID > 0 {
		q = q.Where("(post_created_at < ? OR (post_created_at = ? AND post_id < ?))", beforeTS, beforeTS, beforeID)
	}
	var rows []models.FanoutTimeline
	if err := q.Order("post_created_at DESC, post_id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("fanout read: %w", err)
	}
	return rows, nil
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
