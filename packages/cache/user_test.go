package cache_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"rednote/config"
	userv1 "rednote/gen/go/user/v1"
	common "rednote/internal/common/models"
	media "rednote/internal/media/models"
	user "rednote/internal/user/models"
	"rednote/packages/cache"
	"rednote/test/testredis"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

// flakyCache 可注入故障的 cache.Cache 包装（错误路径用例的测试替身）：
// err 字段非 nil 时对应操作返回该错误；corrupt 非 nil 时改写 MGet 命中值模拟缓存值损坏。
// 仅限单 goroutine 使用。
type flakyCache struct {
	*testredis.Client
	mgetErr error
	setErr  error
	delErr  error
	incrErr error
	corrupt func(val []byte) []byte
}

func (f *flakyCache) MGet(ctx context.Context, keys []string) ([][]byte, error) {
	if f.mgetErr != nil {
		return nil, f.mgetErr
	}
	vals, err := f.Client.MGet(ctx, keys)
	if err != nil {
		return nil, err
	}
	if f.corrupt != nil {
		for i := range vals {
			if vals[i] != nil {
				vals[i] = f.corrupt(vals[i])
			}
		}
	}
	return vals, nil
}

func (f *flakyCache) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	if f.setErr != nil {
		return f.setErr
	}
	return f.Client.Set(ctx, key, val, ttl)
}

// MSet 写错误注入：实体缓存回填走批量口，故须同时覆写批量口
func (f *flakyCache) MSet(ctx context.Context, pairs []cache.KVPair, ttl time.Duration) error {
	if f.setErr != nil {
		return f.setErr
	}
	return f.Client.MSet(ctx, pairs, ttl)
}

func (f *flakyCache) Del(ctx context.Context, keys ...string) error {
	if f.delErr != nil {
		return f.delErr
	}
	return f.Client.Del(ctx, keys...)
}

func (f *flakyCache) IncrCounter(ctx context.Context, key string, ttl time.Duration) error {
	if f.incrErr != nil {
		return f.incrErr
	}
	return f.Client.IncrCounter(ctx, key, ttl)
}

// newProfile 构造全字段用户资料（统计列非零，验证入缓存时被清零）
func newProfile(id int64) *user.UserProfile {
	return &user.UserProfile{
		BaseModel: common.BaseModel{ID: id, CreatedAt: 1700000000000, UpdatedAt: 1700000001000},
		Name:      "小红薯",
		Bio:       "简介",
		Avatar: &media.MediaFile{
			BaseModel:     common.BaseModel{ID: id * 10, CreatedAt: 1700000000000, UpdatedAt: 1700000001000},
			AssetID:       id * 100,
			Type:          0,
			ObjKey:        "avatar/obj",
			Bucket:        "rednote",
			Sha256Sum:     "abc",
			SizeBytes:     2048,
			Format:        0,
			Height:        100,
			Width:         100,
			MediaMetaData: datatypes.JSONMap{"k": "v"},
		},
		Followers:  5,
		Followings: 7,
		Status:     userv1.UserStatus_USER_STATUS_ACTIVE,
		MetaData:   datatypes.JSONMap{"theme": "dark"},
	}
}

// ---------- 资料主体（非统计列） ----------

func TestStoreLoadUserProfile_全字段往返_统计列清零(t *testing.T) {
	tc := testredis.New()
	ec := cache.NewUserEntityCache(tc, nil)
	ctx := context.Background()
	profile := newProfile(3001)

	require.NoError(t, ec.StoreUserProfile(ctx, profile.ID, profile))

	got, err := ec.LoadUserProfiles(ctx, []int64{profile.ID})
	require.NoError(t, err)
	require.Contains(t, got, profile.ID)
	require.NotNil(t, got[profile.ID])

	want := *profile
	want.Followers = 0 // 只缓存非统计列
	want.Followings = 0
	assert.Equal(t, want, *got[profile.ID], "缓存往返应全字段一致且统计列清零")

	// 入缓存的清零动作不得改动调用方实体
	assert.Equal(t, int64(5), profile.Followers)
	assert.Equal(t, int64(7), profile.Followings)
}

