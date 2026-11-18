package rpc

import (
	"errors"

	"connectrpc.com/connect"
)

// MapServerError 服务端 handler 错误出口统一映射（错误契约见 doc/proto/proto.md）：
//   - 已是 connect.Error 的原样透传——biz 可显式指定精确码表达语义
//     （如 CodeInternal 表达程序不变量问题、CodeUnimplemented 表达占位）；
//   - 其余裸 error（DB/Redis/S3 等依赖故障）→ CodeUnavailable：瞬时故障语义，
//     调用方（BFF / consumer / 重试熔断治理）按连接层码分类处理，
//     不再混入业务 fail/err 响应体。
//
// 各服务本地 mapErr 先处理服务专属哨兵错误（如 ErrNotImplemented → CodeUnimplemented），
// 再委托本函数兜底。
func MapServerError(err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return err
	}
	return connect.NewError(connect.CodeUnavailable, err)
}
