package v1

import (
	"context"

	bffv1 "rednote/gen/go/bff/v1"
	commonv1 "rednote/gen/go/common/v1"
	mediav1 "rednote/gen/go/media/v1"
	userv1 "rednote/gen/go/user/v1"
	"rednote/packages/errmsg"
	"rednote/packages/jwt"

	"connectrpc.com/connect"
	"golang.org/x/sync/errgroup"
)

// ==================== 用户 ====================

// sessionUID 从会话解析当前用户 ID；未鉴权返回 Unauthorized。
// BFF 契约：除 Register/Login 外，请求体一律不携带 uid，身份由会话注入下游，杜绝伪造。
func (u *BFFUsecaseImpl) sessionUID(ctx context.Context) (string, *commonv1.Error) {
	claims := jwt.ClaimsFromContext(ctx)
	if claims == nil {
		return "", errmsg.Unauthorized
	}
	return claims.UID, nil
}

// ==================== 面客视图映射（下游类型不面客） ====================

// userBriefView 把下游 UserBrief 降维为面客镜像：avatar 降为 MediaView，其余字段直拷。
func userBriefView(brief *userv1.UserBrief, avatar *bffv1.MediaView) *bffv1.UserBrief {
	return &bffv1.UserBrief{
		Avatar: avatar,
		Name:   brief.GetName(),
		Uid:    brief.GetUid(),
	}
}

// userBriefsView 批量降维用户简视图：头像收集后一次批量签发（公开 TTL），结果与入参对齐。
func (u *BFFUsecaseImpl) userBriefsView(ctx context.Context, briefs []*userv1.UserBrief) ([]*bffv1.UserBrief, *commonv1.Error) {
	files := make([]*mediav1.MediaFile, len(briefs))
	for i, b := range briefs {
		files[i] = b.GetAvatar()
	}
	avatars, errCode := u.signMediaViews(ctx, publicMediaTTL, files)
	if errCode != nil {
		return nil, errCode
	}
	views := make([]*bffv1.UserBrief, len(briefs))
	for i, b := range briefs {
		views[i] = userBriefView(b, avatars[i])
	}
	return views, nil
}

// userProfileView 降维下游 UserProfile：brief.avatar 同步降维为 MediaView（公开 TTL）。
func (u *BFFUsecaseImpl) userProfileView(ctx context.Context, p *userv1.UserProfile) (*bffv1.UserProfile, *commonv1.Error) {
	brief := p.GetBrief()
	var avatar *bffv1.MediaView
	if brief.GetAvatar() != nil {
		avatars, errCode := u.signMediaViews(ctx, publicMediaTTL, []*mediav1.MediaFile{brief.GetAvatar()})
		if errCode != nil {
			return nil, errCode
		}
		avatar = avatars[0]
	}
	return &bffv1.UserProfile{
		Brief:    userBriefView(brief, avatar),
		Bio:      p.GetBio(),
		Stat:     p.GetStat(),
		Status:   p.GetStatus(),
		Metadata: p.GetMetadata(),
	}, nil
}