func TestStoreUserProfiles_批量回填_两批TTL与计数清零(t *testing.T) {
	tc := testredis.New()
	ec := cache.NewUserEntityCache(tc, &config.Config{
		Cache: config.CacheConfig{ProfileTTL: time.Hour, NullTTL: time.Minute},
	})
	ctx := context.Background()

	hits := map[int64]*user.UserProfile{3001: newProfile(3001), 3002: newProfile(3002)}
	require.NoError(t, ec.StoreUserProfiles(ctx, hits, []int64{3003}))

	// 实体一批 + 空哨兵一批 = 两次 MSet；key 数：2 实体 + 1 哨兵
	assert.Equal(t, 2, tc.MSets, "实体与空哨兵 TTL 不同，分两批提交")
	assert.Equal(t, 3, tc.Sets)

	got, err := ec.LoadUserProfiles(ctx, []int64{3001, 3002, 3003})
	require.NoError(t, err)
	require.Len(t, got, 3)
	for _, id := range []int64{3001, 3002} {
		require.NotNil(t, got[id])
		assert.Zero(t, got[id].Followers, "入缓存统计列应清零")
		assert.Zero(t, got[id].Followings)
		assert.Equal(t, "小红薯", got[id].Name)
		require.NotNil(t, got[id].Avatar, "嵌入头像随实体一并回填")
	}
	assert.Nil(t, got[3003], "missing 写空哨兵")

	// 调用方实体不被修改
	assert.Equal(t, int64(5), hits[3001].Followers)

	t.Run("hits 中 nil 值按空哨兵处理", func(t *testing.T) {
		tc := testredis.New()
		ec := cache.NewUserEntityCache(tc, nil)
		require.NoError(t, ec.StoreUserProfiles(ctx, map[int64]*user.UserProfile{77: nil}, nil))
		got, err := ec.LoadUserProfiles(ctx, []int64{77})
		require.NoError(t, err)
		require.Contains(t, got, int64(77))
		assert.Nil(t, got[77])
	})

	t.Run("空入参零网络往返", func(t *testing.T) {
		tc := testredis.New()
		ec := cache.NewUserEntityCache(tc, nil)
		require.NoError(t, ec.StoreUserProfiles(ctx, nil, nil))
		assert.Zero(t, tc.MSets)
		assert.Zero(t, tc.Sets)
	})
}

func TestStoreUserProfile_Nil写空哨兵_与实体互相覆盖(t *testing.T) {
	tc := testredis.New()
	ec := cache.NewUserEntityCache(tc, nil)
	ctx := context.Background()
	const id int64 = 3001

	// 实体 → 空哨兵
	require.NoError(t, ec.StoreUserProfile(ctx, id, newProfile(id)))
	require.NoError(t, ec.StoreUserProfile(ctx, id, nil))
	got, err := ec.LoadUserProfiles(ctx, []int64{id})
	require.NoError(t, err)
	require.Contains(t, got, id)
	assert.Nil(t, got[id], "空哨兵应映射为 nil")

	// 空哨兵 → 实体
	require.NoError(t, ec.StoreUserProfile(ctx, id, newProfile(id)))
	got, err = ec.LoadUserProfiles(ctx, []int64{id})
	require.NoError(t, err)
	require.NotNil(t, got[id])
	assert.Equal(t, "小红薯", got[id].Name)
}

