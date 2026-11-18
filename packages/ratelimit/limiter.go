// Package ratelimit 服务端入口限流：token bucket 两级（全局 + per-IP），仅 bff 挂载
// （面客唯一入口，防突发洪水与单客户端刷量）。
//
// 限流判定在 handler 薄壳层（internal/bff/v1/service.go 的 admit）——溢出按面客契约
// 构造业务 fail（InternalError），不产生连接层 error（BFF 例外规则）；
// 与调用端舱壁（packages/rpc/bulkhead.go，防被慢下游拖死）正交互补。
package ratelimit

import (
	"net"
	"sync"
	"time"

	"rednote/config"

	"golang.org/x/time/rate"
)

// ipIdleTTL IP 条目空闲回收阈值：超过该时长未活跃的条目在惰性清理时回收
const ipIdleTTL = 10 * time.Minute

// ipMaxEntries IP 条目数上限：达到上限触发一次惰性清理（防 map 无界增长）。
// 正常流量远低于此值（内网单实例），逼近说明有 IP 洪水
const ipMaxEntries = 4096

// Limiter 两级入口限流器（token bucket）。零值/nil 未配置时全放行（0 = 关闭）：
// global 全局限速桶；per-IP 每 IP 独立桶（条目惰性创建、空闲回收）。
// Allow 快速拒绝不排队（排队把过载变慢失败）
type Limiter struct {
	global *rate.Limiter // nil = 全局限流关闭

	ipQPS   rate.Limit
	ipBurst int

	mu  sync.Mutex
	ips map[string]*ipEntry
}

type ipEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// New 构造入口限流器。qps <= 0 关闭全局限流；ipQPS <= 0 关闭 per-IP 限流；
// burst <= 0 取 1（桶容量至少放行一个）
func New(qps float64, burst int, ipQPS float64, ipBurst int) *Limiter {
	l := &Limiter{ipQPS: rate.Limit(ipQPS), ipBurst: ipBurst}
	if qps > 0 {
		l.global = rate.NewLimiter(rate.Limit(qps), max(burst, 1))
	}
	return l
}

// FromConfig 从配置构造入口限流器（server.rate_limit；0 = 关闭该项）
func FromConfig(cfg *config.Config) *Limiter {
	rl := cfg.Server.RateLimit
	return New(rl.QPS, rl.Burst, rl.IPQPS, rl.IPBurst)
}

// Allow 判定一次请求是否放行（先全局后 per-IP，全部通过才放行）。
// ip 为空只走全局桶
func (l *Limiter) Allow(ip string) bool {
	if l == nil {
		return true
	}
	if l.global != nil && !l.global.Allow() {
		return false
	}
	if l.ipQPS <= 0 {
		return true
	}
	return l.ipLimiter(ip).Allow()
}

// ipLimiter 取/建 per-IP 桶并更新活跃时间；条目达上限时先惰性回收空闲条目
func (l *Limiter) ipLimiter(ip string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.ips[ip]; ok {
		e.lastSeen = time.Now()
		return e.limiter
	}
	if len(l.ips) >= ipMaxEntries {
		l.evictLocked(time.Now())
	}
	if l.ips == nil {
		l.ips = make(map[string]*ipEntry)
	}
	burst := max(l.ipBurst, 1)
	e := &ipEntry{limiter: rate.NewLimiter(l.ipQPS, burst), lastSeen: time.Now()}
	l.ips[ip] = e
	return e.limiter
}

// evictLocked 回收空闲超时条目（调用方持锁）
func (l *Limiter) evictLocked(now time.Time) {
	for ip, e := range l.ips {
		if now.Sub(e.lastSeen) > ipIdleTTL {
			delete(l.ips, ip)
		}
	}
}

// Host 从 connect.Peer().Addr（host:port）提取客户端 IP（限流键）。
// 解析失败时原样返回（仍可作为独立限流键，不会误合并到其他客户端）
func Host(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}
