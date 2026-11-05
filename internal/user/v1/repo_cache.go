package v1

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"

	media "rednote/internal/media/models"
	models "rednote/internal/user/models"
	"rednote/packages/cache"
	"rednote/packages/logger"

	"golang.org/x/sync/singleflight"
	"gorm.io/datatypes"
	"gorm.io/gen"
	"gorm.io/gorm"
)

// dedupeUIDs 按首次出现顺序去重
func dedupeUIDs(uids []int64) []int64 {
	order := make([]int64, 0, len(uids))
	seen := make(map[int64]struct{}, len(uids))
	for _, id := range uids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		order = append(order, id)
	}
	return order
}

// CachedUserRepo UserRepository 的缓存装饰器（cache-aside），基于 cache.UserEntityCache
// typed API 存取（key 构造/序列化/空哨兵/TTL 抖动均收口在 cache 层）。
//
// 缓存范围：GetUserProfiles 的非统计列（user:profile:{id}，单 key 粒度）。
//   - 简视图（UserBrief）不设独立缓存与查询：与 profile 共用同一条 cache-aside 路径，
//     由 UserProfile.ToBrief() 派生（biz.GetUserBrief 走 GetUserProfiles）；
//   - 统计列（followers/followings）暂不缓存：GetUserStat 直通轻查，GetUserProfiles
//     组装时每回轻查覆盖计数——保证精确，follow/取关因此完全不触碰缓存。
//     若后续统计轻查成为热点，再启用 UserEntityCache.Load/StoreUserStat（写回式缓存）。
//
// 一致性模型（单实例部署假设，多副本失效广播为后续规划）——接受有界陈旧：
//   - 读：批量 cache-aside，部分命中只回源缺失；miss 回填空哨兵防穿透；
//     并发相同回源经 singleflight 合并防击穿；TTL ±10% 抖动防雪崩（cache 层内建）
//   - 写：方法返回后 DEL 对应 key（事务内写为 commit 前 DEL）；写后读通常即刻可见
//   - 陈旧上界：cache-aside 固有竞态（慢查询回填 commit 前旧值）下，最长陈旧 ≤ TTL 或下次写；
//     写后回读理论上可能回显旧值（需并发读恰好跨越写窗口，概率级低）——产品接受陈旧读
//   - 事务模式（WithTX）：读直穿不缓存——事务内必须读到自身未提交的写入（事务语义，非陈旧容忍问题）
//   - 降级：任何缓存错误只记 warn 并回源 DB，绝不因缓存故障失败业务请求。
type CachedUserRepo struct {
	inner UserRepository
	c     cache.UserEntityCache
	log   *slog.Logger

	// txMode 事务绑定模式：读直穿不缓存
	txMode bool

	// sf 合并并发相同回源（防击穿）
	sf *singleflight.Group
}

var _ UserRepository = (*CachedUserRepo)(nil)

// NewCachedUserRepo 创建用户存储库缓存装饰器。
// TTL/序列化策略由 UserEntityCache 持有（config 其次 defaults），本装饰器不再关心。
func NewCachedUserRepo(inner *UserRepoImpl, c cache.UserEntityCache, lf logger.LoggerFactory) *CachedUserRepo {
	if inner == nil {
		panic(fmt.Errorf("cache: inner user repo is required"))
	}
	if c == nil {
		panic(fmt.Errorf("cache: cache client is required"))
	}
	return &CachedUserRepo{
		inner: inner,
		c:     c,
		log:   lf.GetLoggerOf("user.repo.cache"),
		sf:    &singleflight.Group{},
	}
}

// WithTX 返回绑定事务的装饰器（txMode）：
// 读直穿不缓存（事务内必须读到自身未提交的写入），写照常登记失效。
func (r *CachedUserRepo) WithTX(tx *gorm.DB) UserRepository {
	clone := *r // 结构体浅拷贝：sf 指针与 outer 共享，配置字段只读
	clone.inner = r.inner.WithTX(tx)
	clone.txMode = true
	return &clone
}

// ==================== 读路径 ====================

