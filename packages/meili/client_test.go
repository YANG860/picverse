package meili

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"rednote/config"
	"rednote/packages/logger"

	ms "github.com/meilisearch/meilisearch-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestLogger 构造测试用 logger 工厂
func newTestLogger(t *testing.T) logger.LoggerFactory {
	t.Helper()
	lf, cleanup := logger.NewLoggerFactory(&config.Config{
		Logger: config.LoggerConfig{File: true, Out: filepath.Join(t.TempDir(), "meili-test.log")},
	})
	t.Cleanup(cleanup)
	return lf
}

// newTestServer 以 httptest 服务模拟 Meilisearch 健康接口
func newTestServer(t *testing.T, status string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"` + status + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestNewClientOK 启动连通性校验通过
func TestNewClientOK(t *testing.T) {
	srv := newTestServer(t, "available")
	cfg := &config.Config{Meili: config.MeiliConfig{URL: srv.URL, APIKey: "test-key"}}

	mc, cleanup := NewClient(cfg, newTestLogger(t))
	t.Cleanup(cleanup)

	assert.NoError(t, mc.HealthCheck(context.Background()))
}

// TestNewClientFailFast 连接不可用时启动即 panic（fail-fast，对齐 packages/redis）
func TestNewClientFailFast(t *testing.T) {
	// 立即关闭的地址：连接必然失败
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	cfg := &config.Config{Meili: config.MeiliConfig{URL: url, APIKey: "test-key"}}
	assert.Panics(t, func() { NewClient(cfg, newTestLogger(t)) })
}

// TestHealthCheckUnavailable 健康状态非 available 时报错
// （NewClient 启动即校验，此处绕过 Connect 直接构造，覆盖运行期健康退化路径）
func TestHealthCheckUnavailable(t *testing.T) {
	srv := newTestServer(t, "unavailable")

	mc := &MeiliClient{
		ServiceManager: ms.New(srv.URL, ms.WithAPIKey("test-key")),
		log:            newTestLogger(t).GetLoggerOf("meili"),
	}

	err := mc.HealthCheck(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unavailable")
}
