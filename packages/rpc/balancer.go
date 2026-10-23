package rpc

import (
	"math/rand/v2"
	"sync/atomic"
)

// Balancer 负载均衡策略：从候选实例中选出本次请求的目标。
// 策略可插拔，在 NewTransport 时通过 WithBalancer 指定（默认轮询）。
//
// 约定：
//   - Pick 入参 candidates 保证非空，返回值必须是其中一个元素；
//   - 实现须并发安全（RoundTrip 并发调用）；有状态策略（如轮询游标）
//     建议每个 Transport 持有独立实例，避免多个传输层互相干扰；
//   - 故障转移由 Transport 负责：选中实例拨号失败后会被移出候选集，
//     策略只需关注"当下选谁"，无需感知重试。
type Balancer interface {
	// Pick 从非空候选实例中选出一个作为本次请求的目标
	Pick(candidates []string) string
}

// RoundRobin 轮询策略（默认）：游标递增依次命中候选实例，
// 请求均匀分布在地址池上
type RoundRobin struct {
	offset atomic.Uint64 // 轮询游标
}

// NewRoundRobin 创建轮询策略
func NewRoundRobin() *RoundRobin {
	return &RoundRobin{}
}

// Pick 命中候选集中游标位置的实例
func (r *RoundRobin) Pick(candidates []string) string {
	return candidates[int(r.offset.Add(1)-1)%len(candidates)]
}

// Random 随机策略：均匀随机选实例。无状态无锁，
// 候选顺序固定导致轮询可预测时（如实例数远少于请求量）的替代选择
type Random struct{}

// Pick 随机命中一个候选实例
func (Random) Pick(candidates []string) string {
	return candidates[rand.IntN(len(candidates))]
}

type First struct{}

// Pick 返回候选实例中的第一个
func (First) Pick(candidates []string) string {
	return candidates[0]
}
