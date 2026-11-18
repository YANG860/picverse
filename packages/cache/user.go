package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"rednote/config"
	user "rednote/internal/user/models"
	"rednote/packages/defaults"
)

// UserEntityCache 用户实体缓存（cache-aside 存取层）：以 uid 为粒度缓存用户实体，
// 分两块存储——资料主体（user:profile，非统计列）与统计列（user:stat），各自独立 TTL/失效。
//
// 读语义（"nil 与 空"双标记）：
//   - uid 不在返回 map 中：miss（未缓存），调用方回源后用 Store* 回填；
//   - map[uid] == nil：空哨兵命中，确认该 uid 不存在（无需回源，防穿透）；
//   - map[uid] != nil：命中缓存实体。
//
// 写语义：Store*(ctx, id, nil) 写入空哨兵（缓存"查询结果为空"）。
// 失效语义：Del* 删除对应实体 key（幂等，缺失静默跳过），供写后失效登记。
// 错误一律上抛由调用方降级（fail-open），不在本层吞错。
type UserEntityCache interface {
	// LoadUserProfiles 批量读用户资料（非统计列，统计列为零值，调用方以 LoadUserStats 补齐）
	LoadUserProfiles(ctx context.Context, ids []int64) (map[int64]*user.UserProfile, error)
	// StoreUserProfile 写用户资料（只存非统计列）；profile == nil 时写空哨兵
	StoreUserProfile(ctx context.Context, id int64, profile *user.UserProfile) error
	// StoreUserProfiles 批量回填资料（MSet，一轮 RTT）：hits 写实体（只存非统计列）、
	// missing 写空哨兵；hits 中值为 nil 的按空哨兵处理（与 StoreUserProfile 同口径）。
	// 空入参零网络往返。
	StoreUserProfiles(ctx context.Context, hits map[int64]*user.UserProfile, missing []int64) error
	// DelUserProfiles 批量删除用户资料缓存（写后失效）
	DelUserProfiles(ctx context.Context, ids []int64) error

	// LoadUserStats 批量读用户统计列
	LoadUserStats(ctx context.Context, ids []int64) (map[int64]*user.UserStat, error)
	// StoreUserStat 写用户统计列；stat == nil 时写空哨兵
	StoreUserStat(ctx context.Context, id int64, stat *user.UserStat) error
	// StoreUserStats 批量写统计列（MSet，一轮 RTT）；值为 nil 的按空哨兵处理。
	// 空入参零网络往返。
	StoreUserStats(ctx context.Context, stats map[int64]*user.UserStat) error
	// DelUserStats 批量删除用户统计列缓存（写后失效）
	DelUserStats(ctx context.Context, ids []int64) error

	// 简视图（UserBrief）不设独立缓存方法：与资料共用缓存与查询，由 UserProfile.ToBrief() 派生
}

// UserEntityCacheImpl UserEntityCache 的 Cache 实现：key 构造 + JSON 序列化 +
// 空哨兵（NullSentinel）+ TTL 抖动（JitterTTL），底层原语经 Cache 注入（Redis/Noop/测试替身）。
type UserEntityCacheImpl struct {
	c   Cache
	cfg config.CacheConfig // TTL 来源：字段 > 0 取配置，否则回退 defaults
}

var _ UserEntityCache = (*UserEntityCacheImpl)(nil)

// NewUserEntityCache 创建用户实体缓存。cfg 可为 nil（全部 TTL 回退 defaults）；
// cfg 字段为 0 时同样回退 defaults（"config 其次 defaults"）。
func NewUserEntityCache(c Cache, cfg *config.Config) *UserEntityCacheImpl {
	if c == nil {
		panic(fmt.Errorf("cache: cache client is required"))
	}
	var cc config.CacheConfig
	if cfg != nil {
		cc = cfg.Cache
	}
	return &UserEntityCacheImpl{c: c, cfg: cc}
}

// ==================== key 与 TTL ====================

// userProfileKey 用户资料缓存 key（KeyPrefix 的 v1 为序列化 schema 版本）
func userProfileKey(id int64) string {
	return idKey(userProfilePrefix, id)
}

// userStatKey 用户统计列缓存 key
func userStatKey(id int64) string {
	return idKey(userStatPrefix, id)
}

// profileTTL 资料缓存 TTL：config 其次 defaults
func (u *UserEntityCacheImpl) profileTTL() time.Duration {
	if u.cfg.ProfileTTL > 0 {
		return u.cfg.ProfileTTL
	}
	return defaults.CacheProfileTTL
}

// statTTL 统计列缓存 TTL：config 其次 defaults（启用/禁用裁决在 repo 装配处，cache 层纯语义）
func (u *UserEntityCacheImpl) statTTL() time.Duration {
	if u.cfg.StatTTL > 0 {
		return u.cfg.StatTTL
	}
	return defaults.CacheStatTTL
}

