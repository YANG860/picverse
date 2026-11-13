# 配置说明

配置通过 Viper 加载 `config/config.yaml`（`config.Init(path)`），反序列化为 `config.Config`，全局单例为 `config.Global`。服务进程通过 `-config` 指定配置目录（默认 `./`）。

## 配置结构总览

```yaml
snowflake:   # 雪花节点
database:    # PostgreSQL
cache:       # 实体/计数/时间线缓存（Redis/Noop）
logger:      # 日志
s3:          # MinIO / S3
jwt:         # JWT 签发
redis:       # Redis
jetstream:   # NATS JetStream
poller:      # Outbox Poller
consumer:    # 消费者
server:      # 各服务监听地址
services:    # 下游服务地址（服务发现）
meili:       # Meilisearch 搜索引擎
post:        # 帖子服务运行参数
bigv:        # 发帖侧分流判定（大V晋升线）
fanout:      # 收件箱保留期清理（维护 fanout_timelines 表大小）
hot:         # 热榜池维护任务与热度分（rn:v1:hot:posts）
recommend:   # 推荐流（seen 去重/打散/探索位）
```

## 各节详解

### snowflake — 雪花节点
| 键 | 类型 | 说明 |
| --- | --- | --- |
| `id` | int64 | 节点 ID（0–1023），多实例部署时需互不相同 |

### database — 数据库
| 键 | 类型 | 说明 |
| --- | --- | --- |
| `database.pg.url` | string | PostgreSQL 连接串，如 `postgres://user:pass@host:5432/db?sslmode=disable` |

### server — 各服务监听地址

多进程共用同一配置文件，各服务取自己的条目；缺失时该服务无法启动。

| 键 | 类型 | 说明 |
| --- | --- | --- |
| `auth` / `user` / `media` / `post` / `bff` / `discover` | string | 各服务监听地址，如 `:8081`（bff 为 `:8084`，discover 为 `:8085`） |

### services — 下游服务地址（服务发现）

`packages/rpc` 静态实现的地址源，BFF 等下游调用方经此解析地址（详见 [rpc](../packages/rpc/rpc.md)）。

| 键 | 类型 | 说明 |
| --- | --- | --- |
| `auth` / `user` / `media` / `post` / `discover` | string | 下游服务地址，`host:port` 或 `scheme://host:port`；同机 `127.0.0.1:端口`，k8s 填 Service DNS 名 |

### logger — 日志
见 [log.md](../log.md)。关键项：`level`、`file`、`out`、`rotate`、`maxsize`、`maxbackups`、`maxage`、`compress`、`buffersize`、`std`。

### s3 — 对象存储
| 键 | 类型 | 说明 |
| --- | --- | --- |
| `endpoint` | string | MinIO/S3 端点，如 `host:9000` |
| `access_key` / `secret_key` | string | 凭据 |
| `region` | string | 区域 |
| `use_ssl` | bool | 是否启用 TLS |
| `bucket` | string | 默认存储桶（不存在时自动创建） |

### jwt — JWT 签发
| 键 | 类型 | 说明 |
| --- | --- | --- |
| `secret` | string | HMAC-SHA256 签名密钥，**必填**，缺失启动 panic |
| `issuer` | string | 签发者（可选） |
| `access_expire` | duration | access token 有效期，默认 24h |
| `refresh_expire` | duration | refresh token 有效期，默认 24h |

> `config.yaml` 中出现的 `expire` 键与结构体字段不匹配（结构体为 `access_expire`/`refresh_expire`），未配置时按 `packages/defaults.TokenExpire`（24h）回退。

### redis — Redis
| 键 | 类型 | 说明 |
| --- | --- | --- |
| `addr` | string | 连接地址 |
| `password` | string | 密码（可空） |
| `db` | int | 库编号 |
| `pool_size` | int | 连接池大小，`0` 使用默认（10） |

### meili — Meilisearch 搜索引擎

搜推服务（discover）的索引同步与查询入口。

| 键 | 类型 | 说明 |
| --- | --- | --- |
| `url` | string | 服务地址，如 `http://127.0.0.1:7700` |
| `api_key` | string | API 密钥（MEILI_MASTER_KEY），必填，至少 16 字符；启动时连通性校验，失败 fail-fast |

### jetstream — 消息队列
| 键 | 类型 | 说明 |
| --- | --- | --- |
| `url` | string | NATS 连接串，如 `nats://host:4222` |

### poller — Outbox Poller
| 键 | 类型 | 说明 |
| --- | --- | --- |
| `workers` | int | worker 数量 |
| `interval` | duration | 轮询间隔 |
| `batch_size` | int | 每批拉取条数 |
| `max_retry` | int | 最大重试次数 |
| `publish_timeout` | duration | 单批发布超时 |

### consumer — 消费者
| 键 | 类型 | 说明 |
| --- | --- | --- |
| `consumer.media.name` | string | JetStream durable consumer 名称 |
| `consumer.media.max_retry` | int | 消息最大投递次数（MaxDeliver） |
| `consumer.media.working_dir` | string | 图片处理工作目录（不存在时自动创建） |
| `consumer.post.name` / `max_retry` / `working_dir` | — | post 消费者（同构） |
| `consumer.discover.name` / `max_retry` | — | 搜推消费者（索引同步/扇出，跑在 cmd/discover 进程内，无工作目录） |

