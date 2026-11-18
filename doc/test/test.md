# test — 测试体系

## 测试类型

| 类型 | 位置 | 依赖 | 说明 |
| --- | --- | --- | --- |
| 单元/集成测试 | `internal/*/v1/*_test.go`、`packages/*/…_test.go` | 真实 PostgreSQL（克隆库）,内存mock模拟等 | 存储层、拦截器等 |
| E2E 黑盒测试 | `test/e2e/` | 已部署的服务 | 仅通过 ConnectRPC 访问，服务不可达时自动跳过 |

## 单元测试

| 维度 | 说明 |
| --- | --- |
| 类型 | ① 纯逻辑测试（无依赖）：验证校验/转换等纯函数，如 `ValidateName` / `ValidateBio` |
|  | ② 存储集成测试（`internal/*/v1/repo_test.go`）：验证 SQL、事务、outbox 落库等存储语义 |
|  | ③ 业务逻辑测试（`internal/*/v1/biz_test.go`）：验证业务分支与外部编排（错误码、参数校验、消息/对象操作） |
| 目的 | 确定性、快速地覆盖存储语义与业务分支；数据库按用例克隆隔离，MQ/S3 用内存模拟注入，天然支持 `t.Parallel()` |
| 依赖 | [testdb](testdb.md)（模板库克隆真实 PG）、[testsnowflake](testsnowflake.md)（雪花节点）、[testmq](testmq.md)（`mq.MQClient` 模拟）、[testoss](testoss.md)（`oss.S3Repo` 模拟） |

## E2E 黑盒端到端测试

| 维度 | 说明 |
| --- | --- |
| 类型 | 黑盒端到端：仅通过 ConnectRPC 调用已部署服务，不依赖内部实现；现有用例 `TestMediaServiceE2E`（上传→处理→分页→封禁）、`TestAuthServiceE2E`（注册→登录→换发→登出）、`TestPostServiceE2E`（建草稿→改文本/媒体→发布→可见性→统计→删除/封禁）、`TestUserServiceE2E`、`TestDiscoverFeedE2E`（发帖→扇出→关注流/热榜/推荐见）、`TestBFFFeedE2E`（面客 PostCard + 会话注入 + seen 去重） |
| 目的 | 验证部署后的真实链路：服务装配、鉴权拦截器、DB / MinIO / NATS 全链路协作 |
| 依赖 | 已部署的服务进程、真实 PostgreSQL / MinIO / NATS；地址用 `AUTH_E2E_ADDR`、`MEDIA_E2E_ADDR`、`MEDIA_E2E_S3_*`、`POST_E2E_ADDR`、`USER_E2E_ADDR`、`DISCOVER_E2E_ADDR`、`BFF_E2E_ADDR` 配置，服务不可达时自动跳过 |

代码：`test/e2e/`。对已部署的服务做黑盒测试，不依赖内部实现，仅通过 ConnectRPC 访问；
服务不可达时自动跳过，便于本地/CI 按需运行。
共享工具集中在 `test/e2e/common_test.go`：地址解析（`e2eAddr`，各服务 `*_E2E_ADDR` 环境变量覆盖）、
可达性探测（`mustReachable`）、业务错误断言（`assertBizErr`）与测试数据构造（`e2eID` / `e2eMediaFile` /
`activeMediaFiles`）；各服务用例文件只保留自身链路步骤与专属 helper。

**媒体前置（发布用例的硬约束）**：`audit_post` 会校验帖内媒体资产全部 `ACTIVE` 才允许发布，
不可用即回退 `DRAFT`。因此凡是会走到"发布"的用例（post / discover / bff）必须用 `activeMediaFiles`
经 media 服务**真上传**并等到 `ACTIVE`（`e2eMediaFile` 伪造 asset_id 只适用于不经过审核的路径，
如用户头像、只建草稿/删帖回收）；这些用例要求 media + consumer + poller 一并部署，单个用例约 20~70s。

**共享环境的断言口径**（测试库会累积历史数据，断言必须只依赖确定性语义）：

| 断言对象 | 口径 |
| --- | --- |
| 热榜 | 断言"入池"（按 offset 翻页扫池可见），不断言"前十名"——池含 7d 窗口历史帖，新帖热度≈0 排不进前列 |
| 推荐流 | 只断言 seen 去重与页间不重复；探索位是随机采样，"首轮必见本轮新帖"在数百条池内不成立（仅日志记录） |
| 统计列（`SyncPostStat`） | 计数列走短 TTL 读缓存且写路径不失效：面客读取按"≤ `stat_ttl`（默认 1m）内收敛"轮询验证，不断言写后立即读到新值 |
| 列表游标 | 只有满页才返回游标，半页/空页游标为空（用 `Cursor{Limit:1}` 验证满页分支） |


前置：对应服务已部署；auth e2e 需 PostgreSQL 中
`jwt.secret` 等加密配置与服务端一致。

### post 服务远程部署（测试环境）

post e2e 的测试环境部署在测试服务器 `tencent2`（106.53.172.186，ssh 别名见 `~/.ssh/config`）：

```bash
# 1. 本地构建 linux 二进制并上传（依赖容器 rednote-pg/nats/minio 已在服务器上运行）
go build -o /tmp/post-build ./cmd/post
ssh tencent2 'mkdir -p ~/rednote/config ~/rednote/logs'
scp /tmp/post-build tencent2:~/rednote/post
scp config/config.yaml tencent2:~/rednote/config/config.yaml

# 2. sql/*.sql 变更后同步目标库（幂等，含存量库补列）
go run ./scripts/dbsync -db prod

# 3. 启动（监听 :8082）
ssh tencent2 'cd ~/rednote && chmod +x post && nohup ./post -config ./config/ > post.log 2>&1 &'

# 4. 跑 e2e：安全组未放行 8082，本地经 SSH 隧道访问
ssh -f -N -L 8082:127.0.0.1:8082 tencent2
go test ./test/e2e/ -run TestPostServiceE2E -count=1 -v
```

注：e2e 产生的数据（帖子草稿/软删记录、outbox 消息、auth 测试账号、media 资产）留在服务端数据库中不清理。


注：e2e 产生的数据（media 资产、auth 测试账号）留在服务端数据库中不清理。

## 运行

```bash
go run ./scripts/dbinit                # 准备模板库
go test ./internal/media/v1/... -count=1
go test -short ./...                   # 跳过依赖真实数据库的用例
```

> **`sql/*.sql` 变更后必须重跑 `go run ./scripts/dbinit`**，否则测试将运行在旧 schema 上。
