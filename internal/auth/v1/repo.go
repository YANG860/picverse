package v1

import (
	"context"
	"errors"
	"rednote/gen/query"
	auth "rednote/internal/auth/models"

	"gorm.io/gen"
	"gorm.io/gorm"
)

type AuthRepository interface {
	WithTX(*gorm.DB) AuthRepository
	// email是否注册
	EmailExist(context.Context, string) (bool, error)
	// 创建注册记录
	CreateCredential(context.Context, *auth.UserCredential) error
	// 设置web端tokenID
	SetRefreshTokenIDWeb(context.Context, int64, string) (gen.ResultInfo, error)
	// 设置mobile端tokenID
	SetRefreshTokenIDMobile(ctx context.Context, userID int64, tokenID string) (gen.ResultInfo, error)
	// 获取注册记录
	GetCredentialByEmail(ctx context.Context, email string) (*auth.UserCredential, error)
	// 根据用户ID获取注册记录
	GetCredentialByUID(ctx context.Context, userID int64) (*auth.UserCredential, error)
	// 更新密码哈希（改密用）；uid 不存在时 RowsAffected=0
	UpdatePasswordHash(ctx context.Context, userID int64, passwordHash string) (gen.ResultInfo, error)
	// 删除注册记录（物理删除，一并失效其登录态；供注册回滚）
	DeleteCredential(ctx context.Context, userID int64) (gen.ResultInfo, error)
}

type AuthRepoImpl struct {
	DB *gorm.DB
	q  *query.Query
}

func NewAuthRepoImpl(db *gorm.DB) *AuthRepoImpl {
	return &AuthRepoImpl{
		DB: db,
		q:  query.Use(db),
	}
}

func (r *AuthRepoImpl) WithTX(tx *gorm.DB) AuthRepository {
	return &AuthRepoImpl{
		DB: tx,
		q:  query.Use(tx),
	}
}

func (r *AuthRepoImpl) EmailExist(ctx context.Context, email string) (bool, error) {
	_, err := r.q.WithContext(ctx).UserCredential.Where(
		r.q.UserCredential.Email.Eq(email),
	).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (r *AuthRepoImpl) CreateCredential(ctx context.Context, credential *auth.UserCredential) error {
	return r.q.WithContext(ctx).UserCredential.Create(credential)
}

func (r *AuthRepoImpl) SetRefreshTokenIDWeb(ctx context.Context, userID int64, tokenID string) (gen.ResultInfo, error) {
	info, err := r.q.UserCredential.WithContext(ctx).Where(
		r.q.UserCredential.ID.Eq(userID),
	).Update(
		r.q.UserCredential.RefreshTokenIDWeb,
		tokenID,
	)
	return info, err
}

func (r *AuthRepoImpl) SetRefreshTokenIDMobile(ctx context.Context, userID int64, tokenID string) (gen.ResultInfo, error) {
	info, err := r.q.UserCredential.WithContext(ctx).Where(
		r.q.UserCredential.ID.Eq(userID),
	).Update(
		r.q.UserCredential.RefreshTokenIDMobile,
		tokenID,
	)
	return info, err
}

func (r *AuthRepoImpl) GetCredentialByEmail(ctx context.Context, email string) (*auth.UserCredential, error) {
	credential, err := r.q.WithContext(ctx).UserCredential.Where(
		r.q.UserCredential.Email.Eq(email),
	).First()

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	return credential, nil
}

func (r *AuthRepoImpl) GetCredentialByUID(ctx context.Context, userID int64) (*auth.UserCredential, error) {
	credentials, err := r.q.WithContext(ctx).UserCredential.Where(
		r.q.UserCredential.ID.Eq(userID),
	).First()

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	return credentials, nil
}

func (r *AuthRepoImpl) UpdatePasswordHash(ctx context.Context, userID int64, passwordHash string) (gen.ResultInfo, error) {
	info, err := r.q.UserCredential.WithContext(ctx).Where(
		r.q.UserCredential.ID.Eq(userID),
	).Update(
		r.q.UserCredential.PasswordHash,
		passwordHash,
	)
	return info, err
}

func (r *AuthRepoImpl) DeleteCredential(ctx context.Context, userID int64) (gen.ResultInfo, error) {
	return r.q.WithContext(ctx).UserCredential.Where(
		r.q.UserCredential.ID.Eq(userID),
	).Delete()
}
