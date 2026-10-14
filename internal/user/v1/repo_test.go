package v1

import (
	"context"
	"errors"
	"testing"
	"time"

	mediav1 "rednote/gen/go/media/v1"
	userv1 "rednote/gen/go/user/v1"
	common "rednote/internal/common/models"
	media "rednote/internal/media/models"
	models "rednote/internal/user/models"
	"rednote/test/testdb"
	"rednote/test/testsnowflake"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// 固定测试 ID，避免依赖雪花生成结果，便于断言
const (
	uid1 int64 = 3001
	uid2 int64 = 3002
	uid3 int64 = 3003
)

func newRepo(t *testing.T) (*UserRepoImpl, *gorm.DB) {
	t.Helper()
	testsnowflake.InitTestSnoflake(3) // 使用不同的 worker ID 避免与 media/auth 测试冲突
	db := testdb.Setup(t)
	return NewUserRepo(db), db
}

// newProfile 构造带头像的用户资料（头像业务必填，见 biz.CreateUser）
func newProfile(id int64, name, bio string) *models.UserProfile {
	return &models.UserProfile{
		BaseModel: common.BaseModel{ID: id},
		Name:      name,
		Bio:       bio,
		Avatar: &media.MediaFile{
			BaseModel:     common.BaseModel{ID: id * 10},
			AssetID:       id * 100,
			Type:          mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN,
			ObjKey:        "avatar/obj",
			Bucket:        "rednote",
			Sha256Sum:     "abc",
			SizeBytes:     2048,
			Format:        mediav1.MediaFormat_MEDIA_FORMAT_WEBP,
			Height:        100,
			Width:         100,
			MediaMetaData: datatypes.JSONMap{"k": "v"},
		},
		Status:   userv1.UserStatus_USER_STATUS_ACTIVE,
		MetaData: datatypes.JSONMap{"theme": "dark"},
	}
}

// seedProfile 造一条用户资料（含头像嵌入列与元数据）
func seedProfile(t *testing.T, db *gorm.DB, id int64, name, bio string, status userv1.UserStatus) *models.UserProfile {
	t.Helper()
	p := &models.UserProfile{
		BaseModel: common.BaseModel{ID: id},
		Name:      name,
		Bio:       bio,
		Avatar: &media.MediaFile{
			BaseModel:     common.BaseModel{ID: id * 10},
			AssetID:       id * 100,
			Type:          mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN,
			ObjKey:        "avatar/obj",
			Bucket:        "rednote",
			Sha256Sum:     "abc",
			SizeBytes:     2048,
			Format:        mediav1.MediaFormat_MEDIA_FORMAT_WEBP,
			Height:        100,
			Width:         100,
			MediaMetaData: datatypes.JSONMap{"k": "v"},
		},
		Followers:  5,
		Followings: 7,
		Status:     status,
		MetaData:   datatypes.JSONMap{"theme": "dark"},
	}
	require.NoError(t, db.Create(p).Error)
	return p
}

// ---------- CreateUser ----------

func TestCreateUser(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	t.Run("创建成功并持久化全部字段", func(t *testing.T) {
		profile := &models.UserProfile{
			BaseModel: common.BaseModel{ID: uid1},
			Name:      "小红薯",
			Bio:       "简介",
			Avatar: &media.MediaFile{
				BaseModel: common.BaseModel{ID: uid1 * 10},
				AssetID:   uid1 * 100,
				Type:      mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN,
				ObjKey:    "avatar/obj",
				Bucket:    "rednote",
				Sha256Sum: "abc",
				SizeBytes: 2048,
				Format:    mediav1.MediaFormat_MEDIA_FORMAT_WEBP,
				Height:    100,
				Width:     100,
			},
			Followers:  5,
			Followings: 7,
			Status:     userv1.UserStatus_USER_STATUS_ACTIVE,
			MetaData:   datatypes.JSONMap{"theme": "dark"},
		}
		ok, err := repo.CreateUser(ctx, profile)
		require.NoError(t, err)
		assert.True(t, ok)

		var got models.UserProfile
		require.NoError(t, db.Where("id = ?", uid1).First(&got).Error)
		assert.Equal(t, "小红薯", got.Name)
		assert.Equal(t, "简介", got.Bio)
		assert.Equal(t, int64(5), got.Followers)
		assert.Equal(t, int64(7), got.Followings)
		assert.Equal(t, userv1.UserStatus_USER_STATUS_ACTIVE, got.Status)
		assert.Equal(t, datatypes.JSONMap{"theme": "dark"}, got.MetaData)

		// 头像嵌入列（avatar_ 前缀）应完整回读
		require.NotNil(t, got.Avatar, "嵌入的头像媒体文件应被回读")
		assert.Equal(t, uid1*10, got.Avatar.ID)
		assert.Equal(t, uid1*100, got.Avatar.AssetID)
		assert.Equal(t, "avatar/obj", got.Avatar.ObjKey)
		assert.Equal(t, "rednote", got.Avatar.Bucket)
		assert.Equal(t, int64(2048), got.Avatar.SizeBytes)
		assert.Equal(t, int64(100), got.Avatar.Height)
		assert.Equal(t, int64(100), got.Avatar.Width)
	})

	t.Run("未指定ID时 BeforeCreate 自动填充雪花ID", func(t *testing.T) {
		profile := newProfile(0, "auto-id", "")
		ok, err := repo.CreateUser(ctx, profile)
		require.NoError(t, err)
		assert.True(t, ok)

		// BeforeCreate 钩子应填充雪花 ID，autoCreateTime 填充毫秒时间戳
		assert.NotZero(t, profile.ID, "创建后应自动填充雪花 ID")
		assert.NotZero(t, profile.CreatedAt, "创建后应自动填充 created_at")

		var got models.UserProfile
		require.NoError(t, db.Where("id = ?", profile.ID).First(&got).Error)
		assert.Equal(t, "auto-id", got.Name)
	})

	t.Run("重复ID幂等返回 ok=false（ON CONFLICT DO NOTHING）", func(t *testing.T) {
		seedProfile(t, db, uid2, "dup", "", userv1.UserStatus_USER_STATUS_ACTIVE)

		ok, err := repo.CreateUser(ctx, newProfile(uid2, "dup-again", ""))
		require.NoError(t, err, "ON CONFLICT DO NOTHING 不应报错")
		assert.False(t, ok, "重复 ID 应返回 ok=false")

		var got models.UserProfile
		require.NoError(t, db.Where("id = ?", uid2).First(&got).Error)
		assert.Equal(t, "dup", got.Name, "原记录不应被改动")
	})
}

// ---------- UpdateUser ----------

// ---------- UpdateUserName / UpdateUserBio / UpdateUserMetadata / UpdateUserAvatar ----------

func TestUpdateUserName(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	t.Run("更新成功并自动刷新 updated_at", func(t *testing.T) {
		seedProfile(t, db, uid1, "旧昵称", "旧简介", userv1.UserStatus_USER_STATUS_ACTIVE)
		// 人为把 updated_at 改旧，避免同毫秒精度导致断言不稳定
		require.NoError(t, db.Exec("UPDATE user_profiles SET updated_at = 1 WHERE id = ?", uid1).Error)

		info, err := repo.UpdateUserName(ctx, uid1, "新昵称")
		require.NoError(t, err)
		assert.Equal(t, int64(1), info.RowsAffected)

		var got models.UserProfile
		require.NoError(t, db.Where("id = ?", uid1).First(&got).Error)
		assert.Equal(t, "新昵称", got.Name)
		assert.Equal(t, "旧简介", got.Bio, "只更新昵称，简介不应变化")
		assert.Greater(t, got.UpdatedAt, int64(1), "updated_at 应自动更新")
	})

	t.Run("用户不存在时 RowsAffected 为 0", func(t *testing.T) {
		info, err := repo.UpdateUserName(ctx, 999999, "不存在")
		require.NoError(t, err)
		assert.Equal(t, int64(0), info.RowsAffected)
	})
}

func TestUpdateUserBio(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	t.Run("更新成功且不影响其他字段", func(t *testing.T) {
		seedProfile(t, db, uid1, "旧昵称", "旧简介", userv1.UserStatus_USER_STATUS_ACTIVE)

		info, err := repo.UpdateUserBio(ctx, uid1, "新简介")
		require.NoError(t, err)
		assert.Equal(t, int64(1), info.RowsAffected)

		var got models.UserProfile
		require.NoError(t, db.Where("id = ?", uid1).First(&got).Error)
		assert.Equal(t, "新简介", got.Bio)
		assert.Equal(t, "旧昵称", got.Name, "只更新简介，昵称不应变化")
	})

	t.Run("可清空为零值", func(t *testing.T) {
		seedProfile(t, db, uid2, "有简介", "非空简介", userv1.UserStatus_USER_STATUS_ACTIVE)

		info, err := repo.UpdateUserBio(ctx, uid2, "")
		require.NoError(t, err)
		assert.Equal(t, int64(1), info.RowsAffected)

		var got models.UserProfile
		require.NoError(t, db.Where("id = ?", uid2).First(&got).Error)
		assert.Empty(t, got.Bio, "bio 应被清空为零值")
		assert.Equal(t, "有简介", got.Name)
	})

	t.Run("用户不存在时 RowsAffected 为 0", func(t *testing.T) {
		info, err := repo.UpdateUserBio(ctx, 999999, "不存在")
		require.NoError(t, err)
		assert.Equal(t, int64(0), info.RowsAffected)
	})
}

func TestUpdateUserMetadata(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	t.Run("更新成功", func(t *testing.T) {
		seedProfile(t, db, uid1, "旧昵称", "旧简介", userv1.UserStatus_USER_STATUS_ACTIVE)

		info, err := repo.UpdateUserMetadata(ctx, uid1, datatypes.JSONMap{"theme": "light"})
		require.NoError(t, err)
		assert.Equal(t, int64(1), info.RowsAffected)

		var got models.UserProfile
		require.NoError(t, db.Where("id = ?", uid1).First(&got).Error)
		assert.Equal(t, datatypes.JSONMap{"theme": "light"}, got.MetaData)
		assert.Equal(t, "旧昵称", got.Name)
	})

	t.Run("可清空为 {}", func(t *testing.T) {
		seedProfile(t, db, uid2, "有元数据", "", userv1.UserStatus_USER_STATUS_ACTIVE)

		info, err := repo.UpdateUserMetadata(ctx, uid2, datatypes.JSONMap{})
		require.NoError(t, err)
		assert.Equal(t, int64(1), info.RowsAffected)

		var got models.UserProfile
		require.NoError(t, db.Where("id = ?", uid2).First(&got).Error)
		assert.Empty(t, got.MetaData, "metadata 应被清空为 {}")
	})

	t.Run("用户不存在时 RowsAffected 为 0", func(t *testing.T) {
		info, err := repo.UpdateUserMetadata(ctx, 999999, datatypes.JSONMap{"a": "b"})
		require.NoError(t, err)
		assert.Equal(t, int64(0), info.RowsAffected)
	})
}

func TestUpdateUserAvatar(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	t.Run("换头像时 avatar_id 应被更新（不受嵌入主键条件影响）", func(t *testing.T) {
		seedProfile(t, db, uid1, "旧昵称", "旧简介", userv1.UserStatus_USER_STATUS_ACTIVE)

		avatar := newProfile(uid1, "", "").Avatar
		avatar.ID = uid1*10 + 1 // 新头像文件 ID，与库中旧值不同
		avatar.ObjKey = "avatar/changed"

		info, err := repo.UpdateUserAvatar(ctx, uid1, avatar)
		require.NoError(t, err)
		assert.Equal(t, int64(1), info.RowsAffected)

		var got models.UserProfile
		require.NoError(t, db.Where("id = ?", uid1).First(&got).Error)
		require.NotNil(t, got.Avatar)
		assert.Equal(t, uid1*10+1, got.Avatar.ID, "avatar_id 应更新为新头像")
		assert.Equal(t, "avatar/changed", got.Avatar.ObjKey)
		assert.Equal(t, "旧昵称", got.Name, "只更新头像，昵称不应变化")
	})

	t.Run("用户不存在时 RowsAffected 为 0", func(t *testing.T) {
		info, err := repo.UpdateUserAvatar(ctx, 999999, newProfile(999999, "", "").Avatar)
		require.NoError(t, err)
		assert.Equal(t, int64(0), info.RowsAffected)
	})
}

func TestGetUserProfiles(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	seedProfile(t, db, uid1, "user-1", "bio-1", userv1.UserStatus_USER_STATUS_ACTIVE)
	seedProfile(t, db, uid2, "user-2", "bio-2", userv1.UserStatus_USER_STATUS_BANNED)

	t.Run("批量命中", func(t *testing.T) {
		profiles, err := repo.GetUserProfiles(ctx, []int64{uid1, uid2})
		require.NoError(t, err)
		require.Len(t, profiles, 2)

		got := map[int64]string{}
		for _, p := range profiles {
			got[p.ID] = p.Name
		}
		assert.Equal(t, map[int64]string{uid1: "user-1", uid2: "user-2"}, got)
	})

	t.Run("状态与头像字段完整回读", func(t *testing.T) {
		profiles, err := repo.GetUserProfiles(ctx, []int64{uid2})
		require.NoError(t, err)
		require.Len(t, profiles, 1)
		assert.Equal(t, userv1.UserStatus_USER_STATUS_BANNED, profiles[0].Status)
		assert.Equal(t, "bio-2", profiles[0].Bio)
		require.NotNil(t, profiles[0].Avatar)
		assert.Equal(t, "avatar/obj", profiles[0].Avatar.ObjKey)
	})

	t.Run("部分不存在只返回命中的", func(t *testing.T) {
		profiles, err := repo.GetUserProfiles(ctx, []int64{uid1, 999999})
		require.NoError(t, err)
		require.Len(t, profiles, 1)
		assert.Equal(t, uid1, profiles[0].ID)
	})

	t.Run("全部不存在返回空集", func(t *testing.T) {
		profiles, err := repo.GetUserProfiles(ctx, []int64{999998, 999999})
		require.NoError(t, err)
		assert.Empty(t, profiles)
	})

	t.Run("空入参返回空集", func(t *testing.T) {
		profiles, err := repo.GetUserProfiles(ctx, []int64{})
		require.NoError(t, err)
		assert.Empty(t, profiles)
	})
}

// ---------- 事务 ----------

func TestWithTX(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	t.Run("提交后数据可见", func(t *testing.T) {
		err := db.Transaction(func(tx *gorm.DB) error {
			_, err := repo.WithTX(tx).CreateUser(ctx, newProfile(uid1, "tx-commit", ""))
			return err
		})
		require.NoError(t, err)

		var count int64
		require.NoError(t, db.Model(&models.UserProfile{}).Where("id = ?", uid1).Count(&count).Error)
		assert.Equal(t, int64(1), count)
	})

	t.Run("回滚后数据不可见", func(t *testing.T) {
		sentinel := errors.New("force rollback")
		err := db.Transaction(func(tx *gorm.DB) error {
			if _, err := repo.WithTX(tx).CreateUser(ctx, newProfile(uid3, "tx-rollback", "")); err != nil {
				return err
			}
			return sentinel
		})
		require.ErrorIs(t, err, sentinel)

		var count int64
		require.NoError(t, db.Model(&models.UserProfile{}).Where("id = ?", uid3).Count(&count).Error)
		assert.Equal(t, int64(0), count, "回滚后不应存在记录")
	})

	t.Run("事务内读自身写入", func(t *testing.T) {
		err := db.Transaction(func(tx *gorm.DB) error {
			txRepo := repo.WithTX(tx)
			if _, err := txRepo.CreateUser(ctx, newProfile(uid2, "tx-read", "")); err != nil {
				return err
			}

			// 事务内应能读到刚写入、未提交的记录
			profiles, err := txRepo.GetUserProfiles(ctx, []int64{uid2})
			if err != nil {
				return err
			}
			assert.Len(t, profiles, 1)
			assert.Equal(t, "tx-read", profiles[0].Name)
			return nil
		})
		require.NoError(t, err)
	})
}

// ---------- SetFollow ----------

// followingsOf 读取用户的关注数（计数断言用）
func followingsOf(t *testing.T, db *gorm.DB, id int64) int64 {
	t.Helper()
	var p models.UserProfile
	require.NoError(t, db.Where("id = ?", id).First(&p).Error)
	return p.Followings
}

// followersOf 读取用户的粉丝数（计数断言用）
func followersOf(t *testing.T, db *gorm.DB, id int64) int64 {
	t.Helper()
	var p models.UserProfile
	require.NoError(t, db.Where("id = ?", id).First(&p).Error)
	return p.Followers
}

// seedFollowPair 造一对互不相交的用户（fan/following），初始计数 Followers 5 / Followings 7。
// 每个子测试传入各自的 ID 对并自行 seed，子测试之间零共享可变状态，可单独运行（FIRST-Independent）。
func seedFollowPair(t *testing.T, db *gorm.DB, fanID, followingID int64) (int64, int64) {
	t.Helper()
	seedProfile(t, db, fanID, "fan", "", userv1.UserStatus_USER_STATUS_ACTIVE) // Followers 5, Followings 7
	seedProfile(t, db, followingID, "following", "", userv1.UserStatus_USER_STATUS_ACTIVE)
	return fanID, followingID
}

func TestSetFollow(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	t.Run("新建关注关系并递增双端计数", func(t *testing.T) {
		// Arrange：独立用户对
		fan, target := seedFollowPair(t, db, 4001, 4002)

		// Act
		ok, err := repo.SetFollow(ctx, fan, target)

		// Assert
		require.NoError(t, err)
		assert.True(t, ok)

		var follow models.UserFollow
		require.NoError(t, db.Where("fan_id = ? AND following_id = ?", fan, target).First(&follow).Error)
		assert.Equal(t, fan, follow.FanID)
		assert.Equal(t, target, follow.FollowingID)
		assert.NotZero(t, follow.CreatedAt, "关注行应自动填充 created_at")

		assert.Equal(t, int64(8), followingsOf(t, db, fan), "fan.followings 应 +1")
		assert.Equal(t, int64(6), followersOf(t, db, target), "following.followers 应 +1")
	})

	t.Run("重复关注幂等：不重复计数", func(t *testing.T) {
		// Arrange：先建立一次关注（8/6）
		fan, target := seedFollowPair(t, db, 4003, 4004)
		ok, err := repo.SetFollow(ctx, fan, target)
		require.NoError(t, err)
		require.True(t, ok)

		// Act：对同一目标再次关注
		ok, err = repo.SetFollow(ctx, fan, target)

		// Assert
		require.NoError(t, err)
		assert.True(t, ok, "关系已存在时应幂等成功")

		var cnt int64
		require.NoError(t, db.Model(&models.UserFollow{}).
			Where("fan_id = ? AND following_id = ?", fan, target).Count(&cnt).Error)
		assert.Equal(t, int64(1), cnt, "不应产生重复关注行")
		assert.Equal(t, int64(8), followingsOf(t, db, fan), "幂等调用不应膨胀计数")
		assert.Equal(t, int64(6), followersOf(t, db, target), "幂等调用不应膨胀计数")
	})

	t.Run("目标用户不存在：返回 false 且不落库", func(t *testing.T) {
		// Arrange
		fan, _ := seedFollowPair(t, db, 4005, 4006)
		const missing int64 = 999999

		// Act
		ok, err := repo.SetFollow(ctx, fan, missing)

		// Assert
		require.NoError(t, err)
		assert.False(t, ok)

		var cnt int64
		require.NoError(t, db.Model(&models.UserFollow{}).
			Where("fan_id = ? AND following_id = ?", fan, missing).Count(&cnt).Error)
		assert.Zero(t, cnt)
		assert.Equal(t, int64(7), followingsOf(t, db, fan), "失败不应改计数")
		assert.Equal(t, int64(5), followersOf(t, db, fan), "失败不应改计数")
	})

	t.Run("自关注被拒绝", func(t *testing.T) {
		// Arrange
		fan, _ := seedFollowPair(t, db, 4007, 4008)

		// Act
		ok, err := repo.SetFollow(ctx, fan, fan)

		// Assert
		require.NoError(t, err)
		assert.False(t, ok)

		var cnt int64
		require.NoError(t, db.Model(&models.UserFollow{}).
			Where("fan_id = ? AND following_id = ?", fan, fan).Count(&cnt).Error)
		assert.Zero(t, cnt)
		assert.Equal(t, int64(7), followingsOf(t, db, fan), "自关注不应改计数")
	})
}

// ---------- SetUnFollow ----------

func TestSetUnFollow(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	t.Run("取关成功并递减双端计数", func(t *testing.T) {
		// Arrange：先建立关注（8/6）
		fan, target := seedFollowPair(t, db, 4101, 4102)
		ok, err := repo.SetFollow(ctx, fan, target)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, int64(8), followingsOf(t, db, fan))
		require.Equal(t, int64(6), followersOf(t, db, target))

		// Act
		require.NoError(t, repo.SetUnFollow(ctx, fan, target))

		// Assert
		var cnt int64
		require.NoError(t, db.Model(&models.UserFollow{}).
			Where("fan_id = ? AND following_id = ?", fan, target).Count(&cnt).Error)
		assert.Zero(t, cnt, "关注关系应被删除")
		assert.Equal(t, int64(7), followingsOf(t, db, fan), "fan.followings 应 -1")
		assert.Equal(t, int64(5), followersOf(t, db, target), "following.followers 应 -1")
	})

	t.Run("未关注时幂等成功且不递减计数", func(t *testing.T) {
		// Arrange：从未建立关注，初始计数即 7/5
		fan, target := seedFollowPair(t, db, 4103, 4104)

		// Act
		require.NoError(t, repo.SetUnFollow(ctx, fan, target))

		// Assert
		assert.Equal(t, int64(7), followingsOf(t, db, fan), "关系不存在不应递减")
		assert.Equal(t, int64(5), followersOf(t, db, target), "关系不存在不应递减")
	})
}