// GetUserProfiles 批量获取用户资料（cache-aside，按 uid 粒度缓存非统计列）。
// 统计列每次经 GetUserStat 轻查覆盖（暂不缓存，精确）。
// 返回顺序按 uids 首次出现顺序去重排列（调用方均按 ID 匹配或单 uid 使用，顺序无依赖）。
func (r *CachedUserRepo) GetUserProfiles(ctx context.Context, uids []int64) ([]*models.UserProfile, error) {
	// 空入参直通（空集不进缓存）；事务模式直通（须见事务内自身写入）
	if len(uids) == 0 || r.txMode {
		return r.inner.GetUserProfiles(ctx, uids)
	}

	// 按首次出现顺序去重：MGET 不重复取同 key，返回集每 uid 至多一条
	order := dedupeUIDs(uids)

	// 1) 批量读缓存；缓存错误降级为全量回源（fail-open）
	byID := make(map[int64]*models.UserProfile, len(order))
	missing := make([]int64, 0, len(order))
	if cached, err := r.c.LoadUserProfiles(ctx, order); err != nil {
		r.log.Warn("cache load user profiles failed，降级全量回源", "error", err)
		missing = append(missing, order...)
	} else {
		for _, id := range order {
			p, ok := cached[id]
			if !ok {
				missing = append(missing, id) // miss 回源
				continue
			}
			if p == nil {
				continue // 空哨兵：确认不存在，不回源不回填
			}
			byID[id] = p
		}
	}

	// 2) 缺失项回源并回填
	if len(missing) > 0 {
		fetched, err := r.fetchMissing(ctx, missing)
		if err != nil {
			return nil, err
		}
		maps.Copy(byID, fetched)
	}

	// 3) 统计列暂不缓存：每次轻查 GetUserStat（精确，follow 事件无需失效缓存）
	statByID := make(map[int64]*models.UserStat, len(byID))
	if len(byID) > 0 {
		foundIDs := make([]int64, 0, len(order))
		for _, id := range order {
			if _, ok := byID[id]; ok {
				foundIDs = append(foundIDs, id)
			}
		}
		stats, err := r.inner.GetUserStat(ctx, foundIDs)
		if err != nil {
			return nil, err
		}
		for _, s := range stats {
			statByID[s.ID] = s
		}
	}

	// 4) 按入参首次出现顺序组装。
	// 拷贝后覆盖统计列：singleflight 合并窗口内共享的回源结果对象只读，
	// 直接改写会与其它并发调用方构成数据竞态。
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
	return profiles, nil
}

// fetchMissing 回源查询缺失 uid 并回填缓存。
// singleflight 按有序 uid 集合合并并发相同回源（同一热 key 过期后的击穿流量只落库一次）；
// 合并窗口内共享结果对象，调用方按只读使用（组装时拷贝，见 GetUserProfiles 步骤 4）。
func (r *CachedUserRepo) fetchMissing(ctx context.Context, ids []int64) (map[int64]*models.UserProfile, error) {
	sorted := append([]int64(nil), ids...)
	slices.Sort(sorted)
	strs := make([]string, len(sorted))
	for i, id := range sorted {
		strs[i] = strconv.FormatInt(id, 10)
	}

	v, err, _ := r.sf.Do("p:"+strings.Join(strs, ","), func() (any, error) {
		profiles, err := r.inner.GetUserProfiles(ctx, ids)
		if err != nil {
			return nil, err
		}
		byID := make(map[int64]*models.UserProfile, len(profiles))
		for _, p := range profiles {
			byID[p.ID] = p
		}
		r.fillCache(ctx, ids, byID)
		return byID, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(map[int64]*models.UserProfile), nil
}

// fillCache 回填缓存：命中的写实体（cache 层只存非统计列、TTL 自带抖动），
// 缺失的写空哨兵（StoreUserProfile(ctx, id, nil)）。
// 回填失败只记 warn：缓存缺失即下次回源，不影响正确性。
func (r *CachedUserRepo) fillCache(ctx context.Context, ids []int64, byID map[int64]*models.UserProfile) {
	for _, id := range ids {
		p, ok := byID[id]
		if !ok {
			if err := r.c.StoreUserProfile(ctx, id, nil); err != nil {
				r.log.Warn("cache store null sentinel failed", "uid", id, "error", err)
			}
			continue
		}
		if err := r.c.StoreUserProfile(ctx, id, p); err != nil {
			r.log.Warn("cache store user profile failed", "uid", id, "error", err)
		}
	}
}

// ==================== 写路径（失效登记） ====================

// invalidate 写后失效：DEL 用户资料缓存 key（幂等）。
// 事务内写为 commit 前 DEL——cache-aside 固有竞态下可能残留旧值，陈旧 ≤ TTL（产品接受有界陈旧）；
// 失效失败同样靠 TTL 兜底，不阻断业务。
func (r *CachedUserRepo) invalidate(ctx context.Context, ids ...int64) {
	if err := r.c.DelUserProfiles(ctx, ids); err != nil {
		r.log.Warn("cache invalidate failed，降级靠 TTL 兜底", "uids", ids, "error", err)
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

// SetFollow 设置关注关系（不触碰缓存：统计列暂不缓存，计数经 GetUserStat 实时读取）
func (r *CachedUserRepo) SetFollow(ctx context.Context, fanID, followingID int64) (bool, error) {
	return r.inner.SetFollow(ctx, fanID, followingID)
}

// SetUnFollow 取消关注（不触碰缓存：同 SetFollow，计数变更不影响缓存实体）
func (r *CachedUserRepo) SetUnFollow(ctx context.Context, fanID, followingID int64) error {
	return r.inner.SetUnFollow(ctx, fanID, followingID)
}

// ==================== 直通路径（不缓存） ====================

// GetUserStat 直通（统计列暂不缓存：每次 DB 轻查保证精确；
// 若轻查成为热点，再启用 UserEntityCache.Load/StoreUserStat 升级写回式缓存）
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