// GetMyProfile 我的主页资料：透传下游。
func (u *BFFUsecaseImpl) GetMyProfile(ctx context.Context, req *bffv1.GetMyProfileRequest) (*bffv1.GetMyProfileResponse, error) {
	uid, errCode := u.sessionUID(ctx)
	if errCode != nil {
		return &bffv1.GetMyProfileResponse{Fail: true, Err: errCode}, nil
	}

	resp, err := u.Repo.User.GetUserProfile(ctx, connect.NewRequest(&userv1.GetUserProfileRequest{Uid: uid}))
	if err != nil {
		u.log.Error("user.GetUserProfile failed", "uid", uid, "error", err)
		return &bffv1.GetMyProfileResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.Fail {
		return &bffv1.GetMyProfileResponse{Fail: resp.Msg.Fail, Err: resp.Msg.Err}, nil
	}

	view, mapErr := u.userProfileView(ctx, resp.Msg.GetProfile())
	if mapErr != nil {
		return &bffv1.GetMyProfileResponse{Fail: true, Err: mapErr}, nil
	}
	return &bffv1.GetMyProfileResponse{Profile: view}, nil
}

// GetUserProfile 他人主页资料：并行获取目标资料与会话用户对其的关注关系，组装 ProfileView。
// 关系查询无关系记录（NotFound）按未关注/未被关注展示，不打挂主页聚合；其余业务错误透传。
func (u *BFFUsecaseImpl) GetUserProfile(ctx context.Context, req *bffv1.GetUserProfileRequest) (*bffv1.GetUserProfileResponse, error) {
	uid, errCode := u.sessionUID(ctx)
	if errCode != nil {
		return &bffv1.GetUserProfileResponse{Fail: true, Err: errCode}, nil
	}
	target := req.GetUid()

	var profileResp *connect.Response[userv1.GetUserProfileResponse]
	var relationResp *connect.Response[userv1.GetFollowingRelationResponse]
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		var err error
		profileResp, err = u.Repo.User.GetUserProfile(gctx, connect.NewRequest(&userv1.GetUserProfileRequest{Uid: target}))
		return err
	})
	g.Go(func() error {
		var err error
		relationResp, err = u.Repo.User.GetFollowingRelation(gctx, connect.NewRequest(&userv1.GetFollowingRelationRequest{Uid: uid, TargetUid: target}))
		return err
	})
	if err := g.Wait(); err != nil {
		u.log.Error("user.GetUserProfile/GetFollowingRelation failed", "uid", uid, "target", target, "error", err)
		return &bffv1.GetUserProfileResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if profileResp.Msg.GetFail() {
		return &bffv1.GetUserProfileResponse{Fail: true, Err: profileResp.Msg.GetErr()}, nil
	}

	relation := &bffv1.Relation{}
	if !relationResp.Msg.GetFail() {
		relation.IsFollowing = relationResp.Msg.GetIsFollowing()
		relation.IsFollower = relationResp.Msg.GetIsFollower()
	} else if relationResp.Msg.GetErr().GetCode() != errmsg.NotFound.Code {
		u.log.Error("user.GetFollowingRelation failed", "uid", uid, "target", target,
			"err_code", relationResp.Msg.GetErr().GetCode())
		return &bffv1.GetUserProfileResponse{Fail: true, Err: relationResp.Msg.GetErr()}, nil
	}

	profile, mapErr := u.userProfileView(ctx, profileResp.Msg.GetProfile())
	if mapErr != nil {
		return &bffv1.GetUserProfileResponse{Fail: true, Err: mapErr}, nil
	}
	return &bffv1.GetUserProfileResponse{
		View: &bffv1.ProfileView{
			Profile:  profile,
			Relation: relation,
		},
	}, nil
}

