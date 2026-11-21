<div align="center">

# picverse

**一个以图片为核心的社交分享社区**

记录生活、分享图片、发现同好

[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![React](https://img.shields.io/badge/React-18-61DAFB?logo=react&logoColor=black)](https://react.dev/)
[![ConnectRPC](https://img.shields.io/badge/ConnectRPC-v1-1C2E8A)](https://connectrpc.com/)
[![Vite](https://img.shields.io/badge/Vite-8-646CFF?logo=vite&logoColor=white)](https://vite.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

</div>

---

## 简介

picverse 是一个图片社交平台：用户发布图文笔记、在推荐/热门/关注信息流中浏览内容、通过点赞、收藏、关注、评论与其他用户互动，并借助搜索发现感兴趣的内容。

后端采用 **Go 微服务 + BFF（Backend For Frontend）** 架构，通过 [ConnectRPC](https://connectrpc.com/) 对外提供统一契约；前端为 **React + Vite** 单页应用，媒体文件直传对象存储（预签名 URL），全链路自带草稿、审核与曝光去重等社区能力。

## ✨ 功能特性

- **账户体系**：注册 / 登录（JWT 双令牌 + 无感刷新）/ 登出 / 修改密码 / 编辑资料与头像
- **信息流**：推荐流（曝光去重）/ 热门 / 关注流，游标分页，稳定瀑布流布局
- **内容发布**：多图上传（直传 MinIO，预签名 URL）→ 草稿 → 发布 → 审核，带轮询进度
- **互动**：点赞 / 收藏 / 关注 / 多级评论（含楼中楼回复）
- **搜索**：基于 Meilisearch 的笔记全文搜索（综合 / 最新排序）
- **用户主页**：笔记与收藏列表、关注 / 粉丝、他人主页
- **媒体流水线**：上传后自动转码多档变体（216 / 540 / 1080），异步激活

## 🏗 架构

```
                         ┌──────────────────────────────────────────────┐
  Browser (HTTPS)        │                  应用层（tencent2）           │
  picverse.site ────────▶│  cloudflared ─▶ Caddy :8088                  │
                         │     ├─ /bff.v1.BFFService* ─▶ BFF :8084      │
                         │     └─ 其余 ─▶ 静态 SPA (dist)                │
  media.picverse.site ──▶│  cloudflared ──────────────┐                 │
                         └─────────────────────────── │ ────────────────┘
                                                      ▼ (Host 保留，过 SigV4)
                         ┌──────────────────────────────────────────────┐
                         │              媒体层（tencent1）               │
                         │              MinIO :9000（预签名直出）         │
                         └──────────────────────────────────────────────┘

  BFF :8084 ──┬── auth     :8081  账户 / JWT 签发
              ├── user     :8083  用户资料 / 关系
              ├── post     :8082  笔记 / 评论 / 互动
              ├── media    :8080  媒体资产 / 预签名
              └── discover :—     搜索索引查询
                     │
  NATS JetStream ◀───┴──▶ consumer（media 转码 / post 流转 / discover 索引 / fanout）
                     │
  PostgreSQL  ·  Redis  ·  Meilisearch  ·  MinIO
```

- **BFF** 是前端唯一入口（39 个 RPC，`proto/bff/v1`），聚合下游微服务并统一鉴权、视图裁剪
- **媒体链路**：客户端持预签名 URL 直传 MinIO → NATS 消息驱动变体转码与激活 → 阅读时按场景签发预签名读 URL
- **最终一致**：发布走「两步流」（CreateDraft → PublishDraft），事件经 outbox + poller 兑现进 JetStream

## 🛠 技术栈

| 层 | 技术 |
| --- | --- |
| 后端 | Go 1.26 · ConnectRPC · connect-middleware（JWT 鉴权/校验） |
| 消息 | NATS JetStream（outbox + poller 事务兑现） |
| 存储 | PostgreSQL · Redis · MinIO（S3）· Meilisearch |
| 前端 | React 18 · Vite 8 · TypeScript 7 · Tailwind CSS 4 · shadcn/ui · TanStack Query · zustand |
| 契约 | buf（protobuf）→ Go 服务端 + TS 客户端双端代码生成 |
| 部署 | Docker Compose（数据层）· Cloudflare Tunnel · Caddy |

## 📁 目录结构

```
.
├── cmd/                  # 可执行入口：bff / auth / user / post / media / discover
│                         #           / consumer / poller / gen（代码生成器）
├── internal/             # 各服务业务实现（discover 索引、consumer 消费者等）
├── packages/             # 公共库（auth JWT、oss 对象存储、mq、errmsg、defaults…）
├── proto/                # ConnectRPC 契约（bff/v1 为前端唯一入口契约）
├── gen/                  # buf 生成的 Go 代码
├── web/                  # 前端（React + Vite），web/src/gen 为 TS 契约产物
├── sql/                  # 数据库初始化脚本（compose initdb 自动挂载）
├── config/               # 服务配置样例（config.yaml，凭据请自行替换）
├── scripts/compose/      # 本地数据层一键起（pg/nats/minio/redis/meilisearch）
├── deploy/               # 生产部署编排（tencent1 媒体层 / tencent2 主站）
├── test/                 # Go 测试
└── doc/                  # 项目文档（general.md 为总览）
```

## 🚀 快速开始

### 前置

- Go ≥ 1.26、Node ≥ 20、pnpm 10、Docker + Docker Compose
- buf（生成契约代码）

### 1. 启动数据层

```bash
cd scripts/compose
cp .env.example .env   # 按需修改凭据
docker compose up -d   # pg / nats / minio / redis / meilisearch + 两个 init 容器
```

### 2. 生成契约代码

```bash
buf generate   # 产出 gen/（Go）与 web/src/gen/（TS）
```

### 3. 配置并启动服务

```bash
cp config/config.yaml.example config/config.yaml   # 填入你的数据层地址与凭据
./build.sh                                          # 编译全部服务到 tmp/
# 依次启动（生产用 systemd/nohup，本地直接跑二进制）：
tmp/auth/auth       -config ./config/
tmp/user/user       -config ./config/
tmp/post/post       -config ./config/
tmp/media/media     -config ./config/
tmp/discover/discover -config ./config/
tmp/consumer/consumer -config ./config/   # NATS 消费者（转码/索引/fanout）
tmp/poller/poller   -config ./config/     # outbox 兑现器
tmp/bff/bff         -config ./config/     # 前端唯一入口 :8084
```

> ⚠️ consumer 需在 NATS stream 就绪后启动，否则 durable 订阅会静默创建失败（详见 `doc/general.md` 运维坑）。

### 4. 启动前端

```bash
cd web
pnpm install
pnpm dev        # http://localhost:5173，/bff.v1.BFFService 反代到 :8084
```

开发期另有 `/mock/*` 路由展示纯 UI（不调 API），用于视觉验证。

## 📡 契约与代码生成

- 契约在 `proto/`（`buf.yaml` + `buf.gen.yaml` 管理），**BFF 契约（`proto/bff/v1`）是前端唯一入口**
- `buf generate` 双端产出：Go（`gen/`）、TypeScript（`web/src/gen/`，protobuf-es v2 + ConnectRPC Web 客户端）
- 变更契约后请同步 `doc/proto/` 下文档

## 🧪 测试

```bash
go test ./...        # Go 单测/集成测试
cd web && pnpm lint  # 前端 lint（oxlint）；tsc --noEmit 亦可
```

## 🚢 部署

生产为双机部署（Docker Compose 数据层 + systemd/nohup 服务进程 + Cloudflare Tunnel 入口）：

- **主站机**：PG / NATS / Redis / Meilisearch + 全部 Go 服务 + Caddy + cloudflared
- **媒体机**：MinIO（跨机直传，预签名 URL 经独立域名 `media.picverse.site` 出口）

完整拓扑、配置与踩坑记录见 [`doc/general.md`](doc/general.md)。

## 📖 文档

| 文档 | 内容 |
| --- | --- |
| [`doc/general.md`](doc/general.md) | 项目总览、部署拓扑、运维坑 |
| [`doc/doc.md`](doc/doc.md) | 文档索引 |
| [`doc/proto/`](doc/proto/) | 契约说明 |
| [`doc/internal/`](doc/internal/) `doc/packages/` | 内部模块说明 |

## 📄 License

[MIT](LICENSE) © picverse contributors
