package ratelimit

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLimiter_GlobalBurst 全局桶边界：burst 内放行、超出拒绝、速率恢复后放行
func TestLimiter_GlobalBurst(t *testing.T) {
	l := New(10, 2, 0, 0) // 10 qps，突发 2

	require.True(t, l.Allow("1.1.1.1"), "突发桶第 1 个")
	require.True(t, l.Allow("2.2.2.2"), "突发桶第 2 个")
	require.False(t, l.Allow("3.3.3.3"), "超出突发桶拒绝")

	time.Sleep(120 * time.Millisecond) // 10 qps 下补充 >1 个令牌
	assert.True(t, l.Allow("4.4.4.4"), "速率恢复后放行")
}

// TestLimiter_IPsIsolated per-IP 桶隔离：IP A 耗尽不影响 IP B
func TestLimiter_IPsIsolated(t *testing.T) {
	l := New(0, 0, 1, 1) // 仅 per-IP：1 qps，突发 1

	require.True(t, l.Allow("1.1.1.1"))
	require.False(t, l.Allow("1.1.1.1"), "同 IP 突发耗尽拒绝")
	assert.True(t, l.Allow("2.2.2.2"), "其他 IP 不受影响")
}

// TestLimiter_GlobalAndIPBothGates 两级同时开启：任一层拒绝即拒绝
func TestLimiter_GlobalAndIPBothGates(t *testing.T) {
	l := New(1, 1, 1, 1) // 全局与 per-IP 均只有 1 个突发

	require.True(t, l.Allow("1.1.1.1"))
	require.False(t, l.Allow("1.1.1.1"), "全局与 per-IP 均耗尽（任一层拒绝即拒绝）")
	require.False(t, l.Allow("2.2.2.2"), "全局层拒绝（全局桶已耗尽，新 IP 也放不进）")
}

// TestLimiter_Disabled 全 0 = 关闭（安全默认）；nil 全放行
func TestLimiter_Disabled(t *testing.T) {
	l := New(0, 0, 0, 0)
	for range 5 {
		assert.True(t, l.Allow("1.1.1.1"), "关闭时限流不生效")
	}

	var nilLimiter *Limiter
	assert.True(t, nilLimiter.Allow("1.1.1.1"), "nil 防御：全放行")
}

// TestLimiter_EvictIdle 惰性清理：空闲超时的 IP 条目被回收，活跃条目保留
func TestLimiter_EvictIdle(t *testing.T) {
	l := New(0, 0, 1, 1)
	require.True(t, l.Allow("idle-ip"))
	require.True(t, l.Allow("active-ip"))

	// 手工把 idle-ip 的活跃时间拨回过去，模拟空闲超时
	l.mu.Lock()
	l.ips["idle-ip"].lastSeen = time.Now().Add(-ipIdleTTL - time.Minute)
	l.mu.Unlock()

	l.mu.Lock()
	l.evictLocked(time.Now())
	_, idleKept := l.ips["idle-ip"]
	_, activeKept := l.ips["active-ip"]
	l.mu.Unlock()

	assert.False(t, idleKept, "空闲超时条目应被回收")
	assert.True(t, activeKept, "活跃条目保留")
}

// TestHost Peer 地址提取客户端 IP；解析失败原样返回
func TestHost(t *testing.T) {
	assert.Equal(t, "10.0.0.1", Host("10.0.0.1:52344"))
	assert.Equal(t, "::1", Host("[::1]:8080"))
	assert.Equal(t, "no-port", Host("no-port"), "解析失败原样返回")
}
