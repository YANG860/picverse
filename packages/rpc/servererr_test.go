package rpc

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMapServerError 错误映射回归（契约见 doc/proto/proto.md）：
// 底层错误 → Unavailable；connect.Error 原样透传不重复包装
func TestMapServerError(t *testing.T) {
	t.Run("底层依赖故障 → Unavailable", func(t *testing.T) {
		mapped := MapServerError(errors.New("db down"))
		var ce *connect.Error
		require.True(t, errors.As(mapped, &ce))
		assert.Equal(t, connect.CodeUnavailable, ce.Code())
		assert.ErrorContains(t, mapped, "db down")
	})

	t.Run("connect.Error 原样透传", func(t *testing.T) {
		orig := connect.NewError(connect.CodeInternal, errors.New("invariant"))
		assert.Equal(t, orig, MapServerError(orig))
	})

	t.Run("wrap 过的底层错误同样映射为 Unavailable", func(t *testing.T) {
		mapped := MapServerError(errors.Join(errors.New("ctx"), errors.New("redis down")))
		var ce *connect.Error
		require.True(t, errors.As(mapped, &ce))
		assert.Equal(t, connect.CodeUnavailable, ce.Code())
	})
}
