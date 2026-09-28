package v1

import (
	"context"
	"errors"
	"testing"
	"time"

	authv1 "rednote/gen/go/auth/v1"
	auth "rednote/internal/auth/models"
	common "rednote/internal/common/models"
	"rednote/test/testdb"
	"rednote/test/testsnowflake"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 固定测试 ID，避免依赖雪花生成结果，便于断言
const (
	userID1 int64 = 2001
	userID2 int64 = 2002
	userID3 int64 = 2003
)

func newRepo(t *testing.T) (*AuthRepoImpl, *gorm.DB) {
	t.Helper()
	testsnowflake.InitTestSnoflake(2) // 使用不同的 worker ID 避免与 media 测试冲突
	db := testdb.Setup(t)
	return NewAuthRepoImpl(db), db
}

func seedCredential(t *testing.T, db *gorm.DB, id int64, email, passwordHash string, role authv1.Role) *auth.UserCredential {
	t.Helper()
	cred := &auth.UserCredential{
		BaseModel:    common.BaseModel{ID: id, CreatedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli()},
		Email:        email,
		PasswordHash: passwordHash,
		Role:         role,
	}
	require.NoError(t, db.Create(cred).Error)
	return cred
}

// ---------- EmailExist ----------

func TestEmailExist(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	seedCredential(t, db, userID1, "exist@example.com", "hash", authv1.Role_ROLE_USER)

	t.Run("邮箱已存在", func(t *testing.T) {
		exist, err := repo.EmailExist(ctx, "exist@example.com")
		require.NoError(t, err)
		assert.True(t, exist)
	})

	t.Run("邮箱不存在", func(t *testing.T) {
		exist, err := repo.EmailExist(ctx, "notexist@example.com")
		require.NoError(t, err)
		assert.False(t, exist)
	})
}

// ---------- CreateCredential ----------

func TestCreateCredential(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	cred := &auth.UserCredential{
		Email:        "new@example.com",
		PasswordHash: "hashedpassword",
		Role:         authv1.Role_ROLE_USER,
	}
	require.NoError(t, repo.CreateCredential(ctx, cred))

	// BeforeCreate 钩子应填充雪花 ID，autoCreateTime 填充毫秒时间戳
	assert.NotZero(t, cred.ID, "创建后应自动填充雪花 ID")
	assert.NotZero(t, cred.CreatedAt, "创建后应自动填充 created_at")

	var got auth.UserCredential
	require.NoError(t, db.Where("id = ?", cred.ID).First(&got).Error)
	assert.Equal(t, "new@example.com", got.Email)
	assert.Equal(t, "hashedpassword", got.PasswordHash)
	assert.Equal(t, authv1.Role_ROLE_USER, got.Role)
}

// ---------- SetRefreshTokenIDWeb ----------

func TestSetRefreshTokenIDWeb(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	seedCredential(t, db, userID1, "web@example.com", "hash", authv1.Role_ROLE_USER)

	t.Run("更新成功", func(t *testing.T) {
		info, err := repo.SetRefreshTokenIDWeb(ctx, userID1, "token-web-123")
		require.NoError(t, err)
		assert.Equal(t, int64(1), info.RowsAffected)

		var got auth.UserCredential
		require.NoError(t, db.Where("id = ?", userID1).First(&got).Error)
		assert.Equal(t, "token-web-123", got.RefreshTokenIDWeb)
	})

	t.Run("用户不存在时更新 0 行", func(t *testing.T) {
		info, err := repo.SetRefreshTokenIDWeb(ctx, 999999, "token-web-456")
		require.NoError(t, err)
		assert.Equal(t, int64(0), info.RowsAffected)
	})
}

// ---------- SetRefreshTokenIDMobile ----------

func TestSetRefreshTokenIDMobile(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	seedCredential(t, db, userID1, "mobile@example.com", "hash", authv1.Role_ROLE_USER)

	t.Run("更新成功", func(t *testing.T) {
		info, err := repo.SetRefreshTokenIDMobile(ctx, userID1, "token-mobile-123")
		require.NoError(t, err)
		assert.Equal(t, int64(1), info.RowsAffected)

		var got auth.UserCredential
		require.NoError(t, db.Where("id = ?", userID1).First(&got).Error)
		assert.Equal(t, "token-mobile-123", got.RefreshTokenIDMobile)
	})

	t.Run("用户不存在时更新 0 行", func(t *testing.T) {
		info, err := repo.SetRefreshTokenIDMobile(ctx, 999999, "token-mobile-456")
		require.NoError(t, err)
		assert.Equal(t, int64(0), info.RowsAffected)
	})
}

// ---------- GetCredentialByEmail ----------

func TestGetCredentialByEmail(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	seedCredential(t, db, userID1, "getbyemail@example.com", "hash", authv1.Role_ROLE_USER)

	t.Run("邮箱存在时返回记录", func(t *testing.T) {
		cred, err := repo.GetCredentialByEmail(ctx, "getbyemail@example.com")
		require.NoError(t, err)
		require.NotNil(t, cred)
		assert.Equal(t, userID1, cred.ID)
		assert.Equal(t, "getbyemail@example.com", cred.Email)
	})

	t.Run("邮箱不存在时返回 nil", func(t *testing.T) {
		cred, err := repo.GetCredentialByEmail(ctx, "nonexistent@example.com")
		require.NoError(t, err)
		assert.Nil(t, cred)
	})
}

// ---------- GetCredentialByUID ----------

func TestGetCredentialByUID(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	seedCredential(t, db, userID1, "getbyuid@example.com", "hash", authv1.Role_ROLE_USER)

	t.Run("用户 ID 存在时返回记录", func(t *testing.T) {
		cred, err := repo.GetCredentialByUID(ctx, userID1)
		require.NoError(t, err)
		require.NotNil(t, cred)
		assert.Equal(t, userID1, cred.ID)
		assert.Equal(t, "getbyuid@example.com", cred.Email)
	})

	t.Run("用户 ID 不存在时返回 nil", func(t *testing.T) {
		cred, err := repo.GetCredentialByUID(ctx, 999999)
		require.NoError(t, err)
		assert.Nil(t, cred)
	})
}

// ---------- 事务 ----------

func TestWithTX(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	t.Run("提交后数据可见", func(t *testing.T) {
		err := db.Transaction(func(tx *gorm.DB) error {
			return repo.WithTX(tx).CreateCredential(ctx, &auth.UserCredential{
				Email:        "tx-commit@example.com",
				PasswordHash: "hash",
				Role:         authv1.Role_ROLE_USER,
			})
		})
		require.NoError(t, err)

		var count int64
		require.NoError(t, db.Model(&auth.UserCredential{}).Where("email = ?", "tx-commit@example.com").Count(&count).Error)
		assert.Equal(t, int64(1), count)
	})

	t.Run("回滚后数据不可见", func(t *testing.T) {
		sentinel := errors.New("force rollback")
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := repo.WithTX(tx).CreateCredential(ctx, &auth.UserCredential{
				Email:        "tx-rollback@example.com",
				PasswordHash: "hash",
				Role:         authv1.Role_ROLE_USER,
			}); err != nil {
				return err
			}
			return sentinel
		})
		require.ErrorIs(t, err, sentinel)

		var count int64
		require.NoError(t, db.Model(&auth.UserCredential{}).Where("email = ?", "tx-rollback@example.com").Count(&count).Error)
		assert.Equal(t, int64(0), count, "回滚后不应存在记录")
	})
}
