//go:build wireinject
// +build wireinject

package gen

import (
	"rednote/config"
	"rednote/gen/go/media/v1/mediav1connect"

	authv1connect "rednote/gen/go/auth/v1/authv1connect"
	commonv1 "rednote/internal/common/v1"
	"rednote/internal/consumer"

	authv1 "rednote/internal/auth/v1"
	mediav1 "rednote/internal/media/v1"
	"rednote/internal/poller"
	"rednote/packages/db"
	"rednote/packages/jwt"
	"rednote/packages/logger"
	"rednote/packages/mq"
	"rednote/packages/oss"

	"github.com/google/wire"
)

var Repos = wire.NewSet(
	mediav1.NewMediaRepo,
	wire.Bind(new(mediav1.MediaRepository), new(*mediav1.MediaRepoImpl)),

	authv1.NewAuthRepoImpl,
	wire.Bind(new(authv1.AuthRepository), new(*authv1.AuthRepoImpl)),

	commonv1.NewOutBoxRepo,
	wire.Bind(new(commonv1.OutBoxRepository), new(*commonv1.OutBoxRepoImpl)),
)

var Services = wire.NewSet(

	mediav1.NewMediaServiceHandler,
	wire.Bind(new(mediav1connect.MediaServiceHandler), new(*mediav1.MediaServiceHandler)),

	authv1.NewAuthServiceHandler,
	wire.Bind(new(authv1connect.AuthServiceHandler), new(*authv1.AuthServiceHandler)),
)

var Usecases = wire.NewSet(
	mediav1.NewMediaUsecase,
	wire.Bind(new(mediav1.MediaUsecase), new(*mediav1.MediaUsecaseImpl)),

	authv1.NewAuthUsecaseImpl,
	wire.Bind(new(authv1.AuthUsecase), new(*authv1.AuthUsecaseImpl)),
)

var Middlewares = wire.NewSet(
	db.NewPG,
	mq.NewJetStreamClient,
	logger.NewLoggerFactory,
	wire.Bind(new(logger.LoggerFactory), new(*logger.LoggerFactoryImpl)),

	oss.NewClient,
	wire.Bind(new(oss.S3Repo), new(*oss.MinioClient)),
)

var Utils = wire.NewSet(
	jwt.NewManager,
	wire.Bind(new(jwt.Manager), new(*jwt.ManagerImpl)),
	jwt.NewServerInterceptor,
)

func InitializeConsumer(cfg *config.Config) (*consumer.MediaConsumer, func()) {
	wire.Build(
		consumer.NewMediaConsumer,
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

func InitializeAuthInterceptor(cfg *config.Config) *jwt.ServerInterceptor {
	wire.Build(
		Utils,
	)
	return nil
}
