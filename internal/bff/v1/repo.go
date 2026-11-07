package v1

import (
	"rednote/gen/go/auth/v1/authv1connect"
	discoverv1connect "rednote/gen/go/discover/v1/discoverv1connect"
	"rednote/gen/go/media/v1/mediav1connect"
	"rednote/gen/go/post/v1/postv1connect"
	"rednote/gen/go/user/v1/userv1connect"
)

// DownstreamRepo 下游服务客户端集合。
// BFF 不直连数据库，"存储层"即下游 auth/user/media/post/discover 的 RPC 调用，
// 聚合编排所需的所有数据获取都经由本仓库。
type DownstreamRepo struct {
	Auth     authv1connect.AuthServiceClient
	User     userv1connect.UserServiceClient
	Media    mediav1connect.MediaServiceClient
	Post     postv1connect.PostServiceClient
	Discover discoverv1connect.DiscoverServiceClient
}

// NewDownstreamRepo 创建下游客户端集合
func NewDownstreamRepo(
	auth authv1connect.AuthServiceClient,
	user userv1connect.UserServiceClient,
	media mediav1connect.MediaServiceClient,
	post postv1connect.PostServiceClient,
	discover discoverv1connect.DiscoverServiceClient,
) *DownstreamRepo {
	return &DownstreamRepo{
		Auth:     auth,
		User:     user,
		Media:    media,
		Post:     post,
		Discover: discover,
	}
}
