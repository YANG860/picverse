package v1

import (
	"context"
	"fmt"
	"log/slog"
	"maps"

	"rednote/config"
	media "rednote/internal/media/models"
	models "rednote/internal/user/models"
	"rednote/packages/cache"
	"rednote/packages/logger"

	"gorm.io/datatypes"
	"gorm.io/gen"
	"gorm.io/gorm"
)

// CachedUserRepo UserRepository 的缓存装饰器（cache-aside）。key 构造/序列化/空哨兵/TTL 抖动
// 全部收口在 cache 层 typed API，本文件只管“读哪几层、写后失效谁”，与 CachedPostRepo 同一定式。
//
// 缓存范围：GetUserProfiles 的非统计列（user:profile:{id}，单 uid 粒度）。简视图
// （UserBrief）不设独立缓存与查询：与 profile 共用同一条 cache-aside 路径，由 ToBrief() 派生。
//
// 只读契约：返回实体是缓存/回源对象的浅拷贝，Avatar（指针）与 MetaData（map）与共享对象同底
// ——调用方禁止原地修改（见 assembleProfiles 与 models.UserProfile）。
//
// 统计列（followers/followings）分块缓存（user:stat:{id}，短 TTL；stat_ttl < 0 禁用）：
// 计数读写频率与陈旧容忍度都和资料主体不同，且实体入库前计数已清零，故只有资料命中者需要统计列。
//   - 回源即权威：回源行是全列查询、自带计数 → 回源者直接采信，不读也不写 stat 缓存
//     （拿更旧的 stat 副本覆盖手上的精确值没有意义）；
//   - 资料命中者：计数为零值 → stat 短 TTL 读缓存（miss 合并回源 + 回填），写路径（follow/取关）
//     零失效，陈旧 ≤ stat_ttl；空哨兵不用于 stat（stat 存在 ⟺ 行存在，“不存在”由 profile 层裁决）；
//   - 契约：UserRepoImpl.GetUserProfiles 必须返回统计列——改成列裁剪会让回源者计数静默变零。
//
// 一致性（有界陈旧，单实例部署假设，多副本失效广播为后续规划）：并发同 key 回源经
// cache.BatchFlight 合并（profile/stat 各一条 flight）；同一响应内回源者计数精确、命中者
// 陈旧 ≤ stat_ttl；写后 DEL 对应 key（事务内为 commit 前）；事务模式（WithTX）读直穿不缓存；
// 任何缓存错误只 warn 并回源 DB，不失败业务请求。
type CachedUserRepo struct {
	inner UserRepository
	c     cache.UserEntityCache
	log   *slog.Logger

	// txMode 事务绑定模式：读直穿不缓存
	txMode bool

	// bfProfile 合并并发相同资料回源（防击穿：per-key 合并 + 并集批查），fetch 构造时绑定
	bfProfile *cache.BatchFlight[int64, *models.UserProfile]

	// bfStat 合并并发相同统计列回源（同 bfProfile；statCache 禁用时为 nil，读取前须过开关）
	bfStat *cache.BatchFlight[int64, *models.UserStat]

	// statCache 统计列缓存开关（cache.stat_ttl < 0 禁用 → 命中者直通轻查）
	statCache bool
}

var _ UserRepository = (*CachedUserRepo)(nil)

// NewCachedUserRepo 创建用户存储库缓存装饰器。
// TTL/序列化策略由 UserEntityCache 持有（config 其次 defaults），本装饰器不再关心。
// cfg 为 nil 时回源合并器用默认参数（config > defaults 分层在 cache.NewBatchFlight 内建）。
func NewCachedUserRepo(inner *UserRepoImpl, c cache.UserEntityCache, cfg *config.Config, lf logger.LoggerFactory) *CachedUserRepo {
	if inner == nil {
		panic(fmt.Errorf("cache: inner user repo is required"))
	}
	if c == nil {
		panic(fmt.Errorf("cache: cache client is required"))
	}
	r := &CachedUserRepo{
		inner: inner,
		c:     c,
		log:   lf.GetLoggerOf("user.repo.cache"),
		// 统计列短 TTL 读缓存默认启用；stat_ttl < 0 禁用（回退直通轻查）
		statCache: cfg == nil || cfg.Cache.StatTTL >= 0,
	}
	// 回源合并器：fetch 构造时绑定（方法值调用时读 r.inner，TX 克隆不触碰）
	var cc config.CacheConfig
	if cfg != nil {
		cc = cfg.Cache
	}
	r.bfProfile = cache.NewBatchFlight(cc.FlightWorkers, cc.FlightBatchSize, cc.FlightTimeout, r.fetchProfileAndFill)
	// 禁用 stat 缓存时不建 flight：否则常驻空转 worker 且无人收敛
	if r.statCache {
		r.bfStat = cache.NewBatchFlight(cc.FlightWorkers, cc.FlightBatchSize, cc.FlightTimeout, r.fetchStatAndFill)
	}
	return r
}

