package v1

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	bffv1 "rednote/gen/go/bff/v1"
	commonv1 "rednote/gen/go/common/v1"
	mediav1 "rednote/gen/go/media/v1"
	"rednote/gen/go/media/v1/mediav1connect"
	userv1 "rednote/gen/go/user/v1"
	"rednote/gen/go/user/v1/userv1connect"
	"rednote/packages/defaults"
	"rednote/packages/errmsg"
	"rednote/packages/jwt"
	"rednote/packages/mq"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// refCall 记录一次引用计数变更（对象 + 增量），供断言挂载/卸载顺序
type refCall struct {
	id    string
	delta int64
}

// fakeUserClient 按预设响应并记录请求；仅覆写 user 域被测方法（未覆写方法调用会 panic）
type fakeUserClient struct {
	userv1connect.UserServiceClient

	profile    *userv1.UserProfile
	profileErr *commonv1.Error

	relation    *userv1.GetFollowingRelationResponse
	relationErr *commonv1.Error

	avatar    *userv1.UpdateUserAvatarResponse
	avatarErr *commonv1.Error

	updateProfileRPCErr error
	gotUpdateProfile    *userv1.UpdateUserProfileRequest

	setFollowErr    *commonv1.Error
	setFollowRPCErr error
	setUnFollowErr  *commonv1.Error
	setUnFollowRPC  error
	followersRPCErr error

	createUserErr    *commonv1.Error
	createUserRPCErr error

	// followingsResp / followersResp 自定义列表响应（nil 时回退默认 fixture）
	followingsResp *userv1.GetFollowingsResponse
	followersResp  *userv1.GetFollowersResponse

	gotProfileUIDs  []string
	gotRelationReqs []*userv1.GetFollowingRelationRequest
	gotAvatarReq    *userv1.UpdateUserAvatarRequest
	gotSetFollow    *userv1.SetFollowRequest
	gotSetUnFollow  *userv1.SetUnFollowRequest
	gotFollowings   *userv1.GetFollowingsRequest
	gotFollowers    *userv1.GetFollowersRequest
	gotCreateUser   *userv1.CreateUserRequest
	gotBriefReqs    []*userv1.GetUserBriefRequest

	briefResults []*userv1.UserBriefResult
	briefRPCErr  error
}

func (f *fakeUserClient) CreateUser(_ context.Context, req *connect.Request[userv1.CreateUserRequest]) (*connect.Response[userv1.CreateUserResponse], error) {
	f.gotCreateUser = req.Msg
	if f.createUserRPCErr != nil {
		return nil, f.createUserRPCErr
	}
	return connect.NewResponse(&userv1.CreateUserResponse{Fail: f.createUserErr != nil, Err: f.createUserErr, Profile: f.profile}), nil
}

func (f *fakeUserClient) GetUserProfile(_ context.Context, req *connect.Request[userv1.GetUserProfileRequest]) (*connect.Response[userv1.GetUserProfileResponse], error) {
	f.gotProfileUIDs = append(f.gotProfileUIDs, req.Msg.GetUid())
	return connect.NewResponse(&userv1.GetUserProfileResponse{Fail: f.profileErr != nil, Err: f.profileErr, Profile: f.profile}), nil
}

func (f *fakeUserClient) GetFollowingRelation(_ context.Context, req *connect.Request[userv1.GetFollowingRelationRequest]) (*connect.Response[userv1.GetFollowingRelationResponse], error) {
	f.gotRelationReqs = append(f.gotRelationReqs, req.Msg)
	if f.relationErr != nil {
		return connect.NewResponse(&userv1.GetFollowingRelationResponse{Fail: true, Err: f.relationErr}), nil
	}
	return connect.NewResponse(f.relation), nil
}

func (f *fakeUserClient) UpdateUserAvatar(_ context.Context, req *connect.Request[userv1.UpdateUserAvatarRequest]) (*connect.Response[userv1.UpdateUserAvatarResponse], error) {
	f.gotAvatarReq = req.Msg
	return connect.NewResponse(&userv1.UpdateUserAvatarResponse{Fail: f.avatarErr != nil, Err: f.avatarErr, Profile: f.avatar.GetProfile(), OldAssetId: f.avatar.GetOldAssetId()}), nil
}

func (f *fakeUserClient) UpdateUserProfile(_ context.Context, req *connect.Request[userv1.UpdateUserProfileRequest]) (*connect.Response[userv1.UpdateUserProfileResponse], error) {
	f.gotUpdateProfile = req.Msg
	if f.updateProfileRPCErr != nil {
		return nil, f.updateProfileRPCErr
	}
	// 复用 profile/profileErr 作为更新后的资料与业务失败注入
	return connect.NewResponse(&userv1.UpdateUserProfileResponse{Fail: f.profileErr != nil, Err: f.profileErr, Profile: f.profile}), nil
}

func (f *fakeUserClient) SetFollow(_ context.Context, req *connect.Request[userv1.SetFollowRequest]) (*connect.Response[userv1.SetFollowResponse], error) {
	f.gotSetFollow = req.Msg
	if f.setFollowRPCErr != nil {
		return nil, f.setFollowRPCErr
	}
	return connect.NewResponse(&userv1.SetFollowResponse{Fail: f.setFollowErr != nil, Err: f.setFollowErr}), nil
}

