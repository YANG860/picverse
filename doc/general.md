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
| `cmd/discover` | `:8085` | 搜推服务 ConnectRPC 入口（关注流/推荐流/搜索） |
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
├── web/            前端（React + Vite + shadcn/ui，ConnectRPC 客户端对接 BFF；TS 生成代码在 web/src/gen）
├── internal/       业务模块（auth / user / media / post / bff / discover / common / consumer / poller）
├── packages/       基础组件（db / mq / oss / jwt / trace / logger / utils / defaults / errmsg / redis）
├── sql/            建表与索引迁移脚本（幂等）
├── scripts/        dbinit（重建模板库）/ dbsync（同步表结构）/ dbtool（清空测试基线：表+流+消费者）/ jsprobe（JetStream 状态探测）/ compose
├── test/           测试基础设施（testdb 模板库克隆、e2e 黑盒测试）
├── doc/            项目文档（本目录）
└── build.sh        本地编译脚本（输出到 tmp/）
```

## 构建与代码生成

- **编译**：`./build.sh`（编译 `cmd/` 下除 `gen` 外的命令到 `tmp/`，支持 `RACE=1 ./build.sh`）
- **proto 生成**：`buf generate`（配置见 `buf.gen.yaml`，Go 产物输出到 `gen/go`，前端 TS 产物输出到 `web/src/gen`，含 `include_imports` 生成的 protovalidate 依赖）
- **GORM 查询生成**：`go run ./cmd/gen/gorm`（从模型生成 `gen/query`）
- **Wire 生成**：`go generate ./cmd/gen`（`wire.go` → `wire_gen.go`）

## 前端（`web/`）

- **技术栈**：React 18 + Vite + TypeScript + Tailwind（shadcn/ui base-nova + tweakcn 主题）+ TanStack Query + zustand；包管理 pnpm 10
- **契约与鉴权**：TS 生成代码在 `web/src/gen`（`buf generate` 产出，与 `gen/go` 同源）；`src/api/transport.ts` 为 ConnectRPC transport（鉴权拦截器按方法注入 Bearer token，1003 时单飞刷新并重放一次）；错误码镜像 `packages/errmsg`（`src/api/errors.ts`）
- **跨域方案**：BFF 未开 CORS，开发期 Vite 代理同源转发 `/bff.v1.BFFService` → BFF（默认 `106.53.172.186:8084`，`VITE_BFF_PROXY_TARGET` 可覆盖）
- **路由**：`/login` `/register`（真实 API）；`/`（账号首页：GetMyProfile + 退出登录）；`/mock/*`（纯合成数据的视觉验证页：信息流 / 详情 / 发布 / 主页，未接入真实 API 的页面统一导流至此）
- **命令**（`web/` 下）：`pnpm dev` 开发、`pnpm build` 构建、`pnpm lint` 检查

### 前端对接进度

- **已接真实 API**：登录/注册/登出、账号页（GetMyProfile）、信息流（推荐/热门/关注，游标分页）、帖子详情（GetPost）、评论浏览与发布（GetComments/GetReplies/AddComment/ReplyComment/DeleteComment）、互动（点赞/收藏/关注）、用户主页（GetUserProfile、GetMyPosts/GetUserPosts、收藏聚合、关注/粉丝列表弹层）
- **互动态判态**：`GetMyLikedPostIDs` / `GetMyFavouritePostIDs` / `GetFollowingList` 提前预取集合本地判态，不逐请求查询；写操作幂等、响应确定态回填本地集合
- **推荐流空页**：曝光去重整页为空但游标有后续（短取≠到底），前端自动续拉跳过空页
- **发布流**（/publish）：UploadMedia 预签名 PUT 直传 MinIO → GetMediaAssetStatus 轮询 UPLOADING→ACTIVE → CreateDraft（首图即封面，话题存 metadata JSON）→ PublishDraft（DRAFT→AUDITING）；发布链路依赖 **consumer + poller 进程**（media_consumer 处理媒体：上传确认消息立即投递（对象未到时退避重投）+ 同一次投递内联完成三档变体生成；poller 兑现事务 outbox 到 NATS，interval 500ms（每跳平均等待 interval/2，短链路按跳数累计））——测试服务器已部署运行，勿在开发机重复启动（同名 durable consumer 会竞争消费）
- **搜索**（/search）：SearchPosts（关键词 + 综合/最新排序，offset 游标），顶栏搜索框回车直达；URL `?q=&sort=` 驱动可分享；「最新」排序依赖 Meili posts 索引 sortableAttributes（created_at/post_id），discover_index_consumer 启动时幂等 EnsurePostsIndex 应用（此前无调用方，索引由 UpsertPost 隐式创建缺 settings，「最新」排序会 internal error——存量环境需应用一次 settings 或重启 consumer）
- **编辑资料**（自己主页「编辑资料」弹层）：昵称/简介走 UpdateMyProfile（部分更新：仅传变更字段，空 patch 会被判 invalid argument——无变更须不调用）；头像走 UploadMedia 直传 → 等 ACTIVE → UpdateMyAvatar（BFF 校验归属 + 引用计数换绑旧资产）
- **UI 占位图**：动态流光 mesh 横幅（features/ui/GradientBanner：3-4 个大半径柔光斑叠加 + 22-32s 超慢漂移动画，prefers-reduced-motion 时静止；多彩低饱和四组色板按 seed 轮换，见 lib/placeholder.ts MESH_PALETTES）；无头像时 avatarPlaceholder（渐变圆+首字）；mock 内容图（gradientImage）低饱和
- **仍为 mock**：/mock/* 保留作视觉稿（发布器/主页已有真实版 /publish、/user/:uid）
- **环境注意**：MinIO 社区版无桶级 CORS（仅 AIStor），浏览器直传走全局配置 MINIO_API_CORS_ALLOW_ORIGIN（compose 已配 "*"）；发布轮询用 Worker 定时器规避 Chrome 空闲页 timer 唤醒节流

## 部署（picverse.site，Cloudflare Tunnel）

- **入口**：https://picverse.site → CF Tunnel（cloudflared，locally-managed，`/etc/cloudflared/config.yml`）
- **ingress**：`picverse.site` → Caddy `:8088`（静态 `/srv/picverse/web` + `/bff.v1.BFFService/*` 反代 `localhost:8084`）；`media.picverse.site` → MinIO `:9000`（保留原始 Host 过 SigV4 签名校验）
- **media 域名**：BFF 调 media 服务 `SignMediaFileURLs` 签发预签名 URL，`s3.public_endpoint: media.picverse.site` + `public_use_ssl: true`（SigV4 canonical host 参与签名，签发时以对外域名为 endpoint；**改该配置需重启 media 服务**，BFF 不是签发方）
- **DNS**：`picverse.site` / `media.picverse.site` 均为 CNAME → `<tunnel-id>.cfargotunnel.com`（橙云代理）
- **cloudflared 已知限制**：ingress 不支持 `file://` 静态直出（上游 PR #1799 未合并），静态与 API 反代由 Caddy 承担；`file://` 写法会报 invalid address
- **服务进程**（远程 `/home/ubuntu/rednote/`，nohup 裸进程 + `/tmp/bff.log` `/tmp/media.log`）：`bff`（:8084，BFF 聚合）、`media`（:8080，媒体 RPC + 签发）；config 统一 `config/config.yaml`

## 生产部署（picverse.site × 双机）

- **拓扑（按延迟敏感度拆，跨地区无内网）**：
  - **tencent2**（106.53.172.186，主站+数据）：PG/NATS/Redis/Meili（`deploy/tencent2/docker-compose.yml`）+ 8 服务（bff/media/auth/user/post/discover/consumer/poller，nohup 裸进程）+ Caddy :8088 + cloudflared 入口
  - **tencent1**（81.70.152.142，媒体+监控）：MinIO（`deploy/tencent1/docker-compose.yml`，含 minio-init 建桶）；跨机仅发布/图片读写（安全组放行 9000）
- **凭据**：全部强随机（`/tmp/prod-secrets` 生成，compose .env + config/config.yaml 注入）；Redis 有 requirepass、NATS 有 --auth token（连接串 `nats://<token>@host`）
- **media 域名**：`media.picverse.site`（CNAME → tunnel）→ cloudflared → **tencent1:9000**；`s3.public_endpoint=media.picverse.site` 签名 host 稳定（**改配置须重启 media 服务**，签发方是 media 服务不是 BFF）

### 运维坑（踩过的）

1. **compose `../../sql` 相对路径**：部署目录变化后 initdb 挂载断链 → 一张表都不建且无报错。已改 `./sql` 自包含（`deploy/tencent2/sql/`）
2. **consumer 启动时序竞态**：NATS stream 未就绪时 consumer 创建 durable **失败静默**（日志只见 started，无消费）。症状=stream 积压 + `consumer ls` 为 No Consumers；修复=重启 consumer。**compose up 后必须确认消费正常**（发一条测试消息或查 `nats consumer ls media`）
3. **默认头像资产需预置**（Register 硬依赖 `defaults.AvatarAssetID=2115637161121284096` 的 216 变体）：清库后需 ① MinIO 上传 `media/2115637161121284096/216.png` ② 插 `media_assets`（status=3 ACTIVE）+ `media_files`（type=2）行；缺失时注册报 1002 resource not found
4. **8 服务清单**：bff/media/auth/user/post/discover/consumer/poller（漏启任一即半残；启动脚本按序 nohup）
