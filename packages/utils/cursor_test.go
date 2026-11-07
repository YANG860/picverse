package utils

import (
	"testing"
	"time"

	commonv1 "rednote/gen/go/common/v1"
	"rednote/packages/defaults"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseTimeCursor 游标解析：非法串回退到当前时间，limit 越界回退到默认值
func TestParseTimeCursor(t *testing.T) {
	t.Run("nil 游标回退到当前时间与默认 limit", func(t *testing.T) {
		// Arrange
		before := time.Now().UnixMilli()

		// Act
		stamp, limit := ParseTimeCursor(nil)

		// Assert
		after := time.Now().UnixMilli()
		assert.GreaterOrEqual(t, stamp, before)
		assert.LessOrEqual(t, stamp, after)
		assert.Equal(t, int64(defaults.CursorLimit), limit)
	})

	t.Run("合法时间串被解析", func(t *testing.T) {
		stamp, limit := ParseTimeCursor(&commonv1.Cursor{Str: "1700000000123", Limit: 10})

		assert.Equal(t, int64(1700000000123), stamp)
		assert.Equal(t, int64(10), limit)
	})

	t.Run("非法时间串回退到当前时间", func(t *testing.T) {
		before := time.Now().UnixMilli()

		stamp, _ := ParseTimeCursor(&commonv1.Cursor{Str: "not-a-number", Limit: 10})

		after := time.Now().UnixMilli()
		assert.GreaterOrEqual(t, stamp, before)
		assert.LessOrEqual(t, stamp, after)
	})

	t.Run("limit 为 0 回退默认值", func(t *testing.T) {
		_, limit := ParseTimeCursor(&commonv1.Cursor{Str: "1", Limit: 0})
		assert.Equal(t, int64(defaults.CursorLimit), limit)
	})

	t.Run("limit 为负回退默认值", func(t *testing.T) {
		_, limit := ParseTimeCursor(&commonv1.Cursor{Str: "1", Limit: -5})
		assert.Equal(t, int64(defaults.CursorLimit), limit)
	})

	t.Run("limit 超上限回退默认值", func(t *testing.T) {
		_, limit := ParseTimeCursor(&commonv1.Cursor{Str: "1", Limit: defaults.MaxLimit + 1})
		assert.Equal(t, int64(defaults.CursorLimit), limit)
	})

	t.Run("limit 等于上限被保留（边界）", func(t *testing.T) {
		_, limit := ParseTimeCursor(&commonv1.Cursor{Str: "1", Limit: defaults.MaxLimit})
		assert.Equal(t, int64(defaults.MaxLimit), limit)
	})

	t.Run("limit 为 1 被保留（边界）", func(t *testing.T) {
		_, limit := ParseTimeCursor(&commonv1.Cursor{Str: "1", Limit: 1})
		assert.Equal(t, int64(1), limit)
	})
}

// TestNewTimeCursor 游标构造：limit 规则与 ParseTimeCursor 一致，串格式可被解析回去
func TestNewTimeCursor(t *testing.T) {
	t.Run("合法 limit 原样保留且串可往返", func(t *testing.T) {
		// Act
		cursor := NewTimeCursor(1700000000123, 10)

		// Assert
		require.NotNil(t, cursor)
		assert.Equal(t, "1700000000123", cursor.GetStr())
		assert.Equal(t, int64(10), cursor.GetLimit())

		stamp, limit := ParseTimeCursor(cursor)
		assert.Equal(t, int64(1700000000123), stamp, "构造出的游标应能被解析回原值")
		assert.Equal(t, int64(10), limit)
	})

	t.Run("limit 为 0 回退默认值", func(t *testing.T) {
		cursor := NewTimeCursor(1, 0)
		assert.Equal(t, int64(defaults.CursorLimit), cursor.GetLimit())
	})

	t.Run("limit 为负回退默认值", func(t *testing.T) {
		cursor := NewTimeCursor(1, -1)
		assert.Equal(t, int64(defaults.CursorLimit), cursor.GetLimit())
	})

	t.Run("limit 超上限回退默认值", func(t *testing.T) {
		cursor := NewTimeCursor(1, defaults.MaxLimit+1)
		assert.Equal(t, int64(defaults.CursorLimit), cursor.GetLimit())
	})

	t.Run("limit 等于上限被保留（边界）", func(t *testing.T) {
		cursor := NewTimeCursor(1, defaults.MaxLimit)
		assert.Equal(t, int64(defaults.MaxLimit), cursor.GetLimit())
	})
}

// TestParseOffsetCursor offset 游标解析：非法/负数回退 0，limit 越界回退默认值
func TestParseOffsetCursor(t *testing.T) {
	t.Run("nil 游标回退到 0 与默认 limit", func(t *testing.T) {
		offset, limit := ParseOffsetCursor(nil)
		assert.Equal(t, int64(0), offset)
		assert.Equal(t, int64(defaults.CursorLimit), limit)
	})

	t.Run("合法偏移被解析", func(t *testing.T) {
		offset, limit := ParseOffsetCursor(&commonv1.Cursor{Str: "40", Limit: 10})
		assert.Equal(t, int64(40), offset)
		assert.Equal(t, int64(10), limit)
	})

	t.Run("非法串/负数回退到 0", func(t *testing.T) {
		offset, _ := ParseOffsetCursor(&commonv1.Cursor{Str: "not-a-number"})
		assert.Equal(t, int64(0), offset)

		offset, _ = ParseOffsetCursor(&commonv1.Cursor{Str: "-5"})
		assert.Equal(t, int64(0), offset)
	})
}

// TestNewOffsetCursor offset 游标构造：往返一致 + 负偏移/limit 越界回退
func TestNewOffsetCursor(t *testing.T) {
	t.Run("构造与解析往返一致", func(t *testing.T) {
		cursor := NewOffsetCursor(40, 10)
		offset, limit := ParseOffsetCursor(cursor)
		assert.Equal(t, int64(40), offset)
		assert.Equal(t, int64(10), limit)
	})

	t.Run("负偏移回退 0", func(t *testing.T) {
		cursor := NewOffsetCursor(-1, 10)
		assert.Equal(t, "0", cursor.GetStr())
	})

	t.Run("limit 越界回退默认值", func(t *testing.T) {
		cursor := NewOffsetCursor(0, 0)
		assert.Equal(t, int64(defaults.CursorLimit), cursor.GetLimit())
	})
}
