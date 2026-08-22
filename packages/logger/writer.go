package logger

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"rednote/config"

	"gopkg.in/natefinch/lumberjack.v2"
)

// buildWriter 根据配置构建 writer，支持标准输出、文件输出和轮转
func (lf *LoggerFactory) buildWriter(cfg *config.Config) io.Writer {
	lcfg := cfg.Server.Logger

	var writers []io.Writer

	// 标准输出
	if lcfg.Std {
		writers = append(writers, os.Stdout)
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

			// 填充默认值
			if rotateWriter.MaxSize <= 0 {
				rotateWriter.MaxSize = 100
			}
			if rotateWriter.MaxBackups <= 0 {
				rotateWriter.MaxBackups = 7
			}
			if rotateWriter.MaxAge <= 0 {
				rotateWriter.MaxAge = 28
			}
			
			writers = append(writers, rotateWriter)
		} else {
			// 普通文件追加写入
			f, err := os.OpenFile(lcfg.Out, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
			if err != nil {
				panic(err)
			} else {
				writers = append(writers, f)
			}
		}
	}

	// 至少有一个输出
	var w io.Writer
	switch len(writers) {
	case 0:
		return os.Stdout
	case 1:
		w = writers[0]
	default:
		w = io.MultiWriter(writers...)
	}

	// 缓冲区包装：按固定大小刷盘
	bufSize := lcfg.BufferSize
	if bufSize <= 0 {
		bufSize = 256 * 1024 // 默认 256KB
	}
	return bufio.NewWriterSize(w, bufSize)
}
