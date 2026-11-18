package oss

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"rednote/config"
	"rednote/packages/logger"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type S3Repo interface {
	// Upload 上传文件到 S3
	Upload(ctx context.Context, key string, reader io.Reader, size int64, contentType string) (*UploadResult, error)
	// Download 下载文件
	Download(ctx context.Context, key string) (*DownloadResult, error)
	// DownloadToWriter 下载文件到 Writer
	DownloadToWriter(ctx context.Context, key string, writer io.Writer) (int64, error)
	// Exist 检查对象是否存在
	Exist(ctx context.Context, key string) (bool, error)
	// DeleteObject 删除对象
	DeleteObject(ctx context.Context, key string) error
	// DeleteObjects 批量删除对象
	DeleteObjects(ctx context.Context, keys []string) error
	// PresignedPutURL 生成预签名上传 URL
	PresignedPutURL(ctx context.Context, key string, expiry time.Duration, contentType string) (*PresignedURLResult, error)
	// PresignedGetURL 生成预签名下载 URL
	PresignedGetURL(ctx context.Context, key string, expiry time.Duration) (*PresignedURLResult, error)
	// PresignedDeleteURL 生成预签名删除 URL
	PresignedDeleteURL(ctx context.Context, key string, expiry time.Duration) (*PresignedURLResult, error)
	// PresignedURL 生成预签名 URL（通用方法）
	PresignedURL(ctx context.Context, method PresignMethod, key string, expiry time.Duration, reqParams map[string][]string) (*PresignedURLResult, error)
	// Bucket 获取存储桶名称
	Bucket() string
	// HealthCheck 健康检查
	HealthCheck(ctx context.Context) error
}

// MinioClient S3/Minio 客户端封装
//
// 双 client：内嵌的 Client 走 Endpoint（服务端 Upload/Download/Delete 内网直连）；
// presign 走 PublicEndpoint（对外 CDN/反代域名），仅用于生成预签名 URL。
// 两者未配置差异时指向同一个 client。
type MinioClient struct {
	*minio.Client
	presign *minio.Client
	bucket  string
	log     *slog.Logger
}

// NewClient 创建新的 S3/Minio 客户端
// cfg 从 config.S3Config 获取配置
func NewClient(cfg *config.Config, lf logger.LoggerFactory) *MinioClient {
	if cfg == nil {
		panic(fmt.Errorf("s3 config is required"))
	}

	minioClient, err := minio.New(cfg.S3.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.S3.AccessKey, cfg.S3.SecretKey, ""),
		Secure: cfg.S3.UseSSL,
		Region: cfg.S3.Region,
	})

	if err != nil {
		panic(fmt.Errorf("failed to create minio client: %w", err))
	}

	// 预签名 client：host 参与 SigV4 计算，对外域名必须作为 endpoint 参与签名（不能事后替换 URI）
	presignClient := minioClient
	if pub := cfg.S3.PublicEndpoint; pub != "" && (pub != cfg.S3.Endpoint || cfg.S3.PublicUseSSL != cfg.S3.UseSSL) {
		presignClient, err = minio.New(presignEndpoint(cfg.S3), &minio.Options{
			Creds:  credentials.NewStaticV4(cfg.S3.AccessKey, cfg.S3.SecretKey, ""),
			Secure: presignSecure(cfg.S3),
			Region: cfg.S3.Region,
		})
		if err != nil {
			panic(fmt.Errorf("failed to create minio presign client for %s: %w", pub, err))
		}
	}

	client := &MinioClient{
		Client:  minioClient,
		presign: presignClient,
		bucket:  cfg.S3.Bucket,
		log:     lf.GetLoggerOf("minio"),
	}

	// 确保存储桶存在
	ctx := context.Background()
	exists, err := minioClient.BucketExists(ctx, cfg.S3.Bucket)
	if err != nil {
		panic(fmt.Errorf("failed to check bucket existence: %w", err))
	}

	if !exists {
		if err := minioClient.MakeBucket(ctx, cfg.S3.Bucket, minio.MakeBucketOptions{
			Region: cfg.S3.Region,
		}); err != nil {
			panic(fmt.Errorf("failed to create bucket %s: %w", cfg.S3.Bucket, err))
		}
		client.log.Info("created bucket", slog.String("bucket", cfg.S3.Bucket))
	}

	return client
}

// presignEndpoint 预签名使用的 endpoint：配了 public_endpoint 用对外域名，否则回落 endpoint
func presignEndpoint(cfg config.S3Config) string {
	if cfg.PublicEndpoint != "" {
		return cfg.PublicEndpoint
	}
	return cfg.Endpoint
}

// presignSecure 预签名使用的协议：跟随 public_endpoint 的独立开关，未配置对外域名时跟随 use_ssl
func presignSecure(cfg config.S3Config) bool {
	if cfg.PublicEndpoint != "" {
		return cfg.PublicUseSSL
	}
	return cfg.UseSSL
}

// Bucket 获取存储桶名称
func (c *MinioClient) Bucket() string {
	return c.bucket
}

// HealthCheck 健康检查
func (c *MinioClient) HealthCheck(ctx context.Context) error {
	_, err := c.Client.BucketExists(ctx, c.bucket)
	if err != nil {
		return fmt.Errorf("s3 health check failed: %w", err)
	}
	return nil
}
