package v1

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"rednote/config"
	models "rednote/internal/discover/models"
	"rednote/packages/defaults"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// bigVSnapshot 不可变快照：pull 现拉名单（集合成员 = 有快照行的 uid，含宽限期降级者），
// 读路径无锁（atomic 整体替换，永不原地修改）
type bigVSnapshot struct {
	pullSet  map[int64]struct{}
	syncedAt time.Time // 本快照的回源时刻；零值 = 尚未同步（触发首次回源）
}

// isStale 是否超过惰性同步间隔（须回源）
func (s *bigVSnapshot) isStale(interval time.Duration) bool {
	return s.syncedAt.IsZero() || time.Since(s.syncedAt) >= interval
}

// BigVRepo 大V快照读取仓库（模型见 internal/discover/models）：惰性内存快照——
// 距上次回源超过 sync_interval 的读触发一次全量回源，否则读原子快照；
// 外部视角 ≈ 稍微滞后（<= sync_interval）的无感直连 DB 查询。
// 使用模式：先拿关注列表与名单求交集分流——命中走 pull 现拉、未命中走收件箱（fanout_timelines）。
// 并发语义：首次回源完成前读阻塞等待（空快照不是滞后数据，当作"无大V"会错判分流）；
// 之后读路径无锁且永不阻塞（回源期间读旧快照，正合"稍微滞后"语义），
// 过期读用 TryLock 抢回源权（并发过期读只放行一个回源，零排队）；
// 回源失败不换快照、不推进同步点（错误只给触发者、下次读重试），失败语义与直连一致。
type BigVRepo struct {
	db       *gorm.DB
	interval time.Duration

	snap atomic.Pointer[bigVSnapshot] // 当前快照（不可变，回源后整体替换）

	syncMu sync.Mutex // 回源互斥（读以 TryLock 抢回源权，抢不到不阻塞）
}

// NewBigVRepo 创建大V快照仓库（sync_interval 为 0 使用默认 1m）
func NewBigVRepo(db *gorm.DB, cfg *config.Config) *BigVRepo {
	interval := cfg.BigV.SyncInterval
	if interval <= 0 {
		interval = defaults.BigVSyncInterval
	}
	r := &BigVRepo{db: db, interval: interval}
	r.snap.Store(&bigVSnapshot{pullSet: map[int64]struct{}{}})
	return r
}

// IsInPullSet 判定 uid 是否在 pull 现拉名单（现任大V + 宽限期降级者）：
// 在名单内 → 发帖不写扩散、由 pull 现拉（宁重不漏）；不在 → 走收件箱写扩散。
// 发布时判定（P1）的 fanout 跳过判定用
func (r *BigVRepo) IsInPullSet(ctx context.Context, uid int64) (bool, error) {
	if err := r.refresh(ctx); err != nil {
		return false, err
	}
	_, ok := r.snap.Load().pullSet[uid]
	return ok, nil
}

// JoinPullSet 取 uids 与 pull 现拉名单的交集（保持输入顺序）：
// 命中走 pull 现拉，未命中即走收件箱——关注流组装时按此分流
func (r *BigVRepo) JoinPullSet(ctx context.Context, uids []int64) ([]int64, error) {
	if err := r.refresh(ctx); err != nil {
		return nil, err
	}
	set := r.snap.Load().pullSet
	out := make([]int64, 0, len(uids))
	for _, uid := range uids {
		if _, ok := set[uid]; ok {
			out = append(out, uid)
		}
	}
	return out, nil
}

// refresh 惰性回源：快照未过期零开销返回；过期则抢回源权（syncMu 只保护回源动作，不挡读）。
// 首次回源前用阻塞 Lock——空快照当"无大V"会错判分流，必须等到真实数据；
// 已有快照后用 TryLock——抢不到（他 goroutine 正在回源）直接用旧快照返回，读不被回源拖慢
func (r *BigVRepo) refresh(ctx context.Context) error {
	s := r.snap.Load()
	if !s.isStale(r.interval) {
		return nil
	}
	if s.syncedAt.IsZero() {
		r.syncMu.Lock()
	} else if !r.syncMu.TryLock() {
		return nil
	}
	defer r.syncMu.Unlock()
	// double-check：等锁窗口内可能已有别的读完成回源
	if s = r.snap.Load(); !s.isStale(r.interval) {
		return nil
	}
	return r.sync(ctx)
}

// sync 全量回源并原子换快照；失败不换快照、不推进同步点（下次读重试）。
// 名单 = 全部快照行（现任 + 宽限期降级者），只取 uid（成员判定不需要行数据）
func (r *BigVRepo) sync(ctx context.Context) error {
	var uids []int64
	if err := r.db.WithContext(ctx).Model(&models.BigVAuthor{}).Pluck("uid", &uids).Error; err != nil {
		return fmt.Errorf("bigv repo load: %w", err)
	}
	pullSet := make(map[int64]struct{}, len(uids))
	for _, uid := range uids {
		pullSet[uid] = struct{}{}
	}
	r.snap.Store(&bigVSnapshot{pullSet: pullSet, syncedAt: time.Now()})
	return nil
}

// FanoutRepo 收件箱（fanout_timelines）读写仓库：推拉结合的 push 路径。
// 一行 = 一名粉丝收件箱里的一条帖子引用；游标口径与 pull 路径共享
// (post_created_at, post_id)（即帖子的 (created_at, id)），2 路归并按 post_id 去重。
// 写入方为扇出消费（名单外作者发帖 → 扇出粉丝收件箱），读取方为关注流 push 路径（P1）。
// 不做删帖级联删除：已删帖行留在收件箱，由下游（BFF 回查组装时过滤）与前端自行过滤，
// 行随保留期清理（按 post_created_at 时间窗，策略待定）回收。
type FanoutRepo struct {
	db *gorm.DB
}

// NewFanoutRepo 创建收件箱仓库
func NewFanoutRepo(db *gorm.DB) *FanoutRepo {
	return &FanoutRepo{db: db}
}

// WriteInbox 扇出写扩散：为 followers 各写一行收件箱。
// 幂等可安全重投（复合主键 (uid, post_id) ON CONFLICT DO NOTHING）；
// postCreatedAt 为帖子创建时刻（unix毫秒），作为游标分量写入。
// 分批插入防单语句过大；空粉丝列表为 no-op
func (r *FanoutRepo) WriteInbox(ctx context.Context, postID, authorID, postCreatedAt int64, followerUIDs []int64) error {
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

// ReadInbox 收件箱 keyset 分页：返回严格早于游标 (beforeTS, beforeID) 的至多 limit 行，
// 按 (post_created_at, post_id) 倒序（新帖在前）；首页游标传 (0, 0)。
// 游标为帖子的 (created_at, id)，与 pull 路径共享同一游标空间
func (r *FanoutRepo) ReadInbox(ctx context.Context, uid, beforeTS, beforeID int64, limit int) ([]models.FanoutTimeline, error) {
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

// CleanupExpired 保留期清理：删除 post_created_at 严格早于 cutoffMillis 的行，返回删除总数。
// 按批循环（ctid 批删，批间独立提交防长锁）；无过期行即返回，ctx 取消即中止。
// 多实例并发安全（删除幂等，重复执行只是空转）；不做删帖级联删除，已删帖行同样随保留期回收
func (r *FanoutRepo) CleanupExpired(ctx context.Context, cutoffMillis int64, batch int) (int64, error) {
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
