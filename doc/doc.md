# rednote 文档索引

rednote 是一个仿小红书的图片社交论坛后端，基于 Go + ConnectRPC + NATS JetStream + GORM/PostgreSQL + MinIO 构建。

本目录采用与代码镜像的结构组织文档。阅读顺序建议：`general.md`（全局约定与架构）→ 各模块文档。

## 目录

### 顶层
| 文档 | 说明 |
| --- | --- |
| [general.md](general.md) | 全局约定、技术栈、架构总览、目录结构 |
| [log.md](log.md) | 日志体系（logger 包、trace 关联、分级与记录约定、目标可观测栈） |
| [proto/proto.md](proto/proto.md) | proto 消息与错误返回规范（Result / fail / err 约定） |
| [config/config.md](config/config.md) | 配置项参考（config.yaml） |

### internal — 业务模块
| 文档 | 说明 |
| --- | --- |
| [internal/auth/auth.md](internal/auth/auth.md) | 认证服务：注册 / 登录 / Token 换发 / 登出 |
| [internal/user/user.md](internal/user/user.md) | 用户服务：资料增改（含独立头像更新）、关注关系、封禁 |
| [internal/media/media.md](internal/media/media.md) | 媒体服务：上传、异步处理流水线、引用计数、清理 |
| [internal/common/common.md](internal/common/common.md) | 公共模型与 Outbox（事务性发件箱） |
| [internal/consumer/consumer.md](internal/consumer/consumer.md) | 媒体消费者：JetStream 消费 + 图片处理状态机 |
| [internal/poller/poller.md](internal/poller/poller.md) | Outbox Poller：轮询发件箱投递到 JetStream |

### packages — 基础组件
| 文档 | 说明 |
| --- | --- |
| [packages/db/db.md](packages/db/db.md) | GORM 连接（PG / SQLite）、日志适配、语义化错误 |
| [packages/rpc/rpc.md](packages/rpc/rpc.md) | 下游 connectrpc 客户端、服务发现传输层（负载均衡 / 故障转移） |
| [packages/mq/mq.md](packages/mq/mq.md) | NATS JetStream 客户端、事务性消息（Send / SendTX） |
| [packages/oss/oss.md](packages/oss/oss.md) | S3 / MinIO 对象存储封装、预签名 URL |
| [packages/jwt/jwt.md](packages/jwt/jwt.md) | JWT 签发 / 校验、Claims、ConnectRPC 拦截器 |
| [packages/trace/trace.md](packages/trace/trace.md) | 链路追踪（trace_id 透传）与拦截器 |
| [packages/logger/logger.md](packages/logger/logger.md) | slog 日志工厂、缓冲/轮转输出、trace 关联 |
| [packages/utils/utils.md](packages/utils/utils.md) | 工具集：雪花 ID、S3 key、协程池、JSON 解析、格式校验 |
| [packages/defaults/defaults.md](packages/defaults/defaults.md) | 全局默认常量 |
| [packages/errmsg/err.md](packages/errmsg/err.md) | 可枚举业务错误码 |
| [packages/cache/cache.md](packages/cache/cache.md) | 缓存层设计（Cache-Aside + MQ 事件失效，**设计定稿待实现**） |

### design — 设计稿（施工中，未实现）
| 文档 | 说明 |
| --- | --- |
| [design/comment.md](design/comment.md) | 评论服务 0→1 设计：两级楼中楼、点赞、删除/封禁回收、帖子计数联动 |

### test — 测试
| 文档 | 说明 |
| --- | --- |
| [test/test.md](test/test.md) | 测试体系总览（模板库克隆、单元/集成、E2E） |
| [test/testdb.md](test/testdb.md) | 模板库克隆独立测试库（templateDB 方案） |
| [test/testsnowflake.md](test/testsnowflake.md) | 测试用雪花节点初始化 |
| [test/testoss.md](test/testoss.md) | `oss.S3Repo` 本地内存模拟 |
| [test/testmq.md](test/testmq.md) | `mq.MQClient` 本地内存模拟 |

## 模块完成度

文档聚焦于**已实现/可用**的模块（`design/` 下为未实现模块的设计稿）。以下模块仍在施工中，暂不展开文档：


- **redis**：仅封装了客户端与健康检查，业务接口待补全。
- **cache**：缓存层设计已定稿（见 [packages/cache/cache.md](packages/cache/cache.md)），代码尚未实现。
