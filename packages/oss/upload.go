package oss

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/minio/minio-go/v7"
)

// UploadResult 上传结果
type UploadResult struct {
	Key  string
	ETag string
	Size int64
}

// Upload 上传文件到 S3
// key: 对象键
// reader: 文件内容
// size: 文件大小（字节）
// contentType: 内容类型
func (c *MinioClient) Upload(ctx context.Context, key string, reader io.Reader, size int64, contentType string) (*UploadResult, error) {
	opts := minio.PutObjectOptions{
		ContentType: contentType,
	}

	info, err := c.Client.PutObject(ctx, c.bucket, key, reader, size, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to upload object %s: %w", key, err)
	}

	c.log.Info("uploaded object",
		"key", key,
		"etag", info.ETag,
		"size", info.Size,
	)

	return &UploadResult{
		Key:  key,
		ETag: info.ETag,
		Size: info.Size,
	}, nil
}

// UploadWithExpiry 上传带过期时间的文件
// expiry: 过期时间（秒）
func (c *MinioClient) UploadWithExpiry(ctx context.Context, key string, reader io.Reader, size int64, contentType string, expiry int) (*UploadResult, error) {
	opts := minio.PutObjectOptions{
		ContentType: contentType,
		// 注意：Minio 服务端需要配置 expiry 策略
		// 这里只设置元数据，实际过期由服务端策略决定
	}

	info, err := c.Client.PutObject(ctx, c.bucket, key, reader, size, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to upload object with expiry %s: %w", key, err)
	}

	c.log.Info("uploaded object with expiry",
		"key", key,
		"expiry_seconds", expiry,
	)

	return &UploadResult{
		Key:  key,
		ETag: info.ETag,
		Size: info.Size,
	}, nil
}

// GenerateKey 生成对象键
// prefix: 前缀（如 "images/", "videos/"）
// filename: 文件名
// 返回格式: prefix/timestamp_random.ext
func GenerateKey(prefix, filename string) string {
	// 简单实现：prefix/timestamp_filename
	// 实际项目中可能需要更复杂的 key 生成策略
	timestamp := time.Now().UnixMilli()
	return fmt.Sprintf("%s%d_%s", prefix, timestamp, filename)
}

// GenerateKeyWithID 生成带 ID 的对象键
// prefix: 前缀
// id: 唯一 ID
// ext: 扩展名
func GenerateKeyWithID(prefix, id, ext string) string {
	return fmt.Sprintf("%s%s%s", prefix, id, ext)
}