// WithTX 返回绑定事务的装饰器（txMode）：
// 读直穿不缓存（事务内必须读到自身未提交的写入），写照常登记失效。
func (r *CachedUserRepo) WithTX(tx *gorm.DB) UserRepository {
	clone := *r // 结构体浅拷贝：flight 指针与 outer 共享，配置字段只读
	clone.inner = r.inner.WithTX(tx)
	clone.txMode = true
	return &clone
}

// ==================== 读路径 ====================

// GetUserProfiles 批量获取用户资料（cache-aside，按 uid 粒度缓存非统计列）。
// 统计列分层：回源者采信回源行计数（精确），资料命中者走 stat 短 TTL 读缓存（陈旧 ≤ stat_ttl）。
// 返回顺序按 uids 首次出现顺序去重排列（调用方均按 ID 匹配或单 uid 使用）——保序成本为零
// （order 反正要为 MGET 去重），保留它换来响应确定性（同输入同输出）。
func (r *CachedUserRepo) GetUserProfiles(ctx context.Context, uids []int64) ([]*models.UserProfile, error) {
	// 空入参直通（空集不进缓存）；事务模式直通（须见事务内自身写入）
	if len(uids) == 0 || r.txMode {
		return r.inner.GetUserProfiles(ctx, uids)
	}

	// 按首次出现顺序去重：MGET 不重复取同 key，返回集每 uid 至多一条
	order := cache.DedupeIDs(uids)

	// 1) 读资料缓存（fail-open：缓存故障整批当未命中）
	byID, hitIDs, missIDs := r.readProfileCache(ctx, order)

	// 2) 未命中回源（BatchFlight 合并防击穿）+ 回填。回源行自带统计列 → 计数即权威，
	//    这批 uid 不再进步骤 3（见文件头「回源即权威」）
	if len(missIDs) > 0 {
		fetched, err := r.bfProfile.Do(ctx, missIDs)
		if err != nil {
			return nil, err
		}
		maps.Copy(byID, fetched)
	}

	// 3) 统计列只服务资料命中者（其缓存实体计数为零值）；缓存错误 fail-open、DB 错误上抛
	statByID, err := r.loadStats(ctx, hitIDs)
	if err != nil {
		return nil, err
	}

	// 4) 保序组装（拷贝实体后覆盖统计列）
	return assembleProfiles(order, byID, statByID), nil
}

// readProfileCache 批量读资料缓存（fail-open）：返回命中实体、命中 uid（保持输入序，供统计列取样）
// 与未命中 uid（miss → 回源）。空哨兵命中 = 确认不存在：不进任何集合，也不回填。
func (r *CachedUserRepo) readProfileCache(ctx context.Context, ids []int64) (byID map[int64]*models.UserProfile, hitIDs, missIDs []int64) {
	cached, err := r.c.LoadUserProfiles(ctx, ids)
	if err != nil {
		// 降级全量回源：缓存不可用不得失败业务请求
		r.log.Warn("cache load user profiles failed, fallback to full fetch", "count", len(ids), "error", err)
		return cache.SplitHits[models.UserProfile](ids, nil)
	}
	return cache.SplitHits(ids, cached)
}

// fetchProfileAndFill 资料回源 + 回填（bfProfile fetch，构造时绑定）：fetchIDs 为并集，
// 命中写实体、缺失写空哨兵；只有批执行者回填（合并掉的等待者不重复写）。
func (r *CachedUserRepo) fetchProfileAndFill(ctx context.Context, fetchIDs []int64) (map[int64]*models.UserProfile, error) {
	profiles, err := r.inner.GetUserProfiles(ctx, fetchIDs)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]*models.UserProfile, len(profiles))
	for _, p := range profiles {
		byID[p.ID] = p
	}
	r.fillProfileCache(ctx, fetchIDs, byID)
	return byID, nil
}

// fillProfileCache 回填资料缓存（批量 MSet，一轮 RTT）：命中写实体（cache 层只存非统计列、
// TTL 自带抖动）、缺失写空哨兵（StoreUserProfiles 内部按 TTL 分两批）。
// 回填失败只记 warn：缓存缺失即下次回源，不影响正确性。
func (r *CachedUserRepo) fillProfileCache(ctx context.Context, ids []int64, byID map[int64]*models.UserProfile) {
	hits := make(map[int64]*models.UserProfile, len(byID))
	missing := make([]int64, 0, len(ids))
	for _, id := range ids {
		p, ok := byID[id]
		if !ok {
			missing = append(missing, id)
			continue
		}
		hits[id] = p
	}
	if err := r.c.StoreUserProfiles(ctx, hits, missing); err != nil {
		r.log.Warn("cache store user profiles failed, fallback to next fetch",
			"hits", len(hits), "missing", len(missing), "error", err)
	}
}

