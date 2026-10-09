package v1

import (
	"context"
	"strings"
	"testing"

	commonv1 "rednote/gen/go/common/v1"
	mediav1 "rednote/gen/go/media/v1"
	userv1 "rednote/gen/go/user/v1"
	common "rednote/internal/common/models"
	models "rednote/internal/user/models"
	"rednote/packages/errmsg"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gen"
	"gorm.io/gorm"
)

// ---------- ValidateName ----------

func TestValidateName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"普通昵称", "alice", true},
		{"中文昵称", "小红薯123", true},
		{"含 emoji", "🍑小红薯", true},
		{"单字符", "a", true},
		{"首尾空白但有内容", "  alice  ", true},
		{"正好64字符", strings.Repeat("a", NameMaxLen), true},
		{"64个汉字", strings.Repeat("好", NameMaxLen), true},
		{"空串", "", false},
		{"纯空格", "   ", false},
		{"纯制表符", "\t\t", false},
		{"65字符超长", strings.Repeat("a", NameMaxLen+1), false},
		{"65个汉字超长", strings.Repeat("好", NameMaxLen+1), false},
		{"含换行", "ali\nce", false},
		{"含制表符", "ali\tce", false},
		{"含NUL控制字符", "ali\x00ce", false},
		{"含ANSI转义序列", "alice\x1b[31m", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ValidateName(tc.in))
		})
	}
}

// ---------- ValidateBio ----------

func TestValidateBio(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"空简介合法", "", true},
		{"普通简介", "这个人很懒，什么都没写", true},
		{"多行简介", "第一行\n第二行\t缩进", true},
		{"含回车换行", "line1\r\nline2", true},
		{"正好512字符", strings.Repeat("好", BioMaxLen), true},
		{"513字符超长", strings.Repeat("好", BioMaxLen+1), false},
		{"含NUL控制字符", "ab\x00cd", false},
		{"含ANSI转义序列", "ab\x1b[31mcd", false},
		{"含退格符", "ab\x08cd", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ValidateBio(tc.in))
		})
	}
}

// ---------- CreateUser（biz 层） ----------

// fakeUserRepo 内存仓储，用于 biz 层单测（不落库）
type fakeUserRepo struct {
	created  []*models.UserProfile
	updated  []*models.UserProfile
	profiles map[int64]*models.UserProfile
	// follows 关注关系：key = [fanID, followingID]
	follows map[[2]int64]bool
}

func (f *fakeUserRepo) WithTX(_ *gorm.DB) UserRepository { return f }

// CreateUser 模拟 ON CONFLICT DO NOTHING：重复 ID 幂等返回 ok=false
func (f *fakeUserRepo) CreateUser(_ context.Context, profile *models.UserProfile) (bool, error) {
	if _, ok := f.profiles[profile.ID]; ok {
		return false, nil
	}
	f.created = append(f.created, profile)
	if f.profiles == nil {
		f.profiles = make(map[int64]*models.UserProfile)
	}
	f.profiles[profile.ID] = profile
	return true, nil
}

func (f *fakeUserRepo) UpdateUser(_ context.Context, profile *models.UserProfile) (gen.ResultInfo, error) {
	old, ok := f.profiles[profile.ID]
	if !ok {
		return gen.ResultInfo{RowsAffected: 0}, nil
	}
	// 与 repo 实现同语义：仅覆盖 name/bio/头像/metadata，状态与统计保留
	updated := *profile
	updated.CreatedAt = old.CreatedAt
	updated.Followers = old.Followers
	updated.Followings = old.Followings
	updated.Status = old.Status
	f.profiles[profile.ID] = &updated
	f.updated = append(f.updated, &updated)
	return gen.ResultInfo{RowsAffected: 1}, nil
}

