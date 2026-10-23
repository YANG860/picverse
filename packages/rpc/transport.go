package rpc

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// defaultCooldown 实例被动冷却时长：拨号失败后的窗口期内不再选中该实例。
// k8s 部署后平台 readinessProbe 负责主动健康检查，此处仅作滚动更新瞬间的兜底。
const defaultCooldown = 5 * time.Second

// Transport 服务发现传输层：实现 http.RoundTripper，是 connectrpc 客户端
// 挂接发现/负载均衡/故障转移的唯一集成点（connect 基于 net/http，
// 不走 grpc-go 的 resolver/balancer 体系）。
//
// 行为：
//   - 每次请求解析服务地址池，池内按负载均衡策略选实例
//     （默认轮询，经 WithBalancer 可换随机或自定义策略）；
//   - 拨号失败→ 冷却该实例并尝试下一实例；
//     仅在"请求确定未到达服务端"时重试，收到任何响应
//     后不再重试——写操作不会因重试产生重复写；
//   - 全部实例处于冷却期时仍强制尝试（冷却仅是优选信号）。
type Transport struct {
	provider EndpointProvider
	service  ServiceName
	base     http.RoundTripper
	log      *slog.Logger

	balancer    Balancer
	mu          sync.Mutex
	cooldown    map[string]time.Time // 实例地址 -> 冷却截止时间
	cooldownDur time.Duration
}

// TransportOption 传输层可选项
type TransportOption func(*Transport)

// WithCooldown 覆盖默认冷却时长（测试用）
func WithCooldown(d time.Duration) TransportOption {
	return func(t *Transport) { t.cooldownDur = d }
}

// WithBaseTransport 覆盖底层 RoundTripper（测试注入用，默认 http.DefaultTransport）
func WithBaseTransport(base http.RoundTripper) TransportOption {
	return func(t *Transport) { t.base = base }
}

// WithBalancer 指定负载均衡策略（默认 NewRoundRobin；nil 忽略）。
// 有状态策略请每个 Transport 传入独立实例，避免多传输层共享游标
func WithBalancer(b Balancer) TransportOption {
	return func(t *Transport) {
		if b != nil {
			t.balancer = b
		}
	}
}

// NewTransport 创建绑定单个服务的发现传输层
func NewTransport(provider EndpointProvider, balancer Balancer, service ServiceName, lf *slog.Logger, opts ...TransportOption) *Transport {
	t := &Transport{
		provider:    provider,
		service:     service,
		base:        http.DefaultTransport,
		log:         lf,
		balancer:    balancer,
		cooldown:    make(map[string]time.Time),
		cooldownDur: defaultCooldown,
	}
	if t.log == nil {
		// 归一为非 nil logger：调用点无需判空，无日志诉求时静默丢弃
		t.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	if t.balancer == nil {
		t.balancer = NewRoundRobin()
	}
	for _, opt := range opts {
		opt(t)
	}
	if t.balancer == nil {
		t.balancer = NewRoundRobin()
	}
	return t
}

// RoundTrip 解析地址池并转发请求，拨号失败自动故障转移到下一实例
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	endpoints, err := t.provider.Resolve(req.Context(), t.service)
	if err != nil {
		return nil, fmt.Errorf("resolve %s endpoints: %w", t.service, err)
	}
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("no endpoints for %s", t.service)
	}

	// 健康实例优先构建候选集
	candidates := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		if !t.isCooling(endpoint) {
			candidates = append(candidates, endpoint)
		}
	}
	if len(candidates) == 0 {
		// 全部实例处于冷却期：仍强制尝试——冷却只是优选信号而非熔断，
		// 不能因为全冷却就拒绝服务
		candidates = append(candidates, endpoints...)
	}

	var lastErr error
	for len(candidates) > 0 {
		// 策略选目标；拨号失败后移出候选集，让策略在剩余实例中继续选
		endpoint := t.balancer.Pick(candidates)
		candidates = removeCandidate(candidates, endpoint)
		resp, err := t.roundTripTo(req, endpoint)
		if err == nil {
			return resp, nil
		}
		if !isDialError(err) {
			// 非拨号错误（TLS 握手后错误/超时/响应中途中断等）：
			// 请求可能已被服务端处理，重试会有重复写风险，原样返回
			return nil, err
		}

		// 拨号失败：该实例确定未收到请求，冷却并转移到下一实例
		lastErr = err
		t.markCooling(endpoint)
		t.log.Warn("endpoint dial failed, failover", "service", t.service, "endpoint", endpoint, "error", err)
	}
	return nil, fmt.Errorf("request to %s failed after failover: %w", t.service, lastErr)
}

// removeCandidate 从候选集中移除已尝试的实例（按值首个匹配），
// 保证同一请求内不重复尝试同一实例
func removeCandidate(candidates []string, endpoint string) []string {
	for i, c := range candidates {
		if c == endpoint {
			return append(candidates[:i], candidates[i+1:]...)
		}
	}
	return candidates
}

// roundTripTo 克隆请求并发往指定实例（克隆避免多次尝试间共享可变 URL）
func (t *Transport) roundTripTo(req *http.Request, endpoint string) (*http.Response, error) {
	clone := req.Clone(req.Context())
	scheme, host, err := splitEndpoint(endpoint, clone.URL.Scheme)
	if err != nil {
		return nil, err
	}
	clone.URL.Scheme = scheme
	clone.URL.Host = host
	// Host 头跟随实际目标：清除调用方在占位 URL 上设置的 req.Host（如 "auth"），
	// 避免报文 Host 头与真实目标不一致（req.Host 只影响 Host 头，不影响拨号）
	clone.Host = ""
	// 本传输层仅在拨号失败时重试（见 isDialError），net/http 在连接建立前
	// 不会读取 body，各次尝试天然不争抢 body；即便如此仍经 GetBody 重建 body，
	// 确保未来放宽重试条件时不会把已消费的 body 发给下一个实例
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, fmt.Errorf("reset request body: %w", err)
		}
		clone.Body = body
	}
	return t.base.RoundTrip(clone)
}

// splitEndpoint 解析实例地址：支持 "scheme://host:port" 与 "host:port" 两种形式，
// 后者沿用请求原始 scheme（connect 客户端 base URL 统一为 http）。
// 实例地址只取 scheme 与 host，path/query 一律丢弃（实例地址不应携带路径）
func splitEndpoint(endpoint, fallbackScheme string) (scheme, host string, err error) {
	if !strings.Contains(endpoint, "://") {
		return fallbackScheme, endpoint, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", "", fmt.Errorf("invalid endpoint %q: %w", endpoint, err)
	}
	return u.Scheme, u.Host, nil
}

// isCooling 实例是否处于被动冷却期
func (t *Transport) isCooling(endpoint string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	until, ok := t.cooldown[endpoint]
	return ok && time.Now().Before(until)
}

// markCooling 将实例冷却至当前时间加冷却窗口，顺带清理已过期条目，
// 防止动态端点（注册中心/k8s）场景下 map 无界增长
func (t *Transport) markCooling(endpoint string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	for ep, until := range t.cooldown {
		if !now.Before(until) {
			delete(t.cooldown, ep)
		}
	}
	t.cooldown[endpoint] = now.Add(t.cooldownDur)
}

// isDialError 判断是否为拨号层错误（请求确定未到达服务端，可安全转移重试）
func isDialError(err error) bool {
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}
