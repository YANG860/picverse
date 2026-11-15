package v1

import (
	"context"
	"strings"
	"testing"

	commonv1 "rednote/gen/go/common/v1"
	mediav1 "rednote/gen/go/media/v1"
	userv1 "rednote/gen/go/user/v1"
	common "rednote/internal/common/models"
	media "rednote/internal/media/models"
	models "rednote/internal/user/models"
	"rednote/packages/errmsg"
	"rednote/packages/utils"
	"rednote/test/testdb"
	"rednote/test/testmq"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// newUsecase 构造 biz 层测试环境：真实 repo + testdb 克隆库。
// newProfile / seedProfile / followingsOf / followersOf 复用 repo_test.go 中的同包 helper。
func newUsecase(t *testing.T) (*UserUsecaseImpl, *gorm.DB) {
	t.Helper()
	db := testdb.Setup(t)
	return NewUserUsecase(NewUserRepo(db), db, testmq.New()), db
}

// setupFollowers 预置两个用户（uid1 粉丝 / uid2 被关注者）
func setupFollowers(t *testing.T, db *gorm.DB) {
	t.Helper()
	seedProfile(t, db, uid1, "fan", "", userv1.UserStatus_USER_STATUS_ACTIVE)
	seedProfile(t, db, uid2, "target", "", userv1.UserStatus_USER_STATUS_ACTIVE)
}

// validAvatar 合法头像 proto（仅结构校验，内容审核由审核服务负责）
func validAvatar() *mediav1.MediaFile {
	return &mediav1.MediaFile{
		Base:    &commonv1.BaseModel{Id: "10001"},
		AssetId: "10002",
		Type:    mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN,
		ObjKey:  "avatar/obj",
		Bucket:  "rednote",
		Meta: &mediav1.MediaMetaData{
			Sha_256Sum: "abc",
			SizeBytes:  2048,
			Format:     mediav1.MediaFormat_MEDIA_FORMAT_WEBP,
			Height:     100,
			Width:      100,
		},
	}
}

// newCreateUserReq 构造创建用户请求（头像按需传入）
func newCreateUserReq(uid, name, bio string, avatar *mediav1.MediaFile) *userv1.CreateUserRequest {
	return &userv1.CreateUserRequest{
		Uid:      uid,
		Name:     name,
		Bio:      bio,
		Avatar:   avatar,
		Metadata: &commonv1.JsonStr{Payload: "{}"},
	}
}

// strVal 构造可空字符串字段（nil 表示不更新）
func strVal(s string) *wrapperspb.StringValue {
	return wrapperspb.String(s)
}

func TestUserUsecase_CreateUser(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name    string
		exist   bool // 目标 uid 是否已存在
		req     *userv1.CreateUserRequest
		wantErr *commonv1.Error // nil 表示应创建成功
	}{
		{
			name:    "无头像请求被拒绝",
			req:     newCreateUserReq("3001", "小红薯", "简介", nil),
			wantErr: errmsg.InvalidAvatar,
		},
		{
			name:    "非法uid被拒绝",
			req:     newCreateUserReq("not-a-number", "小红薯", "简介", validAvatar()),
			wantErr: errmsg.InvalidID,
		},
		{
			name:    "纯空白昵称被拒绝",
			req:     newCreateUserReq("3001", "   ", "简介", validAvatar()),
			wantErr: errmsg.InvalidName,
		},
		{
			name:    "超长昵称被拒绝",
			req:     newCreateUserReq("3001", strings.Repeat("好", utils.NameMaxLen+1), "简介", validAvatar()),
			wantErr: errmsg.InvalidName,
		},
		{
			name:    "超长简介被拒绝",
			req:     newCreateUserReq("3001", "小红薯", strings.Repeat("好", utils.BioMaxLen+1), validAvatar()),
			wantErr: errmsg.InvalidBio,
		},
		{
			name: "头像结构不合法被拒绝",
			req: newCreateUserReq("3001", "小红薯", "简介", &mediav1.MediaFile{
				Base:    &commonv1.BaseModel{Id: "10001"},
				AssetId: "not-a-number",
			}),
			wantErr: errmsg.InvalidAvatar,
		},
		{
			name: "合法请求创建成功",
			req:  newCreateUserReq("3001", "小红薯", "简介", validAvatar()),
		},
		{
			name:    "重复创建返回 AlreadyExist",
			exist:   true,
			req:     newCreateUserReq("3001", "小红薯", "简介", validAvatar()),
			wantErr: errmsg.AlreadyExist,
		},
		{
			name: "metadata 未提供视为空对象",
			req: &userv1.CreateUserRequest{
				Uid: "3001", Name: "小红薯", Bio: "简介", Avatar: validAvatar(),
			},
		},
		{
			name: "metadata 为空串视为空对象",
			req: &userv1.CreateUserRequest{
				Uid:      "3001",
				Name:     "小红薯",
				Bio:      "简介",
				Avatar:   validAvatar(),
				Metadata: &commonv1.JsonStr{Payload: ""},
			},
		},
		{
			name: "非法 metadata 被拒绝",
			req: &userv1.CreateUserRequest{
				Uid:      "3001",
				Name:     "小红薯",
				Bio:      "简介",
				Avatar:   validAvatar(),
				Metadata: &commonv1.JsonStr{Payload: "not-json"},
			},
			wantErr: errmsg.InvalidArgument,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uc, db := newUsecase(t)
			if tc.exist {
				seedProfile(t, db, uid1, "已存在", "", userv1.UserStatus_USER_STATUS_ACTIVE)
			}
			var baseCount int64
			require.NoError(t, db.Model(&models.UserProfile{}).Count(&baseCount).Error)

			resp, err := uc.CreateUser(ctx, tc.req)
			require.NoError(t, err)
			require.NotNil(t, resp)

			if tc.wantErr != nil {
				assert.True(t, resp.GetFail())
				assert.Equal(t, tc.wantErr, resp.GetErr())
				assert.Nil(t, resp.GetProfile(), "被拒绝的请求不应返回资料")
				var cnt int64
				require.NoError(t, db.Model(&models.UserProfile{}).Count(&cnt).Error)
				assert.Equal(t, baseCount, cnt, "被拒绝的请求不应落库")
				return
			}

			// Assert：响应完整字段
			assert.False(t, resp.GetFail())
			assert.Nil(t, resp.GetErr())
			require.NotNil(t, resp.GetProfile())
			assert.Equal(t, "3001", resp.GetProfile().GetBrief().GetUid())
			assert.Equal(t, "小红薯", resp.GetProfile().GetBrief().GetName())
			assert.Equal(t, "简介", resp.GetProfile().GetBio())
			assert.Equal(t, userv1.UserStatus_USER_STATUS_ACTIVE, resp.GetProfile().GetStatus())
			assert.Zero(t, resp.GetProfile().GetStat().GetFollowers(), "新建用户粉丝数应为 0")
			assert.Zero(t, resp.GetProfile().GetStat().GetFollowings(), "新建用户关注数应为 0")

			// 落库校验：全部字段完整回读
			var got models.UserProfile
			require.NoError(t, db.Where("id = ?", uid1).First(&got).Error)
			assert.Equal(t, "小红薯", got.Name)
			assert.Equal(t, "简介", got.Bio)
			assert.Equal(t, userv1.UserStatus_USER_STATUS_ACTIVE, got.Status)
			assert.Zero(t, got.Followers)
			assert.Zero(t, got.Followings)
			assert.NotZero(t, got.CreatedAt, "创建时间应自动填充")
			assert.Empty(t, got.MetaData, "metadata 空值应落库为 {}")

			// 头像嵌入列完整回读
			require.NotNil(t, got.Avatar, "头像应落库")
			assert.Equal(t, int64(10001), got.Avatar.ID)
			assert.Equal(t, int64(10002), got.Avatar.AssetID)
			assert.Equal(t, mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN, got.Avatar.Type)
			assert.Equal(t, "avatar/obj", got.Avatar.ObjKey)
			assert.Equal(t, "rednote", got.Avatar.Bucket)
			assert.Equal(t, "abc", got.Avatar.Sha256Sum)
			assert.Equal(t, int64(2048), got.Avatar.SizeBytes)
			assert.Equal(t, mediav1.MediaFormat_MEDIA_FORMAT_WEBP, got.Avatar.Format)
			assert.Equal(t, int64(100), got.Avatar.Height)
			assert.Equal(t, int64(100), got.Avatar.Width)
		})
	}
}