func (f *fakeUserRepo) GetUserProfiles(_ context.Context, uids []int64) ([]*models.UserProfile, error) {
	out := make([]*models.UserProfile, 0, len(uids))
	for _, id := range uids {
		if p, ok := f.profiles[id]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeUserRepo) SetFollow(_ context.Context, fanID, followingID int64) (bool, error) {
	// 与 repo 同语义：双方用户均存在才关注，自关注拒绝
	if _, ok := f.profiles[fanID]; !ok {
		return false, nil
	}
	if _, ok := f.profiles[followingID]; !ok {
		return false, nil
	}
	if fanID == followingID {
		return false, nil
	}
	if f.follows == nil {
		f.follows = make(map[[2]int64]bool)
	}
	f.follows[[2]int64{fanID, followingID}] = true
	return true, nil
}

func (f *fakeUserRepo) SetUnFollow(_ context.Context, fanID, followingID int64) error {
	delete(f.follows, [2]int64{fanID, followingID})
	return nil
}

func (f *fakeUserRepo) GetFollowings(_ context.Context, uid int64, _ int64, _ int64) ([]*models.UserFollow, error) {
	out := make([]*models.UserFollow, 0)
	for pair := range f.follows {
		if pair[0] == uid {
			out = append(out, &models.UserFollow{FanID: pair[0], FollowingID: pair[1], Following: f.profiles[pair[1]]})
		}
	}
	return out, nil
}

func (f *fakeUserRepo) GetFollowingRelation(_ context.Context, fanID, followingID int64) (bool, bool, error) {
	return f.follows[[2]int64{fanID, followingID}], f.follows[[2]int64{followingID, fanID}], nil
}

func (f *fakeUserRepo) BanUser(_ context.Context, uid int64) error {
	if p, ok := f.profiles[uid]; ok {
		p.Status = userv1.UserStatus_USER_STATUS_BANNED
	}
	return nil
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
			req:     newCreateUserReq("3001", strings.Repeat("好", NameMaxLen+1), "简介", validAvatar()),
			wantErr: errmsg.InvalidName,
		},
		{
			name:    "超长简介被拒绝",
			req:     newCreateUserReq("3001", "小红薯", strings.Repeat("好", BioMaxLen+1), validAvatar()),
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
			repo := &fakeUserRepo{}
			if tc.exist {
				_, err := repo.CreateUser(ctx, newProfile(3001, "已存在", ""))
				require.NoError(t, err)
			}
			baseCreated := len(repo.created) // 预置记录不计入落库断言
			uc := NewUserUsecase(repo)

			resp, err := uc.CreateUser(ctx, tc.req)
			require.NoError(t, err)
			require.NotNil(t, resp)

			if tc.wantErr != nil {
				assert.True(t, resp.GetFail())
				assert.Equal(t, tc.wantErr, resp.GetErr())
				assert.Len(t, repo.created, baseCreated, "被拒绝的请求不应落库")
				return
			}

			assert.False(t, resp.GetFail())
			require.NotNil(t, resp.GetProfile())
			assert.Equal(t, "3001", resp.GetProfile().GetBrief().GetUid())
			assert.Equal(t, "小红薯", resp.GetProfile().GetBrief().GetName())
			assert.Equal(t, userv1.UserStatus_USER_STATUS_ACTIVE, resp.GetProfile().GetStatus())
			require.Len(t, repo.created, 1)
			assert.Equal(t, int64(3001), repo.created[0].ID)
			assert.NotNil(t, repo.created[0].MetaData, "metadata 空值应落库为空对象 {}")
		})
	}
}

// newUpdateUserReq 构造更新用户请求（头像按需传入）
func newUpdateUserReq(uid, name, bio string, avatar *mediav1.MediaFile) *userv1.UpdateUserRequest {
	return &userv1.UpdateUserRequest{
		Uid:      uid,
		Name:     name,
		Bio:      bio,
		Avatar:   avatar,
		Metadata: &commonv1.JsonStr{Payload: "{}"},
	}
}