func TestLoadUserProfiles_Miss空入参与去重(t *testing.T) {
	tc := testredis.New()
	ec := cache.NewUserEntityCache(tc, nil)
	ctx := context.Background()

	t.Run("未缓存的 uid 不在返回 map 中（miss）", func(t *testing.T) {
		got, err := ec.LoadUserProfiles(ctx, []int64{999})
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("空 ids 返回空 map 且不触缓存", func(t *testing.T) {
		gets := tc.Gets
		got, err := ec.LoadUserProfiles(ctx, []int64{})
		require.NoError(t, err)
		assert.Empty(t, got)
		assert.Equal(t, gets, tc.Gets)
	})

	t.Run("重复 ids 返回单条", func(t *testing.T) {
		require.NoError(t, ec.StoreUserProfile(ctx, 3001, newProfile(3001)))
		got, err := ec.LoadUserProfiles(ctx, []int64{3001, 3001})
		require.NoError(t, err)
		assert.Len(t, got, 1)
	})

	t.Run("id 参数为 key 依据（与实体内 ID 解耦）", func(t *testing.T) {
		require.NoError(t, ec.StoreUserProfile(ctx, 55, newProfile(777)))
		got, err := ec.LoadUserProfiles(ctx, []int64{55})
		require.NoError(t, err)
		require.Contains(t, got, int64(55))

		got, err = ec.LoadUserProfiles(ctx, []int64{777})
		require.NoError(t, err)
		assert.NotContains(t, got, int64(777), "key 依据应为入参 id 而非实体 ID")
	})
}

// ---------- 统计列 ----------

func TestStoreLoadUserStat_全字段往返与哨兵(t *testing.T) {
	tc := testredis.New()
	ec := cache.NewUserEntityCache(tc, nil)
	ctx := context.Background()
	stat := &user.UserStat{ID: 3001, Followers: 5, Followings: 7}

	require.NoError(t, ec.StoreUserStat(ctx, stat.ID, stat))

	got, err := ec.LoadUserStats(ctx, []int64{stat.ID, 3002, 3003})
	require.NoError(t, err)
	require.Contains(t, got, stat.ID)
	assert.Equal(t, stat, got[stat.ID], "统计应全字段一致")
	assert.NotContains(t, got, int64(3002), "未缓存 uid 应为 miss")
	assert.NotContains(t, got, int64(3003))

	require.NoError(t, ec.StoreUserStat(ctx, 3002, nil))
	got, err = ec.LoadUserStats(ctx, []int64{3002})
	require.NoError(t, err)
	require.Contains(t, got, int64(3002))
	assert.Nil(t, got[3002], "空哨兵应映射为 nil")
}

func TestStoreUserStats_批量写入_一轮MSet(t *testing.T) {
	tc := testredis.New()
	ec := cache.NewUserEntityCache(tc, nil)
	ctx := context.Background()

	stats := map[int64]*user.UserStat{
		3001: {ID: 3001, Followers: 5, Followings: 7},
		3002: {ID: 3002, Followers: 8, Followings: 9},
		3003: nil, // 按空哨兵处理
	}
	require.NoError(t, ec.StoreUserStats(ctx, stats))
	assert.Equal(t, 2, tc.MSets, "实体一批 + 空哨兵一批")
	assert.Equal(t, 3, tc.Sets)

	got, err := ec.LoadUserStats(ctx, []int64{3001, 3002, 3003})
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, stats[3001], got[3001])
	assert.Equal(t, stats[3002], got[3002])
	assert.Nil(t, got[3003], "nil 值应写为空哨兵")

	t.Run("空入参零网络往返", func(t *testing.T) {
		tc := testredis.New()
		ec := cache.NewUserEntityCache(tc, nil)
		require.NoError(t, ec.StoreUserStats(ctx, nil))
		assert.Zero(t, tc.MSets)
		assert.Zero(t, tc.Sets)
	})
}

// ---------- 失效（Del） ----------

func TestDel_失效独立且幂等(t *testing.T) {
	tc := testredis.New()
	ec := cache.NewUserEntityCache(tc, nil)
	ctx := context.Background()
	const id int64 = 3001

	require.NoError(t, ec.StoreUserProfile(ctx, id, newProfile(id)))
	require.NoError(t, ec.StoreUserStat(ctx, id, &user.UserStat{ID: id, Followers: 5, Followings: 7}))

	require.NoError(t, ec.DelUserProfiles(ctx, []int64{id}))
	got, err := ec.LoadUserProfiles(ctx, []int64{id})
	require.NoError(t, err)
	assert.NotContains(t, got, id)

	stats, err := ec.LoadUserStats(ctx, []int64{id})
	require.NoError(t, err)
	assert.Contains(t, stats, id, "DelUserProfiles 不应波及统计 key 空间")

	require.NoError(t, ec.DelUserStats(ctx, []int64{id}))
	stats, err = ec.LoadUserStats(ctx, []int64{id})
	require.NoError(t, err)
	assert.NotContains(t, stats, id)

	t.Run("重复删除幂等无错", func(t *testing.T) {
		require.NoError(t, ec.DelUserProfiles(ctx, []int64{id}))
		require.NoError(t, ec.DelUserStats(ctx, []int64{id}))
	})

	t.Run("空 ids 不触缓存", func(t *testing.T) {
		dels := tc.Dels
		require.NoError(t, ec.DelUserProfiles(ctx, []int64{}))
		require.NoError(t, ec.DelUserStats(ctx, []int64{}))
		assert.Equal(t, dels, tc.Dels)
	})
}

// ---------- 错误路径 ----------

func TestCache错误上抛_可降级(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("redis down")

	t.Run("批量读错误上抛", func(t *testing.T) {
		ec := cache.NewUserEntityCache(&flakyCache{Client: testredis.New(), mgetErr: sentinel}, nil)
		_, err := ec.LoadUserProfiles(ctx, []int64{3001})
		require.ErrorIs(t, err, sentinel)
		assert.ErrorContains(t, err, "cache user profile mget")

		_, err = ec.LoadUserStats(ctx, []int64{3001})
		require.ErrorIs(t, err, sentinel)
		assert.ErrorContains(t, err, "cache user stat mget")
	})

	t.Run("写错误上抛（实体与空哨兵）", func(t *testing.T) {
		ec := cache.NewUserEntityCache(&flakyCache{Client: testredis.New(), setErr: sentinel}, nil)
		err := ec.StoreUserProfile(ctx, 3001, newProfile(3001))
		require.ErrorIs(t, err, sentinel)
		assert.ErrorContains(t, err, "cache user profile set")

		err = ec.StoreUserProfile(ctx, 3001, nil)
		require.ErrorIs(t, err, sentinel)
		assert.ErrorContains(t, err, "null sentinel")

		err = ec.StoreUserStat(ctx, 3001, &user.UserStat{ID: 3001})
		require.ErrorIs(t, err, sentinel)
		assert.ErrorContains(t, err, "cache user stat set")
	})

	t.Run("删除错误上抛", func(t *testing.T) {
		ec := cache.NewUserEntityCache(&flakyCache{Client: testredis.New(), delErr: sentinel}, nil)
		err := ec.DelUserProfiles(ctx, []int64{3001})
		require.ErrorIs(t, err, sentinel)
		assert.ErrorContains(t, err, "cache user profile del")

		err = ec.DelUserStats(ctx, []int64{3001})
		require.ErrorIs(t, err, sentinel)
		assert.ErrorContains(t, err, "cache user stat del")
	})
}

func Test损坏值按错误上抛(t *testing.T) {
	ctx := context.Background()
	fc := &flakyCache{
		Client:  testredis.New(),
		corrupt: func(val []byte) []byte { return []byte("not-json") },
	}
	ec := cache.NewUserEntityCache(fc, nil)

	require.NoError(t, ec.StoreUserProfile(ctx, 3001, newProfile(3001)))
	_, err := ec.LoadUserProfiles(ctx, []int64{3001})
	require.Error(t, err, "序列化损坏应上抛而非静默丢弃")
	assert.ErrorContains(t, err, "cache user profile unmarshal")

	require.NoError(t, ec.StoreUserStat(ctx, 3001, &user.UserStat{ID: 3001}))
	_, err = ec.LoadUserStats(ctx, []int64{3001})
	require.Error(t, err)
	assert.ErrorContains(t, err, "cache user stat unmarshal")
}

// ---------- TTL ----------

func TestTTL_配置生效与默认回退(t *testing.T) {
	ctx := context.Background()

	t.Run("ProfileTTL 到期后按 miss 处理", func(t *testing.T) {
		ec := cache.NewUserEntityCache(testredis.New(), &config.Config{
			Cache: config.CacheConfig{ProfileTTL: 20 * time.Millisecond},
		})
		require.NoError(t, ec.StoreUserProfile(ctx, 3001, newProfile(3001)))

		require.Eventually(t, func() bool {
			got, err := ec.LoadUserProfiles(ctx, []int64{3001})
			return err == nil && len(got) == 0
		}, 2*time.Second, 5*time.Millisecond, "配置 TTL（含抖动）到期后应 miss")
	})

	t.Run("空哨兵按 NullTTL 到期", func(t *testing.T) {
		ec := cache.NewUserEntityCache(testredis.New(), &config.Config{
			Cache: config.CacheConfig{NullTTL: 20 * time.Millisecond},
		})
		require.NoError(t, ec.StoreUserProfile(ctx, 3001, nil))

		require.Eventually(t, func() bool {
			got, err := ec.LoadUserProfiles(ctx, []int64{3001})
			return err == nil && len(got) == 0
		}, 2*time.Second, 5*time.Millisecond, "空哨兵到期后应 miss")
	})

	t.Run("cfg 为 nil 时回退默认 TTL 且往返可用", func(t *testing.T) {
		ec := cache.NewUserEntityCache(testredis.New(), nil)
		require.NoError(t, ec.StoreUserProfile(ctx, 3001, newProfile(3001)))
		got, err := ec.LoadUserProfiles(ctx, []int64{3001})
		require.NoError(t, err)
		assert.Contains(t, got, int64(3001))
	})
}

// ---------- 构造校验 ----------

func TestNewUserEntityCache_依赖校验(t *testing.T) {
	require.Panics(t, func() { cache.NewUserEntityCache(nil, nil) }, "Cache 缺失应 fail-fast")
	require.NotPanics(t, func() { cache.NewUserEntityCache(testredis.New(), nil) }, "cfg 可为 nil（TTL 回退 defaults）")
}