// nullTTL 空哨兵 TTL：config 其次 defaults
func (u *UserEntityCacheImpl) nullTTL() time.Duration {
	if u.cfg.NullTTL > 0 {
		return u.cfg.NullTTL
	}
	return defaults.CacheNullTTL
}

// ==================== 资料主体（非统计列） ====================

// LoadUserProfiles 批量读用户资料缓存（MGET 对齐，逐 uid 判定 miss/哨兵/命中）。
// 序列化损坏按错误上抛（调用方降级全量回源并回填，自愈）。
func (u *UserEntityCacheImpl) LoadUserProfiles(ctx context.Context, ids []int64) (map[int64]*user.UserProfile, error) {
	vals, err := MGetKeys(ctx, u.c, ids, userProfileKey)
	if err != nil {
		return nil, fmt.Errorf("cache user profile mget: %w", err)
	}
	profiles := make(map[int64]*user.UserProfile, len(ids))
	for i, id := range ids {
		val := vals[i]
		if val == nil {
			continue // miss：不入 map，调用方回源
		}
		if IsNullSentinel(val) {
			profiles[id] = nil // 空哨兵：确认不存在
			continue
		}
		var p user.UserProfile
		if err := json.Unmarshal(val, &p); err != nil {
			return nil, fmt.Errorf("cache user profile unmarshal %d: %w", id, err)
		}
		profiles[id] = &p
	}
	return profiles, nil
}

// StoreUserProfile 写用户资料缓存：只存非统计列（计数清零后入缓存，统计列由
// LoadUserStats 独立提供——两者各自 TTL/失效，旧缓存残留计数也无害）。
// 写入 TTL 附带 ±10% 抖动防雪崩；profile == nil 写空哨兵（短 TTL 防穿透）。
// id 为 key 依据。
func (u *UserEntityCacheImpl) StoreUserProfile(ctx context.Context, id int64, profile *user.UserProfile) error {
	key := userProfileKey(id)
	if profile == nil {
		if err := u.c.Set(ctx, key, NullSentinel, u.nullTTL()); err != nil {
			return fmt.Errorf("cache user profile set null sentinel %d: %w", id, err)
		}
		return nil
	}
	val, err := cacheProfileVal(profile)
	if err != nil {
		return fmt.Errorf("cache user profile marshal %d: %w", id, err)
	}
	if err := u.c.Set(ctx, key, val, JitterTTL(u.profileTTL(), defaults.CacheTTLJitter)); err != nil {
		return fmt.Errorf("cache user profile set %d: %w", id, err)
	}
	return nil
}

// cacheProfileVal 序列化待缓存资料：计数列清零后入缓存（统计列由 stat 缓存/轻查提供），
// 拷贝后清零，不改动调用方实体。
func cacheProfileVal(profile *user.UserProfile) ([]byte, error) {
	cached := *profile
	cached.Followers = 0
	cached.Followings = 0
	return json.Marshal(&cached)
}

// StoreUserProfiles 批量回填资料（MSet，一轮 RTT）：hits 写实体（计数清零后入缓存）、
// missing 写空哨兵（nullTTL）；hits 中值为 nil 按空哨兵处理（与 StoreUserProfile 同口径）。
// 两组 TTL 不同 → 分两批提交；抖动粒度 = 本批（同批 shared TTL 让“同页 key 一起过期”成为
// 一次批查询 + 一次批量回填的廉价形状，口径见 JitterTTL）。
// 非原子：部分 key 写失败则整批报错，调用方 fail-open（下次回源自愈，不影响正确性）。
func (u *UserEntityCacheImpl) StoreUserProfiles(ctx context.Context, hits map[int64]*user.UserProfile, missing []int64) error {
	entityPairs := make([]KVPair, 0, len(hits))
	sentinelIDs := make([]int64, 0, len(missing)+len(hits))
	sentinelIDs = append(sentinelIDs, missing...)

	for _, id := range slices.Sorted(maps.Keys(hits)) { // 批内顺序确定（错误里的 key 可复现）
		profile := hits[id]
		if profile == nil {
			sentinelIDs = append(sentinelIDs, id)
			continue
		}
		val, err := cacheProfileVal(profile)
		if err != nil {
			return fmt.Errorf("cache user profile marshal %d: %w", id, err)
		}
		entityPairs = append(entityPairs, KVPair{Key: userProfileKey(id), Val: val})
	}

	if len(entityPairs) > 0 {
		ttl := JitterTTL(u.profileTTL(), defaults.CacheTTLJitter)
		if err := u.c.MSet(ctx, entityPairs, ttl); err != nil {
			return fmt.Errorf("cache user profile set %d profiles: %w", len(entityPairs), err)
		}
	}
	if len(sentinelIDs) > 0 {
		pairs := make([]KVPair, 0, len(sentinelIDs))
		for _, id := range sentinelIDs {
			pairs = append(pairs, KVPair{Key: userProfileKey(id), Val: NullSentinel})
		}
		if err := u.c.MSet(ctx, pairs, u.nullTTL()); err != nil {
			return fmt.Errorf("cache user profile set %d null sentinels: %w", len(pairs), err)
		}
	}
	return nil
}

