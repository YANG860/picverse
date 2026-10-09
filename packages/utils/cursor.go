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
