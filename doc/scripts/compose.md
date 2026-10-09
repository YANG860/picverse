# rednote 测试容器（Docker Compose）

一条命令拉起 PostgreSQL + NATS JetStream + MinIO 测试环境，含幂等初始化：

| 服务 | 镜像 | 端口 | 初始化 |
| --- | --- | --- | --- |
| `pg` | postgres:16-alpine | 5432 | 首次启动自动执行 `sql/*.sql`（001~005，挂载到 `/docker-entrypoint-initdb.d`） |
| `nats` | nats:2.10-alpine (`-js`) | 4222 / 8222 | `init-nats` 创建 stream `media`（subjects `media`, `media.>`） |
| `minio` | minio/minio | 9000 / 9001 | `init-minio` 创建 bucket（默认 `rednote`） |

## 快速开始

```bash
cp .env.example .env   # 填写真实凭据（.env 不入库）
docker compose up -d
docker compose ps      # 等待 pg/nats/minio 全部 healthy，init-* 服务 exited(0)
```

> 首次拉起或数据卷为空时，还需在本机重建测试模板库：`go run ./scripts/dbinit`

## 前提

- 目录结构保持仓库布局（`docker-compose.yml` 通过 `../../sql` 引用迁移脚本）：

```
<root>/
├── sql/                  # 迁移脚本，随仓库演进
└── scripts/compose/
    ├── docker-compose.yml
    ├── .env.example
    └── .env              # 真实凭据，不入库
```

- 部署到远程服务器时，将 `sql/` 与 `scripts/compose/` 按上述相对结构同步（如 `/opt/rednote/`）。
  **`sql/` 变更后须重新同步并重建模板库**（`go run ./scripts/dbinit`）。

## 凭据

- 全部经 `.env` 注入容器，**禁止提交 `.env`**。
- 凭据须与应用侧保持一致（三处同步修改）：
  - `config/config.yaml`（pg 密码 / S3 密钥）
  - `test/testdb/db.go`（`DefaultDSN`）
  - `test/e2e/media_e2e_test.go`（`defaultS3Key` / `defaultS3Secret`）
- 轮换凭据：改 `.env` → `docker compose up -d`（重建容器生效）→ 同步修改上述三处。

## 常用命令

```bash
docker compose up -d          # 拉起（含一次性 init 服务）
docker compose ps             # 状态（含健康检查）
docker compose logs -f nats   # 跟踪日志
docker compose restart pg     # 重启单个服务
docker compose down           # 停止并删除容器（数据卷保留）
docker compose down -v        # 停止并删除数据卷（清空全部数据，慎用）
```

## 幂等性与重建语义

- `init-nats` / `init-minio` 均幂等：stream/bucket 已存在时跳过，可随 `docker compose up -d` 重复执行。
- `pg` 的建表脚本仅在数据卷为空时执行一次；schema 变更请走 `go run ./scripts/dbinit` 重建测试模板库。
- 数据卷（`pgdata` / `natsdata` / `miniodata`）在 `down` 时保留，`down -v` 才会删除。
