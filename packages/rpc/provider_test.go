package rpc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"rednote/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeProvider 测试桩：按服务名返回固定地址池
type fakeProvider map[ServiceName][]string

func (f fakeProvider) Resolve(_ context.Context, service ServiceName) ([]string, error) {
	endpoints, ok := f[service]
	if !ok {
		return nil, fmt.Errorf("unknown service: %s", service)
	}
	return endpoints, nil
}

// countingBackend 计数后端：记录被请求次数，统一返回 200
type countingBackend struct {
	*httptest.Server
	hits atomic.Int64
}

func newCountingBackend(t *testing.T) *countingBackend {
	t.Helper()
	cb := &countingBackend{}
	cb.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cb.hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(cb.Server.Close)
	return cb
}

func newTestTransport(p EndpointProvider, svc ServiceName, opts ...TransportOption) *Transport {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewTransport(p, NewRoundRobin(), svc, quiet, opts...)
}

func testConfig() *config.Config {
	return &config.Config{Services: config.ServicesConfig{
		Auth: "127.0.0.1:8081", User: "127.0.0.1:8083", Media: "127.0.0.1:8080", Post: "127.0.0.1:8082",
	}}
}

// ---------- StaticProvider ----------

func TestStaticProvider(t *testing.T) {
	t.Run("解析四服务地址", func(t *testing.T) {
		p := NewStaticProvider(testConfig())

		endpoints, err := p.Resolve(context.Background(), ServicePost)
		require.NoError(t, err)
		assert.Equal(t, []string{"127.0.0.1:8082"}, endpoints)
	})

	t.Run("未知服务返回错误", func(t *testing.T) {
		p := NewStaticProvider(testConfig())
		_, err := p.Resolve(context.Background(), "nope")
		assert.Error(t, err)
	})

	t.Run("地址缺失启动即失败（fail-fast）", func(t *testing.T) {
		cfg := testConfig()
		cfg.Services.Post = ""
		assert.PanicsWithError(t, "services.post is required", func() {
			NewStaticProvider(cfg)
		})
	})
}

// ---------- Transport ----------

func TestTransport_RoundRobin(t *testing.T) {
	b1, b2 := newCountingBackend(t), newCountingBackend(t)
	tr := newTestTransport(fakeProvider{ServicePost: {host(b1.URL), host(b2.URL)}}, ServicePost)
	client := &http.Client{Transport: tr}

	// 4 次请求应在两个实例间交替
	for range 4 {
		resp, err := client.Get("http://post/post.v1.PostService/ViewPost")
		require.NoError(t, err)
		resp.Body.Close()
	}
	assert.Equal(t, int64(2), b1.hits.Load())
	assert.Equal(t, int64(2), b2.hits.Load())
}

// 回归：拨号失败（请求确定未到达服务端）应自动转移到下一实例并冷却失败实例
func TestTransport_DialFailureFailover(t *testing.T) {
	dead, live := newCountingBackend(t), newCountingBackend(t)
	deadURL := dead.URL
	dead.Close() // 关闭后拨号必然失败

	tr := newTestTransport(fakeProvider{ServicePost: {host(deadURL), host(live.URL)}}, ServicePost)

	client := &http.Client{Transport: tr}
	resp, err := client.Get("http://post/post.v1.PostService/ViewPost")
	require.NoError(t, err, "第一实例拨号失败应转移到第二实例")
	resp.Body.Close()
	assert.Equal(t, int64(1), live.hits.Load())
}

// 回归：拨号失败转移到下一实例时，请求体必须完整送达（经 GetBody 重建，不共享已消费 body）
func TestTransport_FailoverReplaysBody(t *testing.T) {
	dead := newCountingBackend(t)
	deadURL := dead.URL
	dead.Close()

	var gotBody atomic.Value
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody.Store(string(b))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(live.Close)

	tr := newTestTransport(fakeProvider{ServicePost: {host(deadURL), host(live.URL)}}, ServicePost)
	client := &http.Client{Transport: tr}
	resp, err := client.Post("http://post/post.v1.PostService/CreatePost",
		"application/json", strings.NewReader(`{"title":"hi"}`))
	require.NoError(t, err, "第一实例拨号失败应带着完整 body 转移到第二实例")
	resp.Body.Close()
	assert.Equal(t, `{"title":"hi"}`, gotBody.Load(), "转移到下一实例时 body 应完整重放")
}