// TestUserUsecase_UpdateUserProfile 部分更新语义：仅更新传入的字段，nil 字段不修改（头像由 UpdateUserAvatar 独立更新）
func TestUserUsecase_UpdateUserProfile(t *testing.T) {
	ctx := context.Background()

	t.Run("无任何更新字段被拒绝", func(t *testing.T) {
		uc, _ := newUsecase(t)
		resp, err := uc.UpdateUserProfile(ctx, &userv1.UpdateUserProfileRequest{Uid: "3001"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidArgument, resp.GetErr())
		assert.Nil(t, resp.GetProfile(), "失败响应不应携带资料")
	})

	t.Run("非法uid被拒绝", func(t *testing.T) {
		uc, _ := newUsecase(t)
		resp, err := uc.UpdateUserProfile(ctx, &userv1.UpdateUserProfileRequest{Uid: "bad", Name: strVal("新昵称")})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetErr())
		assert.Nil(t, resp.GetProfile(), "失败响应不应携带资料")
	})

	t.Run("非法昵称被拒绝", func(t *testing.T) {
		uc, db := newUsecase(t)
		seedProfile(t, db, uid1, "旧昵称", "旧简介", userv1.UserStatus_USER_STATUS_ACTIVE)
		resp, err := uc.UpdateUserProfile(ctx, &userv1.UpdateUserProfileRequest{Uid: "3001", Name: strVal("   ")})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidName, resp.GetErr())
		assert.Nil(t, resp.GetProfile(), "失败响应不应携带资料")
	})

	t.Run("超长简介被拒绝", func(t *testing.T) {
		uc, db := newUsecase(t)
		seedProfile(t, db, uid1, "旧昵称", "旧简介", userv1.UserStatus_USER_STATUS_ACTIVE)
		resp, err := uc.UpdateUserProfile(ctx, &userv1.UpdateUserProfileRequest{Uid: "3001", Bio: strVal(strings.Repeat("好", utils.BioMaxLen+1))})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidBio, resp.GetErr())
		assert.Nil(t, resp.GetProfile(), "失败响应不应携带资料")
	})

	t.Run("非法 metadata 被拒绝", func(t *testing.T) {
		uc, db := newUsecase(t)
		seedProfile(t, db, uid1, "旧昵称", "旧简介", userv1.UserStatus_USER_STATUS_ACTIVE)
		resp, err := uc.UpdateUserProfile(ctx, &userv1.UpdateUserProfileRequest{Uid: "3001", Metadata: &commonv1.JsonStr{Payload: "not-json"}})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidArgument, resp.GetErr())
		assert.Nil(t, resp.GetProfile(), "失败响应不应携带资料")
	})

	t.Run("用户不存在返回 NotFound", func(t *testing.T) {
		uc, _ := newUsecase(t)
		resp, err := uc.UpdateUserProfile(ctx, &userv1.UpdateUserProfileRequest{Uid: "3001", Name: strVal("新昵称")})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.NotFound, resp.GetErr())
		assert.Nil(t, resp.GetProfile(), "失败响应不应携带资料")
	})

	t.Run("只更新昵称", func(t *testing.T) {
		uc, db := newUsecase(t)
		seedProfile(t, db, uid1, "旧昵称", "旧简介", userv1.UserStatus_USER_STATUS_ACTIVE)

		resp, err := uc.UpdateUserProfile(ctx, &userv1.UpdateUserProfileRequest{Uid: "3001", Name: strVal("新昵称")})
		require.NoError(t, err)
		require.False(t, resp.GetFail())
		assert.Equal(t, "新昵称", resp.GetProfile().GetBrief().GetName())

		var got models.UserProfile
		require.NoError(t, db.Where("id = ?", uid1).First(&got).Error)
		assert.Equal(t, "新昵称", got.Name)
		assert.Equal(t, "旧简介", got.Bio, "未传入的字段不应被修改")
		assert.Equal(t, datatypes.JSONMap{"theme": "dark"}, got.MetaData, "未传入的字段不应被修改")
		assert.Equal(t, int64(uid1*100), got.Avatar.AssetID, "头像不在本接口更新范围")
	})

	t.Run("只更新简介（可清空）", func(t *testing.T) {
		uc, db := newUsecase(t)
		seedProfile(t, db, uid1, "旧昵称", "旧简介", userv1.UserStatus_USER_STATUS_ACTIVE)

		resp, err := uc.UpdateUserProfile(ctx, &userv1.UpdateUserProfileRequest{Uid: "3001", Bio: strVal("")})
		require.NoError(t, err)
		require.False(t, resp.GetFail())

		var got models.UserProfile
		require.NoError(t, db.Where("id = ?", uid1).First(&got).Error)
		assert.Empty(t, got.Bio, "简介应被清空")
		assert.Equal(t, "旧昵称", got.Name, "未传入的字段不应被修改")
	})

	t.Run("只更新 metadata（清空为 {}）", func(t *testing.T) {
		uc, db := newUsecase(t)
		seedProfile(t, db, uid1, "旧昵称", "旧简介", userv1.UserStatus_USER_STATUS_ACTIVE)

		resp, err := uc.UpdateUserProfile(ctx, &userv1.UpdateUserProfileRequest{Uid: "3001", Metadata: &commonv1.JsonStr{Payload: "{}"}})
		require.NoError(t, err)
		require.False(t, resp.GetFail())

		var got models.UserProfile
		require.NoError(t, db.Where("id = ?", uid1).First(&got).Error)
		assert.Empty(t, got.MetaData, "metadata 应被清空为 {}")
		assert.Equal(t, "旧昵称", got.Name, "未传入的字段不应被修改")
		assert.Equal(t, "旧简介", got.Bio, "未传入的字段不应被修改")
	})

	t.Run("组合更新（昵称+简介）", func(t *testing.T) {
		uc, db := newUsecase(t)
		seedProfile(t, db, uid1, "旧昵称", "旧简介", userv1.UserStatus_USER_STATUS_ACTIVE)

		resp, err := uc.UpdateUserProfile(ctx, &userv1.UpdateUserProfileRequest{Uid: "3001", Name: strVal("新昵称"), Bio: strVal("新简介")})
		require.NoError(t, err)
		require.False(t, resp.GetFail())
		assert.Equal(t, userv1.UserStatus_USER_STATUS_ACTIVE, resp.GetProfile().GetStatus(),
			"回读应包含未在本接口修改的状态与统计")

		var got models.UserProfile
		require.NoError(t, db.Where("id = ?", uid1).First(&got).Error)
		assert.Equal(t, "新昵称", got.Name)
		assert.Equal(t, "新简介", got.Bio)
		assert.Equal(t, datatypes.JSONMap{"theme": "dark"}, got.MetaData, "未传入的字段不应被修改")
	})
}

