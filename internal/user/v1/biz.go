package v1

import (
	"context"
	"errors"
	"fmt"

	commonv1 "rednote/gen/go/common/v1"
	userv1 "rednote/gen/go/user/v1"
	common "rednote/internal/common/models"
	media "rednote/internal/media/models"
	models "rednote/internal/user/models"
	"rednote/packages/errmsg"
	"rednote/packages/mq"
	"rednote/packages/utils"

	"github.com/bwmarrin/snowflake"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// UserUsecase 用户业务逻辑接口
type UserUsecase interface {
	// ==================== 用户资料 ====================

	// CreateUser 创建用户资料（uid 由鉴权服务分配后透传）
	CreateUser(ctx context.Context, req *userv1.CreateUserRequest) (*userv1.CreateUserResponse, error)
	// UpdateUserProfile 更新用户资料（部分更新：仅更新传入的字段，nil 字段不修改）
	UpdateUserProfile(ctx context.Context, req *userv1.UpdateUserProfileRequest) (*userv1.UpdateUserProfileResponse, error)
	// UpdateUserAvatar 更新用户头像
	UpdateUserAvatar(ctx context.Context, req *userv1.UpdateUserAvatarRequest) (*userv1.UpdateUserAvatarResponse, error)
	// GetUserBrief 批量获取用户简要信息
	GetUserBrief(ctx context.Context, req *userv1.GetUserBriefRequest) (*userv1.GetUserBriefResponse, error)
	// GetUserProfile 获取用户资料
	GetUserProfile(ctx context.Context, req *userv1.GetUserProfileRequest) (*userv1.GetUserProfileResponse, error)
	// GetUserStatus 获取用户状态
	GetUserStatus(ctx context.Context, req *userv1.GetUserStatusRequest) (*userv1.GetUserStatusResponse, error)
	// SetFollow 关注用户
	SetFollow(ctx context.Context, req *userv1.SetFollowRequest) (*userv1.SetFollowResponse, error)
	// SetUnFollow 取消关注
	SetUnFollow(ctx context.Context, req *userv1.SetUnFollowRequest) (*userv1.SetUnFollowResponse, error)
	// GetFollowings 获取关注列表
	GetFollowings(ctx context.Context, req *userv1.GetFollowingsRequest) (*userv1.GetFollowingsResponse, error)
	// GetFollowingRelation 获取关注关系
	GetFollowingRelation(ctx context.Context, req *userv1.GetFollowingRelationRequest) (*userv1.GetFollowingRelationResponse, error)
	// BanUser 封禁用户
	BanUser(ctx context.Context, req *userv1.BanUserRequest) (*userv1.BanUserResponse, error)
}

// UserUsecaseImpl 用户业务逻辑实现
type UserUsecaseImpl struct {
	Repo UserRepository
	DB   *gorm.DB
	MQ   mq.MQClient
}

var _ UserUsecase = (*UserUsecaseImpl)(nil)

// NewUserUsecase 创建 UserUsecase 实例
func NewUserUsecase(repo UserRepository, db *gorm.DB, mq mq.MQClient) *UserUsecaseImpl {
	return &UserUsecaseImpl{
		Repo: repo,
		DB:   db,
		MQ:   mq,
	}
}

// ==================== 用户资料 ====================

// CreateUser 创建用户资料
func (u *UserUsecaseImpl) CreateUser(ctx context.Context, req *userv1.CreateUserRequest) (*userv1.CreateUserResponse, error) {

	id, err := snowflake.ParseString(req.GetUid())
	if err != nil {
		return &userv1.CreateUserResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	if !utils.ValidateName(req.GetName()) {
		return &userv1.CreateUserResponse{Fail: true, Err: errmsg.InvalidName}, nil
	}

	if !utils.ValidateBio(req.GetBio()) {
		return &userv1.CreateUserResponse{Fail: true, Err: errmsg.InvalidBio}, nil
	}

	// 头像必填：用户未提供时由 BFF 填充默认头像，此处直接拒绝无头像请求
	if req.GetAvatar() == nil || !utils.ValidateMedia(req.GetAvatar()) {
		return &userv1.CreateUserResponse{Fail: true, Err: errmsg.InvalidAvatar}, nil
	}

	avatarFile, err := media.MediaFileFromProto(req.GetAvatar())
	if err != nil {
		return &userv1.CreateUserResponse{Fail: true, Err: errmsg.InvalidAvatar}, nil
	}
	// metadata 可选：空值等价于 {}（空对象），仅非法 JSON 才拒绝
	metadata, err := utils.UnmarshalJSON([]byte(req.GetMetadata().GetPayload()))
	if err != nil {
		return &userv1.CreateUserResponse{Fail: true, Err: errmsg.InvalidArgument}, nil
	}

	profile := &models.UserProfile{
		BaseModel: common.BaseModel{
			ID: id.Int64(),
		},
		Name: req.GetName(),
		Bio:  req.GetBio(),

		Avatar: &avatarFile,
		Status: userv1.UserStatus_USER_STATUS_ACTIVE,

		MetaData: metadata,
	}
	ok, err := u.Repo.CreateUser(ctx, profile)
	if err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}

	if !ok {
		return &userv1.CreateUserResponse{Fail: true, Err: errmsg.AlreadyExist}, nil
	}

	proto := profile.ToProto()
	return &userv1.CreateUserResponse{
		Profile: proto,
	}, nil
}

// errUserNotFound 更新目标用户不存在（事务内哨兵，触发整体回滚）
var errUserNotFound = errors.New("user: profile not found")

// UpdateUserProfile 更新用户资料（部分更新：仅更新传入的字段，nil 字段不修改；统计与状态不在此接口修改）
func (u *UserUsecaseImpl) UpdateUserProfile(ctx context.Context, req *userv1.UpdateUserProfileRequest) (*userv1.UpdateUserProfileResponse, error) {

	// 可空字段指示更新范围：nil 表示不修改
	updateName := req.GetName() != nil
	updateBio := req.GetBio() != nil
	updateMetadata := req.GetMetadata() != nil

	if !updateName && !updateBio && !updateMetadata {
		return &userv1.UpdateUserProfileResponse{Fail: true, Err: errmsg.InvalidArgument}, nil
	}

	id, err := snowflake.ParseString(req.GetUid())
	if err != nil {
		return &userv1.UpdateUserProfileResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	if updateName && !utils.ValidateName(req.GetName().GetValue()) {
		return &userv1.UpdateUserProfileResponse{Fail: true, Err: errmsg.InvalidName}, nil
	}

	if updateBio && !utils.ValidateBio(req.GetBio().GetValue()) {
		return &userv1.UpdateUserProfileResponse{Fail: true, Err: errmsg.InvalidBio}, nil
	}

	var metadata datatypes.JSONMap
	if updateMetadata {
		// metadata 可选：空值等价于 {}（空对象），仅非法 JSON 才拒绝
		metadata, err = utils.UnmarshalJSON([]byte(req.GetMetadata().GetPayload()))
		if err != nil {
			return &userv1.UpdateUserProfileResponse{Fail: true, Err: errmsg.InvalidArgument}, nil
		}
	}

	// 单事务内逐字段更新，保证部分更新的原子性；
	// PG 中存在的行即使值不变 UPDATE 也计 1 行，故任一更新 0 行即视为用户不存在，整体回滚
	err = u.DB.Transaction(func(tx *gorm.DB) error {
		repo := u.Repo.WithTX(tx)

		if updateName {
			info, err := repo.UpdateUserName(ctx, id.Int64(), req.GetName().GetValue())
			if err != nil {
				return err
			}
			if info.RowsAffected == 0 {
				return errUserNotFound
			}
		}

		if updateBio {
			info, err := repo.UpdateUserBio(ctx, id.Int64(), req.GetBio().GetValue())
			if err != nil {
				return err
			}
			if info.RowsAffected == 0 {
				return errUserNotFound
			}
		}

		if updateMetadata {
			info, err := repo.UpdateUserMetadata(ctx, id.Int64(), metadata)
			if err != nil {
				return err
			}
			if info.RowsAffected == 0 {
				return errUserNotFound
			}
		}

		return nil
	})

	if errors.Is(err, errUserNotFound) {
		return &userv1.UpdateUserProfileResponse{Fail: true, Err: errmsg.NotFound}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to update user: %w", err)
	}

	// 回读完整资料返回（含未在本接口修改的状态与统计）
	profiles, err := u.Repo.GetUserProfiles(ctx, []int64{id.Int64()})
	if err != nil {
		return nil, fmt.Errorf("failed to get updated user profile: %w", err)
	}
	if len(profiles) == 0 {
		return &userv1.UpdateUserProfileResponse{Fail: true, Err: errmsg.NotFound}, nil
	}

	return &userv1.UpdateUserProfileResponse{Profile: profiles[0].ToProto()}, nil
}

// UpdateUserAvatar 更新头像
// 返回的 old_asset_id 为被替换的旧头像所属媒体资产 ID，由 BFF 据此调整旧资产引用计数；首次设置头像时为空
func (u *UserUsecaseImpl) UpdateUserAvatar(ctx context.Context, req *userv1.UpdateUserAvatarRequest) (*userv1.UpdateUserAvatarResponse, error) {

	id, err := snowflake.ParseString(req.GetUid())
	if err != nil {
		return &userv1.UpdateUserAvatarResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	if req.GetAvatar() == nil || !utils.ValidateMedia(req.GetAvatar()) {
		return &userv1.UpdateUserAvatarResponse{Fail: true, Err: errmsg.InvalidAvatar}, nil
	}

	file, err := media.MediaFileFromProto(req.GetAvatar())
	if err != nil {
		return &userv1.UpdateUserAvatarResponse{Fail: true, Err: errmsg.InvalidAvatar}, nil
	}
	avatarFile := &file

	// 被替换的旧头像所属媒体资产 ID（首次设置头像时为空），供 BFF 调整旧资产引用计数
	var oldAssetID string

	// 旧值读取与更新在同一事务内，并发场景下返回的 old_asset_id 与实际被替换的头像一致；
	// PG 中存在的行即使值不变 UPDATE 也计 1 行，故更新 0 行即视为用户不存在，整体回滚
	err = u.DB.Transaction(func(tx *gorm.DB) error {
		repo := u.Repo.WithTX(tx)

		profiles, err := repo.GetUserProfiles(ctx, []int64{id.Int64()})
		if err != nil {
			return err
		}
		if len(profiles) == 0 {
			return errUserNotFound
		}
		if old := profiles[0].Avatar; old != nil && old.AssetID != 0 {
			oldAssetID = snowflake.ParseInt64(old.AssetID).String()
		}

		info, err := repo.UpdateUserAvatar(ctx, id.Int64(), avatarFile)
		if err != nil {
			return err
		}
		if info.RowsAffected == 0 {
			return errUserNotFound
		}

		return nil
	})

	if errors.Is(err, errUserNotFound) {
		return &userv1.UpdateUserAvatarResponse{Fail: true, Err: errmsg.NotFound}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to update user: %w", err)
	}

	// 回读完整资料返回（含未在本接口修改的状态与统计）
	profiles, err := u.Repo.GetUserProfiles(ctx, []int64{id.Int64()})
	if err != nil {
		return nil, fmt.Errorf("failed to get updated user profile: %w", err)
	}
	if len(profiles) == 0 {
		return &userv1.UpdateUserAvatarResponse{Fail: true, Err: errmsg.NotFound}, nil
	}

	return &userv1.UpdateUserAvatarResponse{Profile: profiles[0].ToProto(), OldAssetId: oldAssetID}, nil
}

// GetUserBrief 批量获取用户简要信息
func (u *UserUsecaseImpl) GetUserBrief(ctx context.Context, req *userv1.GetUserBriefRequest) (*userv1.GetUserBriefResponse, error) {

	if len(req.GetUids()) == 0 {
		return &userv1.GetUserBriefResponse{Results: []*userv1.UserBriefResult{}}, nil
	}

	uids := make([]int64, 0, len(req.GetUids()))
	results := make([]*userv1.UserBriefResult, 0)
	for _, id := range req.GetUids() {
		if parsedID, err := snowflake.ParseString(id); err == nil {
			uids = append(uids, parsedID.Int64())
		} else {
			results = append(results, &userv1.UserBriefResult{Fail: true, Err: errmsg.InvalidID})
		}
	}

	profiles, err := u.Repo.GetUserProfiles(ctx, uids)
	if err != nil {
		return nil, fmt.Errorf("failed to get user profiles: %w", err)
	}

	for _, id := range uids {
		found := false
		for _, v := range profiles {
			if v.ID == id {
				results = append(results, &userv1.UserBriefResult{Brief: v.ToBrief()})
				found = true
				break
			}
		}
		if !found {
			results = append(results, &userv1.UserBriefResult{Fail: true, Err: errmsg.NotFound})
		}
	}

	return &userv1.GetUserBriefResponse{Results: results}, nil
}

// GetUserProfile 获取用户资料
func (u *UserUsecaseImpl) GetUserProfile(ctx context.Context, req *userv1.GetUserProfileRequest) (*userv1.GetUserProfileResponse, error) {

	uid, err := snowflake.ParseString(req.GetUid())
	if err != nil {
		return &userv1.GetUserProfileResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	profiles, err := u.Repo.GetUserProfiles(ctx, []int64{uid.Int64()})
	if err != nil {
		return nil, fmt.Errorf("failed to get user profile: %w", err)
	}

	if len(profiles) == 0 {
		return &userv1.GetUserProfileResponse{Fail: true, Err: errmsg.NotFound}, nil
	}

	proto := profiles[0].ToProto()

	return &userv1.GetUserProfileResponse{Profile: proto}, nil
}

// GetUserStatus 获取用户状态
func (u *UserUsecaseImpl) GetUserStatus(ctx context.Context, req *userv1.GetUserStatusRequest) (*userv1.GetUserStatusResponse, error) {
	uid, err := snowflake.ParseString(req.GetUid())
	if err != nil {
		return &userv1.GetUserStatusResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	profiles, err := u.Repo.GetUserProfiles(ctx, []int64{uid.Int64()})
	if err != nil {
		return nil, fmt.Errorf("failed to get user status: %w", err)
	}

	if len(profiles) == 0 {
		return &userv1.GetUserStatusResponse{Fail: true, Err: errmsg.NotFound}, nil
	}

	return &userv1.GetUserStatusResponse{Status: profiles[0].Status}, nil
}

func (u *UserUsecaseImpl) SetFollow(ctx context.Context, req *userv1.SetFollowRequest) (*userv1.SetFollowResponse, error) {
	fanID, err := snowflake.ParseString(req.GetUid())
	if err != nil {
		return &userv1.SetFollowResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	followingID, err := snowflake.ParseString(req.GetTargetUid())
	if err != nil {
		return &userv1.SetFollowResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	if fanID == followingID {
		return &userv1.SetFollowResponse{Fail: true, Err: errmsg.InvalidArgument}, nil
	}

	found, err := u.Repo.SetFollow(ctx, fanID.Int64(), followingID.Int64())
	if err != nil {
		return nil, fmt.Errorf("failed to set follow: %w", err)
	}

	if !found {
		return &userv1.SetFollowResponse{Fail: true, Err: errmsg.NotFound}, nil
	}

	return &userv1.SetFollowResponse{}, nil
}

func (u *UserUsecaseImpl) SetUnFollow(ctx context.Context, req *userv1.SetUnFollowRequest) (*userv1.SetUnFollowResponse, error) {
	fanID, err := snowflake.ParseString(req.GetUid())
	if err != nil {
		return &userv1.SetUnFollowResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	followingID, err := snowflake.ParseString(req.GetTargetUid())
	if err != nil {
		return &userv1.SetUnFollowResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	if fanID == followingID {
		return &userv1.SetUnFollowResponse{Fail: true, Err: errmsg.InvalidArgument}, nil
	}

	err = u.Repo.SetUnFollow(ctx, fanID.Int64(), followingID.Int64())
	if err != nil {
		return nil, fmt.Errorf("failed to set unfollow: %w", err)
	}

	return &userv1.SetUnFollowResponse{}, nil
}

func (u *UserUsecaseImpl) GetFollowingRelation(ctx context.Context, req *userv1.GetFollowingRelationRequest) (*userv1.GetFollowingRelationResponse, error) {
	fanID, err := snowflake.ParseString(req.GetUid())
	if err != nil {
		return &userv1.GetFollowingRelationResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	followingID, err := snowflake.ParseString(req.GetTargetUid())
	if err != nil {
		return &userv1.GetFollowingRelationResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	isFollowing, isFollower, err := u.Repo.GetFollowingRelation(ctx, fanID.Int64(), followingID.Int64())
	if err != nil {
		return nil, fmt.Errorf("failed to get following relation: %w", err)
	}

	return &userv1.GetFollowingRelationResponse{
		IsFollowing: isFollowing,
		IsFollower:  isFollower,
	}, nil
}

func (u *UserUsecaseImpl) GetFollowings(ctx context.Context, req *userv1.GetFollowingsRequest) (*userv1.GetFollowingsResponse, error) {
	uid, err := snowflake.ParseString(req.GetUid())
	if err != nil {
		return &userv1.GetFollowingsResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	stamp, limit := utils.ParseTimeCursor(req.GetCursor())

	follows, err := u.Repo.GetFollowings(ctx, uid.Int64(), stamp, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get followings: %w", err)
	}

	var cursor *commonv1.Cursor
	if len(follows) > 0 {
		cursor = utils.NewTimeCursor(follows[len(follows)-1].CreatedAt, limit)
	}

	proto := make([]*userv1.UserBrief, 0, len(follows))
	for _, follow := range follows {
		if follow.Following == nil {
			continue
		}
		proto = append(proto, follow.Following.ToBrief())
	}

	return &userv1.GetFollowingsResponse{
		UserBriefs: proto,
		Cursor:     cursor,
	}, nil
}

func (u *UserUsecaseImpl) BanUser(ctx context.Context, req *userv1.BanUserRequest) (*userv1.BanUserResponse, error) {
	uid, err := snowflake.ParseString(req.GetUid())
	if err != nil {
		return &userv1.BanUserResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	err = u.Repo.BanUser(ctx, uid.Int64())
	if err != nil {
		return nil, fmt.Errorf("failed to ban user: %w", err)
	}

	return &userv1.BanUserResponse{}, nil
}
