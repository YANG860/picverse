package v1

import (
	"context"

	userv1 "rednote/gen/go/user/v1"
	"rednote/gen/query"
	media "rednote/internal/media/models"
	models "rednote/internal/user/models"

	"gorm.io/datatypes"
	"gorm.io/gen"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UserRepository 用户资料存储库接口
type UserRepository interface {
	// 使用事务
	WithTX(tx *gorm.DB) UserRepository

	// ==================== 用户资料 ====================

	// CreateUser 创建用户资料
	CreateUser(ctx context.Context, profile *models.UserProfile) (ok bool, err error)
	// UpdateUserName 更新昵称
	UpdateUserName(ctx context.Context, id int64, name string) (gen.ResultInfo, error)
	// UpdateUserBio 更新简介
	UpdateUserBio(ctx context.Context, id int64, bio string) (gen.ResultInfo, error)
	// UpdateUserMetadata 更新元数据
	UpdateUserMetadata(ctx context.Context, id int64, metadata datatypes.JSONMap) (gen.ResultInfo, error)
	// UpdateUserAvatar 更新头像（含 avatar_* 嵌入列全量覆盖）
	UpdateUserAvatar(ctx context.Context, id int64, avatar *media.MediaFile) (gen.ResultInfo, error)
	// GetUserProfiles 根据用户ID获取用户资料
	GetUserProfiles(ctx context.Context, uids []int64) ([]*models.UserProfile, error)
	// GetFollowings 根据用户ID获取关注列表
	GetFollowings(ctx context.Context, uid int64, before int64, limit int64) ([]*models.UserFollow, error)
	// GetFollowingRelation 获取关注关系
	GetFollowingRelation(ctx context.Context, fanID, followingID int64) (isFollowing, isFollower bool, err error)
	// SetFollow 设置关注关系
	SetFollow(ctx context.Context, fanID, followingID int64) (ok bool, err error)
	// SetUnFollow 取消关注
	SetUnFollow(ctx context.Context, fanID, followingID int64) error
	// BanUser 封禁用户
	BanUser(ctx context.Context, uid int64) error
}

var _ UserRepository = (*UserRepoImpl)(nil)

// UserRepoImpl 用户资料存储库实现
type UserRepoImpl struct {
	DB *gorm.DB
	q  *query.Query
}

// NewUserRepo 创建 UserRepoImpl
func NewUserRepo(db *gorm.DB) *UserRepoImpl {
	return &UserRepoImpl{DB: db, q: query.Use(db)}
}

func (r *UserRepoImpl) WithTX(tx *gorm.DB) UserRepository {
	return &UserRepoImpl{DB: tx, q: query.Use(tx)}
}

// ==================== 用户资料 ====================

// CreateUser 创建用户资料
func (r *UserRepoImpl) CreateUser(ctx context.Context, profile *models.UserProfile) (ok bool, err error) {
	res := r.q.UserProfile.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		UnderlyingDB().
		Create(profile)
	if res.Error != nil {
		return false, res.Error
	}

	return res.RowsAffected > 0, nil
}

// UpdateUserName 更新昵称
func (r *UserRepoImpl) UpdateUserName(ctx context.Context, id int64, name string) (gen.ResultInfo, error) {
	return r.q.UserProfile.WithContext(ctx).Where(r.q.UserProfile.ID.Eq(id)).Update(r.q.UserProfile.Name, name)
}

// UpdateUserBio 更新简介
func (r *UserRepoImpl) UpdateUserBio(ctx context.Context, id int64, bio string) (gen.ResultInfo, error) {
	return r.q.UserProfile.WithContext(ctx).Where(r.q.UserProfile.ID.Eq(id)).Update(r.q.UserProfile.Bio, bio)
}

// UpdateUserMetadata 更新元数据（空 map 落库为 {}）
func (r *UserRepoImpl) UpdateUserMetadata(ctx context.Context, id int64, metadata datatypes.JSONMap) (gen.ResultInfo, error) {
	return r.q.UserProfile.WithContext(ctx).Where(r.q.UserProfile.ID.Eq(id)).Update(r.q.UserProfile.MetaData, metadata)
}

// UpdateUserAvatar 更新头像（含 avatar_* 嵌入列全量覆盖）
//
// 注意：必须用 UpdateSimple 表达式逐列赋值，不能用 Updates(struct)——
// 嵌入的 MediaFile.BaseModel.ID 带 primaryKey tag，gorm 会把嵌入主键拼进 WHERE 条件，
// 导致换头像时匹配 0 行误报 NotFound。
func (r *UserRepoImpl) UpdateUserAvatar(ctx context.Context, id int64, avatar *media.MediaFile) (gen.ResultInfo, error) {
	return r.q.UserProfile.WithContext(ctx).Where(r.q.UserProfile.ID.Eq(id)).
		UpdateSimple(
			r.q.UserProfile.AvatarID.Value(avatar.ID),
			r.q.UserProfile.AvatarCreatedAt.Value(avatar.CreatedAt),
			// avatar_updated_at 为 autoUpdateTime 列，gorm 自动维护，显式赋值会触发重复赋值错误
			r.q.UserProfile.AvatarAssetID.Value(avatar.AssetID),
			r.q.UserProfile.AvatarType.Value(int32(avatar.Type)),
			r.q.UserProfile.AvatarObjKey.Value(avatar.ObjKey),
			r.q.UserProfile.AvatarBucket.Value(avatar.Bucket),
			r.q.UserProfile.AvatarSha256Sum.Value(avatar.Sha256Sum),
			r.q.UserProfile.AvatarSizeBytes.Value(avatar.SizeBytes),
			r.q.UserProfile.AvatarFormat.Value(int32(avatar.Format)),
			r.q.UserProfile.AvatarHeight.Value(avatar.Height),
			r.q.UserProfile.AvatarWidth.Value(avatar.Width),
			r.q.UserProfile.AvatarMediaMetaData.Value(avatar.MediaMetaData),
		)
}

// GetUserProfiles 根据用户ID获取用户资料
func (r *UserRepoImpl) GetUserProfiles(ctx context.Context, uids []int64) ([]*models.UserProfile, error) {
	profiles, err := r.q.UserProfile.WithContext(ctx).
		Where(r.q.UserProfile.ID.In(uids...)).Find()
	return profiles, err
}

func (r *UserRepoImpl) SetFollow(ctx context.Context, fanID, followingID int64) (ok bool, err error) {

	found := true
	err = r.DB.Transaction(func(tx *gorm.DB) error {
		q := query.Use(tx)
		cnt, err := q.UserProfile.WithContext(ctx).
			Where(q.UserProfile.ID.Eq(followingID)).
			Or(q.UserProfile.ID.Eq(fanID)).
			Count()
		if err != nil {
			return err
		}

		if cnt < 2 {
			found = false
			return nil
		}

		// 幂等创建关注关系：唯一键 idx_user_follows_fan_following 冲突时 DO NOTHING，不报错
		res := q.UserFollow.WithContext(ctx).
			Clauses(clause.OnConflict{DoNothing: true}).
			UnderlyingDB().
			Create(&models.UserFollow{FanID: fanID, FollowingID: followingID})
		if res.Error != nil {
			return res.Error
		}
		// RowsAffected=0 表示关注关系已存在：幂等成功，但不得重复递增计数
		if res.RowsAffected == 0 {
			return nil
		}

		_, err = q.UserProfile.WithContext(ctx).
			Where(q.UserProfile.ID.Eq(fanID)).
			Update(q.UserProfile.Followings, q.UserProfile.Followings.Add(1))
		if err != nil {
			return err
		}

		_, err = q.UserProfile.WithContext(ctx).
			Where(q.UserProfile.ID.Eq(followingID)).
			Update(q.UserProfile.Followers, q.UserProfile.Followers.Add(1))
		if err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		return false, err
	}

	return found, err
}

func (r *UserRepoImpl) SetUnFollow(ctx context.Context, fanID, followingID int64) error {
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		q := query.Use(tx)
		info, err := q.UserFollow.WithContext(ctx).
			Where(
				q.UserFollow.FanID.Eq(fanID),
				q.UserFollow.FollowingID.Eq(followingID),
			).
			Delete()
		if err != nil {
			return err
		}

		if info.RowsAffected == 0 {
			return nil
		}

		_, err = q.UserProfile.WithContext(ctx).
			Where(q.UserProfile.ID.Eq(fanID)).
			Update(q.UserProfile.Followings, q.UserProfile.Followings.Sub(1))
		if err != nil {
			return err
		}

		_, err = q.UserProfile.WithContext(ctx).
			Where(q.UserProfile.ID.Eq(followingID)).
			Update(q.UserProfile.Followers, q.UserProfile.Followers.Sub(1))
		if err != nil {
			return err
		}

		return nil
	})

	return err
}

