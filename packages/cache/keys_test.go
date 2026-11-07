package cache

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKeyForms key 形态兼容性锚点：全部 key 字符串锁定（防止误改导致线上 key 空间错乱、
// 旧 key 靠 TTL 才能出清）——本测试失败即 key 契约变更，须显式决策（升 KeyPrefix 版本或回退）
func TestKeyForms(t *testing.T) {
	assert.Equal(t, "rn:v1:post:detail:2001", postDetailKey(2001))
	assert.Equal(t, "rn:v1:post:stat:2001", postStatKey(2001))
	assert.Equal(t, "rn:v1:post:view:2001", postViewKey(2001))
	assert.Equal(t, "rn:v1:user:profile:2001", userProfileKey(2001))
	assert.Equal(t, "rn:v1:user:stat:2001", userStatKey(2001))
}

// TestIDKeyRoundTrip 构造/回读同源不变式：任意 id 经 idKey → idFromKey 往返恒等
func TestIDKeyRoundTrip(t *testing.T) {
	for _, prefix := range []string{postDetailPrefix, postStatPrefix, postViewPrefix, userProfilePrefix, userStatPrefix} {
		for _, id := range []int64{0, 1, 2001, -1, 1 << 62, -1 << 62} {
			got, err := idFromKey(prefix, idKey(prefix, id))
			require.NoError(t, err, "prefix=%s id=%d", prefix, id)
			assert.Equal(t, id, got)
		}
	}

	// postView 配对同源（取走增量的回读路径依赖此不变式）
	got, err := postViewID(postViewKey(2001))
	require.NoError(t, err)
	assert.Equal(t, int64(2001), got)

	// 非法 key 按错误上抛（垃圾丢弃路径依赖）
	_, err = postViewID(postViewPrefix + "abc")
	assert.Error(t, err)
}
