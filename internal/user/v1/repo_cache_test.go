package v1

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"rednote/config"
	userv1 "rednote/gen/go/user/v1"
	common "rednote/internal/common/models"
	media "rednote/internal/media/models"
	models "rednote/internal/user/models"
	"rednote/packages/cache"
	"rednote/packages/logger"
	"rednote/test/testredis"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// cachedRepoEnv 缓存装饰器测试环境：真实 repo（testdb 克隆库）+ testredis 内存替身 +
// 真实 cache.UserEntityCache（序列化/空哨兵/TTL 真实链路）。
// 日志落临时文件（避免测试污染 stdout）。
type cachedRepoEnv struct {
	repo  *CachedUserRepo
	inner *UserRepoImpl
	db    *gorm.DB
	tc    *testredis.Client
	ec    cache.UserEntityCache
	lf    logger.LoggerFactory
}

func newCachedRepoEnv(t *testing.T) *cachedRepoEnv {
	t.Helper()
	inner, db := newRepo(t)
	tc := testredis.New()
	lf, cleanup := logger.NewLoggerFactory(&config.Config{
		Logger: config.LoggerConfig{File: true, Out: filepath.Join(t.TempDir(), "cache-test.log")},
	})
	t.Cleanup(cleanup)

	ec := cache.NewUserEntityCache(tc, &config.Config{
		Cache: config.CacheConfig{
			ProfileTTL: time.Hour,
			NullTTL:    time.Minute,
		},
	})
	return &cachedRepoEnv{
		repo:  NewCachedUserRepo(inner, ec, lf),
		inner: inner,
		db:    db,
		tc:    tc,
		ec:    ec,
		lf:    lf,
	}
}

// errEntityCache 可注入错误的 cache.UserEntityCache 包装（降级路径用例的测试替身）：
// 各 err 非 nil 时对应操作返回该错误，否则委托真实实现。仅限单 goroutine 使用。
type errEntityCache struct {
	cache.UserEntityCache
	loadErr  error
	storeErr error
	delErr   error
}

func (e *errEntityCache) LoadUserProfiles(ctx context.Context, ids []int64) (map[int64]*models.UserProfile, error) {
	if e.loadErr != nil {
		return nil, e.loadErr
	}
	return e.UserEntityCache.LoadUserProfiles(ctx, ids)
}

func (e *errEntityCache) StoreUserProfile(ctx context.Context, id int64, profile *models.UserProfile) error {
	if e.storeErr != nil {
		return e.storeErr
	}
	return e.UserEntityCache.StoreUserProfile(ctx, id, profile)
}

func (e *errEntityCache) DelUserProfiles(ctx context.Context, ids []int64) error {
	if e.delErr != nil {
		return e.delErr
	}
	return e.UserEntityCache.DelUserProfiles(ctx, ids)
}

// ---------- GetUserProfiles 缓存读 ----------