func (f *fakeUserClient) SetUnFollow(_ context.Context, req *connect.Request[userv1.SetUnFollowRequest]) (*connect.Response[userv1.SetUnFollowResponse], error) {
	f.gotSetUnFollow = req.Msg
	if f.setUnFollowRPC != nil {
		return nil, f.setUnFollowRPC
	}
	return connect.NewResponse(&userv1.SetUnFollowResponse{Fail: f.setUnFollowErr != nil, Err: f.setUnFollowErr}), nil
}

func (f *fakeUserClient) GetFollowings(_ context.Context, req *connect.Request[userv1.GetFollowingsRequest]) (*connect.Response[userv1.GetFollowingsResponse], error) {
	f.gotFollowings = req.Msg
	if f.followingsResp != nil {
		return connect.NewResponse(f.followingsResp), nil
	}
	return connect.NewResponse(&userv1.GetFollowingsResponse{
		UserBriefs: []*userv1.UserBrief{{Uid: "u2", Name: "bob"}},
		Cursor:     &commonv1.Cursor{Str: "next", Limit: 10},
	}), nil
}

func (f *fakeUserClient) GetFollowers(_ context.Context, req *connect.Request[userv1.GetFollowersRequest]) (*connect.Response[userv1.GetFollowersResponse], error) {
	f.gotFollowers = req.Msg
	if f.followersRPCErr != nil {
		return nil, f.followersRPCErr
	}
	if f.followersResp != nil {
		return connect.NewResponse(f.followersResp), nil
	}
	return connect.NewResponse(&userv1.GetFollowersResponse{
		UserBriefs: []*userv1.UserBrief{{Uid: "u9", Name: "fan"}},
		Cursor:     &commonv1.Cursor{Str: "fnext", Limit: 10},
	}), nil
}

func (f *fakeUserClient) GetUserBrief(_ context.Context, req *connect.Request[userv1.GetUserBriefRequest]) (*connect.Response[userv1.GetUserBriefResponse], error) {
	f.gotBriefReqs = append(f.gotBriefReqs, req.Msg)
	if f.briefRPCErr != nil {
		return nil, f.briefRPCErr
	}
	return connect.NewResponse(&userv1.GetUserBriefResponse{Results: f.briefResults}), nil
}

// fakeMediaClient media 域被测路径（GetAndRefMediaFile/DeRefMediaFile/UploadMedia 所需），
// 支持业务失败（xxxErr）与连接层错误（xxxRPCErr）两类注入
type fakeMediaClient struct {
	mediav1connect.MediaServiceClient

	asset    *mediav1.MediaAsset
	assetErr *commonv1.Error
	file     *mediav1.MediaFile
	// fileByType 按变体类型返回不同文件（多槽位对齐断言用），未命中回退 file
	fileByType map[mediav1.MediaFileType]*mediav1.MediaFile
	fileErr    *commonv1.Error
	refErr     *commonv1.Error

	upload    *mediav1.UploadMediaResponse
	uploadErr *commonv1.Error

	batch       []*mediav1.MediaAssetResult
	batchRPCErr error

	signRPCErr  error
	signFailIDs map[string]*commonv1.Error // 逐项签发失败注入（按 asset_id）
	// signFailTypes 按变体类型注入失败（模拟该尺寸变体缺失）
	signFailTypes map[mediav1.MediaFileType]bool

	assetRPCErr  error
	fileRPCErr   error
	refRPCErr    error
	uploadRPCErr error

	refCalls      []refCall
	gotUploadReqs []*mediav1.UploadMediaRequest
	gotBatchIDs   []string
	gotSignReqs   []*mediav1.MediaAssetFileQuery
	gotSignTTLs   []int64
}

func (f *fakeMediaClient) SignMediaFileURLs(_ context.Context, req *connect.Request[mediav1.SignMediaFileURLsRequest]) (*connect.Response[mediav1.SignMediaFileURLsResponse], error) {
	f.gotSignReqs = append(f.gotSignReqs, req.Msg.GetRefs()...)
	f.gotSignTTLs = append(f.gotSignTTLs, req.Msg.GetTtlSeconds())
	if f.signRPCErr != nil {
		return nil, f.signRPCErr
	}
	urls := make([]*mediav1.MediaFileURL, 0, len(req.Msg.GetRefs()))
	for _, ref := range req.Msg.GetRefs() {
		if errCode := f.signFailIDs[ref.GetId()]; errCode != nil {
			urls = append(urls, &mediav1.MediaFileURL{Fail: true, Err: errCode, AssetId: ref.GetId(), Type: ref.GetType()})
			continue
		}
		if f.signFailTypes[ref.GetType()] {
			urls = append(urls, &mediav1.MediaFileURL{
				Fail: true, Err: errmsg.NotFound, AssetId: ref.GetId(), Type: ref.GetType(),
			})
			continue
		}
		urls = append(urls, &mediav1.MediaFileURL{
			AssetId:   ref.GetId(),
			Type:      ref.GetType(),
			SignedUrl: "https://signed/" + ref.GetId(),
			ExpireAt:  1700000000000,
		})
	}
	return connect.NewResponse(&mediav1.SignMediaFileURLsResponse{Urls: urls}), nil
}