// ---------- GetFollowings ----------

func TestGetFollowings(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	seedProfile(t, db, uid1, "fan", "", userv1.UserStatus_USER_STATUS_ACTIVE)
	seedProfile(t, db, uid2, "target-1", "", userv1.UserStatus_USER_STATUS_ACTIVE)
	seedProfile(t, db, uid3, "target-2", "", userv1.UserStatus_USER_STATUS_ACTIVE)

	ok, err := repo.SetFollow(ctx, uid1, uid2)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = repo.SetFollow(ctx, uid1, uid3)
	require.NoError(t, err)
	require.True(t, ok)

	base := time.Now().UnixMilli()
	// 人为拉开 created_at，避免同毫秒导致排序/游标断言不稳定
	require.NoError(t, db.Exec("UPDATE user_follows SET created_at = ? WHERE following_id = ?", base-2000, uid2).Error)
	require.NoError(t, db.Exec("UPDATE user_follows SET created_at = ? WHERE following_id = ?", base-1000, uid3).Error)

	t.Run("按 created_at 降序并带出被关注用户资料", func(t *testing.T) {
		follows, err := repo.GetFollowings(ctx, uid1, base, 10)
		require.NoError(t, err)
		require.Len(t, follows, 2)
		assert.Equal(t, uid3, follows[0].FollowingID, "较新的关注应在前")
		assert.Equal(t, uid2, follows[1].FollowingID)
		assert.Equal(t, uid1, follows[0].FanID, "关注行应携带 fan_id")
		assert.Equal(t, base-1000, follows[0].CreatedAt, "created_at 应为造数时设置的值")
		assert.Equal(t, base-2000, follows[1].CreatedAt)
		require.NotNil(t, follows[0].Following, "Preload 应带出被关注用户资料")
		assert.Equal(t, "target-2", follows[0].Following.Name)
	})

	t.Run("limit 为 0 返回空集", func(t *testing.T) {
		follows, err := repo.GetFollowings(ctx, uid1, base, 0)
		require.NoError(t, err)
		assert.Empty(t, follows)
	})

	t.Run("游标 before 严格小于", func(t *testing.T) {
		follows, err := repo.GetFollowings(ctx, uid1, base-1000, 10) // 排除 uid3 那条
		require.NoError(t, err)
		require.Len(t, follows, 1)
		assert.Equal(t, uid2, follows[0].FollowingID)
		assert.Equal(t, base-2000, follows[0].CreatedAt)
	})

	t.Run("limit 生效", func(t *testing.T) {
		follows, err := repo.GetFollowings(ctx, uid1, base, 1)
		require.NoError(t, err)
		require.Len(t, follows, 1)
		assert.Equal(t, uid3, follows[0].FollowingID)
	})

	t.Run("无关注返回空集", func(t *testing.T) {
		follows, err := repo.GetFollowings(ctx, uid3, base, 10) // uid3 不关注任何人
		require.NoError(t, err)
		assert.Empty(t, follows)
	})
}

