// Package meili Meilisearch 客户端封装：搜推服务（discover）索引同步与搜索查询的统一出入口。
// 薄封装风格对齐 packages/redis：内嵌 SDK 接口，底层操作可直接调用。
package meili

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"rednote/config"
	"rednote/packages/logger"

	ms "github.com/meilisearch/meilisearch-go"
)

// taskWaitInterval Meili 异步任务状态轮询间隔
const taskWaitInterval = 50 * time.Millisecond

// PostsIndex 帖子搜索索引名（posts 索引，discover 服务维护）
const PostsIndex = "posts"

// MeiliRepo Meilisearch 工具仓库
type MeiliRepo interface {
	// HealthCheck 健康检查
	HealthCheck(ctx context.Context) error
	// EnsureIndex 幂等确保索引存在并应用设置（primary key + settings），阻塞至设置生效
	EnsureIndex(ctx context.Context, index, primaryKey string, settings *ms.Settings) error
	// UpsertDocuments 批量整文档覆盖写入（按索引主键 upsert，天然幂等），阻塞至写入生效；
	// 显式携带 primaryKey：写入时索引不存在可正确自动建索引（无显式主键时多 id 字段推断失败）
	// documents 为文档切片（如 []T），元素字段经 JSON 序列化入库
	UpsertDocuments(ctx context.Context, index, primaryKey string, documents any) error
	// DeleteDocuments 按主键批量删除文档（天然幂等），阻塞至删除生效
	DeleteDocuments(ctx context.Context, index string, primaryKeys []string) error
	// Search 执行索引检索：命中为 schemaless（ms.Hit），调用方经 Hit.DecodeInto 解码到目标结构
	Search(ctx context.Context, index, query string, req *ms.SearchRequest) (*ms.SearchResponse, error)

	// ===== 帖子搜索领域接口（配置组装/请求/解码/重排收敛包内，见 search.go）=====
	// SearchPosts 搜帖子——"综合"排序：相关度 × 时间衰减当页重排；offset 编码游标
	SearchPosts(ctx context.Context, query string, offset, limit int64) (*PostSearchResult, error)
	// SearchLatestPosts 搜帖子——"最新"排序：(created_at, post_id) 倒序直出无重排
	SearchLatestPosts(ctx context.Context, query string, offset, limit int64) (*PostSearchResult, error)

	// ===== 帖子入库领域接口（索引/文档/设置收敛包内，见 index.go）=====
	// EnsurePostsIndex 幂等确保 posts 索引存在并应用设置
	EnsurePostsIndex(ctx context.Context) error
	// UpsertPost 单条帖子索引写入（按主键覆盖，天然幂等）
	UpsertPost(ctx context.Context, doc PostIndexDoc) error
	// RemovePost 删除帖子索引文档（删不存在的键为 no-op，天然幂等）
	RemovePost(ctx context.Context, postID string) error
}

// MeiliClient meilisearch-go 客户端封装
// 内嵌 ms.ServiceManager，底层索引/搜索操作可直接调用（如 m.Index("posts")）
type MeiliClient struct {
	ms.ServiceManager
	log *slog.Logger
}

var _ MeiliRepo = (*MeiliClient)(nil)

// NewClient 创建 Meilisearch 客户端
// cfg 从 config.MeiliConfig 获取配置；启动时校验连通性，失败 fail-fast（对齐 packages/redis）
func NewClient(cfg *config.Config, lf logger.LoggerFactory) (*MeiliClient, func()) {
	if cfg == nil {
		panic(fmt.Errorf("meili config is required"))
	}

	// Connect 内部做健康检查，不可用直接失败（部署/配置问题早暴露）
	sm, err := ms.Connect(cfg.Meili.URL, ms.WithAPIKey(cfg.Meili.APIKey))
	if err != nil {
		panic(fmt.Errorf("failed to connect meilisearch: %w", err))
	}

	mc := &MeiliClient{
		ServiceManager: sm,
		log:            lf.GetLoggerOf("meili"),
	}

	// SDK 基于 HTTP 无持久连接需要释放
	return mc, func() {}
}

// HealthCheck 健康检查
func (m *MeiliClient) HealthCheck(ctx context.Context) error {
	h, err := m.HealthWithContext(ctx)
	if err != nil {
		return fmt.Errorf("meili health check failed: %w", err)
	}
	if h.Status != "available" {
		return fmt.Errorf("meili health check failed: status %q", h.Status)
	}
	return nil
}

