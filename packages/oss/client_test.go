package oss

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"rednote/config"
)

// newTestClient 只构造 client（不触发网络：不检查 bucket）
func newTestClient(t *testing.T, s3 config.S3Config) *MinioClient {
	t.Helper()
	return &MinioClient{
		Client:  mustMinioClient(t, s3.Endpoint, s3.UseSSL),
		presign: mustMinioClient(t, presignEndpoint(s3), presignSecure(s3)),
		bucket:  s3.Bucket,
		log:     slog.Default(),
	}
}

// TestPresignEndpointResolution 预签名域名解析口径：配了 public_endpoint 用它，否则回落 endpoint
func TestPresignEndpointResolution(t *testing.T) {
	cases := []struct {
		name         string
		s3           config.S3Config
		wantEndpoint string
		wantSecure   bool
	}{
		{
			name:         "未配 public_endpoint：回落 endpoint（本地/测试行为不变）",
			s3:           config.S3Config{Endpoint: "127.0.0.1:9000", UseSSL: false, Bucket: "rednote"},
			wantEndpoint: "127.0.0.1:9000",
			wantSecure:   false,
		},
		{
			name: "配 public_endpoint + https：签名走对外域名",
			s3: config.S3Config{
				Endpoint: "106.53.172.186:9000", UseSSL: false, Bucket: "rednote",
				PublicEndpoint: "media.picverse.site", PublicUseSSL: true,
			},
			wantEndpoint: "media.picverse.site",
			wantSecure:   true,
		},
		{
			name: "public_endpoint 省略协议差异（仅域名不同）",
			s3: config.S3Config{
				Endpoint: "106.53.172.186:9000", UseSSL: false, Bucket: "rednote",
				PublicEndpoint: "media.picverse.site", PublicUseSSL: false,
			},
			wantEndpoint: "media.picverse.site",
			wantSecure:   false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.wantEndpoint, presignEndpoint(c.s3))
			assert.Equal(t, c.wantSecure, presignSecure(c.s3))
		})
	}
}

// TestPresignedPutURL_HostIsPublicEndpoint 预签名 URL 的 host 必须是 public_endpoint
// （SigV4 的 canonical host 参与签名，事后替换 URL host 会使签名失效 —— 这正是必须用独立 presign client 的原因）
func TestPresignedPutURL_HostIsPublicEndpoint(t *testing.T) {
	c := newTestClient(t, config.S3Config{
		Endpoint: "106.53.172.186:9000", UseSSL: false, Bucket: "rednote",
		PublicEndpoint: "media.picverse.site", PublicUseSSL: true,
		Region: "us-east-1",
	})

	res, err := c.PresignedPutURL(t.Context(), "uploads/1/origin.jpg", 15*60e9, "image/jpeg")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(res.URL, "https://media.picverse.site/rednote/uploads/1/origin.jpg?"), res.URL)
	assert.Contains(t, res.URL, "X-Amz-Signature=", "对外域名下仍须是有效 SigV4 签名")
	assert.NotContains(t, res.URL, "106.53.172.186", "签名 host 不得泄露内网 endpoint")
}

// TestPresignedGetURL_FallbackToEndpoint 未配 public_endpoint 时行为与改造前一致
func TestPresignedGetURL_FallbackToEndpoint(t *testing.T) {
	c := newTestClient(t, config.S3Config{Endpoint: "127.0.0.1:9000", UseSSL: false, Bucket: "rednote", Region: "us-east-1"})

	res, err := c.PresignedGetURL(t.Context(), "uploads/1/216.webp", 3600e9)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(res.URL, "http://127.0.0.1:9000/rednote/uploads/1/216.webp?"), res.URL)
}
