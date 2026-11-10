//go:build wireinject
// +build wireinject

package gen

import (
	"rednote/config"
	"rednote/gen/go/bff/v1/bffv1connect"
	"rednote/gen/go/media/v1/mediav1connect"

	authv1connect "rednote/gen/go/auth/v1/authv1connect"
	discoverv1connect "rednote/gen/go/discover/v1/discoverv1connect"
	"rednote/gen/go/post/v1/postv1connect"
	userv1connect "rednote/gen/go/user/v1/userv1connect"
	commonv1 "rednote/internal/common/v1"
	"rednote/internal/consumer"

	authv1 "rednote/internal/auth/v1"
	bffv1 "rednote/internal/bff/v1"
	discoverv1 "rednote/internal/discover/v1"
	mediav1 "rednote/internal/media/v1"
	"rednote/internal/poller"
	postv1 "rednote/internal/post/v1"
	userv1 "rednote/internal/user/v1"
	"rednote/packages/cache"
	"rednote/packages/db"
	"rednote/packages/jwt"
	"rednote/packages/logger"
	"rednote/packages/meili"
	"rednote/packages/mq"
	"rednote/packages/oss"
	"rednote/packages/rpc"

	"github.com/google/wire"
)

var Repos = wire.NewSet(
	mediav1.NewMediaRepo,
	wire.Bind(new(mediav1.MediaRepository), new(*mediav1.MediaRepoImpl)),

	authv1.NewAuthRepoImpl,
	wire.Bind(new(authv1.AuthRepository), new(*authv1.AuthRepoImpl)),

	postv1.NewPostRepo,
	postv1.NewCachedPostRepo,
	wire.Bind(new(postv1.PostRepository), new(*postv1.CachedPostRepo)),

	userv1.NewUserRepo,
	userv1.NewCachedUserRepo,
	wire.Bind(new(userv1.UserRepository), new(*userv1.CachedUserRepo)),

	commonv1.NewOutBoxRepo,
	wire.Bind(new(commonv1.OutBoxRepository), new(*commonv1.OutBoxRepoImpl)),

	commonv1.NewProcessedMessageRepo,
	wire.Bind(new(commonv1.ProcessedMessageRepository), new(*commonv1.ProcessedMessageRepoImpl)),
)

var Services = wire.NewSet(

	mediav1.NewMediaServiceHandler,
	wire.Bind(new(mediav1connect.MediaServiceHandler), new(*mediav1.MediaServiceHandler)),

	authv1.NewAuthServiceHandler,
	wire.Bind(new(authv1connect.AuthServiceHandler), new(*authv1.AuthServiceHandler)),

	postv1.NewPostServiceHandler,
	wire.Bind(new(postv1connect.PostServiceHandler), new(*postv1.PostServiceHandler)),

	userv1.NewUserServiceHandler,
	wire.Bind(new(userv1connect.UserServiceHandler), new(*userv1.UserServiceHandler)),
)

var Usecases = wire.NewSet(
	mediav1.NewMediaUsecase,
	wire.Bind(new(mediav1.MediaUsecase), new(*mediav1.MediaUsecaseImpl)),

	authv1.NewAuthUsecaseImpl,
	wire.Bind(new(authv1.AuthUsecase), new(*authv1.AuthUsecaseImpl)),

	postv1.NewPostUsecase,
	wire.Bind(new(postv1.PostUsecase), new(*postv1.PostUsecaseImpl)),

	userv1.NewUserUsecase,
	wire.Bind(new(userv1.UserUsecase), new(*userv1.UserUsecaseImpl)),
)

var Middlewares = wire.NewSet(
	cache.NewCache,
	cache.NewUserEntityCache,
	wire.Bind(new(cache.UserEntityCache), new(*cache.UserEntityCacheImpl)),
	cache.NewPostEntityCache,
	wire.Bind(new(cache.PostEntityCache), new(*cache.PostEntityCacheImpl)),
	cache.NewAuthorTimelineCache,
	wire.Bind(new(cache.AuthorTimelineCache), new(*cache.AuthorTimelineCacheImpl)),
	db.NewPG,
	mq.NewJetStreamClient,
	wire.Bind(new(mq.MQClient), new(*mq.JetStreamClient)),
	logger.NewLoggerFactory,
	wire.Bind(new(logger.LoggerFactory), new(*logger.LoggerFactoryImpl)),

	oss.NewClient,
	wire.Bind(new(oss.S3Repo), new(*oss.MinioClient)),

	meili.NewClient,
	wire.Bind(new(meili.MeiliRepo), new(*meili.MeiliClient)),
)

var Utils = wire.NewSet(
	jwt.NewManager,
	wire.Bind(new(jwt.Manager), new(*jwt.ManagerImpl)),
	jwt.NewServerInterceptor,
)

