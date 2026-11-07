package utils

import (
	commonv1 "rednote/gen/go/common/v1"
	"rednote/packages/defaults"
	"strconv"
	"time"
)

func ParseTimeCursor(cursor *commonv1.Cursor) (int64, int64) {

	stamp, err := strconv.ParseInt(cursor.GetStr(), 10, 64)
	if err != nil {
		stamp = time.Now().UnixMilli()
	}

	limit := cursor.GetLimit()
	if limit <= 0 || limit > defaults.MaxLimit {
		limit = defaults.CursorLimit
	}

	return stamp, limit
}

func NewTimeCursor(stamp int64, limit int64) *commonv1.Cursor {
	if limit <= 0 || limit > defaults.MaxLimit {
		limit = defaults.CursorLimit
	}

	return &commonv1.Cursor{
		Str:   strconv.FormatInt(stamp, 10),
		Limit: limit,
	}
}

// ParseOffsetCursor 解析 offset 游标（搜索/推荐流用）：str 编码偏移量，非法/负数回退 0（首页）；
// limit 越界回退默认值，口径同 ParseTimeCursor
func ParseOffsetCursor(cursor *commonv1.Cursor) (int64, int64) {
	offset, err := strconv.ParseInt(cursor.GetStr(), 10, 64)
	if err != nil || offset < 0 {
		offset = 0
	}

	limit := cursor.GetLimit()
	if limit <= 0 || limit > defaults.MaxLimit {
		limit = defaults.CursorLimit
	}

	return offset, limit
}

// NewOffsetCursor 构造 offset 游标（nextOffset 为下一页偏移量）
func NewOffsetCursor(nextOffset int64, limit int64) *commonv1.Cursor {
	if limit <= 0 || limit > defaults.MaxLimit {
		limit = defaults.CursorLimit
	}
	if nextOffset < 0 {
		nextOffset = 0
	}

	return &commonv1.Cursor{
		Str:   strconv.FormatInt(nextOffset, 10),
		Limit: limit,
	}
}