func TestCachedGetUserProfiles(t *testing.T) {
	env := newCachedRepoEnv(t)
	ctx := context.Background()

	t.Run("miss 回源并回填，二次读命中缓存", func(t *testing.T) {
		seedProfile(t, env.db, uid1, "user-1", "bio-1", userv1.UserStatus_USER_STATUS_ACTIVE)

		profiles, err := env.repo.GetUserProfiles(ctx, []int64{uid1})
		require.NoError(t, err)
		require.Len(t, profiles, 1)
		assert.Equal(t, "user-1", profiles[0].Name)

		// 已回填缓存（可经 typed API 读回）
		cached, err := env.ec.LoadUserProfiles(ctx, []int64{uid1})
		require.NoError(t, err)
		require.Contains(t, cached, uid1)
		require.NotNil(t, cached[uid1])
		assert.Equal(t, "user-1", cached[uid1].Name)

		// 绕过装饰器改 DB：缓存命中应返回旧值，证明二次读未回源
		require.NoError(t, env.db.Model(&models.UserProfile{}).Where("id = ?", uid1).Update("name", "db-raw").Error)
		profiles, err = env.repo.GetUserProfiles(ctx, []int64{uid1})
		require.NoError(t, err)
		require.Len(t, profiles, 1)
		assert.Equal(t, "user-1", profiles[0].Name, "应命中缓存而非回源")
	})

	t.Run("全字段往返：缓存命中与回源结果一致", func(t *testing.T) {
		seedProfile(t, env.db, uid2, "user-2", "bio-2", userv1.UserStatus_USER_STATUS_BANNED)

		miss, err := env.repo.GetUserProfiles(ctx, []int64{uid2})
		require.NoError(t, err)
		require.Len(t, miss, 1)
		hit, err := env.repo.GetUserProfiles(ctx, []int64{uid2})
		require.NoError(t, err)
		require.Len(t, hit, 1)
		assert.Equal(t, miss[0], hit[0], "缓存往返不应丢失/篡改任何字段")

		// 全字段显式断言（对照 seedProfile 的已知值）
		p := hit[0]
		assert.Equal(t, uid2, p.ID)
		assert.Equal(t, "user-2", p.Name)
		assert.Equal(t, "bio-2", p.Bio)
		assert.Equal(t, userv1.UserStatus_USER_STATUS_BANNED, p.Status)
		assert.Equal(t, datatypes.JSONMap{"theme": "dark"}, p.MetaData)
		assert.Equal(t, int64(5), p.Followers)
		assert.Equal(t, int64(7), p.Followings)
		require.NotNil(t, p.Avatar, "嵌入的头像媒体文件应完整往返")
		assert.Equal(t, uid2*10, p.Avatar.ID)
		assert.Equal(t, uid2*100, p.Avatar.AssetID)
		assert.Equal(t, "avatar/obj", p.Avatar.ObjKey)
		assert.Equal(t, "rednote", p.Avatar.Bucket)
		assert.Equal(t, int64(2048), p.Avatar.SizeBytes)
		assert.Equal(t, int64(100), p.Avatar.Height)
		assert.Equal(t, int64(100), p.Avatar.Width)
		assert.Equal(t, map[string]any{"k": "v"}, map[string]any(p.Avatar.MediaMetaData))
	})

	t.Run("部分命中只回源缺失项", func(t *testing.T) {
		seedProfile(t, env.db, uid3, "user-3", "bio-3", userv1.UserStatus_USER_STATUS_ACTIVE)
		// uid1 已缓存，uid3 未缓存
		sets := env.tc.Sets
		profiles, err := env.repo.GetUserProfiles(ctx, []int64{uid3, uid1})
		require.NoError(t, err)
		require.Len(t, profiles, 2)
		assert.Equal(t, 1, env.tc.Sets-sets, "只应回填缺失项 uid3")

		_, err = env.ec.LoadUserProfiles(ctx, []int64{uid3})
		require.NoError(t, err)
	})

	t.Run("重复 uid 去重返回", func(t *testing.T) {
		profiles, err := env.repo.GetUserProfiles(ctx, []int64{uid1, uid1})
		require.NoError(t, err)
		assert.Len(t, profiles, 1)
	})

	t.Run("结果按入参首次出现顺序组装", func(t *testing.T) {
		profiles, err := env.repo.GetUserProfiles(ctx, []int64{uid3, uid1, uid2, uid3})
		require.NoError(t, err)
		require.Len(t, profiles, 3)
		assert.Equal(t, uid3, profiles[0].ID, "结果应按入参首次出现顺序")
		assert.Equal(t, uid1, profiles[1].ID)
		assert.Equal(t, uid2, profiles[2].ID)
	})

	t.Run("空入参不访问缓存", func(t *testing.T) {
		gets, sets := env.tc.Gets, env.tc.Sets
		profiles, err := env.repo.GetUserProfiles(ctx, []int64{})
		require.NoError(t, err)
		assert.Empty(t, profiles)
		assert.Equal(t, gets, env.tc.Gets, "空入参不应读缓存")
		assert.Equal(t, sets, env.tc.Sets, "空入参不应写缓存")
	})

	t.Run("不存在 uid 回填空哨兵，二次读不回源", func(t *testing.T) {
		profiles, err := env.repo.GetUserProfiles(ctx, []int64{999999})
		require.NoError(t, err)
		assert.Empty(t, profiles)

		// 空哨兵已立：map 中存在该 uid 且值为 nil（确认不存在）
		cached, err := env.ec.LoadUserProfiles(ctx, []int64{999999})
		require.NoError(t, err)
		require.Contains(t, cached, int64(999999))
		assert.Nil(t, cached[999999], "空哨兵应映射为 nil")

		// 二次读命中哨兵：不回源不回填（fillCache 不再写）
		sets := env.tc.Sets
		profiles, err = env.repo.GetUserProfiles(ctx, []int64{999999})
		require.NoError(t, err)
		assert.Empty(t, profiles)
		assert.Equal(t, sets, env.tc.Sets, "哨兵命中不应再次回填")
	})

	t.Run("统计列不入缓存且每次实时覆盖", func(t *testing.T) {
		// uid2 已缓存；缓存实体统计列应为零值
		cached, err := env.ec.LoadUserProfiles(ctx, []int64{uid2})
		require.NoError(t, err)
		require.NotNil(t, cached[uid2])
		assert.Zero(t, cached[uid2].Followers, "统计列不应入缓存")
		assert.Zero(t, cached[uid2].Followings, "统计列不应入缓存")

		// 绕过装饰器改 DB：昵称仍走缓存旧值，计数必须实时可见
		require.NoError(t, env.db.Model(&models.UserProfile{}).Where("id = ?", uid2).
			Update("name", "db-raw").Update("followers", 100).Update("followings", 200).Error)
		profiles, err := env.repo.GetUserProfiles(ctx, []int64{uid2})
		require.NoError(t, err)
		require.Len(t, profiles, 1)
		assert.Equal(t, "user-2", profiles[0].Name, "资料主体命中缓存")
		assert.Equal(t, int64(100), profiles[0].Followers, "统计列应实时来自轻查")
		assert.Equal(t, int64(200), profiles[0].Followings, "统计列应实时来自轻查")
	})

	t.Run("缓存读失败降级全量回源", func(t *testing.T) {
		seedProfile(t, env.db, uid3+2000, "user-load-fail", "", userv1.UserStatus_USER_STATUS_ACTIVE)
		cacheErr := errors.New("cache down")
		failRepo := NewCachedUserRepo(env.inner, &errEntityCache{UserEntityCache: env.ec, loadErr: cacheErr}, env.lf)

		profiles, err := failRepo.GetUserProfiles(ctx, []int64{uid3 + 2000})
		require.NoError(t, err, "缓存故障不应失败业务请求")
		require.Len(t, profiles, 1)
		assert.Equal(t, "user-load-fail", profiles[0].Name)
	})

	t.Run("回填失败不影响结果", func(t *testing.T) {
		seedProfile(t, env.db, uid3+1000, "user-fresh", "", userv1.UserStatus_USER_STATUS_ACTIVE)
		cacheErr := errors.New("cache down")
		failRepo := NewCachedUserRepo(env.inner, &errEntityCache{UserEntityCache: env.ec, storeErr: cacheErr}, env.lf)

		profiles, err := failRepo.GetUserProfiles(ctx, []int64{uid3 + 1000})
		require.NoError(t, err, "回填失败只应降级，不影响结果")
		require.Len(t, profiles, 1)
		assert.Equal(t, "user-fresh", profiles[0].Name)
	})

	t.Run("并发相同回源结果一致", func(t *testing.T) {
		const n = 8
		var wg sync.WaitGroup
		results := make([][]*models.UserProfile, n)
		errs := make([]error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				results[i], errs[i] = env.repo.GetUserProfiles(ctx, []int64{uid1, uid2})
			}(i)
		}
		wg.Wait()

		for i := 0; i < n; i++ {
			require.NoError(t, errs[i])
			require.Len(t, results[i], 2)
			assert.Equal(t, results[0], results[i], "并发合并回源应得到一致结果")
		}
	})
}