func (f *fakeMediaClient) GetMediaAssets(_ context.Context, req *connect.Request[mediav1.GetMediaAssetsRequest]) (*connect.Response[mediav1.GetMediaAssetsResponse], error) {
	f.gotBatchIDs = append(f.gotBatchIDs, req.Msg.GetIds()...)
	if f.batchRPCErr != nil {
		return nil, f.batchRPCErr
	}
	return connect.NewResponse(&mediav1.GetMediaAssetsResponse{Results: f.batch}), nil
}

func (f *fakeMediaClient) GetMediaAsset(_ context.Context, req *connect.Request[mediav1.GetMediaAssetRequest]) (*connect.Response[mediav1.GetMediaAssetResponse], error) {
	if f.assetRPCErr != nil {
		return nil, f.assetRPCErr
	}
	return connect.NewResponse(&mediav1.GetMediaAssetResponse{Fail: f.assetErr != nil, Err: f.assetErr, Asset: f.asset}), nil
}

func (f *fakeMediaClient) GetMediaAssetFile(_ context.Context, req *connect.Request[mediav1.GetMediaAssetFileRequest]) (*connect.Response[mediav1.GetMediaAssetFileResponse], error) {
	if f.fileRPCErr != nil {
		return nil, f.fileRPCErr
	}
	if f.fileErr != nil {
		return connect.NewResponse(&mediav1.GetMediaAssetFileResponse{Fail: true, Err: f.fileErr}), nil
	}
	if file, ok := f.fileByType[req.Msg.GetType()]; ok {
		return connect.NewResponse(&mediav1.GetMediaAssetFileResponse{File: file}), nil
	}
	return connect.NewResponse(&mediav1.GetMediaAssetFileResponse{File: f.file}), nil
}

func (f *fakeMediaClient) ModifyMediaAssetRefCount(_ context.Context, req *connect.Request[mediav1.ModifyMediaAssetRefCountRequest]) (*connect.Response[mediav1.ModifyMediaAssetRefCountResponse], error) {
	if f.refRPCErr != nil {
		return nil, f.refRPCErr
	}
	for _, q := range req.Msg.GetQueries() {
		f.refCalls = append(f.refCalls, refCall{id: q.GetId(), delta: q.GetDelta()})
	}
	return connect.NewResponse(&mediav1.ModifyMediaAssetRefCountResponse{Fail: f.refErr != nil, Err: f.refErr}), nil
}

func (f *fakeMediaClient) UploadMedia(_ context.Context, req *connect.Request[mediav1.UploadMediaRequest]) (*connect.Response[mediav1.UploadMediaResponse], error) {
	f.gotUploadReqs = append(f.gotUploadReqs, req.Msg)
	if f.uploadRPCErr != nil {
		return nil, f.uploadRPCErr
	}
	return connect.NewResponse(&mediav1.UploadMediaResponse{Fail: f.uploadErr != nil, Err: f.uploadErr, Asset: f.upload.GetAsset(), SignedUrl: f.upload.GetSignedUrl(), ExpireAt: f.upload.GetExpireAt()}), nil
}

// fakeMQClient 记录 Send 发出的消息（卸载走 MQ 异步的断言依据），支持发送失败注入
type fakeMQClient struct {
	mq.MQClient
	sendErr error
	sent    []*mq.MQMessage
}

func (f *fakeMQClient) Send(_ context.Context, msg *mq.MQMessage) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, msg)
	return nil
}

// derefSent 解出发送的 DerefMedia 消息资产 ID 序列，并校验目标流与 payload 类型
func derefSent(t *testing.T, u *BFFUsecaseImpl) []string {
	t.Helper()
	f := u.MQ.(*fakeMQClient)
	var ids []string
	for _, m := range f.sent {
		require.Equal(t, defaults.MediaMessageStream, m.Subject, "卸载消息发往 media 流")
		var msg mediav1.MediaMessage
		require.NoError(t, proto.Unmarshal(m.PayLoad, &msg))
		ids = append(ids, msg.GetDerefMedia().GetAssetId())
	}
	return ids
}

func newBFFForTest(user *fakeUserClient, media *fakeMediaClient) *BFFUsecaseImpl {
	return &BFFUsecaseImpl{
		Repo: &DownstreamRepo{User: user, Media: media},
		MQ:   &fakeMQClient{},
		log:  slog.Default(),
	}
}

func ctxWithUID(uid string) context.Context {
	return jwt.WithClaims(context.Background(), &jwt.Claims{UID: uid})
}

