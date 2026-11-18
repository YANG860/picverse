package v1

import (
	"context"
	"log/slog"
	"testing"

	bffv1 "rednote/gen/go/bff/v1"
	"rednote/packages/errmsg"
	"rednote/packages/ratelimit"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeUsecase 记录调用的 BFFUsecase 桩（嵌入接口仅覆盖用到的方法）
type fakeUsecase struct {
	BFFUsecase
	registerCalled bool
}

func (f *fakeUsecase) Register(_ context.Context, _ *bffv1.RegisterRequest) (*bffv1.RegisterResponse, error) {
	f.registerCalled = true
	return &bffv1.RegisterResponse{}, nil
}

// TestHandler_AdmitReject 入口限流溢出：返回 fail+InternalError 业务码
// （不产生连接层 error，BFF 例外规则），且不进业务层
func TestHandler_AdmitReject(t *testing.T) {
	// 预置耗尽全局桶（burst 1）：测试 request 无 Peer 信息，走空 IP 键只受全局桶约束
	limiter := ratelimit.New(1, 1, 0, 0)
	require.True(t, limiter.Allow(""), "预置：消耗突发桶唯一令牌")
	require.False(t, limiter.Allow(""), "预置：全局桶已耗尽")

	biz := &fakeUsecase{}
	s := &BFFServiceHandler{Biz: biz, Limiter: limiter, log: slog.Default()}
	resp, err := s.Register(context.Background(), connect.NewRequest(&bffv1.RegisterRequest{}))
	require.NoError(t, err, "限流溢出不返回连接层 error")
	assert.True(t, resp.Msg.GetFail(), "面客业务 fail 形态")
	assert.Equal(t, errmsg.InternalError.Code, resp.Msg.GetErr().GetCode(), "面客维持 InternalError（不引入独立限流码）")
	assert.False(t, biz.registerCalled, "限流拒绝不进业务层")
}

// TestHandler_AdmitPass 限流放行：正常进业务层
func TestHandler_AdmitPass(t *testing.T) {
	biz := &fakeUsecase{}
	s := &BFFServiceHandler{Biz: biz, Limiter: ratelimit.New(100, 100, 0, 0), log: slog.Default()}

	resp, err := s.Register(context.Background(), connect.NewRequest(&bffv1.RegisterRequest{}))
	require.NoError(t, err)
	assert.False(t, resp.Msg.GetFail())
	assert.True(t, biz.registerCalled)
}