// ---------- GetUserStat 直通 ----------

func TestCachedGetUserStat_直通不触缓存(t *testing.T) {
	env := newCachedRepoEnv(t)
	ctx := context.Background()

	seedProfile(t, env.db, uid1, "user-1", "bio-1", userv1.UserStatus_USER_STATUS_ACTIVE)

	gets, sets, dels := env.tc.Gets, env.tc.Sets, env.tc.Dels
	stats, err := env.repo.GetUserStat(ctx, []int64{uid1, 999999})
	require.NoError(t, err)
	require.Len(t, stats, 1, "不存在的 uid 不返回")

	assert.Equal(t, uid1, stats[0].ID)
	assert.Equal(t, int64(5), stats[0].Followers)
	assert.Equal(t, int64(7), stats[0].Followings)

	assert.Equal(t, gets, env.tc.Gets, "统计直通不应读缓存")
	assert.Equal(t, sets, env.tc.Sets, "统计直通不应写缓存")
	assert.Equal(t, dels, env.tc.Dels, "统计直通不应删缓存")
}

// ---------- 写路径（失效登记） ----------

func TestCachedUserRepo_写后失效(t *testing.T) {
	env := newCachedRepoEnv(t)
	ctx := context.Background()

	t.Run("CreateUser 后清除空哨兵", func(t *testing.T) {
		// 先查询不存在的 uid，立起空哨兵
		profiles, err := env.repo.GetUserProfiles(ctx, []int64{uid1})
		require.NoError(t, err)
		assert.Empty(t, profiles)

		ok, err := env.repo.CreateUser(ctx, newProfile(uid1, "新用户", "新简介"))
		require.NoError(t, err)
		assert.True(t, ok)

		profiles, err = env.repo.GetUserProfiles(ctx, []int64{uid1})
		require.NoError(t, err)
		require.Len(t, profiles, 1, "哨兵/旧值应在创建后失效")
		assert.Equal(t, "新用户", profiles[0].Name)
		assert.Equal(t, "新简介", profiles[0].Bio)
	})

	t.Run("UpdateUserName 后失效", func(t *testing.T) {
		seedProfile(t, env.db, uid2, "旧昵称", "bio-2", userv1.UserStatus_USER_STATUS_ACTIVE)
		_, err := env.repo.GetUserProfiles(ctx, []int64{uid2}) // 预填缓存
		require.NoError(t, err)

		info, err := env.repo.UpdateUserName(ctx, uid2, "新昵称")
		require.NoError(t, err)
		assert.Equal(t, int64(1), info.RowsAffected)

		profiles, err := env.repo.GetUserProfiles(ctx, []int64{uid2})
		require.NoError(t, err)
		require.Len(t, profiles, 1)
		assert.Equal(t, "新昵称", profiles[0].Name, "更新后不应读到缓存旧值")
	})

	t.Run("UpdateUserBio 后失效", func(t *testing.T) {
		_, err := env.repo.GetUserProfiles(ctx, []int64{uid2}) // 重新预填缓存
		require.NoError(t, err)

		info, err := env.repo.UpdateUserBio(ctx, uid2, "新简介")
		require.NoError(t, err)
		assert.Equal(t, int64(1), info.RowsAffected)

		profiles, err := env.repo.GetUserProfiles(ctx, []int64{uid2})
		require.NoError(t, err)
		require.Len(t, profiles, 1)
		assert.Equal(t, "新简介", profiles[0].Bio)
	})

	t.Run("UpdateUserMetadata 后失效", func(t *testing.T) {
		_, err := env.repo.GetUserProfiles(ctx, []int64{uid2}) // 重新预填缓存
		require.NoError(t, err)

		info, err := env.repo.UpdateUserMetadata(ctx, uid2, datatypes.JSONMap{"theme": "light"})
		require.NoError(t, err)
		assert.Equal(t, int64(1), info.RowsAffected)

		profiles, err := env.repo.GetUserProfiles(ctx, []int64{uid2})
		require.NoError(t, err)
		require.Len(t, profiles, 1)
		assert.Equal(t, datatypes.JSONMap{"theme": "light"}, profiles[0].MetaData)
	})

	t.Run("UpdateUserAvatar 后失效", func(t *testing.T) {
		_, err := env.repo.GetUserProfiles(ctx, []int64{uid2}) // 重新预填缓存
		require.NoError(t, err)

		avatar := &media.MediaFile{
			BaseModel:     common.BaseModel{ID: uid2*10 + 1},
			AssetID:       uid2*100 + 1,
			Type:          0,
			ObjKey:        "avatar/new",
			Bucket:        "rednote",
			Sha256Sum:     "def",
			SizeBytes:     4096,
			Format:        0,
			Height:        200,
			Width:         200,
			MediaMetaData: datatypes.JSONMap{"k": "v2"},
		}
		info, err := env.repo.UpdateUserAvatar(ctx, uid2, avatar)
		require.NoError(t, err)
		assert.Equal(t, int64(1), info.RowsAffected)

		profiles, err := env.repo.GetUserProfiles(ctx, []int64{uid2})
		require.NoError(t, err)
		require.Len(t, profiles, 1)
		require.NotNil(t, profiles[0].Avatar)
		assert.Equal(t, "avatar/new", profiles[0].Avatar.ObjKey, "换头像后不应读到缓存旧值")
	})

	t.Run("BanUser 后失效", func(t *testing.T) {
		_, err := env.repo.GetUserProfiles(ctx, []int64{uid2}) // 重新预填缓存
		require.NoError(t, err)

		require.NoError(t, env.repo.BanUser(ctx, uid2))

		profiles, err := env.repo.GetUserProfiles(ctx, []int64{uid2})
		require.NoError(t, err)
		require.Len(t, profiles, 1)
		assert.Equal(t, userv1.UserStatus_USER_STATUS_BANNED, profiles[0].Status)
	})

	t.Run("SetFollow/SetUnFollow 不触碰缓存且计数实时", func(t *testing.T) {
		seedProfile(t, env.db, uid3, "user-3", "", userv1.UserStatus_USER_STATUS_ACTIVE)
		_, err := env.repo.GetUserProfiles(ctx, []int64{uid2, uid3}) // 预填缓存
		require.NoError(t, err)

		gets, sets, dels := env.tc.Gets, env.tc.Sets, env.tc.Dels
		ok, err := env.repo.SetFollow(ctx, uid2, uid3)
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, gets, env.tc.Gets)
		assert.Equal(t, sets, env.tc.Sets)
		assert.Equal(t, dels, env.tc.Dels, "统计不入缓存，follow 不应触碰缓存")

		stats, err := env.repo.GetUserStat(ctx, []int64{uid2, uid3})
		require.NoError(t, err)
		byID := map[int64]*models.UserStat{}
		for _, s := range stats {
			byID[s.ID] = s
		}
		assert.Equal(t, int64(8), byID[uid2].Followings, "fan 关注数 +1")
		assert.Equal(t, int64(6), byID[uid3].Followers, "被关注者粉丝数 +1")

		require.NoError(t, env.repo.SetUnFollow(ctx, uid2, uid3))
		assert.Equal(t, gets, env.tc.Gets)
		assert.Equal(t, sets, env.tc.Sets)
		assert.Equal(t, dels, env.tc.Dels, "取关同样不应触碰缓存")

		stats, err = env.repo.GetUserStat(ctx, []int64{uid2, uid3})
		require.NoError(t, err)
		byID = map[int64]*models.UserStat{}
		for _, s := range stats {
			byID[s.ID] = s
		}
		assert.Equal(t, int64(7), byID[uid2].Followings)
		assert.Equal(t, int64(5), byID[uid3].Followers)
	})

	t.Run("失效失败不阻断业务（降级靠 TTL 兜底）", func(t *testing.T) {
		cacheErr := errors.New("cache down")
		failRepo := NewCachedUserRepo(env.inner, &errEntityCache{UserEntityCache: env.ec, delErr: cacheErr}, env.lf)

		info, err := failRepo.UpdateUserName(ctx, uid2, "降级昵称")
		require.NoError(t, err, "失效失败不应失败业务写入")
		assert.Equal(t, int64(1), info.RowsAffected)
	})
}