// 回归：调用方设置的 req.Host 占位值不应残留为报文 Host 头（应跟随实际目标）
func TestTransport_ClearsPlaceholderHost(t *testing.T) {
	var gotHost atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost.Store(r.Host)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	tr := newTestTransport(fakeProvider{ServicePost: {host(srv.URL)}}, ServicePost)
	client := &http.Client{Transport: tr}
	req, err := http.NewRequest(http.MethodGet, "http://post/post.v1.PostService/ViewPost", nil)
	require.NoError(t, err)
	req.Host = "post" // 模拟调用方在占位 URL 上设置的虚拟 host

	resp, err := client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, host(srv.URL), gotHost.Load(), "Host 头应为实际实例地址而非占位值")
}

func TestTransport_Cooldown(t *testing.T) {
	dead, live := newCountingBackend(t), newCountingBackend(t)
	deadURL := dead.URL
	dead.Close()

	// 默认冷却窗口长于测试周期：失败实例冷却期内不再被选中
	tr := newTestTransport(fakeProvider{ServicePost: {host(deadURL), host(live.URL)}}, ServicePost)

	client := &http.Client{Transport: tr}
	for range 3 {
		resp, err := client.Get("http://post/post.v1.PostService/ViewPost")
		require.NoError(t, err)
		resp.Body.Close()
	}
	assert.Equal(t, int64(3), live.hits.Load(), "冷却期内应始终选健康实例")
}

func TestTransport_CooldownExpiry(t *testing.T) {
	b1, b2 := newCountingBackend(t), newCountingBackend(t)
	deadURL := b1.URL
	b1.Close()

	// 冷却窗口 50ms：过期后恢复轮询（此时池内只剩健康实例，行为等同）
	tr := newTestTransport(fakeProvider{ServicePost: {host(deadURL), host(b2.URL)}}, ServicePost,
		WithCooldown(50*time.Millisecond))

	client := &http.Client{Transport: tr}
	for range 3 {
		resp, err := client.Get("http://post/post.v1.PostService/ViewPost")
		require.NoError(t, err)
		resp.Body.Close()
		time.Sleep(60 * time.Millisecond)
	}
	assert.Equal(t, int64(3), b2.hits.Load())
}

// 回归：收到响应（含 5xx）后不再重试——写操作不会因重试产生重复写
func TestTransport_NoRetryAfterResponse(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	unused := newCountingBackend(t)

	tr := newTestTransport(fakeProvider{ServicePost: {host(srv.URL), host(unused.URL)}}, ServicePost)
	client := &http.Client{Transport: tr}

	resp, err := client.Get("http://post/post.v1.PostService/ViewPost")
	require.NoError(t, err)
	resp.Body.Close()

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, "5xx 应原样透传")
	assert.Equal(t, int64(1), hits.Load(), "收到响应后不得重试")
	assert.Equal(t, int64(0), unused.hits.Load(), "未触发故障转移")
}

// 全部实例拨号失败时返回错误（不悬挂）
func TestTransport_AllEndpointsDown(t *testing.T) {
	b1, b2 := newCountingBackend(t), newCountingBackend(t)
	u1, u2 := b1.URL, b2.URL
	b1.Close()
	b2.Close()

	tr := newTestTransport(fakeProvider{ServicePost: {host(u1), host(u2)}}, ServicePost)
	client := &http.Client{Transport: tr}

	_, err := client.Get("http://post/post.v1.PostService/ViewPost")
	require.Error(t, err)
	assert.ErrorContains(t, err, "failover")
}

// dialFailTransport 模拟拨号失败的底层传输（请求确定未到达服务端）
type dialFailTransport struct{ attempts atomic.Int64 }

func (d *dialFailTransport) RoundTrip(*http.Request) (*http.Response, error) {
	d.attempts.Add(1)
	return nil, &net.OpError{Op: "dial", Err: errors.New("connection refused")}
}

// 回归：全部实例处于冷却期时仍按轮询强制尝试，不因健康信息滞后拒绝请求
func TestTransport_AllCoolingStillAttempts(t *testing.T) {
	base := &dialFailTransport{}
	tr := newTestTransport(fakeProvider{ServicePost: {"h1:8081", "h2:8082"}}, ServicePost, WithBaseTransport(base))
	client := &http.Client{Transport: tr}

	// 第一轮：两实例拨号失败，均进入冷却
	_, err := client.Get("http://post/post.v1.PostService/ViewPost")
	require.Error(t, err)
	assert.Equal(t, int64(2), base.attempts.Load())

	// 第二轮：全部冷却，仍应强制尝试而非直接拒绝
	_, err = client.Get("http://post/post.v1.PostService/ViewPost")
	require.Error(t, err)
	assert.Equal(t, int64(4), base.attempts.Load(), "全冷却时必须仍发起尝试，不得直接拒绝")
}

// ---------- helpers ----------

// host 去掉 httptest URL 的 scheme 部分，还原为 EndpointProvider 的 host:port 形式
func host(serverURL string) string {
	return serverURL[len("http://"):]
}