// UpdateMyProfile 更新我的资料（部分更新：仅更新传入的字段，nil 不修改），透传下游。
func (u *BFFUsecaseImpl) UpdateMyProfile(ctx context.Context, req *bffv1.UpdateMyProfileRequest) (*bffv1.UpdateMyProfileResponse, error) {
	uid, errCode := u.sessionUID(ctx)
	if errCode != nil {
		return &bffv1.UpdateMyProfileResponse{Fail: true, Err: errCode}, nil
	}

	resp, err := u.Repo.User.UpdateUserProfile(ctx, connect.NewRequest(&userv1.UpdateUserProfileRequest{
		Uid:      uid,
		Name:     req.GetName(),
		Bio:      req.GetBio(),
		Metadata: req.GetMetadata(),
	}))
	if err != nil {
		u.log.Error("user.UpdateUserProfile failed", "uid", uid, "error", err)
		return &bffv1.UpdateMyProfileResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.Fail {
		return &bffv1.UpdateMyProfileResponse{Fail: resp.Msg.Fail, Err: resp.Msg.Err}, nil
	}

	view, mapErr := u.userProfileView(ctx, resp.Msg.GetProfile())
	if mapErr != nil {
		return &bffv1.UpdateMyProfileResponse{Fail: true, Err: mapErr}, nil
	}
	return &bffv1.UpdateMyProfileResponse{Profile: view}, nil
}

// UpdateMyAvatar 更新我的头像：头像资产须先经 UploadMedia 上传，且必须属于会话用户。
// 流程：校验归属 → 挂载新头像（+1，同步）→ 下游更新 → 对返回的旧资产 ID 发卸载消息（-1，MQ 异步）。
// 下游更新失败回滚挂载；卸载消息发送失败不打挂已成功的新头像（重试会错卸新资产），
// 记 ERROR 日志待闲置资产回收兜底。
func (u *BFFUsecaseImpl) UpdateMyAvatar(ctx context.Context, req *bffv1.UpdateMyAvatarRequest) (*bffv1.UpdateMyAvatarResponse, error) {
	uid, errCode := u.sessionUID(ctx)
	if errCode != nil {
		return &bffv1.UpdateMyAvatarResponse{Fail: true, Err: errCode}, nil
	}

	avatar := req.GetAvatar()
	assetID := avatar.GetAssetId()
	if assetID == "" {
		return &bffv1.UpdateMyAvatarResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	assetResp, err := u.Repo.Media.GetMediaAsset(ctx, connect.NewRequest(&mediav1.GetMediaAssetRequest{Id: assetID}))
	if err != nil {
		u.log.Error("media.GetMediaAsset failed", "uid", uid, "asset_id", assetID, "error", err)
		return &bffv1.UpdateMyAvatarResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if assetResp.Msg.GetFail() {
		return &bffv1.UpdateMyAvatarResponse{Fail: true, Err: assetResp.Msg.GetErr()}, nil
	}
	if assetResp.Msg.GetAsset().GetUploaderId() != uid {
		return &bffv1.UpdateMyAvatarResponse{Fail: true, Err: errmsg.Forbidden}, nil
	}

	avatarFile, refErr := u.GetAndRefMediaFile(ctx, assetID, avatar.GetType())
	if refErr != nil {
		return &bffv1.UpdateMyAvatarResponse{Fail: true, Err: refErr}, nil
	}

	resp, err := u.Repo.User.UpdateUserAvatar(ctx, connect.NewRequest(&userv1.UpdateUserAvatarRequest{
		Uid:    uid,
		Avatar: avatarFile,
	}))
	if err != nil {
		u.log.Error("user.UpdateUserAvatar failed", "uid", uid, "asset_id", assetID, "error", err)
		if rb := u.DeRefMediaFile(ctx, assetID); rb != nil {
			u.log.Error("rollback media ref count failed", "uid", uid, "asset_id", assetID, "err_code", rb.GetCode())
		}
		return &bffv1.UpdateMyAvatarResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.GetFail() {
		if rb := u.DeRefMediaFile(ctx, assetID); rb != nil {
			u.log.Error("rollback media ref count failed", "uid", uid, "asset_id", assetID, "err_code", rb.GetCode())
		}
		return &bffv1.UpdateMyAvatarResponse{Fail: true, Err: resp.Msg.GetErr()}, nil
	}

	if oldID := resp.Msg.GetOldAssetId(); oldID != "" {
		if derefErr := u.DeRefMediaFile(ctx, oldID); derefErr != nil {
			u.log.Error("deref old avatar failed", "uid", uid, "old_asset_id", oldID, "err_code", derefErr.GetCode())
		}
	}

	view, mapErr := u.userProfileView(ctx, resp.Msg.GetProfile())
	if mapErr != nil {
		return &bffv1.UpdateMyAvatarResponse{Fail: true, Err: mapErr}, nil
	}
	return &bffv1.UpdateMyAvatarResponse{Profile: view}, nil
}

// Follow 关注目标用户：自关注由下游校验拒绝。
func (u *BFFUsecaseImpl) Follow(ctx context.Context, req *bffv1.FollowRequest) (*bffv1.FollowResponse, error) {
	uid, errCode := u.sessionUID(ctx)
	if errCode != nil {
		return &bffv1.FollowResponse{Fail: true, Err: errCode}, nil
	}

	resp, err := u.Repo.User.SetFollow(ctx, connect.NewRequest(&userv1.SetFollowRequest{Uid: uid, TargetUid: req.GetTargetUid()}))
	if err != nil {
		u.log.Error("user.SetFollow failed", "uid", uid, "target", req.GetTargetUid(), "error", err)
		return &bffv1.FollowResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.Fail {
		return &bffv1.FollowResponse{Fail: resp.Msg.Fail, Err: resp.Msg.Err}, nil
	}

	return &bffv1.FollowResponse{}, nil
}

// Unfollow 取消关注目标用户。
func (u *BFFUsecaseImpl) Unfollow(ctx context.Context, req *bffv1.UnfollowRequest) (*bffv1.UnfollowResponse, error) {
	uid, errCode := u.sessionUID(ctx)
	if errCode != nil {
		return &bffv1.UnfollowResponse{Fail: true, Err: errCode}, nil
	}

	resp, err := u.Repo.User.SetUnFollow(ctx, connect.NewRequest(&userv1.SetUnFollowRequest{Uid: uid, TargetUid: req.GetTargetUid()}))
	if err != nil {
		u.log.Error("user.SetUnFollow failed", "uid", uid, "target", req.GetTargetUid(), "error", err)
		return &bffv1.UnfollowResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.Fail {
		return &bffv1.UnfollowResponse{Fail: resp.Msg.Fail, Err: resp.Msg.Err}, nil
	}

	return &bffv1.UnfollowResponse{}, nil
}

// GetFollowingList 我的关注列表：游标透传下游。
func (u *BFFUsecaseImpl) GetFollowingList(ctx context.Context, req *bffv1.GetFollowingListRequest) (*bffv1.GetFollowingListResponse, error) {
	uid, errCode := u.sessionUID(ctx)
	if errCode != nil {
		return &bffv1.GetFollowingListResponse{Fail: true, Err: errCode}, nil
	}

	resp, err := u.Repo.User.GetFollowings(ctx, connect.NewRequest(&userv1.GetFollowingsRequest{Uid: uid, Cursor: req.GetCursor()}))
	if err != nil {
		u.log.Error("user.GetFollowings failed", "uid", uid, "error", err)
		return &bffv1.GetFollowingListResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.Fail {
		return &bffv1.GetFollowingListResponse{Fail: resp.Msg.Fail, Err: resp.Msg.Err}, nil
	}

	briefs, mapErr := u.userBriefsView(ctx, resp.Msg.GetUserBriefs())
	if mapErr != nil {
		return &bffv1.GetFollowingListResponse{Fail: true, Err: mapErr}, nil
	}
	return &bffv1.GetFollowingListResponse{UserBriefs: briefs, Cursor: resp.Msg.GetCursor()}, nil
}

// GetFollowerList 我的粉丝列表：游标透传下游（与 GetFollowingList 同构）。
func (u *BFFUsecaseImpl) GetFollowerList(ctx context.Context, req *bffv1.GetFollowerListRequest) (*bffv1.GetFollowerListResponse, error) {
	uid, errCode := u.sessionUID(ctx)
	if errCode != nil {
		return &bffv1.GetFollowerListResponse{Fail: true, Err: errCode}, nil
	}

	resp, err := u.Repo.User.GetFollowers(ctx, connect.NewRequest(&userv1.GetFollowersRequest{Uid: uid, Cursor: req.GetCursor()}))
	if err != nil {
		u.log.Error("user.GetFollowers failed", "uid", uid, "error", err)
		return &bffv1.GetFollowerListResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.Fail {
		return &bffv1.GetFollowerListResponse{Fail: resp.Msg.Fail, Err: resp.Msg.Err}, nil
	}

	briefs, mapErr := u.userBriefsView(ctx, resp.Msg.GetUserBriefs())
	if mapErr != nil {
		return &bffv1.GetFollowerListResponse{Fail: true, Err: mapErr}, nil
	}
	return &bffv1.GetFollowerListResponse{UserBriefs: briefs, Cursor: resp.Msg.GetCursor()}, nil
}

// GetFollowRelation 我与目标用户的关注关系。
func (u *BFFUsecaseImpl) GetFollowRelation(ctx context.Context, req *bffv1.GetFollowRelationRequest) (*bffv1.GetFollowRelationResponse, error) {
	uid, errCode := u.sessionUID(ctx)
	if errCode != nil {
		return &bffv1.GetFollowRelationResponse{Fail: true, Err: errCode}, nil
	}

	resp, err := u.Repo.User.GetFollowingRelation(ctx, connect.NewRequest(&userv1.GetFollowingRelationRequest{Uid: uid, TargetUid: req.GetTargetUid()}))
	if err != nil {
		u.log.Error("user.GetFollowingRelation failed", "uid", uid, "target", req.GetTargetUid(), "error", err)
		return &bffv1.GetFollowRelationResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.Fail {
		return &bffv1.GetFollowRelationResponse{Fail: resp.Msg.Fail, Err: resp.Msg.Err}, nil
	}

	return &bffv1.GetFollowRelationResponse{IsFollowing: resp.Msg.GetIsFollowing(), IsFollower: resp.Msg.GetIsFollower()}, nil
}
