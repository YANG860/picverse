// user 服务黑盒端到端测试：CreateUser → 资料读取 → 部分更新 → 换头像 → 关注关系 → 封禁。
//
// 前置条件：user 服务已部署（依赖 PostgreSQL）。
// 运行：
//
//	go test ./test/e2e/... -run TestUserServiceE2E -count=1          # 默认 http://localhost:8083
//	USER_E2E_ADDR=http://host:8083 go test ./test/e2e/... -run TestUserServiceE2E
//	go test -short ./...                                             # -short 跳过
//
// 注：e2e 产生的数据（用户资料、关注关系）留在服务端数据库中不清理。
package e2e

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	commonv1 "rednote/gen/go/common/v1"
	mediav1 "rednote/gen/go/media/v1"
	userv1 "rednote/gen/go/user/v1"
	"rednote/gen/go/user/v1/userv1connect"
	"rednote/packages/errmsg"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const defaultUserAddr = "http://localhost:8083"

func userAddr() string { return e2eAddr("USER_E2E_ADDR", defaultUserAddr) }

// newUserClient 创建 connect 客户端，并探测服务可达性（不可达则跳过测试）
func newUserClient(t *testing.T) userv1connect.UserServiceClient {
	t.Helper()

	addr := userAddr()
	mustReachable(t, addr, "/user.v1.UserService/")
	return userv1connect.NewUserServiceClient(http.DefaultClient, addr)
}

