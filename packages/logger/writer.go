package logger

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"rednote/config"
	"rednote/packages/defaults"

	"gopkg.in/natefinch/lumberjack.v2"
)

type LogWriter struct {
	Std        io.WriteCloser
	FileWriter io.WriteCloser
	FileBuffer *bufio.Writer
}

func NewLogWriter(std io.WriteCloser, file io.WriteCloser, bufSize int) *LogWriter {
	if std == nil && file == nil {
		std = os.Stdout
	}

	var buf *bufio.Writer
	if file != nil {
		buf = bufio.NewWriterSize(file, bufSize)
	}
	return &LogWriter{
		Std:        std,
		FileWriter: file,
		FileBuffer: buf,
	}

}

func (lw *LogWriter) Write(p []byte) (n int, err error) {
	// Write to standard output
	if lw.Std != nil {
		if _, err := lw.Std.Write(p); err != nil {
			return 0, err
		}
	}

	// Write to file buffer
	if lw.FileBuffer != nil {
		if _, err := lw.FileBuffer.Write(p); err != nil {
			return 0, err
		}
	}

	return len(p), nil
}

func (lw *LogWriter) Close() error {
	var lastErr error
	if lw.Std != nil {
		if err := lw.Std.Close(); err != nil {
			lastErr = err
		}
	}
	if lw.FileBuffer != nil {
		if err := lw.FileBuffer.Flush(); err != nil {
			lastErr = err
		}
	}
	if lw.FileWriter != nil {
		if err := lw.FileWriter.Close(); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// buildWriter 根据配置构建 writer，支持标准输出、文件输出和轮转
func (lf *LoggerFactoryImpl) buildWriter(cfg *config.Config) *LogWriter {
	lcfg := cfg.Logger

	var stdWriter io.WriteCloser
	var fileWriter io.WriteCloser

	// 标准输出
	if lcfg.Std {
		stdWriter = os.Stdout
	}

	// 文件输出（可选轮转）
	if lcfg.File && lcfg.Out != "" {
		// 确保目录存在
		dir := filepath.Dir(lcfg.Out)
		if dir != "" && dir != "." {
			os.MkdirAll(dir, 0755)
		}

		if lcfg.Rotate {
			// 使用 lumberjack 实现日志轮转
			rotateWriter := &lumberjack.Logger{
				Filename:   lcfg.Out,
				MaxSize:    lcfg.MaxSize,    // MB
				MaxBackups: lcfg.MaxBackups, // 保留旧文件数量
				MaxAge:     lcfg.MaxAge,     // 保留天数
				Compress:   lcfg.Compress,   // 是否压缩
			}
			_ = rotateWriter.Close()

			// 填充默认值
			if rotateWriter.MaxSize <= 0 {
				rotateWriter.MaxSize = defaults.RotateMaxSize
			}
			if rotateWriter.MaxBackups <= 0 {
				rotateWriter.MaxBackups = defaults.RotateMaxBackups
			}
			if rotateWriter.MaxAge <= 0 {
				rotateWriter.MaxAge = defaults.RotateMaxAge
			}

			fileWriter = rotateWriter
		} else {
			// 普通文件追加写入
			f, err := os.OpenFile(lcfg.Out, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
			if err != nil {
				panic(err)
			} else {
				fileWriter = f
			}
		}
	}

	// 缓冲区包装：按固定大小刷盘
	bufSize := lcfg.BufferSize
	if bufSize <= 0 {
		bufSize = defaults.BufferSize // 默认 256KB
	}

	w := NewLogWriter(stdWriter, fileWriter, bufSize)

	return w
}
