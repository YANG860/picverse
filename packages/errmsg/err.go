package errmsg

import (
	"errors"
	commonv1 "rednote/gen/go/common/v1"
)

var (
	InvalidID = errors.New("invalid ID")
	NotFound  = errors.New("resource not found")
)

func Of(code int64, err error) *commonv1.Error {
	return &commonv1.Error{
		Code: code,
		Msg:  err.Error(),
	}
}