// loadStats 统计列读取：只服务资料命中者（回源者计数由回源行提供，不走这里）。
//   - statCache 启用：短 TTL 读缓存 → miss 经 bfStat 合并回源 + 回填（写路径不失效，陈旧 ≤ stat_ttl）；
//   - 禁用（stat_ttl < 0）：DB 轻查直通（历史行为）。
//
// 缓存错误一律 fail-open 降级轻查（warn）；轻查错误是真错误，上抛。
func (r *CachedUserRepo) loadStats(ctx context.Context, ids []int64) (map[int64]*models.UserStat, error) {
	if len(ids) == 0 {
		// 整页回源：统计列已随回源行到位 → 零缓存访问、零轻查
		return nil, nil
	}
	if !r.statCache {
		return r.queryStats(ctx, ids)
	}
	byID, missIDs := r.readStatCache(ctx, ids)
	if len(missIDs) == 0 {
		return byID, nil
	}
	fetched, err := r.bfStat.Do(ctx, missIDs)
	if err != nil {
		return nil, err
	}
	maps.Copy(byID, fetched)
	return byID, nil
}

// readStatCache 批量读统计列缓存（fail-open）：返回命中值与未命中 uid。
// 空哨兵（正常不出现）按无值处理，不回源。
func (r *CachedUserRepo) readStatCache(ctx context.Context, ids []int64) (byID map[int64]*models.UserStat, missIDs []int64) {
	cached, err := r.c.LoadUserStats(ctx, ids)
	if err != nil {
		r.log.Warn("cache load user stats failed, fallback to full query", "count", len(ids), "error", err)
		cached = nil // 降级全量轻查
	}
	byID, _, missIDs = cache.SplitHits(ids, cached)
	return byID, missIDs
}

// queryStats 统计列 DB 轻查（列裁剪：id + 粉丝/关注数）为 uid→stat。
func (r *CachedUserRepo) queryStats(ctx context.Context, ids []int64) (map[int64]*models.UserStat, error) {
	stats, err := r.inner.GetUserStat(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]*models.UserStat, len(stats))
	for _, s := range stats {
		byID[s.ID] = s
	}
	return byID, nil
}

// fetchStatAndFill 统计列回源 + 回填（bfStat fetch，构造时绑定）。
// 只写命中值、不写空哨兵（stat 存在 ⟺ 行存在，“不存在”由 profile 层裁决）；
// 回填走批量口（MSet 一轮 RTT），失败只记 warn（下次回源 refresh）。
func (r *CachedUserRepo) fetchStatAndFill(ctx context.Context, fetchIDs []int64) (map[int64]*models.UserStat, error) {
	byID, err := r.queryStats(ctx, fetchIDs)
	if err != nil {
		return nil, err
	}
	if err := r.c.StoreUserStats(ctx, byID); err != nil {
		r.log.Warn("cache store user stats failed", "count", len(byID), "error", err)
	}
	return byID, nil
}

// assembleProfiles 按 order 保序组装：跳过未命中/空哨兵；统计列以 stat 覆盖。
// 回源者本就带着权威计数（statByID 无此 id，保持原值）；命中者的实体计数是零值，
// 只有 stat 也缺失（极端降级）才会退化成 0，属可接受的降级而非错误。
//
// 只读契约：返回的实体是 byID 对象的浅拷贝（飞行合并窗口内该对象对并发调用方共享），
// 因此 Avatar（指针）与 MetaData（map）与共享对象同底——**禁止调用方原地修改**，
// 否则会与其他并发请求互相污染。
func assembleProfiles(order []int64, byID map[int64]*models.UserProfile, statByID map[int64]*models.UserStat) []*models.UserProfile {
	profiles := make([]*models.UserProfile, 0, len(order))
	for _, id := range order {
		p, ok := byID[id]
		if !ok {
			continue
		}
		out := *p
		if s, ok := statByID[id]; ok {
			out.Followers = s.Followers
			out.Followings = s.Followings
		}
		profiles = append(profiles, &out)
	}
	return profiles
}

// ==================== 写路径（失效登记） ====================