// waitTask 阻塞至 Meili 异步任务结束：写入/设置的可见性与失败上抛由调用方决定 ACK/Nak，
// 避免"已确认但索引未生效"的静默丢同步
func (m *MeiliClient) waitTask(ctx context.Context, task *ms.TaskInfo) error {
	t, err := m.WaitForTaskWithContext(ctx, task.TaskUID, taskWaitInterval)
	if err != nil {
		return fmt.Errorf("wait task %d: %w", task.TaskUID, err)
	}
	if t.Status != ms.TaskStatusSucceeded {
		return fmt.Errorf("task %d finished with status %q: %+v", task.TaskUID, t.Status, t.Error)
	}
	return nil
}

// EnsureIndex 幂等确保索引存在并应用设置
func (m *MeiliClient) EnsureIndex(ctx context.Context, index, primaryKey string, settings *ms.Settings) error {
	// 索引不存在则创建（幂等：并发/重复创建以创建后探测结果为准）
	if _, err := m.GetIndexWithContext(ctx, index); err != nil {
		task, cerr := m.CreateIndexWithContext(ctx, &ms.IndexConfig{Uid: index, PrimaryKey: primaryKey})
		if cerr != nil {
			// 创建失败可能是竞态（已存在）或瞬时故障：以再次探测为准
			if _, probeErr := m.GetIndexWithContext(ctx, index); probeErr != nil {
				return fmt.Errorf("ensure index %q: create: %w, probe: %v", index, cerr, probeErr)
			}
		} else if waitErr := m.waitTask(ctx, task); waitErr != nil {
			return fmt.Errorf("ensure index %q: create %w", index, waitErr)
		}
	}

	// 设置更新幂等（可重复应用）
	task, err := m.Index(index).UpdateSettingsWithContext(ctx, settings)
	if err != nil {
		return fmt.Errorf("update settings %q: %w", index, err)
	}
	if err := m.waitTask(ctx, task); err != nil {
		return fmt.Errorf("update settings %q: %w", index, err)
	}
	return nil
}

// UpsertDocuments 批量整文档覆盖写入（AddDocuments 按主键全量替换语义）；
// 显式携带 primaryKey：索引不存在时由写入方定义主键自动建索引，避免多 id 字段推断失败
func (m *MeiliClient) UpsertDocuments(ctx context.Context, index, primaryKey string, documents any) error {
	task, err := m.Index(index).AddDocumentsWithContext(ctx, documents, &ms.DocumentOptions{PrimaryKey: &primaryKey})
	if err != nil {
		return fmt.Errorf("upsert documents %q: %w", index, err)
	}
	if err := m.waitTask(ctx, task); err != nil {
		return fmt.Errorf("upsert documents %q: %w", index, err)
	}
	return nil
}

// DeleteDocuments 按主键批量删除文档（删除不存在的键为 no-op，天然幂等）
func (m *MeiliClient) DeleteDocuments(ctx context.Context, index string, primaryKeys []string) error {
	task, err := m.Index(index).DeleteDocumentsWithContext(ctx, primaryKeys, nil)
	if err != nil {
		return fmt.Errorf("delete documents %q: %w", index, err)
	}
	if err := m.waitTask(ctx, task); err != nil {
		return fmt.Errorf("delete documents %q: %w", index, err)
	}
	return nil
}

// Search 执行索引检索
func (m *MeiliClient) Search(ctx context.Context, index, query string, req *ms.SearchRequest) (*ms.SearchResponse, error) {
	resp, err := m.Index(index).SearchWithContext(ctx, query, req)
	if err != nil {
		return nil, fmt.Errorf("search %q: %w", index, err)
	}
	return resp, nil
}

// EnsurePostsIndex 幂等确保 posts 索引存在并应用帖子索引设置
func (m *MeiliClient) EnsurePostsIndex(ctx context.Context) error {
	return m.EnsureIndex(ctx, PostsIndex, PostsIndexPrimaryKey, PostsIndexSettings())
}

// UpsertPost 单条帖子索引写入（按主键全量覆盖，天然幂等）
func (m *MeiliClient) UpsertPost(ctx context.Context, doc PostIndexDoc) error {
	return m.UpsertDocuments(ctx, PostsIndex, PostsIndexPrimaryKey, []PostIndexDoc{doc})
}

// RemovePost 删除帖子索引文档（删不存在的键为 no-op，天然幂等）
func (m *MeiliClient) RemovePost(ctx context.Context, postID string) error {
	return m.DeleteDocuments(ctx, PostsIndex, []string{postID})
}
