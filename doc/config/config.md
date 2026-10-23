# 配置说明

配置通过 Viper 加载 `config/config.yaml`（`config.Init(path)`），反序列化为 `config.Config`，全局单例为 `config.Global`。服务进程通过 `-config` 指定配置目录（默认 `./`）。

## 配置结构总览

```yaml
snowflake:   # 雪花节点
database:    # PostgreSQL
logger:      # 日志
s3:          # MinIO / S3
jwt:         # JWT 签发
redis:       # Redis
jetstream:   # NATS JetStream
poller:      # Outbox Poller
consumer:    # 消费者
server:      # 各服务监听地址
services:    # 下游服务地址（服务发现）
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
| `auth` / `user` / `media` / `post` | string | 各服务监听地址，如 `:8081` |

### services — 下游服务地址（服务发现）

`packages/rpc` 静态实现的地址源，BFF 等下游调用方经此解析地址（详见 [rpc](../packages/rpc/rpc.md)）。

| 键 | 类型 | 说明 |
| --- | --- | --- |
| `auth` / `user` / `media` / `post` | string | 下游服务地址，`host:port` 或 `scheme://host:port`；同机 `127.0.0.1:端口`，k8s 填 Service DNS 名 |

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

> `config.yaml` 中 `consumer.media.worker_pool_size` 与当前结构体无对应字段，属预留/失效项。

## 敏感信息

`config.yaml` 内含数据库/对象存储/Redis/JWT 的凭据。**提交前请勿将真实凭据纳入版本库**；生产环境建议改用环境变量或密钥管理方案注入。