// invalidate 写后失效：DEL 用户资料缓存 key（幂等）。
// 事务内写为 commit 前 DEL——cache-aside 固有竞态下可能残留旧值，陈旧 ≤ TTL（产品接受有界陈旧）；
// 失效失败同样靠 TTL 兜底，不阻断业务。
func (r *CachedUserRepo) invalidate(ctx context.Context, ids ...int64) {
	if err := r.c.DelUserProfiles(ctx, ids); err != nil {
		r.log.Warn("cache invalidate failed, fallback to ttl", "uids", ids, "error", err)
	}
}

// CreateUser 创建用户资料（写后失效：清除查询遗留的空哨兵/旧值）
func (r *CachedUserRepo) CreateUser(ctx context.Context, profile *models.UserProfile) (bool, error) {
	ok, err := r.inner.CreateUser(ctx, profile)
	if err == nil {
		r.invalidate(ctx, profile.ID)
	}
	return ok, err
}

// UpdateUserName 更新昵称（写后失效）
func (r *CachedUserRepo) UpdateUserName(ctx context.Context, id int64, name string) (gen.ResultInfo, error) {
	info, err := r.inner.UpdateUserName(ctx, id, name)
	if err == nil {
		r.invalidate(ctx, id)
	}
	return info, err
}

// UpdateUserBio 更新简介（写后失效）
func (r *CachedUserRepo) UpdateUserBio(ctx context.Context, id int64, bio string) (gen.ResultInfo, error) {
	info, err := r.inner.UpdateUserBio(ctx, id, bio)
	if err == nil {
		r.invalidate(ctx, id)
	}
	return info, err
}

// UpdateUserMetadata 更新元数据（写后失效）
func (r *CachedUserRepo) UpdateUserMetadata(ctx context.Context, id int64, metadata datatypes.JSONMap) (gen.ResultInfo, error) {
	info, err := r.inner.UpdateUserMetadata(ctx, id, metadata)
	if err == nil {
		r.invalidate(ctx, id)
	}
	return info, err
}

// UpdateUserAvatar 更新头像（写后失效）
func (r *CachedUserRepo) UpdateUserAvatar(ctx context.Context, id int64, avatar *media.MediaFile) (gen.ResultInfo, error) {
	info, err := r.inner.UpdateUserAvatar(ctx, id, avatar)
	if err == nil {
		r.invalidate(ctx, id)
	}
	return info, err
}

// BanUser 封禁用户（写后失效）
func (r *CachedUserRepo) BanUser(ctx context.Context, uid int64) error {
	err := r.inner.BanUser(ctx, uid)
	if err == nil {
		r.invalidate(ctx, uid)
	}
	return err
}

// SetFollow 设置关注关系（不触碰缓存：统计列不入资料缓存块——资料缓存实体计数已清零，
// 计数由 stat 缓存/轻查单独提供；写路径不失效，读侧陈旧 ≤ cache.stat_ttl）
func (r *CachedUserRepo) SetFollow(ctx context.Context, fanID, followingID int64) (bool, error) {
	return r.inner.SetFollow(ctx, fanID, followingID)
}

// SetUnFollow 取消关注（不触碰缓存：同 SetFollow，陈旧 ≤ stat_ttl）
func (r *CachedUserRepo) SetUnFollow(ctx context.Context, fanID, followingID int64) error {
	return r.inner.SetUnFollow(ctx, fanID, followingID)
}

// ==================== 直通路径（不缓存） ====================

// GetUserStat 直通（恒走 DB 轻查、绕过 stat 缓存）：需要精确计数或不适用 GetUserProfiles
// 统计口径（回源者采信回源行 / 命中者读 stat 缓存）时用本方法
func (r *CachedUserRepo) GetUserStat(ctx context.Context, uids []int64) ([]*models.UserStat, error) {
	return r.inner.GetUserStat(ctx, uids)
}

// GetFollowings 直通（游标分页，key 组合爆炸、命中率低，一期不缓存）
func (r *CachedUserRepo) GetFollowings(ctx context.Context, uid int64, before int64, limit int64) ([]*models.UserFollow, error) {
	return r.inner.GetFollowings(ctx, uid, before, limit)
}

// GetFollowers 直通（游标分页，一期不缓存）
func (r *CachedUserRepo) GetFollowers(ctx context.Context, uid int64, before int64, limit int64) ([]*models.UserFollow, error) {
	return r.inner.GetFollowers(ctx, uid, before, limit)
}

// GetFollowingRelation 直通（一期不缓存，二期纳入）
func (r *CachedUserRepo) GetFollowingRelation(ctx context.Context, fanID, followingID int64) (bool, bool, error) {
	return r.inner.GetFollowingRelation(ctx, fanID, followingID)
}