// ---------- 事务模式 ----------

func TestCachedWithTX_事务直穿不缓存(t *testing.T) {
	env := newCachedRepoEnv(t)
	ctx := context.Background()

	t.Run("事务内读直穿且见自身写入，不触缓存", func(t *testing.T) {
		gets, sets := env.tc.Gets, env.tc.Sets
		err := env.db.Transaction(func(tx *gorm.DB) error {
			txRepo := env.repo.WithTX(tx)
			ok, err := txRepo.CreateUser(ctx, newProfile(uid1, "tx-user", ""))
			require.NoError(t, err)
			assert.True(t, ok)

			profiles, err := txRepo.GetUserProfiles(ctx, []int64{uid1})
			require.NoError(t, err)
			require.Len(t, profiles, 1, "事务内须读到自身未提交的写入")
			assert.Equal(t, "tx-user", profiles[0].Name)
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, gets, env.tc.Gets, "事务读不应读缓存")
		assert.Equal(t, sets, env.tc.Sets, "事务读不应写缓存")
	})

	t.Run("事务内写登记失效，提交后读到新值", func(t *testing.T) {
		seedProfile(t, env.db, uid2, "旧昵称", "", userv1.UserStatus_USER_STATUS_ACTIVE)
		_, err := env.repo.GetUserProfiles(ctx, []int64{uid2}) // 预填缓存
		require.NoError(t, err)

		dels := env.tc.Dels
		err = env.db.Transaction(func(tx *gorm.DB) error {
			txRepo := env.repo.WithTX(tx)
			_, err := txRepo.UpdateUserName(ctx, uid2, "tx-新昵称")
			return err
		})
		require.NoError(t, err)
		assert.Greater(t, env.tc.Dels, dels, "事务内写应登记失效")

		profiles, err := env.repo.GetUserProfiles(ctx, []int64{uid2})
		require.NoError(t, err)
		require.Len(t, profiles, 1)
		assert.Equal(t, "tx-新昵称", profiles[0].Name)
	})
}

// ---------- 构造校验 ----------

func TestNewCachedUserRepo_依赖校验(t *testing.T) {
	require.Panics(t, func() { NewCachedUserRepo(nil, nil, nil) }, "inner 缺失应 fail-fast")
	require.Panics(t, func() { NewCachedUserRepo(&UserRepoImpl{}, nil, nil) }, "cache 缺失应 fail-fast")
}
