package v1

import (
	"context"
	"errors"
	"testing"

	"rednote/test/testdb"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestExecuteIdempotent 幂等执行：去重登记与业务副作用同事务，重复投递不重复执行副作用
func TestExecuteIdempotent(t *testing.T) {
	db := testdb.Setup(t)
	repo := NewProcessedMessageRepo(db)
	ctx := context.Background()

	t.Run("首次执行副作用并登记去重", func(t *testing.T) {
		var ran int
		applied, err := repo.ExecuteIdempotent(ctx, 9001, "test_handler", func(tx *gorm.DB) error {
			ran++
			return nil
		})
		require.NoError(t, err)
		assert.True(t, applied)
		assert.Equal(t, 1, ran)

		var cnt int64
		require.NoError(t, db.Table("processed_messages").Where("message_id = ?", 9001).Count(&cnt).Error)
		assert.Equal(t, int64(1), cnt, "去重记录应落库")
	})

	t.Run("重复投递跳过副作用（关键回归）", func(t *testing.T) {
		var ran int
		applied, err := repo.ExecuteIdempotent(ctx, 9001, "test_handler", func(tx *gorm.DB) error {
			ran++
			return nil
		})
		require.NoError(t, err)
		assert.False(t, applied, "重复消息应识别为已处理")
		assert.Zero(t, ran, "重复投递不得再次执行副作用")
	})

	t.Run("不同消息各执行一次", func(t *testing.T) {
		var ran int
		applied, err := repo.ExecuteIdempotent(ctx, 9002, "test_handler", func(tx *gorm.DB) error {
			ran++
			return nil
		})
		require.NoError(t, err)
		assert.True(t, applied)
		assert.Equal(t, 1, ran)
	})

	t.Run("副作用失败整体回滚（原子性）", func(t *testing.T) {
		_, err := repo.ExecuteIdempotent(ctx, 9003, "test_handler", func(tx *gorm.DB) error {
			return errors.New("side effect failed")
		})
		require.Error(t, err)

		var cnt int64
		require.NoError(t, db.Table("processed_messages").Where("message_id = ?", 9003).Count(&cnt).Error)
		assert.Equal(t, int64(0), cnt, "失败事务的去重记录应回滚，否则重投会被误判重复")
	})

	t.Run("回滚后重投可正常执行（自愈）", func(t *testing.T) {
		var ran int
		applied, err := repo.ExecuteIdempotent(ctx, 9003, "test_handler", func(tx *gorm.DB) error {
			ran++
			return nil
		})
		require.NoError(t, err)
		assert.True(t, applied, "失败回滚后同一消息应可重试")
		assert.Equal(t, 1, ran)
	})
}
