package cache

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"rednote/config"
	"rednote/packages/defaults"
)

// SeenCache 推荐流已曝光账本（ZSET rn:v1:seen:{uid}，member = post_id、score = 曝光时刻毫秒）：
// Filter 批量去重（命中 = 已曝光，不再推荐）；Mark 曝光记账 + 滚动清理窗口外 + TTL 滚动续期。
// 去重窗口 = ttl（默认 30d）；key TTL 随记账续期，停用即账本随 TTL 消亡（彼时帖子已出热池窗口，无害）。
// 容错口径：读写失败均向上抛（调用方整体重试）——Filter 失败不当"未曝光"（会重复推荐）、
// Mark 失败不丢记账。
type SeenCache struct {
	c   Cache
	ttl time.Duration // 去重窗口 = key TTL（滚动续期）
}

// NewSeenCache 创建已曝光账本：cfg.Recommend.SeenTTL 为 0（或 cfg 为 nil）使用默认 720h（30 天）
func NewSeenCache(c Cache, cfg *config.Config) *SeenCache {
	var ttl time.Duration
	if cfg != nil {
		ttl = cfg.Recommend.SeenTTL
	}
	if ttl <= 0 {
		ttl = defaults.RecommendSeenTTL
	}
	return &SeenCache{c: c, ttl: ttl}
}

// Filter 批量判定已曝光，结果与 postIDs 等长对齐（true = 已曝光）；key 不存在全 false
func (s *SeenCache) Filter(ctx context.Context, uid int64, postIDs []int64) ([]bool, error) {
	if len(postIDs) == 0 {
		return nil, nil
	}
	members := make([]string, len(postIDs))
	for i, id := range postIDs {
		members[i] = strconv.FormatInt(id, 10)
	}
	seen, err := s.c.ZMScore(ctx, idKey(seenPrefix, uid), members)
	if err != nil {
		return nil, fmt.Errorf("seen filter uid %d: %w", uid, err)
	}
	return seen, nil
}

// Mark 曝光记账：写入曝光时刻 + 滚动清理去重窗口外的旧账 + TTL 续期（幂等，重复记账无害）
func (s *SeenCache) Mark(ctx context.Context, uid int64, postIDs []int64) error {
	if len(postIDs) == 0 {
		return nil
	}
	key := idKey(seenPrefix, uid)
	now := time.Now().UnixMilli()
	ms := make([]ZMember, len(postIDs))
	for i, id := range postIDs {
		ms[i] = ZMember{Score: float64(now), Member: strconv.FormatInt(id, 10)}
	}
	if err := s.c.ZAdd(ctx, key, ms, s.ttl); err != nil {
		return fmt.Errorf("seen mark uid %d: %w", uid, err)
	}
	// 滚动清理：窗口外（seen_ttl 前）的旧账出列（key TTL 兜底消亡）
	if err := s.c.ZRemRangeByScore(ctx, key, math.Inf(-1), float64(now-s.ttl.Milliseconds())); err != nil {
		return fmt.Errorf("seen trim uid %d: %w", uid, err)
	}
	return nil
}
