// Package testoss 提供 oss.S3Repo 的本地内存模拟，用于单元测试注入：
//   - 对象存内存，Upload/Download/Exist/Delete 等常用方法完整实现
//   - 预签名 URL 返回可断言的假地址（含对象键）
//   - 业务未使用的 PresignedDeleteURL/PresignedURL 直接 panic 以暴露误用
package testoss

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"rednote/packages/oss"
)

// ErrNotFound 对象不存在
var ErrNotFound = errors.New("testoss: object not found")

// Op 调用记录项
type Op struct {
	Method string
	Key    string
}

type object struct {
	data        []byte
	contentType string
}

// Client oss.S3Repo 的内存模拟实现
type Client struct {
	mu      sync.Mutex
	bucket  string
	objects map[string]*object
	ops     []Op
	err     error // 故障注入：非 nil 时所有操作返回
}

var _ oss.S3Repo = (*Client)(nil)

// New 创建内存模拟客户端（存储桶名 test-bucket）
func New() *Client {
	return &Client{
		bucket:  "test-bucket",
		objects: make(map[string]*object),
	}
}

// Upload 将 reader 全量写入内存
func (c *Client) Upload(_ context.Context, key string, reader io.Reader, _ int64, contentType string) (*oss.UploadResult, error) {
	if err := c.begin("Upload", key); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("testoss: read upload body %s: %w", key, err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.objects[key] = &object{data: data, contentType: contentType}
	return &oss.UploadResult{Key: key, ETag: etagOf(key), Size: int64(len(data))}, nil
}

// Download 从内存读取对象
func (c *Client) Download(_ context.Context, key string) (*oss.DownloadResult, error) {
	if err := c.begin("Download", key); err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	obj, ok := c.objects[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	data := make([]byte, len(obj.data))
	copy(data, obj.data)
	return &oss.DownloadResult{
		ContentType: obj.contentType,
		Size:        int64(len(data)),
		ETag:        etagOf(key),
		Body:        io.NopCloser(bytes.NewReader(data)),
	}, nil
}

// DownloadToWriter 将对象内容写入 writer，返回写入字节数
func (c *Client) DownloadToWriter(ctx context.Context, key string, writer io.Writer) (int64, error) {
	if err := c.begin("DownloadToWriter", key); err != nil {
		return 0, err
	}

	c.mu.Lock()
	obj, ok := c.objects[key]
	c.mu.Unlock()
	if !ok {
		return 0, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	return io.Copy(writer, bytes.NewReader(obj.data))
}

// Exist 检查对象是否存在
func (c *Client) Exist(_ context.Context, key string) (bool, error) {
	if err := c.begin("Exist", key); err != nil {
		return false, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.objects[key]
	return ok, nil
}

// DeleteObject 删除对象（不存在视为成功，与 S3 语义一致）
func (c *Client) DeleteObject(_ context.Context, key string) error {
	if err := c.begin("DeleteObject", key); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.objects, key)
	return nil
}

// DeleteObjects 批量删除对象
func (c *Client) DeleteObjects(ctx context.Context, keys []string) error {
	for _, key := range keys {
		if err := c.DeleteObject(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

// PresignedPutURL 返回可断言的假上传地址
func (c *Client) PresignedPutURL(_ context.Context, key string, expiry time.Duration, _ string) (*oss.PresignedURLResult, error) {
	if err := c.begin("PresignedPutURL", key); err != nil {
		return nil, err
	}
	return fakeURL(c.bucket, key, expiry), nil
}

// PresignedGetURL 返回可断言的假下载地址
func (c *Client) PresignedGetURL(_ context.Context, key string, expiry time.Duration) (*oss.PresignedURLResult, error) {
	if err := c.begin("PresignedGetURL", key); err != nil {
		return nil, err
	}
	return fakeURL(c.bucket, key, expiry), nil
}

// PresignedDeleteURL 业务未使用，直接 panic 以暴露误用
func (c *Client) PresignedDeleteURL(_ context.Context, _ string, _ time.Duration) (*oss.PresignedURLResult, error) {
	panic("testoss: PresignedDeleteURL 未实现（业务未使用）")
}

// PresignedURL 业务未使用，直接 panic 以暴露误用
func (c *Client) PresignedURL(_ context.Context, _ oss.PresignMethod, _ string, _ time.Duration, _ map[string][]string) (*oss.PresignedURLResult, error) {
	panic("testoss: PresignedURL 未实现（业务未使用）")
}

// Bucket 返回存储桶名
func (c *Client) Bucket() string {
	return c.bucket
}

// HealthCheck 空实现：内存模拟恒健康
func (c *Client) HealthCheck(_ context.Context) error {
	return nil
}

// ---------- 测试辅助 ----------

// SetObject 直接向内存写入对象（构造测试数据）
func (c *Client) SetObject(key string, data []byte, contentType string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	buf := make([]byte, len(data))
	copy(buf, data)
	c.objects[key] = &object{data: buf, contentType: contentType}
}

// Object 读取对象内容，返回 (内容, 是否存在)
func (c *Client) Object(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	obj, ok := c.objects[key]
	if !ok {
		return nil, false
	}
	buf := make([]byte, len(obj.data))
	copy(buf, obj.data)
	return buf, true
}

// Ops 返回调用记录快照
func (c *Client) Ops() []Op {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Op, len(c.ops))
	copy(out, c.ops)
	return out
}

// Reset 清空对象、调用记录并取消故障注入
func (c *Client) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.objects = make(map[string]*object)
	c.ops = nil
	c.err = nil
}

// SetError 故障注入：此后所有操作返回该错误；传 nil 取消
func (c *Client) SetError(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.err = err
}

// begin 记录调用并返回故障注入的错误
func (c *Client) begin(method, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ops = append(c.ops, Op{Method: method, Key: key})
	return c.err
}

func etagOf(key string) string {
	return "test-etag-" + key
}

func fakeURL(bucket, key string, expiry time.Duration) *oss.PresignedURLResult {
	return &oss.PresignedURLResult{
		URL:       fmt.Sprintf("https://testoss.local/%s/%s?sig=test", bucket, key),
		ExpiresAt: time.Now().Add(expiry),
	}
}