func TestUserUsecase_UpdateUser(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name    string
		exist   bool // 目标用户是否已存在
		req     *userv1.UpdateUserRequest
		wantErr *commonv1.Error // nil 表示应更新成功
	}{
		{
			name:    "无头像请求被拒绝",
			exist:   true,
			req:     newUpdateUserReq("3001", "新昵称", "新简介", nil),
			wantErr: errmsg.InvalidAvatar,
		},
		{
			name:    "非法uid被拒绝",
			req:     newUpdateUserReq("not-a-number", "新昵称", "新简介", validAvatar()),
			wantErr: errmsg.InvalidID,
		},
		{
			name:    "纯空白昵称被拒绝",
			exist:   true,
			req:     newUpdateUserReq("3001", "   ", "新简介", validAvatar()),
			wantErr: errmsg.InvalidName,
		},
		{
			name:    "超长简介被拒绝",
			exist:   true,
			req:     newUpdateUserReq("3001", "新昵称", strings.Repeat("好", BioMaxLen+1), validAvatar()),
			wantErr: errmsg.InvalidBio,
		},
		{
			name:  "非法 metadata 被拒绝",
			exist: true,
			req: &userv1.UpdateUserRequest{
				Uid:      "3001",
				Name:     "新昵称",
				Bio:      "新简介",
				Avatar:   validAvatar(),
				Metadata: &commonv1.JsonStr{Payload: "not-json"},
			},
			wantErr: errmsg.InvalidArgument,
		},
		{
			name:    "用户不存在返回 NotFound",
			req:     newUpdateUserReq("3001", "新昵称", "新简介", validAvatar()),
			wantErr: errmsg.NotFound,
		},
		{
			name:  "更新成功",
			exist: true,
			req:   newUpdateUserReq("3001", "新昵称", "新简介", validAvatar()),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeUserRepo{}
			if tc.exist {
				_, err := repo.CreateUser(ctx, newProfile(3001, "旧昵称", "旧简介"))
				require.NoError(t, err)
			}
			uc := NewUserUsecase(repo)

			resp, err := uc.UpdateUser(ctx, tc.req)
			require.NoError(t, err)
			require.NotNil(t, resp)

			if tc.wantErr != nil {
				assert.True(t, resp.GetFail())
				assert.Equal(t, tc.wantErr, resp.GetErr())
				assert.Empty(t, repo.updated, "被拒绝的请求不应更新落库")
				return
			}

			assert.False(t, resp.GetFail())
			require.NotNil(t, resp.GetProfile())
			assert.Equal(t, "新昵称", resp.GetProfile().GetBrief().GetName())
			assert.Equal(t, "新简介", resp.GetProfile().GetBio())
			assert.Equal(t, userv1.UserStatus_USER_STATUS_ACTIVE, resp.GetProfile().GetStatus(),
				"回读应包含未在本接口修改的状态与统计")
			require.Len(t, repo.updated, 1)
			assert.Equal(t, "新昵称", repo.updated[0].Name)
			assert.Empty(t, repo.updated[0].MetaData, "空 metadata 应覆盖为 {}")
		})
	}
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

// ---------- 关注关系（biz 层） ----------

// newFollowRepo 预置两个用户（uid1/uid2）的仓储
func newFollowRepo(ctx context.Context) *fakeUserRepo {
	repo := &fakeUserRepo{}
	_, _ = repo.CreateUser(ctx, newProfile(uid1, "fan", ""))
	_, _ = repo.CreateUser(ctx, newProfile(uid2, "target", ""))
	return repo
}