// DelUserProfiles 批量删除用户资料缓存（写后失效：DEL 对应 key，幂等）。
// 失效失败错误上抛，调用方记 warn 后靠 TTL 兜底即可（陈旧有界），不阻断业务。
func (u *UserEntityCacheImpl) DelUserProfiles(ctx context.Context, ids []int64) error {
	if err := DelKeys(ctx, u.c, ids, userProfileKey); err != nil {
		return fmt.Errorf("cache user profile del: %w", err)
	}
	return nil
}

// ==================== 统计列 ====================

// LoadUserStats 批量读用户统计列缓存，map 语义同 LoadUserProfiles（nil 与 空双标记）。
func (u *UserEntityCacheImpl) LoadUserStats(ctx context.Context, ids []int64) (map[int64]*user.UserStat, error) {
	vals, err := MGetKeys(ctx, u.c, ids, userStatKey)
	if err != nil {
		return nil, fmt.Errorf("cache user stat mget: %w", err)
	}
	stats := make(map[int64]*user.UserStat, len(ids))
	for i, id := range ids {
		val := vals[i]
		if val == nil {
			continue // miss：不入 map，调用方回源
		}
		if IsNullSentinel(val) {
			stats[id] = nil // 空哨兵：确认不存在
			continue
		}
		var s user.UserStat
		if err := json.Unmarshal(val, &s); err != nil {
			return nil, fmt.Errorf("cache user stat unmarshal %d: %w", id, err)
		}
		stats[id] = &s
	}
	return stats, nil
}

// StoreUserStat 写用户统计列缓存（短 TTL + 抖动，计数高频变更保证有界陈旧）；
// stat == nil 写空哨兵。uid 为 key 依据（单条写：内部转批量口）。
func (u *UserEntityCacheImpl) StoreUserStat(ctx context.Context, id int64, stat *user.UserStat) error {
	return u.StoreUserStats(ctx, map[int64]*user.UserStat{id: stat})
}

// StoreUserStats 批量写统计列缓存（MSet，一轮 RTT）：短 TTL + 抖动（粒度 = 本批，
// 见 JitterTTL）；值为 nil 的按空哨兵处理（与 StoreUserStat 同口径）。
// 非原子：部分 key 写失败则整批报错，调用方 fail-open（下次回源自愈）。
func (u *UserEntityCacheImpl) StoreUserStats(ctx context.Context, stats map[int64]*user.UserStat) error {
	pairs := make([]KVPair, 0, len(stats))
	sentinelIDs := make([]int64, 0)

	for _, id := range slices.Sorted(maps.Keys(stats)) { // 批内顺序确定（错误里的 key 可复现）
		stat := stats[id]
		if stat == nil {
			sentinelIDs = append(sentinelIDs, id)
			continue
		}
		val, err := json.Marshal(stat)
		if err != nil {
			return fmt.Errorf("cache user stat marshal %d: %w", id, err)
		}
		pairs = append(pairs, KVPair{Key: userStatKey(id), Val: val})
	}

	if len(pairs) > 0 {
		ttl := JitterTTL(u.statTTL(), defaults.CacheTTLJitter)
		if err := u.c.MSet(ctx, pairs, ttl); err != nil {
			return fmt.Errorf("cache user stat set %d stats: %w", len(pairs), err)
		}
	}
	if len(sentinelIDs) > 0 {
		nulls := make([]KVPair, 0, len(sentinelIDs))
		for _, id := range sentinelIDs {
			nulls = append(nulls, KVPair{Key: userStatKey(id), Val: NullSentinel})
		}
		if err := u.c.MSet(ctx, nulls, u.nullTTL()); err != nil {
			return fmt.Errorf("cache user stat set %d null sentinels: %w", len(nulls), err)
		}
	}
	return nil
}

// DelUserStats 批量删除用户统计列缓存（写后失效：DEL 对应 key，幂等）。
// follow/取关等计数变更后登记失效；失效失败同样靠 TTL 兜底。
func (u *UserEntityCacheImpl) DelUserStats(ctx context.Context, ids []int64) error {
	if err := DelKeys(ctx, u.c, ids, userStatKey); err != nil {
		return fmt.Errorf("cache user stat del: %w", err)
	}
	return nil
}
