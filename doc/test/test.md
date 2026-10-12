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
| 类型 | 黑盒端到端：仅通过 ConnectRPC 调用已部署服务，不依赖内部实现；现有用例 `TestMediaServiceE2E`（上传→处理→分页→封禁）、`TestAuthServiceE2E`（注册→登录→换发→登出） |
| 目的 | 验证部署后的真实链路：服务装配、鉴权拦截器、DB / MinIO / NATS 全链路协作 |
| 依赖 | 已部署的服务进程、真实 PostgreSQL / MinIO / NATS；地址用 `AUTH_E2E_ADDR`、`MEDIA_E2E_ADDR`、`MEDIA_E2E_S3_*` 配置，服务不可达时自动跳过 |

代码：`test/e2e/`。对已部署的服务做黑盒测试，不依赖内部实现，仅通过 ConnectRPC 访问；
服务不可达时自动跳过，便于本地/CI 按需运行。


前置：对应服务已部署；auth e2e 需 PostgreSQL 中
`jwt.secret` 等加密配置与服务端一致。


注：e2e 产生的数据（media 资产、auth 测试账号）留在服务端数据库中不清理。

## 运行

```bash
go run ./scripts/dbinit                # 准备模板库
go test ./internal/media/v1/... -count=1
go test -short ./...                   # 跳过依赖真实数据库的用例
```

> **`sql/*.sql` 变更后必须重跑 `go run ./scripts/dbinit`**，否则测试将运行在旧 schema 上。