// TestBFFUserUnauthorized 会话缺失时用户域接口一律返回 Unauthorized（不透传下游）
func TestBFFUserUnauthorized(t *testing.T) {
	u := newBFFForTest(&fakeUserClient{}, &fakeMediaClient{})

	cases := []struct {
		name string
		call func() (bool, *commonv1.Error)
	}{
		{"GetMyProfile", func() (bool, *commonv1.Error) {
			r, err := u.GetMyProfile(context.Background(), &bffv1.GetMyProfileRequest{})
			require.NoError(t, err)
			return r.Fail, r.Err
		}},
		{"GetUserProfile", func() (bool, *commonv1.Error) {
			r, err := u.GetUserProfile(context.Background(), &bffv1.GetUserProfileRequest{Uid: "u2"})
			require.NoError(t, err)
			return r.Fail, r.Err
		}},
		{"UpdateMyProfile", func() (bool, *commonv1.Error) {
			r, err := u.UpdateMyProfile(context.Background(), &bffv1.UpdateMyProfileRequest{})
			require.NoError(t, err)
			return r.Fail, r.Err
		}},
		{"UpdateMyAvatar", func() (bool, *commonv1.Error) {
			r, err := u.UpdateMyAvatar(context.Background(), &bffv1.UpdateMyAvatarRequest{})
			require.NoError(t, err)
			return r.Fail, r.Err
		}},
		{"Follow", func() (bool, *commonv1.Error) {
			r, err := u.Follow(context.Background(), &bffv1.FollowRequest{TargetUid: "u2"})
			require.NoError(t, err)
			return r.Fail, r.Err
		}},
		{"Unfollow", func() (bool, *commonv1.Error) {
			r, err := u.Unfollow(context.Background(), &bffv1.UnfollowRequest{TargetUid: "u2"})
			require.NoError(t, err)
			return r.Fail, r.Err
		}},
		{"GetFollowingList", func() (bool, *commonv1.Error) {
			r, err := u.GetFollowingList(context.Background(), &bffv1.GetFollowingListRequest{})
			require.NoError(t, err)
			return r.Fail, r.Err
		}},
		{"GetFollowRelation", func() (bool, *commonv1.Error) {
			r, err := u.GetFollowRelation(context.Background(), &bffv1.GetFollowRelationRequest{TargetUid: "u2"})
			require.NoError(t, err)
			return r.Fail, r.Err
		}},
		{"GetFollowerList", func() (bool, *commonv1.Error) {
			r, err := u.GetFollowerList(context.Background(), &bffv1.GetFollowerListRequest{})
			require.NoError(t, err)
			return r.Fail, r.Err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fail, e := tc.call()
			assert.True(t, fail)
			assert.Equal(t, errmsg.Unauthorized.Code, e.GetCode())
		})
	}
}

// TestBFFGetMyProfile 会话 uid 注入下游 + profile 全字段透传
func TestBFFGetMyProfile(t *testing.T) {
	user := &fakeUserClient{profile: &userv1.UserProfile{
		Brief:  &userv1.UserBrief{Uid: "u1", Name: "alice", Avatar: &mediav1.MediaFile{AssetId: "av1", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_216}},
		Bio:    "hello",
		Stat:   &userv1.UserStat{Followings: 3, Followers: 5},
		Status: userv1.UserStatus_USER_STATUS_ACTIVE,
	}}
	u := newBFFForTest(user, &fakeMediaClient{})

	resp, err := u.GetMyProfile(ctxWithUID("u1"), &bffv1.GetMyProfileRequest{})
	require.NoError(t, err)
	require.False(t, resp.Fail)
	assert.Nil(t, resp.Err)

	// 全字段断言：Profile 逐字段回显
	p := resp.GetProfile()
	assert.Equal(t, "u1", p.GetBrief().GetUid())
	assert.Equal(t, "alice", p.GetBrief().GetName())
	assert.Equal(t, "av1", p.GetBrief().GetAvatar().GetAssetId())
	assert.Equal(t, mediav1.MediaFileType_MEDIA_FILE_TYPE_216, p.GetBrief().GetAvatar().GetType())
	assert.Equal(t, "hello", p.GetBio())
	assert.Equal(t, int64(3), p.GetStat().GetFollowings())
	assert.Equal(t, int64(5), p.GetStat().GetFollowers())
	assert.Equal(t, userv1.UserStatus_USER_STATUS_ACTIVE, p.GetStatus())
	assert.Equal(t, []string{"u1"}, user.gotProfileUIDs, "uid 必须来自会话而非请求体")
}

