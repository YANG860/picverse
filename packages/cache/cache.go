package cache

import (
	"bytes"
	"context"
	"math/rand"
	"strconv"
	"strings"
	"time"
)

type Cache interface {
	// Get 读取单 key；val 不存在（miss）返回 ok=false
	Get(ctx context.Context, key string) (val []byte, ok bool, err error)
	// MGet 批量读取，结果与 keys 等长对齐；miss 的元素为 nil
	MGet(ctx context.Context, keys []string) ([][]byte, error)
	// Set 写入并设置 TTL；ttl <= 0 表示不过期
	Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
	// Del 删除 keys；不存在的 key 静默跳过（幂等）
	Del(ctx context.Context, keys ...string) error

	// ==================== 计数器原语（多写者并发累加） ====================

	// IncrCounter 计数器 +1 并刷新 TTL（原子；key 不存在从 1 起）。
	// 独立于 Get/Set 的读-改-写：多写者并发安全的增量累加（如浏览计数聚合），
	// ttl <= 0 表示不过期。
	IncrCounter(ctx context.Context, key string, ttl time.Duration) error
	// TakeCounters 按前缀原子取走计数器增量（取走即清除，多消费者互斥：取走者独得）。
	// 返回 key → 原始值（十进制字符串）；前缀下无键返回空 map，值的合法性由调用方裁决。
	TakeCounters(ctx context.Context, prefix string) (map[string]string, error)

	// ==================== 有序集合原语（ZSET，score double） ====================

	// ZAdd 追加/更新成员并刷新 TTL（key 不存在创建；同 member 更新 score；ttl <= 0 不过期）
	ZAdd(ctx context.Context, key string, members []ZMember, ttl time.Duration) error
	// ZRevRangeByScore 按 score 降序取 [min, max] 闭区间至多 count 名（count <= 0 不限量）。
	// 同 score 按 member 字典序降序（redis 原生序）；key 不存在返回空
	ZRevRangeByScore(ctx context.Context, key string, max, min float64, count int) ([]ZMember, error)
	// ZRemRangeByRank 按排名删除 [start, stop] 闭区间（排名按 score 升序 0 起，负数自尾部：-1 为最大）；
	// 保最新 keep 条 = ZRemRangeByRank(key, 0, -(keep+1))
	ZRemRangeByRank(ctx context.Context, key string, start, stop int) error
	// ZRemRangeByScore 按 score 删除 [min, max] 闭区间（±Inf 可用）；用于按时间窗截尾
	ZRemRangeByScore(ctx context.Context, key string, max, min float64) error
	// Exists 判定 key 是否存在（区分"未缓存"与"已缓存但结果为空"）
	Exists(ctx context.Context, key string) (bool, error)
}

// ZMember 有序集合成员（score 与 redis ZSET 同为 double；int64 毫秒时间戳在其精确表示范围内）
type ZMember struct {
	Score  float64
	Member string
}

// KeyPrefix 缓存 key 前缀。v1 为序列化 schema 版本：
// 发版修改缓存值结构且不兼容时升版本号，整体切换 key 空间，不做兼容读。
const KeyPrefix = "rn:v1"

// 实体/计数器 key 前缀集中声明（key 空间全览：构造、SCAN、回读解析共用一处，防双源漂移）
const (
	postDetailPrefix     = KeyPrefix + ":post:detail:"
	postStatPrefix       = KeyPrefix + ":post:stat:"
	postViewPrefix       = KeyPrefix + ":post:view:"
	userProfilePrefix    = KeyPrefix + ":user:profile:"
	userStatPrefix       = KeyPrefix + ":user:stat:"
	authorTimelinePrefix = KeyPrefix + ":author:timeline:"
	authorBigVPrefix     = KeyPrefix + ":author:bigv:"
)

// idKey 实体/计数器 key 的唯一构造点：prefix + 十进制 id
func idKey(prefix string, id int64) string {
	return prefix + strconv.FormatInt(id, 10)
}

// idFromKey idKey 的逆运算：剥前缀解析 id（构造/回读同源配对，
// round-trip 不变式见 keys_test.go；非法 key 按错误上抛）
func idFromKey(prefix, key string) (int64, error) {
	return strconv.ParseInt(strings.TrimPrefix(key, prefix), 10, 64)
}

// NullSentinel 空哨兵值：缓存"查询结果为空"，防缓存穿透（恶意/误用的不存在 ID 打穿到 DB）。
// 真实缓存值均为 JSON 对象（以 '{' 开头），与哨兵天然可区分。
var NullSentinel = []byte("__nil__")

// IsNullSentinel 判断缓存值是否为空哨兵
func IsNullSentinel(val []byte) bool {
	return bytes.Equal(val, NullSentinel)
}

// JitterTTL 返回 base ± pct 的随机抖动 TTL，防缓存雪崩（同批 key 同时过期、同时回源）。
// pct 为抖动比例（如 0.1 表示 ±10%）；base <= 0 或 pct <= 0 时原样返回。
func JitterTTL(base time.Duration, pct float64) time.Duration {
	if base <= 0 || pct <= 0 {
		return base
	}
	// [1-pct, 1+pct) 线性分布
	f := 1 + pct*(2*rand.Float64()-1)
	return time.Duration(float64(base) * f)
}

// MGetKeys 批量读底层缓存：keys 由 keyFn 按 ids 构造，返回值与 ids 等长对齐（miss 元素为 nil）。
// 空入参不触缓存。供各实体缓存（User/Post/...）共用。
func MGetKeys(ctx context.Context, c Cache, ids []int64, keyFn func(int64) string) ([][]byte, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = keyFn(id)
	}
	return c.MGet(ctx, keys)
}

// DelKeys 批量删除底层缓存 key（Cache.Del 幂等，缺失静默跳过）；空入参不触缓存。
// 供各实体缓存（User/Post/...）共用。
func DelKeys(ctx context.Context, c Cache, ids []int64, keyFn func(int64) string) error {
	if len(ids) == 0 {
		return nil
	}
	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = keyFn(id)
	}
	return c.Del(ctx, keys...)
}
