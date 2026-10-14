package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInitSnowflake 机器 ID 边界：越界必须报错且不得破坏已有节点（回归：失败时不可覆盖全局 Node）
func TestInitSnowflake(t *testing.T) {
	// Arrange：保存当前全局节点，测试结束后恢复，避免污染同包其他测试
	saved := Node
	t.Cleanup(func() { Node = saved })

	t.Run("负数机器ID报错且保留原节点", func(t *testing.T) {
		// Arrange
		require.NoError(t, InitSnowflake(7))
		working := Node

		// Act
		err := InitSnowflake(-1)

		// Assert
		require.Error(t, err)
		assert.Same(t, working, Node, "初始化失败不应破坏已有节点")
	})

	t.Run("超上限机器ID报错且保留原节点", func(t *testing.T) {
		require.NoError(t, InitSnowflake(7))
		working := Node

		err := InitSnowflake(1024)

		require.Error(t, err)
		assert.Same(t, working, Node, "初始化失败不应破坏已有节点")
	})

	t.Run("边界机器ID 0 与 1023 均可初始化", func(t *testing.T) {
		require.NoError(t, InitSnowflake(0))
		require.NoError(t, InitSnowflake(1023))
	})
}

// TestGenID 生成的雪花 ID 必须非零且互不重复
func TestGenID(t *testing.T) {
	saved := Node
	t.Cleanup(func() { Node = saved })
	require.NoError(t, InitSnowflake(9))

	seen := make(map[int64]bool, 1000)
	for range 1000 {
		id := GenID().Int64()
		assert.NotZero(t, id)
		assert.False(t, seen[id], "雪花 ID 不应重复: %d", id)
		seen[id] = true
	}
}
