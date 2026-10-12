package v1

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	commonv1 "rednote/gen/go/common/v1"
	mediav1 "rednote/gen/go/media/v1"
	userv1 "rednote/gen/go/user/v1"
	common "rednote/internal/common/models"
	media "rednote/internal/media/models"
	models "rednote/internal/user/models"
	"rednote/packages/errmsg"
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
	// UpdateUser 更新用户资料（部分更新：仅更新传入的字段，nil 字段不修改）
	UpdateUser(ctx context.Context, req *userv1.UpdateUserRequest) (*userv1.UpdateUserResponse, error)
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
}

var _ UserUsecase = (*UserUsecaseImpl)(nil)

// NewUserUsecase 创建 UserUsecase 实例
func NewUserUsecase(repo UserRepository, db *gorm.DB) *UserUsecaseImpl {
	return &UserUsecaseImpl{
		Repo: repo,
		DB:   db,
	}
}

// ==================== 用户资料 ====================

// CreateUser 创建用户资料
func (u *UserUsecaseImpl) CreateUser(ctx context.Context, req *userv1.CreateUserRequest) (*userv1.CreateUserResponse, error) {

	id, err := snowflake.ParseString(req.GetUid())
	if err != nil {
		return &userv1.CreateUserResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	if !ValidateName(req.GetName()) {
		return &userv1.CreateUserResponse{Fail: true, Err: errmsg.InvalidName}, nil
	}

	if !ValidateBio(req.GetBio()) {
		return &userv1.CreateUserResponse{Fail: true, Err: errmsg.InvalidBio}, nil
	}

	// 头像必填：用户未提供时由 BFF 填充默认头像，此处直接拒绝无头像请求
	if req.GetAvatar() == nil {
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

// UpdateUser 更新用户资料（部分更新：仅更新传入的字段，nil 字段不修改；统计与状态不在此接口修改）
func (u *UserUsecaseImpl) UpdateUser(ctx context.Context, req *userv1.UpdateUserRequest) (*userv1.UpdateUserResponse, error) {

	// 可空字段指示更新范围：nil 表示不修改
	updateName := req.GetName() != nil
	updateBio := req.GetBio() != nil
	updateAvatar := req.GetAvatar() != nil
	updateMetadata := req.GetMetadata() != nil

	if !updateName && !updateBio && !updateAvatar && !updateMetadata {
		return &userv1.UpdateUserResponse{Fail: true, Err: errmsg.InvalidArgument}, nil
	}

	id, err := snowflake.ParseString(req.GetUid())
	if err != nil {
		return &userv1.UpdateUserResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	if updateName && !ValidateName(req.GetName().GetValue()) {
		return &userv1.UpdateUserResponse{Fail: true, Err: errmsg.InvalidName}, nil
	}

	if updateBio && !ValidateBio(req.GetBio().GetValue()) {
		return &userv1.UpdateUserResponse{Fail: true, Err: errmsg.InvalidBio}, nil
	}

	var avatarFile *media.MediaFile
	if updateAvatar {
		if !ValidateMediaFile(req.GetAvatar()) {
			return &userv1.UpdateUserResponse{Fail: true, Err: errmsg.InvalidAvatar}, nil
		}
		file, err := media.MediaFileFromProto(req.GetAvatar())
		if err != nil {
			return &userv1.UpdateUserResponse{Fail: true, Err: errmsg.InvalidAvatar}, nil
		}
		avatarFile = &file
	}

	var metadata datatypes.JSONMap
	if updateMetadata {
		// metadata 可选：空值等价于 {}（空对象），仅非法 JSON 才拒绝
		metadata, err = utils.UnmarshalJSON([]byte(req.GetMetadata().GetPayload()))
		if err != nil {
			return &userv1.UpdateUserResponse{Fail: true, Err: errmsg.InvalidArgument}, nil
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

		if updateAvatar {
			info, err := repo.UpdateUserAvatar(ctx, id.Int64(), avatarFile)
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
		return &userv1.UpdateUserResponse{Fail: true, Err: errmsg.NotFound}, nil
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
		return &userv1.UpdateUserResponse{Fail: true, Err: errmsg.NotFound}, nil
	}

	return &userv1.UpdateUserResponse{Profile: profiles[0].ToProto()}, nil
}

// 昵称/简介长度上限（rune 计数，与 PostgreSQL VARCHAR(n) 的字符语义一致），
// 与 user_profiles 表列宽 VARCHAR(64)/VARCHAR(512) 对齐，避免落库时被截断/报错
const (
	NameMaxLen = 64
	BioMaxLen  = 512
)

// ValidateName 昵称校验：去除首尾空白后非空、长度 1~64、不含控制字符
func ValidateName(name string) bool {
	if strings.TrimSpace(name) == "" {
		return false
	}
	if utf8.RuneCountInString(name) > NameMaxLen {
		return false
	}
	return !strings.ContainsFunc(name, unicode.IsControl)
}

// ValidateBio 简介校验：可为空、长度 ≤ 512、除换行/回车/制表符外不含控制字符
// （多行简介合法；媒体内容审核由审核服务负责，此处仅做格式校验）
func ValidateBio(bio string) bool {
	if utf8.RuneCountInString(bio) > BioMaxLen {
		return false
	}
	return !strings.ContainsFunc(bio, func(r rune) bool {
		return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t'
	})
}

func ValidateMediaFile(file *mediav1.MediaFile) bool {
	// 头像媒体文件结构校验（内容审核由审核服务负责）
	return file != nil && file.GetBase() != nil && file.GetAssetId() != "" && file.GetObjKey() != ""
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
