package logger

import (
	"io"
	"log/slog"
	"rednote/config"
)

type LoggerFactoryImpl struct {
	wc io.WriteCloser
	l  *slog.Logger
}

type LoggerFactory interface {
	GetLoggerOf(name string) *slog.Logger
	GetWriter() io.Writer
	Default() *slog.Logger
	Close()
}

// NewLoggerFactory 根据配置创建日志工厂
func NewLoggerFactory(cfg *config.Config) (*LoggerFactoryImpl, func()) {
	lf := &LoggerFactoryImpl{}
	lf.wc = lf.buildWriter(cfg)
	lf.l = slog.New(NewTraceHandler(slog.NewJSONHandler(lf.wc, nil)))

	return lf, lf.Close
}

func (lf *LoggerFactoryImpl) GetLoggerOf(name string) *slog.Logger {

	return lf.l.With("name", name)
}

func (lf *LoggerFactoryImpl) GetWriter() io.Writer {
	return lf.wc
}

func (lf *LoggerFactoryImpl) Default() *slog.Logger {
	return lf.l
}

func (lf *LoggerFactoryImpl) Close() {
	_ = lf.wc.Close()
}
