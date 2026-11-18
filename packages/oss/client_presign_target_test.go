package oss

import (
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/stretchr/testify/require"
)

// mustMinioClient 构造裸 minio client（本地不发起网络请求；带静态凭据才能出 SigV4 签名）
func mustMinioClient(t *testing.T, endpoint string, secure bool) *minio.Client {
	t.Helper()
	c, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4("test-access", "test-secret", ""),
		Secure: secure,
		Region: "us-east-1",
	})
	require.NoError(t, err)
	return c
}