// TestBFFGetUserProfile 聚合 ProfileView：资料 + 关注关系并行组装
func TestBFFGetUserProfile(t *testing.T) {
	t.Run("正常聚合：全字段回显", func(t *testing.T) {
		user := &fakeUserClient{
			profile: &userv1.UserProfile{
				Brief:  &userv1.UserBrief{Uid: "u2", Name: "bob", Avatar: &mediav1.MediaFile{AssetId: "av2", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_216}},
				Bio:    "bio2",
				Stat:   &userv1.UserStat{Followings: 1, Followers: 2},
				Status: userv1.UserStatus_USER_STATUS_ACTIVE,
			},
			relation: &userv1.GetFollowingRelationResponse{IsFollowing: true, IsFollower: false},
		}
		u := newBFFForTest(user, &fakeMediaClient{})

		resp, err := u.GetUserProfile(ctxWithUID("u1"), &bffv1.GetUserProfileRequest{Uid: "u2"})
		require.NoError(t, err)
		require.False(t, resp.Fail)
		assert.Nil(t, resp.Err)

		// 全字段断言：ProfileView（profile + relation）逐字段
		v := resp.GetView()
		p := v.GetProfile()
		assert.Equal(t, "u2", p.GetBrief().GetUid())
		assert.Equal(t, "bob", p.GetBrief().GetName())
		assert.Equal(t, "av2", p.GetBrief().GetAvatar().GetAssetId())
		assert.Equal(t, "bio2", p.GetBio())
		assert.Equal(t, int64(1), p.GetStat().GetFollowings())
		assert.Equal(t, int64(2), p.GetStat().GetFollowers())
		assert.Equal(t, userv1.UserStatus_USER_STATUS_ACTIVE, p.GetStatus())
		assert.True(t, v.GetRelation().GetIsFollowing())
		assert.False(t, v.GetRelation().GetIsFollower())
		require.Len(t, user.gotRelationReqs, 1)
		assert.Equal(t, "u1", user.gotRelationReqs[0].GetUid(), "关系查询方向：会话用户 → 目标")
		assert.Equal(t, "u2", user.gotRelationReqs[0].GetTargetUid())
	})

	t.Run("无关系记录按未关注降级", func(t *testing.T) {
		user := &fakeUserClient{
			profile:     &userv1.UserProfile{Brief: &userv1.UserBrief{Uid: "u2"}},
			relationErr: errmsg.NotFound,
		}
		u := newBFFForTest(user, &fakeMediaClient{})

		resp, err := u.GetUserProfile(ctxWithUID("u1"), &bffv1.GetUserProfileRequest{Uid: "u2"})
		require.NoError(t, err)
		require.False(t, resp.Fail, "NotFound 不应打挂主页聚合")
		assert.False(t, resp.View.GetRelation().GetIsFollowing())
		assert.False(t, resp.View.GetRelation().GetIsFollower())
	})

	t.Run("关系查询其他业务错误透传", func(t *testing.T) {
		user := &fakeUserClient{
			profile:     &userv1.UserProfile{Brief: &userv1.UserBrief{Uid: "u2"}},
			relationErr: errmsg.InternalError,
		}
		u := newBFFForTest(user, &fakeMediaClient{})

		resp, err := u.GetUserProfile(ctxWithUID("u1"), &bffv1.GetUserProfileRequest{Uid: "u2"})
		require.NoError(t, err)
		assert.True(t, resp.Fail)
		assert.Equal(t, errmsg.InternalError.Code, resp.Err.GetCode())
	})
}

// TestBFFUpdateMyAvatar 头像更新：归属校验 + 引用计数挂载/卸载编排
func TestBFFUpdateMyAvatar(t *testing.T) {
	newAvatar := &mediav1.MediaFile{
		Base:    &commonv1.BaseModel{Id: "file-new"},
		Type:    mediav1.MediaFileType_MEDIA_FILE_TYPE_216,
		AssetId: "asset-new",
	}
	// 面客请求侧只传 asset_id + type（存储坐标不面客）
	newAvatarRef := &bffv1.AssetRef{AssetId: "asset-new", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_216}

	t.Run("资产归属不符拒绝", func(t *testing.T) {
		media := &fakeMediaClient{asset: &mediav1.MediaAsset{UploaderId: "someone-else"}}
		u := newBFFForTest(&fakeUserClient{}, media)

		resp, err := u.UpdateMyAvatar(ctxWithUID("u1"), &bffv1.UpdateMyAvatarRequest{Avatar: newAvatarRef})
		require.NoError(t, err)
		assert.True(t, resp.Fail)
		assert.Equal(t, errmsg.Forbidden.Code, resp.Err.GetCode())
		assert.Empty(t, media.refCalls, "拒绝场景不得调整引用计数")
	})

	t.Run("成功：挂载同步+1旧资产卸载走 MQ 且不外泄 old_asset_id", func(t *testing.T) {
		media := &fakeMediaClient{
			asset: &mediav1.MediaAsset{UploaderId: "u1", Status: mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE},
			file:  newAvatar,
		}
		user := &fakeUserClient{avatar: &userv1.UpdateUserAvatarResponse{
			Profile:    &userv1.UserProfile{Brief: &userv1.UserBrief{Uid: "u1"}},
			OldAssetId: "asset-old",
		}}
		u := newBFFForTest(user, media)

		resp, err := u.UpdateMyAvatar(ctxWithUID("u1"), &bffv1.UpdateMyAvatarRequest{Avatar: newAvatarRef})
		require.NoError(t, err)
		require.False(t, resp.Fail)
		assert.Equal(t, "u1", resp.Profile.GetBrief().GetUid())
		assert.Equal(t, []refCall{{id: "asset-new", delta: 1}}, media.refCalls,
			"挂载新头像同步 +1（卸载不再同步调 RPC）")
		assert.Equal(t, []string{"asset-old"}, derefSent(t, u),
			"按下游 old_asset_id 发 MQ 异步卸载消息，由 media consumer 递减")
		require.NotNil(t, user.gotAvatarReq)
		assert.Equal(t, "u1", user.gotAvatarReq.GetUid(), "uid 来自会话")
	})

	t.Run("下游业务失败回滚挂载计数", func(t *testing.T) {
		media := &fakeMediaClient{
			asset: &mediav1.MediaAsset{UploaderId: "u1", Status: mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE},
			file:  newAvatar,
		}
		user := &fakeUserClient{avatarErr: errmsg.InvalidName}
		u := newBFFForTest(user, media)

		resp, err := u.UpdateMyAvatar(ctxWithUID("u1"), &bffv1.UpdateMyAvatarRequest{Avatar: newAvatarRef})
		require.NoError(t, err)
		assert.True(t, resp.Fail)
		assert.Equal(t, errmsg.InvalidName.Code, resp.Err.GetCode())
		assert.Equal(t, []refCall{{id: "asset-new", delta: 1}}, media.refCalls)
		assert.Equal(t, []string{"asset-new"}, derefSent(t, u),
			"更新失败回滚：对新资产发卸载消息，不留计数漂移")
	})

	t.Run("空资产 ID 拒绝", func(t *testing.T) {
		u := newBFFForTest(&fakeUserClient{}, &fakeMediaClient{})
		resp, err := u.UpdateMyAvatar(ctxWithUID("u1"), &bffv1.UpdateMyAvatarRequest{Avatar: &bffv1.AssetRef{}})
		require.NoError(t, err)
		assert.True(t, resp.Fail)
		assert.Equal(t, errmsg.InvalidID.Code, resp.Err.GetCode())
	})
}

