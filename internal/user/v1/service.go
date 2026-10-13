package v1

import (
	"context"
	"log/slog"

	userv1 "rednote/gen/go/user/v1"
	"rednote/gen/go/user/v1/userv1connect"
	"rednote/packages/logger"

	"connectrpc.com/connect"
	"gorm.io/gorm"
)

// UserServiceHandler 实现 userv1connect.UserServiceHandler 接口
type UserServiceHandler struct {
	DB   *gorm.DB
	Biz  UserUsecase
	Repo UserRepository
	log  *slog.Logger
}

var _ userv1connect.UserServiceHandler = &UserServiceHandler{}

// NewUserServiceHandler 创建 UserServiceHandler
func NewUserServiceHandler(db *gorm.DB, lf logger.LoggerFactory, useCase UserUsecase, repo UserRepository) *UserServiceHandler {
	return &UserServiceHandler{
		DB:   db,
		log:  lf.GetLoggerOf("user"),
		Biz:  useCase,
		Repo: repo,
	}
}

// --- ConnectRPC 方法实现 ---

// CreateUser 创建用户资料
func (s *UserServiceHandler) CreateUser(ctx context.Context, req *connect.Request[userv1.CreateUserRequest]) (*connect.Response[userv1.CreateUserResponse], error) {
	resp, err := s.Biz.CreateUser(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// UpdateUserProfile 更新用户资料
func (s *UserServiceHandler) UpdateUserProfile(ctx context.Context, req *connect.Request[userv1.UpdateUserProfileRequest]) (*connect.Response[userv1.UpdateUserProfileResponse], error) {
	resp, err := s.Biz.UpdateUserProfile(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// UpdateUserAvatar 更新用户头像
func (s *UserServiceHandler) UpdateUserAvatar(ctx context.Context, req *connect.Request[userv1.UpdateUserAvatarRequest]) (*connect.Response[userv1.UpdateUserAvatarResponse], error) {
	resp, err := s.Biz.UpdateUserAvatar(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// GetUserBrief 获取用户简要信息
func (s *UserServiceHandler) GetUserBrief(ctx context.Context, req *connect.Request[userv1.GetUserBriefRequest]) (*connect.Response[userv1.GetUserBriefResponse], error) {
	resp, err := s.Biz.GetUserBrief(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// GetUserProfile 获取用户资料
func (s *UserServiceHandler) GetUserProfile(ctx context.Context, req *connect.Request[userv1.GetUserProfileRequest]) (*connect.Response[userv1.GetUserProfileResponse], error) {
	resp, err := s.Biz.GetUserProfile(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

// GetUserStatus 获取用户状态
func (s *UserServiceHandler) GetUserStatus(ctx context.Context, req *connect.Request[userv1.GetUserStatusRequest]) (*connect.Response[userv1.GetUserStatusResponse], error) {
	resp, err := s.Biz.GetUserStatus(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

func (s *UserServiceHandler) GetFollowings(ctx context.Context, req *connect.Request[userv1.GetFollowingsRequest]) (*connect.Response[userv1.GetFollowingsResponse], error) {
	resp, err := s.Biz.GetFollowings(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

func (s *UserServiceHandler) GetFollowingRelation(ctx context.Context, req *connect.Request[userv1.GetFollowingRelationRequest]) (*connect.Response[userv1.GetFollowingRelationResponse], error) {
	resp, err := s.Biz.GetFollowingRelation(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

func (s *UserServiceHandler) BanUser(ctx context.Context, req *connect.Request[userv1.BanUserRequest]) (*connect.Response[userv1.BanUserResponse], error) {
	resp, err := s.Biz.BanUser(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

func (s *UserServiceHandler) SetFollow(ctx context.Context, req *connect.Request[userv1.SetFollowRequest]) (*connect.Response[userv1.SetFollowResponse], error) {
	resp, err := s.Biz.SetFollow(ctx, req.Msg)
	return connect.NewResponse(resp), err
}

func (s *UserServiceHandler) SetUnFollow(ctx context.Context, req *connect.Request[userv1.SetUnFollowRequest]) (*connect.Response[userv1.SetUnFollowResponse], error) {
	resp, err := s.Biz.SetUnFollow(ctx, req.Msg)
	return connect.NewResponse(resp), err
}
