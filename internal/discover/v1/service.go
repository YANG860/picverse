package v1

import (
	"context"
	"errors"
	"log/slog"

	discoverv1 "rednote/gen/go/discover/v1"
	"rednote/gen/go/discover/v1/discoverv1connect"
	"rednote/packages/logger"

	"connectrpc.com/connect"
)

// DiscoverServiceHandler 实现 discoverv1connect.DiscoverServiceHandler 接口
type DiscoverServiceHandler struct {
	Biz DiscoverUsecase
	log *slog.Logger
}

var _ discoverv1connect.DiscoverServiceHandler = &DiscoverServiceHandler{}

// NewDiscoverServiceHandler 创建 DiscoverServiceHandler
func NewDiscoverServiceHandler(lf logger.LoggerFactory, useCase DiscoverUsecase) *DiscoverServiceHandler {
	return &DiscoverServiceHandler{
		Biz: useCase,
		log: lf.GetLoggerOf("discover"),
	}
}

// mapErr 占位错误映射为连接层 Unimplemented（不可枚举走连接层 gRPC error，见 doc/proto/proto.md）；
// 其余错误原样透传
func mapErr(err error) error {
	if errors.Is(err, ErrNotImplemented) {
		return connect.NewError(connect.CodeUnimplemented, err)
	}
	return err
}

// --- ConnectRPC 方法实现 ---

// GetFollowingFeed 关注流取数
func (s *DiscoverServiceHandler) GetFollowingFeed(ctx context.Context, req *connect.Request[discoverv1.GetFollowingFeedRequest]) (*connect.Response[discoverv1.GetFollowingFeedResponse], error) {
	resp, err := s.Biz.GetFollowingFeed(ctx, req.Msg)
	if err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(resp), nil
}

// GetRecommendFeed 推荐流取数
func (s *DiscoverServiceHandler) GetRecommendFeed(ctx context.Context, req *connect.Request[discoverv1.GetRecommendFeedRequest]) (*connect.Response[discoverv1.GetRecommendFeedResponse], error) {
	resp, err := s.Biz.GetRecommendFeed(ctx, req.Msg)
	if err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(resp), nil
}

// SearchPosts 帖子搜索
func (s *DiscoverServiceHandler) SearchPosts(ctx context.Context, req *connect.Request[discoverv1.SearchPostsRequest]) (*connect.Response[discoverv1.SearchPostsResponse], error) {
	resp, err := s.Biz.SearchPosts(ctx, req.Msg)
	if err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(resp), nil
}