// TestBFFFollowUnfollow 关注/取关：uid 来自会话，target 透传
func TestBFFFollowUnfollow(t *testing.T) {
	user := &fakeUserClient{}
	u := newBFFForTest(user, &fakeMediaClient{})

	r1, err := u.Follow(ctxWithUID("u1"), &bffv1.FollowRequest{TargetUid: "u2"})
	require.NoError(t, err)
	require.False(t, r1.Fail)
	require.NotNil(t, user.gotSetFollow)
	assert.Equal(t, "u1", user.gotSetFollow.GetUid())
	assert.Equal(t, "u2", user.gotSetFollow.GetTargetUid())

	// 重复关注幂等透传（下游语义），两次均按会话注入
	r2, err := u.Follow(ctxWithUID("u1"), &bffv1.FollowRequest{TargetUid: "u2"})
	require.NoError(t, err)
	require.False(t, r2.Fail, "重复关注幂等透传")
	assert.Equal(t, "u2", user.gotSetFollow.GetTargetUid())

	r3, err := u.Unfollow(ctxWithUID("u1"), &bffv1.UnfollowRequest{TargetUid: "u2"})
	require.NoError(t, err)
	require.False(t, r3.Fail)
	require.NotNil(t, user.gotSetUnFollow)
	assert.Equal(t, "u1", user.gotSetUnFollow.GetUid())
	assert.Equal(t, "u2", user.gotSetUnFollow.GetTargetUid())
}

// TestBFFGetFollowingList 关注列表：uid 注入 + 游标与列表透传
func TestBFFGetFollowingList(t *testing.T) {
	user := &fakeUserClient{}
	u := newBFFForTest(user, &fakeMediaClient{})

	resp, err := u.GetFollowingList(ctxWithUID("u1"), &bffv1.GetFollowingListRequest{Cursor: &commonv1.Cursor{Limit: 10}})
	require.NoError(t, err)
	require.False(t, resp.Fail)
	require.NotNil(t, user.gotFollowings)
	assert.Equal(t, "u1", user.gotFollowings.GetUid())
	assert.Equal(t, int64(10), user.gotFollowings.GetCursor().GetLimit())
	require.Len(t, resp.UserBriefs, 1)
	assert.Equal(t, "bob", resp.UserBriefs[0].GetName())
	assert.Equal(t, "next", resp.Cursor.GetStr())

	t.Run("查他人：显式 target uid 透传目标用户（支持查别人）", func(t *testing.T) {
		other := &fakeUserClient{}
		u := newBFFForTest(other, &fakeMediaClient{})

		r, err := u.GetFollowingList(ctxWithUID("u1"), &bffv1.GetFollowingListRequest{Uid: "u9"})
		require.NoError(t, err)
		require.False(t, r.Fail)
		require.NotNil(t, other.gotFollowings)
		assert.Equal(t, "u9", other.gotFollowings.GetUid(), "需求变更：显式 target 优先于会话用户")
	})

	t.Run("空页：列表为空不报错（翻页到底以空列表表达）", func(t *testing.T) {
		empty := &fakeUserClient{followingsResp: &userv1.GetFollowingsResponse{}}
		u := newBFFForTest(empty, &fakeMediaClient{})

		r, err := u.GetFollowingList(ctxWithUID("u1"), &bffv1.GetFollowingListRequest{})
		require.NoError(t, err)
		require.False(t, r.Fail)
		assert.Empty(t, r.GetUserBriefs())
		assert.Empty(t, r.GetCursor().GetStr(), "空页游标置空")
	})
}