> `config.yaml` 中 `consumer.media.worker_pool_size` 与当前结构体无对应字段，属预留/失效项。

### post — 帖子服务运行参数
| 键 | 类型 | 说明 |
| --- | --- | --- |
| `view_flush_interval` | duration | 浏览计数刷回周期（RedisPoller：计数器增量 → `posts.view_count`），`0` 使用默认（30s） |

### cache — 实体/计数/时间线缓存（packages/cache）

| 键 | 类型 | 说明 |
| --- | --- | --- |
| `enabled` | bool | 缓存总开关：false 不连接 redis（注入 Noop，行为等同无缓存） |
| `profile_ttl` | duration | 用户资料缓存 TTL（±10% 抖动），`0` 使用默认（30m） |
| `post_ttl` | duration | 帖子详情缓存 TTL（±10% 抖动），`0` 使用默认（30m） |
| `null_ttl` | duration | 空哨兵 TTL（防穿透），`0` 使用默认（60s） |
| `timeline_ttl` | duration | 作者发件箱缓存 key TTL（pull 现拉加速层，`AuthorTimelineCache`），`0` 使用默认（720h = 30 天，与 `fanout.retention` 对齐：零漏前提，改须同步） |
| `timeline_capacity` | int | 发件箱缓存条数保险丝（防事故刷帖撑爆内存），`0` 使用默认（5000）；窗口语义 = `fanout.retention` 时间截尾（承诺窗口内全量，短取即见底免回源），正常流量永不触发 |

### bigv — 发帖侧分流判定

| 键 | 类型 | 说明 |
| --- | --- | --- |
| `promote_followers` | int | 大V晋升线（粉丝数 ≥ 该值写发件箱缓存走 pull，否则扇出收件箱），`0` 使用默认（5000） |
| `mark_ttl` | duration | 大V标识 TTL（防抖动保持窗）：晋升后至少该时长维持大V供给路径，粉丝数抖动不切换供给线（发帖缓存连续）；跌破线后标识自然过期回归扇出，`0` 使用默认（7d） |

### fanout — 收件箱保留期清理

| 键 | 类型 | 说明 |
| --- | --- | --- |
| `retention` | duration | 收件箱保留期（按帖子时间 `post_created_at`，与游标同口径，过期行从收件箱视图消失），`0` 使用默认（720h = 30 天） |
| `cleanup_interval` | duration | 清理任务运行周期（FanoutCleaner，跑在 cmd/poller 进程内），`0` 使用默认（1h） |

### hot — 热榜池维护与热度分

热池 `rn:v1:hot:posts`（ZSET，member = post_id、score = 热度分）：热榜与推荐流共享候选池。
维护任务（HotPoolRefresher，跑在 cmd/poller 进程内）定时整池重建（入池/算分/淘汰/TTL 续期一步完成）。

| 键 | 类型 | 说明 |
| --- | --- | --- |
| `refresh_interval` | duration | 维护周期（整池重建），`0` 使用默认（10m） |
| `window` | duration | 候选时间窗（PUBLISHED 且窗口内；窗口外帖子自然出池），`0` 使用默认（168h = 7 天） |
| `ttl` | duration | 池 key TTL 兜底（维护停摆消亡，随任何写入续期），`0` 使用默认（30m） |
| `hard_cap` | int | 池容量事故保险丝（候选超限按分数截尾，正常永不触发），`0` 使用默认（500000） |
| `gravity` | float | 热度分时间衰减陡度（`score = engagement/(age_hours+2)^gravity`，越大越偏重新帖），`0` 使用默认（1.5） |
| `weights.*` | float | 热度分信号权重（`engagement = Σ w·ln(1+count)`，like/favourite/comment/view）；整组未配置使用默认组（1.0/1.5/2.0/0.1），单个 0 是合法权重 |

### recommend — 推荐流

`GetRecommendFeed` 读路径参数（热池候选 + seen 去重 + 同作者打散 + 探索位）。

| 键 | 类型 | 说明 |
| --- | --- | --- |
| `seen_ttl` | duration | seen 已曝光去重窗口（账本 `rn:v1:seen:{uid}` 的 ZSET 窗口与 key TTL，随记账滚动续期），`0` 使用默认（720h = 30 天） |
| `scan_batch_factor` | int | 候选扫描批次 = limit × factor（预留 seen/打散过滤跳过），`0` 使用默认（3） |
| `scatter_window` | int | 同作者打散滑窗（窗内同作者 ≤ 1），`0` 使用默认（3） |
| `explore_ratio` | float | 探索位比例（每页随机采样条数占比，至少 1 条），`0` 使用默认（0.1） |

## 敏感信息

`config.yaml` 内含数据库/对象存储/Redis/Meilisearch/JWT 的凭据。**提交前请勿将真实凭据纳入版本库**；生产环境建议改用环境变量或密钥管理方案注入。
