# meili — Meilisearch 客户端封装

代码：`packages/meili/`。搜推服务（discover）索引同步与搜索查询的统一出入口。
薄封装风格对齐 [redis](../redis)：内嵌 `meilisearch-go` 的 `ServiceManager`，底层索引/搜索操作可直接调用（如 `m.Index("posts")`）。

## MeiliRepo 接口

| 方法 | 说明 |
| --- | --- |
| `HealthCheck(ctx)` | 健康检查（status 非 `available` 报错） |
| `EnsureIndex(ctx, index, primaryKey, settings)` | 幂等确保索引存在并应用设置（primary key + settings），阻塞至生效 |
| `UpsertDocuments(ctx, index, documents)` | 批量整文档覆盖写入（`AddDocuments` 主键替换语义），`documents` 为文档切片（如 `[]T`），阻塞至生效 |
| `DeleteDocuments(ctx, index, primaryKeys)` | 按主键批量删除（删不存在的键为 no-op），阻塞至生效 |
| `Search(ctx, index, query, req)` | 通用索引检索；命中为 schemaless（`ms.Hit`），调用方经 `Hit.DecodeInto` 解码到目标结构 |

### 帖子搜索领域接口（search.go）

配置组装/请求/解码/重排收敛包内，外部只传 query 与游标（offset/limit）即可调用：

| 方法 | 说明 |
| --- | --- |
| `SearchPosts(ctx, query, offset, limit)` | 搜帖子——**综合**排序：相关度（rankingScore）× 时间衰减当页重排；返回 `PostSearchResult` |
| `SearchLatestPosts(ctx, query, offset, limit)` | 搜帖子——**最新**排序：`(created_at, post_id)` 倒序直出，无重排 |

| 类型 | 说明 |
| --- | --- |
| `PostHit` | 解码后命中：`PostID` / `Score`（rankingScore，0~1）/ `CreatedAt`（unix 毫秒） |
| `PostSearchResult` | `Hits []PostHit`（坏命中已跳过）+ `NextOffset`（游标推进量，按 Meili 实际消费计；0 = 空页到底） |

- **常量**：`PostsIndex`（posts 索引名）、`PostsIndexPrimaryKey`（`post_id`）与 search 内部排序配置（`postsAttrRetrieve`、`searchTimeDecayDays=14`）均包内收敛；discover 层不再接触 `ms.SearchRequest`。
- **降级**：坏命中（缺 `post_id`/解码失败）跳过不打挂整页，`NextOffset` 仍按原始命中数推进（防翻页回退）。

### 帖子入库领域接口（index.go）

索引/文档/设置收敛包内，外部只传业务字段即可写入（与搜索领域接口同风格）：

| 方法 | 说明 |
| --- | --- |
| `EnsurePostsIndex(ctx)` | 幂等确保 posts 索引存在并应用 `PostsIndexSettings()` |
| `UpsertPost(ctx, doc)` | 单条帖子索引写入（按主键全量覆盖，天然幂等） |
| `RemovePost(ctx, postID)` | 删除帖子索引文档（删不存在的键为 no-op，天然幂等） |

| 类型/函数 | 说明 |
| --- | --- |
| `PostIndexDoc` | 索引文档值类型（纯值，不依赖业务 proto）：`PostID`/`Title`/`FullText`/`AuthorID`/`CreatedAt`/四项计数 |
| `NewPostIndexDoc(postID, fields)` | 从 `map[string]any` 字段集合组装文档（类型降级收敛；`created_at` 非法降级 0 并返 error） |
| `PostsIndexSettings()` | posts 索引设置（searchable / sortable） |

- **零 proto 依赖**：meili 保持纯 SDK 封装；调用方（consumer）负责 proto→`map[string]any` 字段提取（薄编排），文档组装/类型降级在本包。

## 设计要点

- **NewClient fail-fast**：启动时 `ms.Connect` 健康校验，不可用直接 panic（对齐 `packages/redis`），部署/配置问题早暴露。
- **任务阻塞**：Meili 写操作是异步任务（`TaskInfo`），封装内 `WaitForTask` 阻塞至 `succeeded` 才返回、失败上抛——杜绝"已确认但索引未生效"的静默丢同步；调用方据此决定 ACK/Nak。
- **幂等**：`EnsureIndex` 可重复调用（创建竞态以创建后探测结果为准）；upsert/delete 按主键天然幂等。

## posts 索引（DiscoverConsumer 维护）

| 项 | 值 |
| --- | --- |
| uid / 主键 | `posts`（`PostsIndex`）/ `post_id`（`PostsIndexPrimaryKey`） |
| 文档字段 | `post_id`、`title`、`full_text`、`author_id`、`created_at`（unix 毫秒）、`like_count` / `favourite_count` / `comment_count` / `view_count` |
| `searchableAttributes` | `title`、`full_text`（限定检索面） |
| `sortableAttributes` | `created_at`、`post_id`（"最新"模式 `sort: [created_at:desc, post_id:desc]` 的两个键都必须声明；Meili 对未声明的 sort 属性报 not sortable，主键不豁免） |

索引设置定义于 `packages/meili/index.go`（`PostsIndexSettings()`），入库/删除/建索引经领域接口 `UpsertPost` / `RemovePost` / `EnsurePostsIndex`（`DiscoverConsumer.Start()` 启动时幂等应用）。
