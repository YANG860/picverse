package oss

import (
	"context"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
)

// DownloadResult 下载结果
type DownloadResult struct {
	ContentType string
	Size        int64
	ETag        string
	Body        io.ReadCloser
}

// Download 下载文件
// key: 对象键
// 返回: 下载结果，包含文件内容和元数据
func (c *MinioClient) Download(ctx context.Context, key string) (*DownloadResult, error) {
	object, err := c.Client.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get object %s: %w", key, err)
	}

	// 获取对象信息
	stat, err := object.Stat()
	if err != nil {
		object.Close()
		return nil, fmt.Errorf("failed to stat object %s: %w", key, err)
	}

	c.log.Info("downloaded object",
		"key", key,
		"content_type", stat.ContentType,
		"size", stat.Size,
	)

	return &DownloadResult{
		ContentType: stat.ContentType,
		Size:        stat.Size,
		ETag:        stat.ETag,
		Body:        object,
	}, nil
}

// DownloadToWriter 下载文件到 Writer
// key: 对象键
// writer: 目标 Writer
// 返回: 写入的字节数
func (c *MinioClient) DownloadToWriter(ctx context.Context, key string, writer io.Writer) (int64, error) {
	object, err := c.Client.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return 0, fmt.Errorf("failed to get object %s: %w", key, err)
	}
	defer object.Close()

	n, err := io.Copy(writer, object)
	if err != nil {
		return n, fmt.Errorf("failed to copy object %s: %w", key, err)
	}

	c.log.Info("downloaded object to writer",
		"key", key,
		"bytes", n,
	)

	return n, nil
}

// Exist 检查对象是否存在
func (c *MinioClient) Exist(ctx context.Context, key string) (bool, error) {
	_, err := c.StatObject(ctx, c.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		// 检查是否是对象不存在的错误
		resp := minio.ToErrorResponse(err)
		if resp.Code == "NoSuchKey" || resp.Code == "NoSuchObject" || resp.Code == "NotFound" {
			return false, nil
		}
		return false, fmt.Errorf("failed to check object existence %s: %w", key, err)
	}

	return true, nil
}

// DeleteObject 删除对象
func (c *MinioClient) DeleteObject(ctx context.Context, key string) error {
	err := c.Client.RemoveObject(ctx, c.bucket, key, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete object %s: %w", key, err)
	}

	c.log.Info("deleted object", "key", key)
	return nil
}

// DeleteObjects 批量删除对象
func (c *MinioClient) DeleteObjects(ctx context.Context, keys []string) error {
	objectsCh := make(chan minio.ObjectInfo)

	go func() {
		defer close(objectsCh)
		for _, key := range keys {
			objectsCh <- minio.ObjectInfo{Key: key}
		}
	}()

	errorCh := c.Client.RemoveObjects(ctx, c.bucket, objectsCh, minio.RemoveObjectsOptions{})

	// 收集错误
	var errs []error
	for err := range errorCh {
		if err.Err != nil {
			errs = append(errs, err.Err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to delete %d objects: %v", len(errs), errs)
	}

	c.log.Info("deleted objects", "count", len(keys))
	return nil
}