// Clients 下游服务客户端：服务发现（静态实现）+ 统一拦截器的 connect 客户端
var Clients = wire.NewSet(
	rpc.NewStaticProvider,
	wire.Bind(new(rpc.EndpointProvider), new(*rpc.StaticProvider)),

	rpc.NewAuthClient,
	rpc.NewUserClient,
	rpc.NewMediaClient,
	rpc.NewPostClient,
	rpc.NewDiscoverClient,

	bffv1.NewDownstreamRepo,
)

// BFF bff 服务装配
var BFF = wire.NewSet(
	bffv1.NewBFFUsecase,
	wire.Bind(new(bffv1.BFFUsecase), new(*bffv1.BFFUsecaseImpl)),

	bffv1.NewBFFServiceHandler,
	wire.Bind(new(bffv1connect.BFFServiceHandler), new(*bffv1.BFFServiceHandler)),
)

// Discover 搜推服务装配
var Discover = wire.NewSet(
	discoverv1.NewFanoutRepo,

	discoverv1.NewDiscoverUsecase,
	wire.Bind(new(discoverv1.DiscoverUsecase), new(*discoverv1.DiscoverUsecaseImpl)),

	discoverv1.NewDiscoverServiceHandler,
	wire.Bind(new(discoverv1connect.DiscoverServiceHandler), new(*discoverv1.DiscoverServiceHandler)),
)

func InitializeConsumer(cfg *config.Config) (*consumer.MediaConsumer, func()) {
	wire.Build(
		consumer.NewMediaConsumer,
		Repos,
		Middlewares,
	)
	return nil, func() {}
}

func InitializePostConsumer(cfg *config.Config) (*consumer.PostConsumer, func()) {
	wire.Build(
		consumer.NewPostConsumer,
		Clients,
		Repos,
		Middlewares,
	)
	return nil, func() {}
}

func InitializePoller(cfg *config.Config) (*poller.OutBoxPoller, func()) {
	wire.Build(
		poller.NewPoller,
		Middlewares,
		Repos,
	)
	return nil, func() {}
}

func InitializeDiscoverServiceHandler(cfg *config.Config) (discoverv1connect.DiscoverServiceHandler, func()) {
	wire.Build(
		Discover,
		Middlewares,
	)
	return nil, func() {}
}

func InitializeDiscoverIndexConsumer(cfg *config.Config) (*consumer.DiscoverIndexConsumer, func()) {
	wire.Build(
		consumer.NewDiscoverIndexConsumer,
		Clients,
		Repos,
		Middlewares,
	)
	return nil, func() {}
}

func InitializeFanoutConsumer(cfg *config.Config) (*consumer.FanoutConsumer, func()) {
	wire.Build(
		consumer.NewFanoutConsumer,
		Discover,
		Clients,
		Repos,
		Middlewares,
	)
	return nil, func() {}
}

func InitializeMediaServiceHandler(cfg *config.Config) (mediav1connect.MediaServiceHandler, func()) {
	wire.Build(
		Services,
		Middlewares,
		Repos,
		Usecases,
	)
	return nil, func() {}
}

func InitializeAuthServiceHandler(cfg *config.Config) (authv1connect.AuthServiceHandler, func()) {
	wire.Build(
		Services,
		Middlewares,
		Repos,
		Usecases,
		Utils,
	)
	return nil, func() {}
}

// InitializeRedisPoller 浏览计数刷回器装配（cmd/poller 内常驻：Redis 增量 → posts.view_count）
func InitializeRedisPoller(cfg *config.Config) (*poller.RedisPoller, func()) {
	wire.Build(
		poller.NewRedisPoller,
		Middlewares,
		Repos,
	)
	return nil, func() {}
}

// InitializeFanoutCleaner 收件箱保留期清理装配（cmd/poller 内常驻：维护 fanout_timelines 表大小）
func InitializeFanoutCleaner(cfg *config.Config) (*poller.FanoutCleaner, func()) {
	wire.Build(
		poller.NewFanoutCleaner,
		discoverv1.NewFanoutRepo,
		Middlewares,
	)
	return nil, func() {}
}

func InitializePostServiceHandler(cfg *config.Config) (postv1connect.PostServiceHandler, func()) {
	wire.Build(
		Services,
		Middlewares,
		Repos,
		Usecases,
	)
	return nil, func() {}
}

func InitializeBFFServiceHandler(cfg *config.Config) (bffv1connect.BFFServiceHandler, func()) {
	wire.Build(
		BFF,
		Clients,
		Middlewares,
	)
	return nil, func() {}
}

func InitializeUserServiceHandler(cfg *config.Config) (userv1connect.UserServiceHandler, func()) {
	wire.Build(
		Services,
		Middlewares,
		Repos,
		Usecases,
	)
	return nil, func() {}
}

func InitializeAuthInterceptor(cfg *config.Config) *jwt.ServerInterceptor {
	wire.Build(
		Utils,
	)
	return nil
}
