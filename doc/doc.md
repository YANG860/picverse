# rednote 文档索引

rednote 是一个仿小红书的图片社交论坛后端，基于 Go + ConnectRPC + NATS JetStream + GORM/PostgreSQL + MinIO 构建。

本目录采用与代码镜像的结构组织文档。阅读顺序建议：`general.md`（全局约定与架构）→ 各模块文档。

## 目录

### 顶层
| 文档 | 说明 |
| --- | --- |
| [general.md](general.md) | 全局约定、技术栈、架构总览、目录结构 |
| [log.md](log.md) | 日志体系（logger 包、trace 关联、目标可观测栈） |
| [proto/proto.md](proto/proto.md) | proto 消息与错误返回规范（Result / fail / err 约定） |
| [config/config.md](config/config.md) | 配置项参考（config.yaml） |

### internal — 业务模块
| 文档 | 说明 |
| --- | --- |
| [internal/auth/auth.md](internal/auth/auth.md) | 认证服务：注册 / 登录 / Token 换发 / 登出 |
| [internal/media/media.md](internal/media/media.md) | 媒体服务：上传、异步处理流水线、引用计数、清理 |
| [internal/common/common.md](internal/common/common.md) | 公共模型与 Outbox（事务性发件箱） |
| [internal/consumer/consumer.md](internal/consumer/consumer.md) | 媒体消费者：JetStream 消费 + 图片处理状态机 |
| [internal/poller/poller.md](internal/poller/poller.md) | Outbox Poller：轮询发件箱投递到 JetStream |

### packages — 基础组件
| 文档 | 说明 |
| --- | --- |
| [packages/db/db.md](packages/db/db.md) | GORM 连接（PG / SQLite）、日志适配、语义化错误 |
| [packages/mq/mq.md](packages/mq/mq.md) | NATS JetStream 客户端、事务性消息（Send / SendTX） |
| [packages/oss/oss.md](packages/oss/oss.md) | S3 / MinIO 对象存储封装、预签名 URL |
| [packages/jwt/jwt.md](packages/jwt/jwt.md) | JWT 签发 / 校验、Claims、ConnectRPC 拦截器 |
| [packages/trace/trace.md](packages/trace/trace.md) | 链路追踪（trace_id 透传）与拦截器 |
| [packages/logger/logger.md](packages/logger/logger.md) | slog 日志工厂、缓冲/轮转输出、trace 关联 |
| [packages/utils/utils.md](packages/utils/utils.md) | 工具集：雪花 ID、S3 key、协程池 |
| [packages/defaults/defaults.md](packages/defaults/defaults.md) | 全局默认常量 |
| [packages/errmsg/err.md](packages/errmsg/err.md) | 可枚举业务错误码 |

### test — 测试
| 文档 | 说明 |
| --- | --- |
| [test/test.md](test/test.md) | 测试体系总览（模板库克隆、单元/集成、E2E） |
| [test/testdb/testdb.md](test/testdb/testdb.md) | 模板库克隆独立测试库（templateDB 方案） |
| [test/testsnowflake/testsnowflake.md](test/testsnowflake/testsnowflake.md) | 测试用雪花节点初始化 |
| [test/testoss/testoss.md](test/testoss/testoss.md) | `oss.S3Repo` 本地内存模拟 |
| [test/testmq/testmq.md](test/testmq/testmq.md) | `mq.MQClient` 本地内存模拟 |
| [test/e2e/e2e.md](test/e2e/e2e.md) | 部署后服务的黑盒端到端测试 |

## 模块完成度

文档聚焦于**已实现/可用**的模块。以下模块仍在施工中，暂不展开文档：

- **post（帖子/用户资料）**：proto 与数据模型已定义，业务与仓储层多为脚手架（`ErrNotImplemented`），待实现。
- **redis**：仅封装了客户端与健康检查，业务接口待补全。
