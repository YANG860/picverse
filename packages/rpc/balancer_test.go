package rpc

import (
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------- Balancer ----------

func TestRoundRobin_Pick(t *testing.T) {
	r := NewRoundRobin()
	candidates := []string{"a", "b", "c"}
	got := make([]string, 0, 6)
	for range 6 {
		got = append(got, r.Pick(candidates))
	}
	assert.Equal(t, []string{"a", "b", "c", "a", "b", "c"}, got, "应按游标依次轮询")
}

func TestRandom_Pick(t *testing.T) {
	r := Random{}
	candidates := []string{"a", "b"}
	seen := map[string]bool{}
	for range 100 {
		picked := r.Pick(candidates)
		require.Contains(t, candidates, picked, "必须返回候选集内实例")
		seen[picked] = true
	}
	assert.True(t, seen["a"] && seen["b"], "100 次随机应覆盖全部实例")
}

// firstBalancer 测试桩策略：恒选候选集第一个，验证策略可插拔
type firstBalancer struct{ picks atomic.Int64 }

func (f *firstBalancer) Pick(candidates []string) string {
	f.picks.Add(1)
	return candidates[0]
}

// 回归：WithBalancer 指定的策略应决定实例选择（默认轮询被替换）
func TestTransport_WithBalancer(t *testing.T) {
	b1, b2 := newCountingBackend(t), newCountingBackend(t)
	bal := &firstBalancer{}
	tr := newTestTransport(fakeProvider{ServicePost: {host(b1.URL), host(b2.URL)}}, ServicePost, WithBalancer(bal))
	client := &http.Client{Transport: tr}

	for range 3 {
		resp, err := client.Get("http://post/post.v1.PostService/ViewPost")
		require.NoError(t, err)
		resp.Body.Close()
	}
	assert.Equal(t, int64(3), b1.hits.Load(), "恒选首个候选的策略应全部落到 b1")
	assert.Equal(t, int64(0), b2.hits.Load())
	assert.Equal(t, int64(3), bal.picks.Load(), "每个请求应恰好 Pick 一次")
}

// 回归：自定义策略选中的实例拨号失败后，故障转移仍应在剩余候选中继续选
func TestTransport_CustomBalancerFailover(t *testing.T) {
	dead, live := newCountingBackend(t), newCountingBackend(t)
	deadURL := dead.URL
	dead.Close()

	bal := &firstBalancer{}
	tr := newTestTransport(fakeProvider{ServicePost: {host(deadURL), host(live.URL)}}, ServicePost, WithBalancer(bal))
	client := &http.Client{Transport: tr}

	resp, err := client.Get("http://post/post.v1.PostService/ViewPost")
	require.NoError(t, err, "策略选中的实例拨号失败应转移到剩余候选")
	resp.Body.Close()
	assert.Equal(t, int64(1), live.hits.Load())
	assert.Equal(t, int64(2), bal.picks.Load(), "失败实例移出候选集后应再次 Pick")
}

// 回归：默认策略为轮询（不传 WithBalancer 时行为不变）
func TestTransport_DefaultBalancerIsRoundRobin(t *testing.T) {
	b1, b2 := newCountingBackend(t), newCountingBackend(t)
	tr := newTestTransport(fakeProvider{ServicePost: {host(b1.URL), host(b2.URL)}}, ServicePost)
	client := &http.Client{Transport: tr}

	for range 4 {
		resp, err := client.Get("http://post/post.v1.PostService/ViewPost")
		require.NoError(t, err)
		resp.Body.Close()
	}
	assert.Equal(t, int64(2), b1.hits.Load())
	assert.Equal(t, int64(2), b2.hits.Load())
}
