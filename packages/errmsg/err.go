package errmsg

import (
	"errors"
	commonv1 "rednote/gen/go/common/v1"
)

// 可枚举的业务错误
var (
	InternalError   = New(1000, errors.New("internal error"))
	InvalidID       = New(1001, errors.New("invalid ID"))
	NotFound        = New(1002, errors.New("resource not found"))
	Unauthorized    = New(1003, errors.New("unauthorized"))
	InvalidArgument = New(1004, errors.New("invalid argument"))
)

var (
	InvalidEmail        = New(1101, errors.New("invalid email"))
	InvalidPassword     = New(1102, errors.New("invalid password"))
	IncorrectCredential = New(1103, errors.New("incorrect credential"))
	DuplicateEmail      = New(1104, errors.New("email already exists"))
)

var (
	InvalidFormat = New(1201, errors.New("invalid file format"))
)

func New(code int64, err error) *commonv1.Error {
	return &commonv1.Error{
		Code: code,
		Msg:  err.Error(),
	}
}