// TestUserUsecase_UpdateUserAvatar 头像更新独立接口：返回旧资产 ID 供 BFF 调整引用计数
func TestUserUsecase_UpdateUserAvatar(t *testing.T) {
	ctx := context.Background()

	t.Run("非法uid被拒绝", func(t *testing.T) {
		uc, _ := newUsecase(t)
		resp, err := uc.UpdateUserAvatar(ctx, &userv1.UpdateUserAvatarRequest{Uid: "bad", Avatar: validAvatar()})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetErr())
		assert.Nil(t, resp.GetProfile(), "失败响应不应携带资料")
	})

	t.Run("头像缺失被拒绝", func(t *testing.T) {
		uc, _ := newUsecase(t)
		resp, err := uc.UpdateUserAvatar(ctx, &userv1.UpdateUserAvatarRequest{Uid: "3001"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidAvatar, resp.GetErr())
		assert.Nil(t, resp.GetProfile(), "失败响应不应携带资料")
	})

	t.Run("头像结构不合法被拒绝", func(t *testing.T) {
		uc, _ := newUsecase(t)
		resp, err := uc.UpdateUserAvatar(ctx, &userv1.UpdateUserAvatarRequest{Uid: "3001", Avatar: &mediav1.MediaFile{
			Base:    &commonv1.BaseModel{Id: "10001"},
			AssetId: "not-a-number",
		}})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidAvatar, resp.GetErr())
		assert.Nil(t, resp.GetProfile(), "失败响应不应携带资料")
	})

	t.Run("用户不存在返回 NotFound", func(t *testing.T) {
		uc, _ := newUsecase(t)
		resp, err := uc.UpdateUserAvatar(ctx, &userv1.UpdateUserAvatarRequest{Uid: "3001", Avatar: validAvatar()})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.NotFound, resp.GetErr())
		assert.Nil(t, resp.GetProfile(), "失败响应不应携带资料")
	})

	t.Run("首次设置头像时旧资产为空", func(t *testing.T) {
		uc, db := newUsecase(t)
		// 无头像用户（AssetID 为零值）
		require.NoError(t, db.Create(&models.UserProfile{
			BaseModel: common.BaseModel{ID: uid1},
			Name:      "无头像",
			Avatar:    &media.MediaFile{},
			Status:    userv1.UserStatus_USER_STATUS_ACTIVE,
		}).Error)

		resp, err := uc.UpdateUserAvatar(ctx, &userv1.UpdateUserAvatarRequest{Uid: "3001", Avatar: validAvatar()})
		require.NoError(t, err)
		require.False(t, resp.GetFail())
		assert.Empty(t, resp.GetOldAssetId(), "首次设置头像应返回空旧资产")

		var got models.UserProfile
		require.NoError(t, db.Where("id = ?", uid1).First(&got).Error)
		require.NotNil(t, got.Avatar)
		assert.Equal(t, int64(10002), got.Avatar.AssetID, "头像应更新为新资产")
	})

	t.Run("换头像返回旧资产ID并更新 avatar_id", func(t *testing.T) {
		uc, db := newUsecase(t)
		seedProfile(t, db, uid1, "旧昵称", "旧简介", userv1.UserStatus_USER_STATUS_ACTIVE)

		avatar := validAvatar()
		avatar.Base.Id = "10002" // 新头像文件 ID，与库中旧值不同
		resp, err := uc.UpdateUserAvatar(ctx, &userv1.UpdateUserAvatarRequest{Uid: "3001", Avatar: avatar})
		require.NoError(t, err)
		require.False(t, resp.GetFail())

		// 旧资产 = seed 的 uid1*100 = 300100，BFF 用它对旧资产减引用计数
		assert.Equal(t, "300100", resp.GetOldAssetId())

		var got models.UserProfile
		require.NoError(t, db.Where("id = ?", uid1).First(&got).Error)
		require.NotNil(t, got.Avatar)
		assert.Equal(t, int64(10002), got.Avatar.ID, "avatar_id 应更新为新头像")
		assert.Equal(t, int64(10002), got.Avatar.AssetID, "AssetID 应更新为新资产")
		assert.Equal(t, "旧昵称", got.Name, "昵称不在本接口更新范围")
		assert.Equal(t, "旧简介", got.Bio, "简介不在本接口更新范围")
		assert.Equal(t, datatypes.JSONMap{"theme": "dark"}, got.MetaData, "metadata 不在本接口更新范围")
	})
}

