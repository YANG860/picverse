package cache

// ==================== 批量读写的 id 工具 ====================
// 缓存装饰器（CachedPostRepo / CachedUserRepo）共用：MGET 去重与“命中/未命中/空哨兵”拆分，
// 语义只在这里定义一次，避免两侧实现口径漂移。

// DedupeIDs 按首次出现顺序去重（保留输入序）。MGET 用它对 key 去重：同 id 只取一次，
// 返回集每 id 至多一条，且调用方能按原序组装结果（确定性输出）。
func DedupeIDs(ids []int64) []int64 {
	order := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		order = append(order, id)
	}
	return order
}

// SplitHits 把一次批量缓存读的结果按 ids 的顺序拆成三类：
//   - hits：命中实体（供组装/取数）；
//   - hitIDs：命中 id（保持输入序，供后续短 TTL 统计列取样）；
//   - missIDs：缓存未命中的 id（→ 回源）。
//
// 空哨兵（cached[id] == nil）表示“确认不存在”：既不是命中也不是 miss，
// 不进任何集合、不触发回源，也不回填。
func SplitHits[V any](ids []int64, cached map[int64]*V) (hits map[int64]*V, hitIDs, missIDs []int64) {
	hits = make(map[int64]*V, len(ids))
	hitIDs = make([]int64, 0, len(ids))
	missIDs = make([]int64, 0, len(ids))
	for _, id := range ids {
		v, ok := cached[id]
		switch {
		case !ok:
			missIDs = append(missIDs, id) // miss：回源
		case v == nil:
			// 空哨兵：确认不存在，不回源不回填
		default:
			hits[id] = v
			hitIDs = append(hitIDs, id)
		}
	}
	return hits, hitIDs, missIDs
}
