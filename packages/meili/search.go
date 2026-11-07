package meili

import (
	"context"
	"fmt"
	"sort"
	"time"

	ms "github.com/meilisearch/meilisearch-go"
)

// searchTimeDecayDays "综合"排序的时间衰减常数：发布满 N 天权重减半（ 当页近似重排）
const searchTimeDecayDays = 14

// PostHit 帖子搜索命中（解码后；posts 索引只存排序所需最小集 post_id / created_at，
// 面客数据由下游组装）
type PostHit struct {
	PostID    string  // 帖子 ID（雪花 ID 字符串）
	Score     float64 // 相关度得分（Meili rankingScore，0~1；"最新"模式不取为 0）
	CreatedAt int64   // 发帖时间（unix 毫秒）
}

// postSearchDoc posts 索引命中的解码目标（_rankingScore 为 Meili 动态注入字段）
type postSearchDoc struct {
	PostID       string  `json:"post_id"`
	CreatedAt    int64   `json:"created_at"`
	RankingScore float64 `json:"_rankingScore"`
}

// PostSearchResult 帖子搜索结果：命中已解码为 PostHit，NextOffset 为游标推进量
// （按 Meili 实际消费的原始命中数计，坏命中跳过不回拨，避免翻页回退）
type PostSearchResult struct {
	Hits       []PostHit // 命中列表（坏命中已跳过）
	NextOffset int64     // 下一页偏移量；为 0 表示本页无消费（空页即到底）
}

// postsAttrRetrieve posts 索引命中只取排序所需字段（post_id 为返回 Key）
var postsAttrRetrieve = []string{"post_id", "created_at"}

// SearchPosts 搜帖子——"综合"排序：Meili 相关度序取回，按 rankingScore × 时间衰减当页重排
// （近似口径，翻页以 Meili 序为准）。offset 编码游标（str=偏移量，非法/负数回退首页）。
func (m *MeiliClient) SearchPosts(ctx context.Context, query string, offset, limit int64) (*PostSearchResult, error) {
	resp, err := m.Search(ctx, PostsIndex, query, &ms.SearchRequest{
		Offset:               offset,
		Limit:                limit,
		AttributesToRetrieve: postsAttrRetrieve,
		ShowRankingScore:     true, // 取回相关度得分供重排
	})
	if err != nil {
		return nil, fmt.Errorf("search posts query %q: %w", query, err)
	}

	hits := decodePostHits(resp.Hits)
	// 当页重排（稳定排序保持同分相对序）
	if len(hits) > 1 {
		now := time.Now().UnixMilli()
		sort.SliceStable(hits, func(i, j int) bool {
			return postCombinedScore(hits[i], now) > postCombinedScore(hits[j], now)
		})
	}
	return &PostSearchResult{Hits: hits, NextOffset: offset + int64(len(resp.Hits))}, nil
}

// SearchLatestPosts 搜帖子——"最新"排序：按 (created_at, post_id) 倒序直出（post_id 为同毫秒
// tiebreaker，与复合游标理念一致），无重排。
func (m *MeiliClient) SearchLatestPosts(ctx context.Context, query string, offset, limit int64) (*PostSearchResult, error) {
	resp, err := m.Search(ctx, PostsIndex, query, &ms.SearchRequest{
		Offset:               offset,
		Limit:                limit,
		AttributesToRetrieve: postsAttrRetrieve,
		Sort:                 []string{"created_at:desc", "post_id:desc"},
	})
	if err != nil {
		return nil, fmt.Errorf("search latest posts query %q: %w", query, err)
	}
	return &PostSearchResult{Hits: decodePostHits(resp.Hits), NextOffset: offset + int64(len(resp.Hits))}, nil
}

// decodePostHits schemaless 命中解码为 PostHit；坏命中（缺 post_id/解码失败）跳过不打挂整页
func decodePostHits(hits []ms.Hit) []PostHit {
	result := make([]PostHit, 0, len(hits))
	for _, h := range hits {
		var doc postSearchDoc
		if err := h.DecodeInto(&doc); err != nil {
			continue // 个别坏命中降级跳过，可用性优先（下同）
		}
		if doc.PostID == "" {
			continue
		}
		result = append(result, PostHit{PostID: doc.PostID, Score: doc.RankingScore, CreatedAt: doc.CreatedAt})
	}
	return result
}

// postCombinedScore "综合"分 = 相关度 × 时间衰减（ 当页近似重排）
func postCombinedScore(h PostHit, nowMs int64) float64 {
	ageDays := float64(nowMs-h.CreatedAt) / float64(24*time.Hour/time.Millisecond)
	if ageDays < 0 {
		ageDays = 0 // 未来时间按 0 龄处理
	}
	return h.Score / (1 + ageDays/searchTimeDecayDays)
}