func TestUserUsecase_SetFollow(t *testing.T) {
	ctx := context.Background()

	t.Run("非法uid被拒绝", func(t *testing.T) {
		uc := NewUserUsecase(newFollowRepo(ctx))
		resp, err := uc.SetFollow(ctx, &userv1.SetFollowRequest{Uid: "bad", TargetUid: "3002"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetErr())
	})

	t.Run("非法目标uid被拒绝", func(t *testing.T) {
		uc := NewUserUsecase(newFollowRepo(ctx))
		resp, err := uc.SetFollow(ctx, &userv1.SetFollowRequest{Uid: "3001", TargetUid: "bad"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetErr())
	})

	t.Run("自关注被拒绝", func(t *testing.T) {
		uc := NewUserUsecase(newFollowRepo(ctx))
		resp, err := uc.SetFollow(ctx, &userv1.SetFollowRequest{Uid: "3001", TargetUid: "3001"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidArgument, resp.GetErr())
	})

	t.Run("目标用户不存在返回 NotFound", func(t *testing.T) {
		uc := NewUserUsecase(newFollowRepo(ctx))
		resp, err := uc.SetFollow(ctx, &userv1.SetFollowRequest{Uid: "3001", TargetUid: "999999"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.NotFound, resp.GetErr())
	})

	t.Run("关注成功", func(t *testing.T) {
		repo := newFollowRepo(ctx)
		uc := NewUserUsecase(repo)
		resp, err := uc.SetFollow(ctx, &userv1.SetFollowRequest{Uid: "3001", TargetUid: "3002"})
		require.NoError(t, err)
		assert.False(t, resp.GetFail())
		assert.True(t, repo.follows[[2]int64{uid1, uid2}], "关注关系应建立")
	})
}

func TestUserUsecase_SetUnFollow(t *testing.T) {
	ctx := context.Background()

	t.Run("非法uid被拒绝", func(t *testing.T) {
		uc := NewUserUsecase(newFollowRepo(ctx))
		resp, err := uc.SetUnFollow(ctx, &userv1.SetUnFollowRequest{Uid: "bad", TargetUid: "3002"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetErr())
	})

	t.Run("自取关被拒绝", func(t *testing.T) {
		uc := NewUserUsecase(newFollowRepo(ctx))
		resp, err := uc.SetUnFollow(ctx, &userv1.SetUnFollowRequest{Uid: "3001", TargetUid: "3001"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidArgument, resp.GetErr())
	})

	t.Run("取关成功", func(t *testing.T) {
		repo := newFollowRepo(ctx)
		_, err := repo.SetFollow(ctx, uid1, uid2)
		require.NoError(t, err)

		uc := NewUserUsecase(repo)
		resp, err := uc.SetUnFollow(ctx, &userv1.SetUnFollowRequest{Uid: "3001", TargetUid: "3002"})
		require.NoError(t, err)
		assert.False(t, resp.GetFail())
		assert.False(t, repo.follows[[2]int64{uid1, uid2}], "关注关系应被移除")
	})

	t.Run("未关注时幂等成功", func(t *testing.T) {
		uc := NewUserUsecase(newFollowRepo(ctx))
		resp, err := uc.SetUnFollow(ctx, &userv1.SetUnFollowRequest{Uid: "3001", TargetUid: "3002"})
		require.NoError(t, err)
		assert.False(t, resp.GetFail())
	})
}

func TestUserUsecase_GetFollowingRelation(t *testing.T) {
	ctx := context.Background()

	t.Run("非法uid被拒绝", func(t *testing.T) {
		uc := NewUserUsecase(newFollowRepo(ctx))
		resp, err := uc.GetFollowingRelation(ctx, &userv1.GetFollowingRelationRequest{Uid: "bad", TargetUid: "3002"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetErr())
	})

	t.Run("单向关注", func(t *testing.T) {
		repo := newFollowRepo(ctx)
		_, err := repo.SetFollow(ctx, uid1, uid2)
		require.NoError(t, err)

		uc := NewUserUsecase(repo)
		resp, err := uc.GetFollowingRelation(ctx, &userv1.GetFollowingRelationRequest{Uid: "3001", TargetUid: "3002"})
		require.NoError(t, err)
		assert.False(t, resp.GetFail())
		assert.True(t, resp.GetIsFollowing())
		assert.False(t, resp.GetIsFollower())
	})

	t.Run("互相关注", func(t *testing.T) {
		repo := newFollowRepo(ctx)
		_, err := repo.SetFollow(ctx, uid1, uid2)
		require.NoError(t, err)
		_, err = repo.SetFollow(ctx, uid2, uid1)
		require.NoError(t, err)

		uc := NewUserUsecase(repo)
		resp, err := uc.GetFollowingRelation(ctx, &userv1.GetFollowingRelationRequest{Uid: "3001", TargetUid: "3002"})
		require.NoError(t, err)
		assert.True(t, resp.GetIsFollowing())
		assert.True(t, resp.GetIsFollower())
	})
}

func TestUserUsecase_GetFollowings(t *testing.T) {
	ctx := context.Background()

	t.Run("非法uid被拒绝", func(t *testing.T) {
		uc := NewUserUsecase(newFollowRepo(ctx))
		resp, err := uc.GetFollowings(ctx, &userv1.GetFollowingsRequest{Uid: "bad"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetErr())
	})

	t.Run("返回关注列表与游标", func(t *testing.T) {
		repo := newFollowRepo(ctx)
		_, err := repo.SetFollow(ctx, uid1, uid2)
		require.NoError(t, err)

		uc := NewUserUsecase(repo)
		resp, err := uc.GetFollowings(ctx, &userv1.GetFollowingsRequest{Uid: "3001"})
		require.NoError(t, err)
		require.False(t, resp.GetFail())
		require.Len(t, resp.GetUserBriefs(), 1)
		assert.Equal(t, "3002", resp.GetUserBriefs()[0].GetUid())
		assert.NotNil(t, resp.GetCursor(), "有数据时应返回游标")
	})

	t.Run("无关注返回空列表且无游标", func(t *testing.T) {
		uc := NewUserUsecase(newFollowRepo(ctx))
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
		uc := NewUserUsecase(newFollowRepo(ctx))
		resp, err := uc.BanUser(ctx, &userv1.BanUserRequest{Uid: "bad"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InvalidID, resp.GetErr())
	})

	t.Run("封禁成功", func(t *testing.T) {
		repo := newFollowRepo(ctx)
		uc := NewUserUsecase(repo)
		resp, err := uc.BanUser(ctx, &userv1.BanUserRequest{Uid: "3001"})
		require.NoError(t, err)
		assert.False(t, resp.GetFail())
		assert.Equal(t, userv1.UserStatus_USER_STATUS_BANNED, repo.profiles[uid1].Status)
	})
}