// TestUserServiceE2E 用户资料与关注关系黑盒测试。
// 步骤之间有依赖（同一账号的资料/关注流转链），因此在一个测试内按序执行：
//
//	建档 → 读取 → 批量简要 → 部分更新 → 换头像（old_asset_id）→ 关注/取关
//	→ 关注关系/列表 → 封禁 → 错误路径
func TestUserServiceE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: -short 模式跳过依赖外部部署服务的测试")
	}

	client := newUserClient(t)
	ctx := context.Background()

	now := time.Now().UnixNano()
	uidA := e2eID(now)
	uidB := e2eID(now + 1)
	missingUID := e2eID(now + 2)
	assetA := e2eID(now + 3)
	assetB := e2eID(now + 4)
	avatarA := e2eMediaFile(e2eID(now+5), assetA, "e2e/avatar-a.jpg")
	avatarB := e2eMediaFile(e2eID(now+6), assetB, "e2e/avatar-b.jpg")

	// ---- 1. 创建用户资料（头像必填）----
	create, err := client.CreateUser(ctx, connect.NewRequest(&userv1.CreateUserRequest{
		Uid:      uidA,
		Name:     "e2e user a",
		Avatar:   avatarA,
		Bio:      "e2e bio a",
		Metadata: &commonv1.JsonStr{Payload: `{"level":1}`},
	}))
	require.NoError(t, err, "CreateUser RPC")
	require.False(t, create.Msg.GetFail(), "创建用户业务失败: %v", create.Msg.GetErr())
	assert.Nil(t, create.Msg.GetErr(), "成功响应不应携带错误码")

	// 全字段检测：CreateUser 响应的 UserProfile 每个字段都应与请求一致
	profile := create.Msg.GetProfile()
	require.NotNil(t, profile, "应返回资料")
	assert.Equal(t, uidA, profile.GetBrief().GetUid())
	assert.Equal(t, "e2e user a", profile.GetBrief().GetName())
	assert.Equal(t, "e2e bio a", profile.GetBio())
	assert.Contains(t, profile.GetMetadata().GetPayload(), "level", "metadata 应落库")
	assert.Equal(t, userv1.UserStatus_USER_STATUS_ACTIVE, profile.GetStatus(), "新建用户应为 ACTIVE")
	require.NotNil(t, profile.GetStat(), "统计字段应存在")
	assert.Zero(t, profile.GetStat().GetFollowers())
	assert.Zero(t, profile.GetStat().GetFollowings())

	// 全字段检测：头像媒体关联字段完整落库
	gotAvatar := profile.GetBrief().GetAvatar()
	require.NotNil(t, gotAvatar, "头像必填，应返回头像")
	assert.Equal(t, "e2e/avatar-a.jpg", gotAvatar.GetObjKey(), "头像应落库")
	assert.Equal(t, assetA, gotAvatar.GetAssetId())
	assert.Equal(t, avatarA.GetBase().GetId(), gotAvatar.GetBase().GetId())
	assert.Equal(t, mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN, gotAvatar.GetType())
	assert.Equal(t, "rednote", gotAvatar.GetBucket())
	t.Logf("创建用户 uid=%s", uidA)

	// ---- 2. 重复建档 / 校验失败路径 ----
	dup, err := client.CreateUser(ctx, connect.NewRequest(&userv1.CreateUserRequest{
		Uid: uidA, Name: "e2e user a again", Avatar: avatarA,
	}))
	require.NoError(t, err, "重复建档 RPC")
	assertBizErr(t, dup.Msg.GetFail(), dup.Msg.GetErr(), errmsg.AlreadyExist, "重复建档")

	badName, err := client.CreateUser(ctx, connect.NewRequest(&userv1.CreateUserRequest{
		Uid: missingUID, Name: strings.Repeat("x", 65), Avatar: avatarA,
	}))
	require.NoError(t, err, "非法昵称 RPC")
	assertBizErr(t, badName.Msg.GetFail(), badName.Msg.GetErr(), errmsg.InvalidName, "昵称超长")

	noAvatar, err := client.CreateUser(ctx, connect.NewRequest(&userv1.CreateUserRequest{
		Uid: missingUID, Name: "e2e no avatar",
	}))
	require.NoError(t, err, "缺头像 RPC")
	assertBizErr(t, noAvatar.Msg.GetFail(), noAvatar.Msg.GetErr(), errmsg.InvalidAvatar, "缺头像建档")

	badUID, err := client.CreateUser(ctx, connect.NewRequest(&userv1.CreateUserRequest{
		Uid: "bad", Name: "e2e bad uid", Avatar: avatarA,
	}))
	require.NoError(t, err, "非法 uid RPC")
	assertBizErr(t, badUID.Msg.GetFail(), badUID.Msg.GetErr(), errmsg.InvalidID, "非法 uid")

	// ---- 3. 读取资料（全字段检测）----
	get, err := client.GetUserProfile(ctx, connect.NewRequest(&userv1.GetUserProfileRequest{Uid: uidA}))
	require.NoError(t, err, "GetUserProfile RPC")
	require.False(t, get.Msg.GetFail())
	assert.Nil(t, get.Msg.GetErr(), "成功响应不应携带错误码")
	got := get.Msg.GetProfile()
	require.NotNil(t, got)
	assert.Equal(t, uidA, got.GetBrief().GetUid())
	assert.Equal(t, "e2e user a", got.GetBrief().GetName())
	assert.Equal(t, "e2e/avatar-a.jpg", got.GetBrief().GetAvatar().GetObjKey())
	assert.Equal(t, "e2e bio a", got.GetBio())
	assert.Contains(t, got.GetMetadata().GetPayload(), "level")
	assert.Equal(t, userv1.UserStatus_USER_STATUS_ACTIVE, got.GetStatus())
	assert.Zero(t, got.GetStat().GetFollowers())
	assert.Zero(t, got.GetStat().GetFollowings())

	// ---- 4. 批量简要：非法 ID 先返回且原样回填，其余按请求序回填 ----
	brief, err := client.GetUserBrief(ctx, connect.NewRequest(&userv1.GetUserBriefRequest{
		Uids: []string{"bad", uidA, missingUID},
	}))
	require.NoError(t, err, "GetUserBrief RPC")
	require.Len(t, brief.Msg.GetResults(), 3)
	assert.Equal(t, "bad", brief.Msg.GetResults()[0].GetUid(), "非法 ID 应原样回填")
	assert.True(t, brief.Msg.GetResults()[0].GetFail())
	assert.Equal(t, errmsg.InvalidID, brief.Msg.GetResults()[0].GetErr())

	assert.Equal(t, uidA, brief.Msg.GetResults()[1].GetUid())
	assert.False(t, brief.Msg.GetResults()[1].GetFail())
	assert.Nil(t, brief.Msg.GetResults()[1].GetErr())
	assert.Equal(t, "e2e user a", brief.Msg.GetResults()[1].GetBrief().GetName())
	assert.Equal(t, uidA, brief.Msg.GetResults()[1].GetBrief().GetUid(), "brief 应携带 uid")
	assert.Equal(t, "e2e/avatar-a.jpg", brief.Msg.GetResults()[1].GetBrief().GetAvatar().GetObjKey(), "brief 应携带头像")

	assert.Equal(t, missingUID, brief.Msg.GetResults()[2].GetUid(), "未命中应回填规范化 uid")
	assert.True(t, brief.Msg.GetResults()[2].GetFail())
	assert.Equal(t, errmsg.NotFound, brief.Msg.GetResults()[2].GetErr())

	// 空集合边界：无入参返回空结果
	emptyBrief, err := client.GetUserBrief(ctx, connect.NewRequest(&userv1.GetUserBriefRequest{}))
	require.NoError(t, err, "GetUserBrief(空) RPC")
	assert.Empty(t, emptyBrief.Msg.GetResults(), "空入参应返回空结果")

	// ---- 5. 部分更新：只改昵称，未传的 bio 不变 ----
	upd, err := client.UpdateUserProfile(ctx, connect.NewRequest(&userv1.UpdateUserProfileRequest{
		Uid:  uidA,
		Name: wrapperspb.String("e2e user a v2"),
	}))
	require.NoError(t, err, "UpdateUserProfile RPC")
	require.False(t, upd.Msg.GetFail(), "更新资料业务失败: %v", upd.Msg.GetErr())
	assert.Nil(t, upd.Msg.GetErr(), "成功响应不应携带错误码")
	assert.Equal(t, uidA, upd.Msg.GetProfile().GetBrief().GetUid())
	assert.Equal(t, "e2e user a v2", upd.Msg.GetProfile().GetBrief().GetName(), "昵称应被更新")
	assert.Equal(t, "e2e bio a", upd.Msg.GetProfile().GetBio(), "未传的 bio 不应被修改")
	assert.Contains(t, upd.Msg.GetProfile().GetMetadata().GetPayload(), "level", "未传的 metadata 不应被修改")

	// ---- 6. 换头像：返回旧资产 ID 供 BFF 调整引用计数 ----
	avatar, err := client.UpdateUserAvatar(ctx, connect.NewRequest(&userv1.UpdateUserAvatarRequest{
		Uid:    uidA,
		Avatar: avatarB,
	}))
	require.NoError(t, err, "UpdateUserAvatar RPC")
	require.False(t, avatar.Msg.GetFail(), "换头像业务失败: %v", avatar.Msg.GetErr())
	assert.Nil(t, avatar.Msg.GetErr(), "成功响应不应携带错误码")
	assert.Equal(t, "e2e/avatar-b.jpg", avatar.Msg.GetProfile().GetBrief().GetAvatar().GetObjKey(), "头像应被替换")
	assert.Equal(t, assetA, avatar.Msg.GetOldAssetId(), "应返回被替换的旧头像资产 ID")
	assert.Equal(t, "e2e user a v2", avatar.Msg.GetProfile().GetBrief().GetName(), "换头像不应修改昵称")
	assert.Equal(t, userv1.UserStatus_USER_STATUS_ACTIVE, avatar.Msg.GetProfile().GetStatus(), "换头像不应修改状态")

	// ---- 7. 用户状态 ----
	st, err := client.GetUserStatus(ctx, connect.NewRequest(&userv1.GetUserStatusRequest{Uid: uidA}))
	require.NoError(t, err, "GetUserStatus RPC")
	require.False(t, st.Msg.GetFail())
	assert.Equal(t, userv1.UserStatus_USER_STATUS_ACTIVE, st.Msg.GetStatus())

	// ---- 8. 建第二个用户并关注 ----
	createB, err := client.CreateUser(ctx, connect.NewRequest(&userv1.CreateUserRequest{
		Uid: uidB, Name: "e2e user b", Avatar: e2eMediaFile(e2eID(now+7), e2eID(now+8), "e2e/avatar-c.jpg"),
	}))
	require.NoError(t, err, "CreateUser(B) RPC")
	require.False(t, createB.Msg.GetFail(), "创建用户B业务失败: %v", createB.Msg.GetErr())

	follow, err := client.SetFollow(ctx, connect.NewRequest(&userv1.SetFollowRequest{
		Uid: uidA, TargetUid: uidB,
	}))
	require.NoError(t, err, "SetFollow RPC")
	require.False(t, follow.Msg.GetFail(), "关注业务失败: %v", follow.Msg.GetErr())

	followAgain, err := client.SetFollow(ctx, connect.NewRequest(&userv1.SetFollowRequest{
		Uid: uidA, TargetUid: uidB,
	}))
	require.NoError(t, err, "重复关注 RPC")
	assert.False(t, followAgain.Msg.GetFail(), "重复关注应幂等成功")

	selfFollow, err := client.SetFollow(ctx, connect.NewRequest(&userv1.SetFollowRequest{
		Uid: uidA, TargetUid: uidA,
	}))
	require.NoError(t, err, "自关注 RPC")
	assertBizErr(t, selfFollow.Msg.GetFail(), selfFollow.Msg.GetErr(), errmsg.InvalidArgument, "自关注")

	ghostFollow, err := client.SetFollow(ctx, connect.NewRequest(&userv1.SetFollowRequest{
		Uid: uidA, TargetUid: missingUID,
	}))
	require.NoError(t, err, "关注不存在用户 RPC")
	assertBizErr(t, ghostFollow.Msg.GetFail(), ghostFollow.Msg.GetErr(), errmsg.NotFound, "关注不存在的用户")

	// ---- 9. 关注关系（双向视角）----
	relAB, err := client.GetFollowingRelation(ctx, connect.NewRequest(&userv1.GetFollowingRelationRequest{
		Uid: uidA, TargetUid: uidB,
	}))
	require.NoError(t, err, "GetFollowingRelation(A→B) RPC")
	require.False(t, relAB.Msg.GetFail())
	assert.True(t, relAB.Msg.GetIsFollowing(), "A 应关注 B")
	assert.False(t, relAB.Msg.GetIsFollower(), "B 不应关注 A")

	relBA, err := client.GetFollowingRelation(ctx, connect.NewRequest(&userv1.GetFollowingRelationRequest{
		Uid: uidB, TargetUid: uidA,
	}))
	require.NoError(t, err, "GetFollowingRelation(B→A) RPC")
	assert.False(t, relBA.Msg.GetIsFollowing())
	assert.True(t, relBA.Msg.GetIsFollower(), "B 视角应互为被关注")

	// ---- 10. 关注列表 ----
	followings, err := client.GetFollowings(ctx, connect.NewRequest(&userv1.GetFollowingsRequest{Uid: uidA}))
	require.NoError(t, err, "GetFollowings RPC")
	require.False(t, followings.Msg.GetFail())
	require.Len(t, followings.Msg.GetUserBriefs(), 1, "A 的关注列表应含 B")
	assert.Nil(t, followings.Msg.GetErr(), "成功响应不应携带错误码")
	assert.Equal(t, uidB, followings.Msg.GetUserBriefs()[0].GetUid())
	assert.Equal(t, "e2e user b", followings.Msg.GetUserBriefs()[0].GetName())
	assert.Equal(t, "e2e/avatar-c.jpg", followings.Msg.GetUserBriefs()[0].GetAvatar().GetObjKey(), "关注列表应携带头像")

	// 粉丝列表（关注关系反向）：B 的粉丝应含 A
	followers, err := client.GetFollowers(ctx, connect.NewRequest(&userv1.GetFollowersRequest{Uid: uidB}))
	require.NoError(t, err, "GetFollowers RPC")
	require.False(t, followers.Msg.GetFail(), "GetFollowers 业务失败: %v", followers.Msg.GetErr())
	assert.Nil(t, followers.Msg.GetErr(), "成功响应不应携带错误码")
	require.Len(t, followers.Msg.GetUserBriefs(), 1, "B 的粉丝列表应含 A")
	assert.Equal(t, uidA, followers.Msg.GetUserBriefs()[0].GetUid())
	assert.Equal(t, "e2e user a v2", followers.Msg.GetUserBriefs()[0].GetName())
	assert.Equal(t, "e2e/avatar-b.jpg", followers.Msg.GetUserBriefs()[0].GetAvatar().GetObjKey(), "粉丝列表应携带头像")

	// A 尚无粉丝：空列表 + 空游标（空集合边界）
	followersA, err := client.GetFollowers(ctx, connect.NewRequest(&userv1.GetFollowersRequest{Uid: uidA}))
	require.NoError(t, err)
	assert.False(t, followersA.Msg.GetFail())
	assert.Empty(t, followersA.Msg.GetUserBriefs(), "A 无粉丝应返回空列表")
	assert.Nil(t, followersA.Msg.GetCursor(), "空页应返回空游标")

	// ---- 11. 取关 ----
	unfollow, err := client.SetUnFollow(ctx, connect.NewRequest(&userv1.SetUnFollowRequest{
		Uid: uidA, TargetUid: uidB,
	}))
	require.NoError(t, err, "SetUnFollow RPC")
	require.False(t, unfollow.Msg.GetFail(), "取关业务失败: %v", unfollow.Msg.GetErr())

	followings2, err := client.GetFollowings(ctx, connect.NewRequest(&userv1.GetFollowingsRequest{Uid: uidA}))
	require.NoError(t, err, "取关后 GetFollowings RPC")
	assert.Empty(t, followings2.Msg.GetUserBriefs(), "取关后关注列表应为空")
	assert.Nil(t, followings2.Msg.GetCursor(), "空页应返回空游标")

	followers2, err := client.GetFollowers(ctx, connect.NewRequest(&userv1.GetFollowersRequest{Uid: uidB}))
	require.NoError(t, err, "取关后 GetFollowers RPC")
	assert.Empty(t, followers2.Msg.GetUserBriefs(), "取关后 B 的粉丝列表应为空")
	assert.Nil(t, followers2.Msg.GetCursor(), "空页应返回空游标")

	relAB2, err := client.GetFollowingRelation(ctx, connect.NewRequest(&userv1.GetFollowingRelationRequest{
		Uid: uidA, TargetUid: uidB,
	}))
	require.NoError(t, err)
	assert.False(t, relAB2.Msg.GetIsFollowing())
	assert.False(t, relAB2.Msg.GetIsFollower(), "取关后双向关系应清空")

	// ---- 12. 封禁用户 ----
	ban, err := client.BanUser(ctx, connect.NewRequest(&userv1.BanUserRequest{Uid: uidB}))
	require.NoError(t, err, "BanUser RPC")
	require.False(t, ban.Msg.GetFail(), "封禁业务失败: %v", ban.Msg.GetErr())

	stB, err := client.GetUserStatus(ctx, connect.NewRequest(&userv1.GetUserStatusRequest{Uid: uidB}))
	require.NoError(t, err, "封禁后 GetUserStatus RPC")
	assert.Equal(t, userv1.UserStatus_USER_STATUS_BANNED, stB.Msg.GetStatus(), "封禁后应为 BANNED")

	// ---- 13. 错误路径 ----
	bad, err := client.GetUserProfile(ctx, connect.NewRequest(&userv1.GetUserProfileRequest{Uid: "bad"}))
	require.NoError(t, err, "非法 uid 查询 RPC")
	assertBizErr(t, bad.Msg.GetFail(), bad.Msg.GetErr(), errmsg.InvalidID, "非法 uid 查询")

	missing, err := client.GetUserProfile(ctx, connect.NewRequest(&userv1.GetUserProfileRequest{Uid: missingUID}))
	require.NoError(t, err, "不存在用户查询 RPC")
	assertBizErr(t, missing.Msg.GetFail(), missing.Msg.GetErr(), errmsg.NotFound, "不存在的用户")

	_, err = client.CreateUser(ctx, connect.NewRequest(&userv1.CreateUserRequest{Uid: uidB}))
	require.Error(t, err, "缺 name/avatar 应被传输层拒绝")
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "缺必填字段应返回 InvalidArgument")
}
