# 全局约定与架构

## 项目简介

rednote 是一个仿小红书的图片社交论坛后端。核心链路围绕**图片**：客户端预签名直传 MinIO → 服务端异步校验/处理生成多规格变体 → 供帖子引用。

## 全局约定

| 约定 | 说明 |
| --- | --- |
| ID | 全局使用 **snowflake 64 位雪花 ID**（`packages/utils` 生成，`bwmarrin/snowflake`） |
| 时间 | 全局使用 **unix 毫秒时间戳**（`int64`，不使用 `time.Time` 落库） |
| 分页 | 使用**游标分页**，`limit` 最大 **1000**（proto 层 `Cursor.limit` 用 `lt = 1000` 校验） |
| 注释 / 提交 | 全局使用中文注释与提交信息 |
| ID 表示 | proto 层 ID 为**字符串**（雪花 ID 的字符串形式），DB 层为 `int64`，通过 `snowflake.ParseInt64/ParseString` 互转 |

## 技术栈

- **RPC**：Go + ConnectRPC（`connectrpc.com/connect`），proto 定义于 `proto/`，生成代码于 `gen/go/`
- **消息**：NATS JetStream（`nats.go`），配合 **Outbox 发件箱**保证事务一致性
- **ORM**：GORM + gorm/gen（生成类型安全查询 `gen/query`）
- **数据库**：PostgreSQL（生产），SQLite（本地/单元测试）
- **对象存储**：MinIO（S3 协议，`minio-go/v7`）
- **依赖注入**：Google Wire（`cmd/gen/wire.go` → `cmd/gen/wire_gen.go`）
- **配置**：Viper（`config/config.yaml`）
- **参数校验**：protovalidate（proto 内 `buf.validate` 规则）
- **日志**：标准库 `log/slog`（JSON），目标可观测栈 Loki + Promtail + Grafana

## 架构总览

```
                       ┌─────────────┐   预签名直传    ┌──────────┐
   客户端 ──ConnectRPC──▶  media 服务  │ ─────────────▶ │  MinIO   │
                       └─────┬───────┘                └────▲─────┘
                             │ 写 outbox（事务内）          │ 下载/上传
                             ▼                            │
                       ┌─────────────┐                ┌────┴─────┐
                       │ out_box 表  │                │ consumer │ 图片处理状态机
                       └─────┬───────┘                └────▲─────┘
                             │ 轮询                        │ 消费 JetStream
                             ▼                             │
                       ┌─────────────┐   PublishMsg   ┌────┴─────┐
                       │   poller    │ ─────────────▶ │ JetStream│
                       └─────────────┘                └──────────┘

   客户端 ──ConnectRPC──▶  auth 服务（注册/登录/JWT 换发/登出）
```

### 服务进程（`cmd/`）
| 进程 | 端口 | 职责 |
| --- | --- | --- |
| `cmd/media` | `:8080` | 媒体服务 ConnectRPC 入口 |
| `cmd/auth` | `:8081` | 认证服务 ConnectRPC 入口（接入 JWT 拦截器） |
| `cmd/post` | `:8082` | 帖子服务 ConnectRPC 入口 |
| `cmd/user` | `:8083` | 用户服务 ConnectRPC 入口 |
| `cmd/bff` | `:8084` | 面客聚合网关 ConnectRPC 入口（trace + JWT + validate 拦截器） |
| `cmd/consumer` | — | 消费者，处理 JetStream 中的图片/帖子消息 |
| `cmd/poller` | — | Outbox Poller，把发件箱消息投递到 JetStream |
| `cmd/gen` | — | Wire 注入器 + gorm/gen 代码生成（不作为服务运行） |

### 分层（每个业务模块 `internal/<mod>/`）
- `service.go` — ConnectRPC handler，薄层，委托给 biz
- `biz.go` — 业务逻辑（usecase），事务边界、状态流转、错误映射
- `repo.go` — 数据访问，基于 `gen/query`，接口 + 实现，支持 `WithTX`
- `models/` — GORM 数据模型（含 `ToProto`/`FromProto` 转换）

## 关键机制

### Outbox 事务性消息
业务库变更与"要发的消息"写在**同一事务**（写入 `out_box_entries`），由 `poller` 异步投递到 JetStream，保证"本地事务成功 ⇔ 消息最终发出"。延迟消息（如上传超时检测）通过 `send_at` 控制。详见 [internal/common/common.md](internal/common/common.md)。

### 链路追踪（trace_id）
`packages/trace` 在 ConnectRPC 请求头透传 `trace_id`，写入 context，并由 `packages/logger` 自动附加到日志；MQ 消息头同样携带 `trace_id`。详见 [packages/trace/trace.md](packages/trace/trace.md)。

### 引用计数
媒体资产 `ref_count` 记录被帖子等引用的次数，`ref_count <= 0` 且满足条件的资产由清理任务回收。详见 [internal/media/media.md](internal/media/media.md)。

## 目录结构

```
rednote/
├── cmd/            服务进程与代码生成入口
├── config/         配置结构与加载（Viper）
├── proto/          protobuf / ConnectRPC 服务与消息定义
├── gen/            生成代码（gen/go 连接与消息、gen/query GORM 查询）
├── internal/       业务模块（auth / user / media / post / bff / common / consumer / poller）
├── packages/       基础组件（db / mq / oss / jwt / trace / logger / utils / defaults / errmsg / redis）
├── sql/            建表与索引迁移脚本（幂等）
├── scripts/        dbinit（重建模板库）/ dbsync（同步表结构）
├── test/           测试基础设施（testdb 模板库克隆、e2e 黑盒测试）
├── doc/            项目文档（本目录）
└── build.sh        本地编译脚本（输出到 tmp/）
```

## 构建与代码生成

- **编译**：`./build.sh`（编译 `cmd/` 下除 `gen` 外的命令到 `tmp/`，支持 `RACE=1 ./build.sh`）
- **proto 生成**：`buf generate`（配置见 `buf.gen.yaml`，输出到 `gen/go`）
- **GORM 查询生成**：`go run ./cmd/gen/gorm`（从模型生成 `gen/query`）
- **Wire 生成**：`go generate ./cmd/gen`（`wire.go` → `wire_gen.go`）
