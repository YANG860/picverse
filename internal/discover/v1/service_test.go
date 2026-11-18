package v1

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMapErr 错误映射回归（契约见 doc/proto/proto.md）：
// 底层错误 → 连接层 Unavailable；ErrNotImplemented → Unimplemented；connect.Error 原样透传
func TestMapErr(t *testing.T) {
	t.Run("底层依赖故障 → Unavailable", func(t *testing.T) {
		mapped := mapErr(errors.New("redis down"))
		var ce *connect.Error
		require.True(t, errors.As(mapped, &ce))
		assert.Equal(t, connect.CodeUnavailable, ce.Code())
		assert.ErrorContains(t, mapped, "redis down")
	})

	t.Run("占位错误 → Unimplemented", func(t *testing.T) {
		mapped := mapErr(ErrNotImplemented)
		var ce *connect.Error
		require.True(t, errors.As(mapped, &ce))
		assert.Equal(t, connect.CodeUnimplemented, ce.Code())
	})

	t.Run("connect.Error 原样透传", func(t *testing.T) {
		orig := connect.NewError(connect.CodeInternal, errors.New("already typed"))
		assert.Equal(t, orig, mapErr(orig))
	})
}