func TestUserUsecase_SetFollow(t *testing.T) {
	ctx := context.Background()

	t.Run("非法uid被拒绝", func(t *testing.T) {
		uc, _ := newUsecase(t)
		resp, err := uc.SetFollow(ctx, &userv1.SetFollowRequest{Uid: "bad", TargetUid: "3002"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetErr())
	})

	t.Run("非法目标uid被拒绝", func(t *testing.T) {
		uc, _ := newUsecase(t)
		resp, err := uc.SetFollow(ctx, &userv1.SetFollowRequest{Uid: "3001", TargetUid: "bad"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetErr())
	})

	t.Run("自关注被拒绝", func(t *testing.T) {
		uc, db := newUsecase(t)
		setupFollowers(t, db)
		resp, err := uc.SetFollow(ctx, &userv1.SetFollowRequest{Uid: "3001", TargetUid: "3001"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidArgument, resp.GetErr())

		assert.Equal(t, int64(7), followingsOf(t, db, uid1), "自关注不应改变计数")
	})

	t.Run("目标用户不存在返回 NotFound", func(t *testing.T) {
		uc, db := newUsecase(t)
		setupFollowers(t, db)
		resp, err := uc.SetFollow(ctx, &userv1.SetFollowRequest{Uid: "3001", TargetUid: "999999"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.NotFound, resp.GetErr())

		var cnt int64
		require.NoError(t, db.Table("user_follows").Count(&cnt).Error)
		assert.Zero(t, cnt)
	})

	t.Run("关注成功并递增双端计数", func(t *testing.T) {
		uc, db := newUsecase(t)
		setupFollowers(t, db)
		resp, err := uc.SetFollow(ctx, &userv1.SetFollowRequest{Uid: "3001", TargetUid: "3002"})
		require.NoError(t, err)
		assert.False(t, resp.GetFail())

		var cnt int64
		require.NoError(t, db.Table("user_follows").
			Where("fan_id = ? AND following_id = ?", uid1, uid2).Count(&cnt).Error)
		assert.Equal(t, int64(1), cnt)
		assert.Equal(t, int64(8), followingsOf(t, db, uid1), "fan.followings 应 +1")
		assert.Equal(t, int64(6), followersOf(t, db, uid2), "following.followers 应 +1")
	})
}

func TestUserUsecase_SetUnFollow(t *testing.T) {
	ctx := context.Background()

	t.Run("非法uid被拒绝", func(t *testing.T) {
		uc, _ := newUsecase(t)
		resp, err := uc.SetUnFollow(ctx, &userv1.SetUnFollowRequest{Uid: "bad", TargetUid: "3002"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetErr())
	})

	t.Run("自取关被拒绝", func(t *testing.T) {
		uc, db := newUsecase(t)
		setupFollowers(t, db)
		resp, err := uc.SetUnFollow(ctx, &userv1.SetUnFollowRequest{Uid: "3001", TargetUid: "3001"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidArgument, resp.GetErr())
	})

	t.Run("取关成功并递减双端计数", func(t *testing.T) {
		uc, db := newUsecase(t)
		setupFollowers(t, db)
		_, err := uc.SetFollow(ctx, &userv1.SetFollowRequest{Uid: "3001", TargetUid: "3002"})
		require.NoError(t, err)

		resp, err := uc.SetUnFollow(ctx, &userv1.SetUnFollowRequest{Uid: "3001", TargetUid: "3002"})
		require.NoError(t, err)
		assert.False(t, resp.GetFail())

		var cnt int64
		require.NoError(t, db.Table("user_follows").Count(&cnt).Error)
		assert.Zero(t, cnt, "关注关系应被删除")
		assert.Equal(t, int64(7), followingsOf(t, db, uid1), "fan.followings 应 -1")
		assert.Equal(t, int64(5), followersOf(t, db, uid2), "following.followers 应 -1")
	})

	t.Run("未关注时幂等成功", func(t *testing.T) {
		uc, db := newUsecase(t)
		setupFollowers(t, db)
		resp, err := uc.SetUnFollow(ctx, &userv1.SetUnFollowRequest{Uid: "3001", TargetUid: "3002"})
		require.NoError(t, err)
		assert.False(t, resp.GetFail())
		assert.Equal(t, int64(7), followingsOf(t, db, uid1), "关系不存在不应递减")
	})
}

func TestUserUsecase_GetFollowingRelation(t *testing.T) {
	ctx := context.Background()

	t.Run("非法uid被拒绝", func(t *testing.T) {
		uc, _ := newUsecase(t)
		resp, err := uc.GetFollowingRelation(ctx, &userv1.GetFollowingRelationRequest{Uid: "bad", TargetUid: "3002"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetErr())
	})

	t.Run("单向关注", func(t *testing.T) {
		uc, db := newUsecase(t)
		setupFollowers(t, db)
		_, err := uc.SetFollow(ctx, &userv1.SetFollowRequest{Uid: "3001", TargetUid: "3002"})
		require.NoError(t, err)

		resp, err := uc.GetFollowingRelation(ctx, &userv1.GetFollowingRelationRequest{Uid: "3001", TargetUid: "3002"})
		require.NoError(t, err)
		assert.False(t, resp.GetFail())
		assert.True(t, resp.GetIsFollowing())
		assert.False(t, resp.GetIsFollower())
	})

	t.Run("互相关注", func(t *testing.T) {
		uc, db := newUsecase(t)
		setupFollowers(t, db)
		_, err := uc.SetFollow(ctx, &userv1.SetFollowRequest{Uid: "3001", TargetUid: "3002"})
		require.NoError(t, err)
		_, err = uc.SetFollow(ctx, &userv1.SetFollowRequest{Uid: "3002", TargetUid: "3001"})
		require.NoError(t, err)

		resp, err := uc.GetFollowingRelation(ctx, &userv1.GetFollowingRelationRequest{Uid: "3001", TargetUid: "3002"})
		require.NoError(t, err)
		assert.True(t, resp.GetIsFollowing())
		assert.True(t, resp.GetIsFollower())
	})
}

func TestUserUsecase_GetFollowings(t *testing.T) {
	ctx := context.Background()

	t.Run("非法uid被拒绝", func(t *testing.T) {
		uc, _ := newUsecase(t)
		resp, err := uc.GetFollowings(ctx, &userv1.GetFollowingsRequest{Uid: "bad"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetErr())
	})

	t.Run("返回关注列表与游标", func(t *testing.T) {
		uc, db := newUsecase(t)
		setupFollowers(t, db)
		_, err := uc.SetFollow(ctx, &userv1.SetFollowRequest{Uid: "3001", TargetUid: "3002"})
		require.NoError(t, err)

		resp, err := uc.GetFollowings(ctx, &userv1.GetFollowingsRequest{Uid: "3001"})
		require.NoError(t, err)
		require.False(t, resp.GetFail())
		require.Len(t, resp.GetUserBriefs(), 1)
		assert.Equal(t, "3002", resp.GetUserBriefs()[0].GetUid())
		assert.Equal(t, "target", resp.GetUserBriefs()[0].GetName())
		assert.Nil(t, resp.GetCursor(), "短取即到底：短页（< limit）带数据回空游标")
	})

	t.Run("无关注返回空列表且无游标", func(t *testing.T) {
		uc, db := newUsecase(t)
		setupFollowers(t, db)
		resp, err := uc.GetFollowings(ctx, &userv1.GetFollowingsRequest{Uid: "3001"})
		require.NoError(t, err)
		assert.False(t, resp.GetFail())
		assert.Empty(t, resp.GetUserBriefs())
		assert.Nil(t, resp.GetCursor())
	})
}

func TestUserUsecase_BanUser(t *testing.T) {
	ctx := context.Background()

	t.Run("非法uid被拒绝", func(t *testing.T) {
		uc, _ := newUsecase(t)
		resp, err := uc.BanUser(ctx, &userv1.BanUserRequest{Uid: "bad"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetErr())
	})

	t.Run("封禁成功", func(t *testing.T) {
		uc, db := newUsecase(t)
		setupFollowers(t, db)
		resp, err := uc.BanUser(ctx, &userv1.BanUserRequest{Uid: "3001"})
		require.NoError(t, err)
		assert.False(t, resp.GetFail())

		var got models.UserProfile
		require.NoError(t, db.Where("id = ?", uid1).First(&got).Error)
		assert.Equal(t, userv1.UserStatus_USER_STATUS_BANNED, got.Status)
	})
}

// TestUserUsecase_GetUserBrief 批量查询用户简要信息：每个结果必须回填请求 uid
// （回归：NotFound / 非法 uid 结果曾漏回填，调用方无法定位失败项）
func TestUserUsecase_GetUserBrief(t *testing.T) {
	ctx := context.Background()

	t.Run("空入参返回空结果", func(t *testing.T) {
		// Arrange
		uc, _ := newUsecase(t)

		// Act
		resp, err := uc.GetUserBrief(ctx, &userv1.GetUserBriefRequest{})

		// Assert
		require.NoError(t, err)
		assert.Empty(t, resp.GetResults())
	})

	t.Run("混合结果：命中 / 非法uid / 未找到", func(t *testing.T) {
		// Arrange
		uc, db := newUsecase(t)
		seedProfile(t, db, uid1, "user-1", "bio-1", userv1.UserStatus_USER_STATUS_ACTIVE)

		// Act
		resp, err := uc.GetUserBrief(ctx, &userv1.GetUserBriefRequest{
			Uids: []string{"3001", "bad", "999999"},
		})

		// Assert：非法 uid 结果先返回，其后按有效 uid 顺序
		require.NoError(t, err)
		require.Len(t, resp.GetResults(), 3)

		assert.True(t, resp.GetResults()[0].GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetResults()[0].GetErr())
		assert.Equal(t, "bad", resp.GetResults()[0].GetUid(), "失败项应回填请求串")
		assert.Nil(t, resp.GetResults()[0].GetBrief())

		assert.False(t, resp.GetResults()[1].GetFail())
		assert.Nil(t, resp.GetResults()[1].GetErr())
		assert.Equal(t, "3001", resp.GetResults()[1].GetUid(), "成功项应回填 uid")
		require.NotNil(t, resp.GetResults()[1].GetBrief())
		assert.Equal(t, "3001", resp.GetResults()[1].GetBrief().GetUid())
		assert.Equal(t, "user-1", resp.GetResults()[1].GetBrief().GetName())
		require.NotNil(t, resp.GetResults()[1].GetBrief().GetAvatar(), "seed 用户带头像")
		assert.Equal(t, "avatar/obj", resp.GetResults()[1].GetBrief().GetAvatar().GetObjKey())

		assert.True(t, resp.GetResults()[2].GetFail())
		assert.Equal(t, errmsg.NotFound, resp.GetResults()[2].GetErr())
		assert.Equal(t, "999999", resp.GetResults()[2].GetUid(), "NotFound 项必须回填 uid（回归）")
		assert.Nil(t, resp.GetResults()[2].GetBrief())
	})

	t.Run("全部未命中时每项都回填请求 uid（回归）", func(t *testing.T) {
		// Arrange
		uc, _ := newUsecase(t)

		// Act
		resp, err := uc.GetUserBrief(ctx, &userv1.GetUserBriefRequest{
			Uids: []string{"111111", "bad-uid", "222222"},
		})

		// Assert
		require.NoError(t, err)
		require.Len(t, resp.GetResults(), 3)

		for i, wantUID := range []string{"bad-uid", "111111", "222222"} {
			got := resp.GetResults()[i]
			assert.True(t, got.GetFail())
			assert.Equal(t, wantUID, got.GetUid(), "每个结果都必须回填对应请求 uid")
			assert.Nil(t, got.GetBrief())
		}
		assert.Equal(t, errmsg.InvalidID, resp.GetResults()[0].GetErr())
		assert.Equal(t, errmsg.NotFound, resp.GetResults()[1].GetErr())
		assert.Equal(t, errmsg.NotFound, resp.GetResults()[2].GetErr())
	})
}

// ---------- ToBrief 防御 ----------

func TestToBriefNilAvatar(t *testing.T) {
	p := &models.UserProfile{BaseModel: common.BaseModel{ID: 3001}, Name: "no-avatar"}

	var brief *userv1.UserBrief
	assert.NotPanics(t, func() { brief = p.ToBrief() })
	require.NotNil(t, brief)
	assert.Equal(t, "3001", brief.GetUid())
	assert.Equal(t, "no-avatar", brief.GetName())
	assert.Nil(t, brief.GetAvatar(), "无头像时不应填充 avatar 字段")
}
