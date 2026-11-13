package cache

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"rednote/config"
	"rednote/packages/defaults"
)

// HotEntry 热池成员（member 编码 "post_id:author_id"、score = 热度分）：
// 带 author 是为推荐流同作者打散零额外查询（池即打散所需全量信息）
type HotEntry struct {
	PostID   int64
	AuthorID int64
	Score    float64
}

// HotPoolCache 全局热榜池（ZSET rn:v1:hot:posts，member = "post_id:author_id"、score = 热度分）：
// 热榜与推荐流共享的候选池（见 doc/internal/discover/plan_recommend_hot.md）。
//
// 写侧两条路（入池）：
//   - Add：发帖钩子 0 分占位（ZAddNX 不覆盖维护算好的分数），新帖即时入池可被探索位采样；
//   - Rebuild：维护任务整池重建（build key 写满后 RENAME 原子换，入池/出池/算分一步完成），
//     池内容每轮 = 精确的窗口候选集（时间/状态淘汰 = 下轮不重建）。
//
// TTL 口径：TTL 随任何写入刷新（池 TTL 为维护任务停摆兜底——停摆且无发帖时池自动消亡，
// 热榜/推荐流降级空页，加速层非供给线红线）。
type HotPoolCache struct {
	c   Cache
	ttl time.Duration
}

// NewHotPoolCache 创建热池缓存：ttl 为池 TTL 兜底（停摆消亡，随写入续期）；
// cfg.Hot.TTL 为 0（或 cfg 为 nil）使用默认 30m（defaults.HotTTL）
func NewHotPoolCache(c Cache, cfg *config.Config) *HotPoolCache {
	var ttl time.Duration
	if cfg != nil {
		ttl = cfg.Hot.TTL
	}
	if ttl <= 0 {
		ttl = defaults.HotTTL
	}
	return &HotPoolCache{c: c, ttl: ttl}
}

// Add 发帖钩子 0 分占位入池（幂等：已存在的成员不覆盖分数，重复消息无害）
func (p *HotPoolCache) Add(ctx context.Context, postID, authorID int64) error {
	m := ZMember{Score: 0, Member: formatHotMember(postID, authorID)}
	if err := p.c.ZAddNX(ctx, hotPoolKey, []ZMember{m}, p.ttl); err != nil {
		return fmt.Errorf("hot pool add post %d: %w", postID, err)
	}
	return nil
}

// Rebuild 整池重建（原子发布）：build key 写满（含 TTL）→ RENAME 换入。
// entries 为空视为异常（空池会让热榜整体空窗，宁可保留旧池）；RENAME 失败旧池继续服务。
func (p *HotPoolCache) Rebuild(ctx context.Context, entries []HotEntry) error {
	if len(entries) == 0 {
		return fmt.Errorf("hot pool rebuild: empty entries")
	}
	ms := make([]ZMember, 0, len(entries))
	for _, e := range entries {
		ms = append(ms, ZMember{Score: e.Score, Member: formatHotMember(e.PostID, e.AuthorID)})
	}
	if err := p.c.ZAdd(ctx, hotPoolBuildKey, ms, p.ttl); err != nil {
		return fmt.Errorf("hot pool rebuild build: %w", err)
	}
	if err := p.c.Rename(ctx, hotPoolBuildKey, hotPoolKey); err != nil {
		return fmt.Errorf("hot pool rebuild rename: %w", err)
	}
	return nil
}

// TopN 按热度降序排名取 [offset, offset+n)（榜单/推荐流候选扫描翻页）。
// 池内全序、服务端不剔除已删（BFF 回查过滤兜底）；key 不存在返回空（fail-soft 降级空页）
func (p *HotPoolCache) TopN(ctx context.Context, offset, n int) ([]HotEntry, error) {
	if n <= 0 {
		return nil, nil
	}
	ms, err := p.c.ZRevRange(ctx, hotPoolKey, offset, offset+n-1)
	if err != nil {
		return nil, fmt.Errorf("hot pool topn: %w", err)
	}
	return parseHotEntries(ms)
}

// Sample 随机不放回采样至多 n 名（推荐流探索位；0 分新帖占位同样可采）；key 不存在返回空
func (p *HotPoolCache) Sample(ctx context.Context, n int) ([]HotEntry, error) {
	if n <= 0 {
		return nil, nil
	}
	ms, err := p.c.ZRandMember(ctx, hotPoolKey, n)
	if err != nil {
		return nil, fmt.Errorf("hot pool sample: %w", err)
	}
	return parseHotEntries(ms)
}

// formatHotMember postID:authorID → ZSET member（热池唯一编码点）
func formatHotMember(postID, authorID int64) string {
	return strconv.FormatInt(postID, 10) + ":" + strconv.FormatInt(authorID, 10)
}

// parseHotEntries ZMember → HotEntry（member 解析；畸形 member 跳过——池写者只经 formatHotMember）
func parseHotEntries(ms []ZMember) ([]HotEntry, error) {
	out := make([]HotEntry, 0, len(ms))
	for _, m := range ms {
		postID, authorID, err := parseHotMember(m.Member)
		if err != nil {
			return nil, err
		}
		out = append(out, HotEntry{PostID: postID, AuthorID: authorID, Score: m.Score})
	}
	return out, nil
}

// parseHotMember ZSET member → postID:authorID（formatHotMember 的逆运算）
func parseHotMember(member string) (postID, authorID int64, err error) {
	pid, aid, ok := strings.Cut(member, ":")
	if !ok {
		return 0, 0, fmt.Errorf("hot pool: malformed member %q", member)
	}
	postID, err = strconv.ParseInt(pid, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("hot pool: malformed member %q: %w", member, err)
	}
	authorID, err = strconv.ParseInt(aid, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("hot pool: malformed member %q: %w", member, err)
	}
	return postID, authorID, nil
}
