package errmsg

import (
	"errors"
	commonv1 "rednote/gen/go/common/v1"
)

// 可枚举的业务错误
var (
	InvalidID     = New(1001, errors.New("invalid ID"))
	NotFound      = New(1002, errors.New("resource not found"))
	InvalidFormat = New(1003, errors.New("invalid file format"))
	InternalError = New(1004, errors.New("internal error"))
)

func New(code int64, err error) *commonv1.Error {
	return &commonv1.Error{
		Code: code,
		Msg:  err.Error(),
	}
}