// TestBFFGetFollowRelation 关系查询：uid 来自会话 + is_following/is_follower 透传
func TestBFFGetFollowRelation(t *testing.T) {
	user := &fakeUserClient{relation: &userv1.GetFollowingRelationResponse{IsFollowing: true, IsFollower: true}}
	u := newBFFForTest(user, &fakeMediaClient{})

	resp, err := u.GetFollowRelation(ctxWithUID("u1"), &bffv1.GetFollowRelationRequest{TargetUid: "u2"})
	require.NoError(t, err)
	require.False(t, resp.Fail)
	assert.True(t, resp.IsFollowing)
	assert.True(t, resp.IsFollower)
	require.Len(t, user.gotRelationReqs, 1)
	assert.Equal(t, "u1", user.gotRelationReqs[0].GetUid())
	assert.Equal(t, "u2", user.gotRelationReqs[0].GetTargetUid())
}

// TestBFFGetFollowerList 粉丝列表：uid 注入 + 游标透传（与 GetFollowingList 同构，下游已补齐）
func TestBFFGetFollowerList(t *testing.T) {
	user := &fakeUserClient{}
	u := newBFFForTest(user, &fakeMediaClient{})

	resp, err := u.GetFollowerList(ctxWithUID("u1"), &bffv1.GetFollowerListRequest{Cursor: &commonv1.Cursor{Limit: 10}})
	require.NoError(t, err)
	require.False(t, resp.Fail)
	require.NotNil(t, user.gotFollowers)
	assert.Equal(t, "u1", user.gotFollowers.GetUid(), "uid 必须来自会话")
	assert.Equal(t, int64(10), user.gotFollowers.GetCursor().GetLimit())
	require.Len(t, resp.UserBriefs, 1)
	assert.Equal(t, "fan", resp.UserBriefs[0].GetName())
	assert.Equal(t, "fnext", resp.Cursor.GetStr())

	t.Run("查他人：显式 target uid 透传目标用户（支持查别人）", func(t *testing.T) {
		other := &fakeUserClient{}
		u := newBFFForTest(other, &fakeMediaClient{})

		r, err := u.GetFollowerList(ctxWithUID("u1"), &bffv1.GetFollowerListRequest{Uid: "u9"})
		require.NoError(t, err)
		require.False(t, r.Fail)
		require.NotNil(t, other.gotFollowers)
		assert.Equal(t, "u9", other.gotFollowers.GetUid(), "需求变更：显式 target 优先于会话用户")
	})
}

// TestBFFUpdateMyProfile 个人资料更新：uid/字段注入下游 + Profile 全字段回显（G9 补缺）
func TestBFFUpdateMyProfile(t *testing.T) {
	fixture := &userv1.UserProfile{
		Brief:  &userv1.UserBrief{Uid: "u1", Name: "alice-new", Avatar: &mediav1.MediaFile{AssetId: "av1", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_216}},
		Bio:    "new-bio",
		Stat:   &userv1.UserStat{Followings: 3, Followers: 5},
		Status: userv1.UserStatus_USER_STATUS_ACTIVE,
	}

	t.Run("成功：uid 与三字段注入 + Profile 全字段回显", func(t *testing.T) {
		user := &fakeUserClient{profile: fixture}
		u := newBFFForTest(user, &fakeMediaClient{})

		resp, err := u.UpdateMyProfile(ctxWithUID("u1"), &bffv1.UpdateMyProfileRequest{
			Name:     wrapperspb.String("alice-new"),
			Bio:      wrapperspb.String("new-bio"),
			Metadata: &commonv1.JsonStr{Payload: `{"theme":"dark"}`},
		})
		require.NoError(t, err)
		require.False(t, resp.Fail)
		assert.Nil(t, resp.Err)

		// 注入断言：uid 来自会话、三字段原样透传
		require.NotNil(t, user.gotUpdateProfile)
		assert.Equal(t, "u1", user.gotUpdateProfile.GetUid(), "uid 必须来自会话而非请求体")
		assert.Equal(t, "alice-new", user.gotUpdateProfile.GetName().GetValue())
		assert.Equal(t, "new-bio", user.gotUpdateProfile.GetBio().GetValue())
		assert.Equal(t, `{"theme":"dark"}`, user.gotUpdateProfile.GetMetadata().GetPayload())

		// 全字段断言：Profile 逐字段回显
		p := resp.GetProfile()
		assert.Equal(t, "u1", p.GetBrief().GetUid())
		assert.Equal(t, "alice-new", p.GetBrief().GetName())
		assert.Equal(t, "av1", p.GetBrief().GetAvatar().GetAssetId())
		assert.Equal(t, mediav1.MediaFileType_MEDIA_FILE_TYPE_216, p.GetBrief().GetAvatar().GetType())
		assert.Equal(t, "new-bio", p.GetBio())
		assert.Equal(t, int64(3), p.GetStat().GetFollowings())
		assert.Equal(t, int64(5), p.GetStat().GetFollowers())
		assert.Equal(t, userv1.UserStatus_USER_STATUS_ACTIVE, p.GetStatus())
	})

	t.Run("下游业务失败透传", func(t *testing.T) {
		user := &fakeUserClient{profileErr: errmsg.NotFound}
		u := newBFFForTest(user, &fakeMediaClient{})

		resp, err := u.UpdateMyProfile(ctxWithUID("u1"), &bffv1.UpdateMyProfileRequest{Name: wrapperspb.String("x")})
		require.NoError(t, err)
		assert.True(t, resp.Fail)
		assert.Equal(t, errmsg.NotFound.Code, resp.Err.GetCode())
	})

	t.Run("连接层错误归为 InternalError", func(t *testing.T) {
		user := &fakeUserClient{updateProfileRPCErr: errors.New("dial fail")}
		u := newBFFForTest(user, &fakeMediaClient{})

		resp, err := u.UpdateMyProfile(ctxWithUID("u1"), &bffv1.UpdateMyProfileRequest{Name: wrapperspb.String("x")})
		require.NoError(t, err)
		assert.True(t, resp.Fail)
		assert.Equal(t, errmsg.InternalError.Code, resp.Err.GetCode())
	})
}