func (r *UserRepoImpl) GetFollowings(ctx context.Context, uid int64, before int64, limit int64) ([]*models.UserFollow, error) {
	follows, err := r.q.UserFollow.WithContext(ctx).
		Where(
			r.q.UserFollow.FanID.Eq(uid),
			r.q.UserFollow.CreatedAt.Lt(before),
		).
		Preload(r.q.UserFollow.Following).
		Order(r.q.UserFollow.CreatedAt.Desc()).
		Limit(int(limit)).
		Find()
	return follows, err
}

func (r *UserRepoImpl) GetFollowingRelation(ctx context.Context, fanID, followingID int64) (isFollowing, isFollower bool, err error) {
	// Check if fan is following following
	isFollowingCnt, err := r.q.UserFollow.WithContext(ctx).
		Where(
			r.q.UserFollow.FanID.Eq(fanID),
			r.q.UserFollow.FollowingID.Eq(followingID),
		).
		Count()

	if err != nil {
		return false, false, err
	}

	// Check if following is following fan
	isFollowerCnt, err := r.q.UserFollow.WithContext(ctx).
		Where(
			r.q.UserFollow.FanID.Eq(followingID),
			r.q.UserFollow.FollowingID.Eq(fanID),
		).
		Count()
	if err != nil {
		return false, false, err
	}

	return isFollowingCnt > 0, isFollowerCnt > 0, nil
}

func (r *UserRepoImpl) BanUser(ctx context.Context, uid int64) error {
	_, err := r.q.UserProfile.WithContext(ctx).
		Where(r.q.UserProfile.ID.Eq(uid)).
		Update(r.q.UserProfile.Status, userv1.UserStatus_USER_STATUS_BANNED)
	return err
}
