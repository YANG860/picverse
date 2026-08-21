package logger

import (
	"io"
	"log/slog"
	"rednote/config"
)

type LoggerFactory struct {
	Writer io.Writer
}

// NewLoggerFactory 根据配置创建日志工厂
func NewLoggerFactory(cfg *config.Config) *LoggerFactory {
	lf := &LoggerFactory{}
	lf.Writer = lf.buildWriter(cfg)
	
	return lf
}

func (lf *LoggerFactory) GetLoggerOf(name string) *slog.Logger {
	return slog.Default()
}

func (lf *LoggerFactory) GetWriter() io.Writer {
	return lf.Writer
}