// TestBFFFollowUnfollowErrors 关注/取关错误路径（G10 补缺）
func TestBFFFollowUnfollowErrors(t *testing.T) {
	t.Run("Follow 业务失败透传", func(t *testing.T) {
		user := &fakeUserClient{setFollowErr: errmsg.InvalidArgument}
		u := newBFFForTest(user, &fakeMediaClient{})

		r, err := u.Follow(ctxWithUID("u1"), &bffv1.FollowRequest{TargetUid: "u1"})
		require.NoError(t, err)
		assert.True(t, r.Fail)
		assert.Equal(t, errmsg.InvalidArgument.Code, r.Err.GetCode(), "自关注等业务拒绝按下游码透传")
	})

	t.Run("Follow 连接层错误归为 InternalError", func(t *testing.T) {
		user := &fakeUserClient{setFollowRPCErr: errors.New("dial fail")}
		u := newBFFForTest(user, &fakeMediaClient{})

		r, err := u.Follow(ctxWithUID("u1"), &bffv1.FollowRequest{TargetUid: "u2"})
		require.NoError(t, err)
		assert.True(t, r.Fail)
		assert.Equal(t, errmsg.InternalError.Code, r.Err.GetCode())
	})

	t.Run("Unfollow 业务失败透传", func(t *testing.T) {
		user := &fakeUserClient{setUnFollowErr: errmsg.NotFound}
		u := newBFFForTest(user, &fakeMediaClient{})

		r, err := u.Unfollow(ctxWithUID("u1"), &bffv1.UnfollowRequest{TargetUid: "u2"})
		require.NoError(t, err)
		assert.True(t, r.Fail)
		assert.Equal(t, errmsg.NotFound.Code, r.Err.GetCode())
	})

	t.Run("Unfollow 连接层错误归为 InternalError", func(t *testing.T) {
		user := &fakeUserClient{setUnFollowRPC: errors.New("dial fail")}
		u := newBFFForTest(user, &fakeMediaClient{})

		r, err := u.Unfollow(ctxWithUID("u1"), &bffv1.UnfollowRequest{TargetUid: "u2"})
		require.NoError(t, err)
		assert.True(t, r.Fail)
		assert.Equal(t, errmsg.InternalError.Code, r.Err.GetCode())
	})

	t.Run("GetFollowRelation 业务失败透传", func(t *testing.T) {
		user := &fakeUserClient{relationErr: errmsg.InvalidArgument}
		u := newBFFForTest(user, &fakeMediaClient{})

		r, err := u.GetFollowRelation(ctxWithUID("u1"), &bffv1.GetFollowRelationRequest{TargetUid: "u2"})
		require.NoError(t, err)
		assert.True(t, r.Fail)
		assert.Equal(t, errmsg.InvalidArgument.Code, r.Err.GetCode())
	})

	t.Run("GetFollowRelation 目标=自己：BFF 不拦截照发下游（自关注校验归下游）", func(t *testing.T) {
		user := &fakeUserClient{relation: &userv1.GetFollowingRelationResponse{}}
		u := newBFFForTest(user, &fakeMediaClient{})

		r, err := u.GetFollowRelation(ctxWithUID("u1"), &bffv1.GetFollowRelationRequest{TargetUid: "u1"})
		require.NoError(t, err)
		require.False(t, r.Fail)
		require.Len(t, user.gotRelationReqs, 1)
		assert.Equal(t, "u1", user.gotRelationReqs[0].GetTargetUid(), "target=自己照常透传")
	})

	t.Run("GetFollowerList 业务失败透传", func(t *testing.T) {
		user := &fakeUserClient{followersResp: &userv1.GetFollowersResponse{Fail: true, Err: errmsg.NotFound}}
		u := newBFFForTest(user, &fakeMediaClient{})

		r, err := u.GetFollowerList(ctxWithUID("u1"), &bffv1.GetFollowerListRequest{})
		require.NoError(t, err)
		assert.True(t, r.Fail)
		assert.Equal(t, errmsg.NotFound.Code, r.Err.GetCode())
	})

	t.Run("GetFollowerList 连接层错误归为 InternalError", func(t *testing.T) {
		user := &fakeUserClient{followersRPCErr: errors.New("dial fail")}
		u := newBFFForTest(user, &fakeMediaClient{})

		r, err := u.GetFollowerList(ctxWithUID("u1"), &bffv1.GetFollowerListRequest{})
		require.NoError(t, err)
		assert.True(t, r.Fail)
		assert.Equal(t, errmsg.InternalError.Code, r.Err.GetCode())
	})
}