// ---------- GetFollowingRelation ----------

func TestGetFollowingRelation(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	t.Run("无关系时双向 false", func(t *testing.T) {
		// Arrange
		fan, target := seedFollowPair(t, db, 4201, 4202)

		// Act
		isFollowing, isFollower, err := repo.GetFollowingRelation(ctx, fan, target)

		// Assert
		require.NoError(t, err)
		assert.False(t, isFollowing)
		assert.False(t, isFollower)
	})

	t.Run("单向关注", func(t *testing.T) {
		// Arrange：fan → target
		fan, target := seedFollowPair(t, db, 4203, 4204)
		ok, err := repo.SetFollow(ctx, fan, target)
		require.NoError(t, err)
		require.True(t, ok)

		// Act
		isFollowing, isFollower, err := repo.GetFollowingRelation(ctx, fan, target)

		// Assert
		require.NoError(t, err)
		assert.True(t, isFollowing, "fan 关注了 target")
		assert.False(t, isFollower, "target 未关注 fan")
	})

	t.Run("互相关注", func(t *testing.T) {
		// Arrange：双向关注
		fan, target := seedFollowPair(t, db, 4205, 4206)
		ok, err := repo.SetFollow(ctx, fan, target)
		require.NoError(t, err)
		require.True(t, ok)
		ok, err = repo.SetFollow(ctx, target, fan)
		require.NoError(t, err)
		require.True(t, ok)

		// Act
		isFollowing, isFollower, err := repo.GetFollowingRelation(ctx, fan, target)

		// Assert
		require.NoError(t, err)
		assert.True(t, isFollowing)
		assert.True(t, isFollower)
	})
}

// ---------- BanUser ----------

func TestBanUser(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	t.Run("封禁成功", func(t *testing.T) {
		seedProfile(t, db, uid1, "banned", "", userv1.UserStatus_USER_STATUS_ACTIVE)

		require.NoError(t, repo.BanUser(ctx, uid1))

		var got models.UserProfile
		require.NoError(t, db.Where("id = ?", uid1).First(&got).Error)
		assert.Equal(t, userv1.UserStatus_USER_STATUS_BANNED, got.Status)
	})

	t.Run("不存在的 uid 幂等成功", func(t *testing.T) {
		require.NoError(t, repo.BanUser(ctx, 999999))
	})
}
