package v1

import (
	"context"

	userv1 "rednote/gen/go/user/v1"
	"rednote/gen/query"
	models "rednote/internal/user/models"

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
	// UpdateUser 更新用户资料（name/bio/头像/metadata 全量覆盖，含零值）
	UpdateUser(ctx context.Context, profile *models.UserProfile) (gen.ResultInfo, error)
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

// UpdateUser 更新用户资料（name/bio/头像/metadata 全量覆盖，含零值；统计与状态不在更新范围）
func (r *UserRepoImpl) UpdateUser(ctx context.Context, profile *models.UserProfile) (gen.ResultInfo, error) {
	q := r.q.UserProfile
	return r.q.UserProfile.WithContext(ctx).
		Where(q.ID.Eq(profile.ID)).
		Select(
			q.Name, q.Bio, q.MetaData,
			q.AvatarID, q.AvatarCreatedAt, q.AvatarUpdatedAt,
			q.AvatarAssetID, q.AvatarType, q.AvatarObjKey, q.AvatarBucket,
			q.AvatarSha256Sum, q.AvatarSizeBytes, q.AvatarFormat,
			q.AvatarHeight, q.AvatarWidth, q.AvatarMediaMetaData,
		).
		Updates(profile)
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
