package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnmarshalJSON(t *testing.T) {
	t.Run("nil 输入返回空对象且不报错", func(t *testing.T) {
		// Act
		m, err := UnmarshalJSON(nil)

		// Assert
		require.NoError(t, err)
		assert.NotNil(t, m)
		assert.Empty(t, m)
	})

	t.Run("空输入返回空对象且不报错", func(t *testing.T) {
		m, err := UnmarshalJSON([]byte{})

		require.NoError(t, err)
		assert.Empty(t, m)
	})

	t.Run("空对象返回空 map", func(t *testing.T) {
		m, err := UnmarshalJSON([]byte("{}"))

		require.NoError(t, err)
		assert.Empty(t, m)
	})

	t.Run("合法对象完整往返", func(t *testing.T) {
		m, err := UnmarshalJSON([]byte(`{"theme":"dark","count":2,"nested":{"a":true}}`))

		require.NoError(t, err)
		assert.Equal(t, "dark", m["theme"])
		assert.Equal(t, float64(2), m["count"])
		assert.Equal(t, map[string]any{"a": true}, m["nested"])
	})

	t.Run("null 解析为空对象且不报错", func(t *testing.T) {
		m, err := UnmarshalJSON([]byte("null"))

		require.NoError(t, err)
		assert.Empty(t, m)
	})

	t.Run("非法 JSON 返回错误", func(t *testing.T) {
		m, err := UnmarshalJSON([]byte("not-json"))

		require.Error(t, err)
		assert.Empty(t, m)
	})

	t.Run("数组不是对象返回错误", func(t *testing.T) {
		_, err := UnmarshalJSON([]byte("[1,2]"))

		require.Error(t, err)
	})
}
