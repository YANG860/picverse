package errmsg

import (
	"errors"
	commonv1 "rednote/gen/go/common/v1"
)

// 可枚举的业务错误

// common
var (
	InternalError   = New(1000, errors.New("internal error"))
	InvalidID       = New(1001, errors.New("invalid ID"))
	NotFound        = New(1002, errors.New("resource not found"))
	Unauthorized    = New(1003, errors.New("unauthorized"))
	InvalidArgument = New(1004, errors.New("invalid argument"))
	Forbidden       = New(1005, errors.New("forbidden"))
	AlreadyExist    = New(1006, errors.New("resource already exists"))
)

// auth
var (
	InvalidEmail        = New(1101, errors.New("invalid email"))
	InvalidPassword     = New(1102, errors.New("invalid password"))
	IncorrectCredential = New(1103, errors.New("incorrect credential"))
	DuplicateEmail      = New(1104, errors.New("email already exists"))
)

// media
var (
	InvalidFormat = New(1201, errors.New("invalid file format"))
)

// post
var (
	InvalidTitle   = New(1301, errors.New("invalid title"))
	InvalidContent = New(1302, errors.New("invalid content"))
	InvalidMedia   = New(1303, errors.New("invalid media"))
	NotEditable    = New(1304, errors.New("not editable"))
	Banned         = New(1305, errors.New("post banned"))
)

// user
var (
	InvalidName   = New(1401, errors.New("invalid name"))
	InvalidBio    = New(1402, errors.New("invalid bio"))
	InvalidAvatar = New(1403, errors.New("invalid avatar"))
)

func New(code int64, err error) *commonv1.Error {
	return &commonv1.Error{
		Code: code,
		Msg:  err.Error(),
	}
}
