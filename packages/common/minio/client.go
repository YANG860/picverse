package minio

import (
	"context"
	"fmt"
	"log/slog"

	"rednote/config"
	"rednote/packages/common/logger"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Client S3/Minio 客户端封装
type Client struct {
	*minio.Client
	bucket string
	log    *slog.Logger
}

// NewClient 创建新的 S3/Minio 客户端
// cfg 从 config.S3Config 获取配置
func NewClient(cfg *config.S3Config, lf *logger.LoggerFactory) (*Client, error) {
	if cfg == nil {
		return nil, fmt.Errorf("s3 config is required")
	}

	minioClient, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create minio client: %w", err)
	}

	client := &Client{
		Client: minioClient,
		bucket: cfg.Bucket,
		log:    lf.GetLoggerOf("minio"),
	}

	// 确保存储桶存在
	ctx := context.Background()
	exists, err := minioClient.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("failed to check bucket existence: %w", err)
	}

	if !exists {
		if err := minioClient.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{
			Region: cfg.Region,
		}); err != nil {
			return nil, fmt.Errorf("failed to create bucket %s: %w", cfg.Bucket, err)
		}
		client.log.Info("created bucket", slog.String("bucket", cfg.Bucket))
	}

	return client, nil
}

// Bucket 获取存储桶名称
func (c *Client) Bucket() string {
	return c.bucket
}

// HealthCheck 健康检查
func (c *Client) HealthCheck(ctx context.Context) error {
	_, err := c.Client.BucketExists(ctx, c.bucket)
	if err != nil {
		return fmt.Errorf("s3 health check failed: %w", err)
	}
	return nil
}
