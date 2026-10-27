package v1

import (
	"context"
	"log/slog"

	v1 "rednote/gen/go/auth/v1"
	"rednote/gen/go/auth/v1/authv1connect"
	"rednote/packages/logger"

	connect "connectrpc.com/connect"
)

type AuthServiceHandler struct {
	Biz AuthUsecase
	log *slog.Logger
}

var _ authv1connect.AuthServiceHandler = (*AuthServiceHandler)(nil)

func NewAuthServiceHandler(biz AuthUsecase, lf logger.LoggerFactory) *AuthServiceHandler {
	return &AuthServiceHandler{
		Biz: biz,
		log: lf.GetLoggerOf("auth"),
	}
}

// Register 注册
func (s *AuthServiceHandler) Register(
	ctx context.Context,
	req *connect.Request[v1.RegisterRequest],
) (*connect.Response[v1.RegisterResponse], error) {
	resp, err := s.Biz.Register(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// Login 登录
func (s *AuthServiceHandler) Login(
	ctx context.Context,
	req *connect.Request[v1.LoginRequest],
) (*connect.Response[v1.LoginResponse], error) {
	resp, err := s.Biz.Login(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// GetRefreshToken 获取新的refresh token
func (s *AuthServiceHandler) GetRefreshToken(
	ctx context.Context,
	req *connect.Request[v1.GetRefreshTokenRequest],
) (*connect.Response[v1.GetRefreshTokenResponse], error) {
	resp, err := s.Biz.GetRefreshToken(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// GetAccessToken 获取新的access token
func (s *AuthServiceHandler) GetAccessToken(
	ctx context.Context,
	req *connect.Request[v1.GetAccessTokenRequest],
) (*connect.Response[v1.GetAccessTokenResponse], error) {
	resp, err := s.Biz.GetAccessToken(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (s *AuthServiceHandler) Logout(
	ctx context.Context,
	req *connect.Request[v1.LogoutRequest],
) (*connect.Response[v1.LogoutResponse], error) {
	resp, err := s.Biz.Logout(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// DeleteAccount 删除账户（内部接口：BFF 注册编排回滚用）
func (s *AuthServiceHandler) DeleteAccount(
	ctx context.Context,
	req *connect.Request[v1.DeleteAccountRequest],
) (*connect.Response[v1.DeleteAccountResponse], error) {
	resp, err := s.Biz.DeleteAccount(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
