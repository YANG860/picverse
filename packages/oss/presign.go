package oss

import (
	"context"
	"fmt"
	"time"
)

// PresignedURLResult 预签名 URL 结果
type PresignedURLResult struct {
	URL       string
	ExpiresAt time.Time
}

// PresignedPutURL 生成预签名上传 URL（host = s3.public_endpoint，客户端直传对外域名）
// key: 对象键
// expiry: 过期时间
// contentType: 内容类型（可选，空字符串表示不限制）
func (c *MinioClient) PresignedPutURL(ctx context.Context, key string, expiry time.Duration, contentType string) (*PresignedURLResult, error) {
	reqParams := make(map[string][]string)
	if contentType != "" {
		reqParams["Content-Type"] = []string{contentType}
	}

	url, err := c.presign.PresignedPutObject(ctx, c.bucket, key, expiry)
	if err != nil {
		return nil, fmt.Errorf("failed to generate presigned put URL for %s: %w", key, err)
	}

	c.log.Info("generated presigned put URL",
		"key", key,
		"expires", expiry.String(),
	)

	return &PresignedURLResult{
		URL:       url.String(),
		ExpiresAt: time.Now().Add(expiry),
	}, nil
}

// PresignedGetURL 生成预签名下载 URL（host = s3.public_endpoint）
// key: 对象键
// expiry: 过期时间
func (c *MinioClient) PresignedGetURL(ctx context.Context, key string, expiry time.Duration) (*PresignedURLResult, error) {
	reqParams := make(map[string][]string)

	url, err := c.presign.PresignedGetObject(ctx, c.bucket, key, expiry, reqParams)
	if err != nil {
		return nil, fmt.Errorf("failed to generate presigned get URL for %s: %w", key, err)
	}

	c.log.Info("generated presigned get URL",
		"key", key,
		"expires", expiry.String(),
	)

	return &PresignedURLResult{
		URL:       url.String(),
		ExpiresAt: time.Now().Add(expiry),
	}, nil
}

// PresignedDeleteURL 生成预签名删除 URL
// key: 对象键
// expiry: 过期时间
func (c *MinioClient) PresignedDeleteURL(ctx context.Context, key string, expiry time.Duration) (*PresignedURLResult, error) {
	reqParams := make(map[string][]string)

	// minio-go 没有直接的 PresignedDeleteURL，使用通用方法
	url, err := c.presign.Presign(ctx, "DELETE", c.bucket, key, expiry, reqParams)
	if err != nil {
		return nil, fmt.Errorf("failed to generate presigned delete URL for %s: %w", key, err)
	}

	c.log.Info("generated presigned delete URL",
		"key", key,
		"expires", expiry.String(),
	)

	return &PresignedURLResult{
		URL:       url.String(),
		ExpiresAt: time.Now().Add(expiry),
	}, nil
}

// PresignMethod 预签名方法类型
type PresignMethod string

const (
	PresignMethodGet    PresignMethod = "GET"
	PresignMethodPut    PresignMethod = "PUT"
	PresignMethodDelete PresignMethod = "DELETE"
	PresignMethodHead   PresignMethod = "HEAD"
)

// PresignedURL 生成预签名 URL（通用方法）
// method: HTTP 方法
// key: 对象键
// expiry: 过期时间
// reqParams: 额外的查询参数
func (c *MinioClient) PresignedURL(ctx context.Context, method PresignMethod, key string, expiry time.Duration, reqParams map[string][]string) (*PresignedURLResult, error) {
	url, err := c.presign.Presign(ctx, string(method), c.bucket, key, expiry, reqParams)
	if err != nil {
		return nil, fmt.Errorf("failed to generate presigned %s URL for %s: %w", method, key, err)
	}

	c.log.Info("generated presigned URL",
		"method", method,
		"key", key,
		"expires", expiry.String(),
	)

	return &PresignedURLResult{
		URL:       url.String(),
		ExpiresAt: time.Now().Add(expiry),
	}, nil
}
