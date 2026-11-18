# rpc — 下游 connectrpc 客户端与服务发现传输层

代码：`packages/rpc/`（`provider.go`、`transport.go`、`balancer.go`、`client.go`、`servererr.go`）。

为 BFF 等下游调用方构造 connectrpc 客户端：统一挂**服务发现传输层**（发现 / 负载均衡 / 故障转移）与 **trace / jwt 拦截器**，wire 装配后注入各服务的 `DownstreamRepo` / consumer 等下游调用方；另提供**服务端错误出口映射** `MapServerError`（见文末）。

## ServiceName — 服务逻辑名

| 常量 | 值 |
| --- | --- |
| `ServiceAuth` | `"auth"` |
| `ServiceUser` | `"user"` |
| `ServiceMedia` | `"media"` |
| `ServicePost` | `"post"` |
| `ServiceDiscover` | `"discover"` |

调用方按名字解析地址，不感知具体实例。

## EndpointProvider — 服务地址解析

| 方法 | 说明 |
| --- | --- |
| `Resolve(ctx, service) ([]string, error)` | 返回 service 当前可用的**地址池**（非单地址：静态多实例场景池内轮询；k8s 场景池退化为 1 个） |

### StaticProvider（初版实现）

`NewStaticProvider(cfg)` 从 `config.services`（见 [config](../../config/config.md)）构造静态地址提供者：

- 四个下游服务地址均**必填**，缺失启动即 panic（fail-fast，与 `packages/redis`、`packages/oss` 的配置校验风格一致，避免运行期才发现配置残缺）；
- `cfg == nil` 同样 panic；
- 未知服务名 `Resolve` 返回错误；
- 上 k8s 后由 Service DNS 实现替换（config 直接填 DNS 名，无需改代码）。

## Transport — 服务发现传输层

`NewTransport(provider, balancer, service, lf, opts...)` 实现 `http.RoundTripper`，是 connectrpc 客户端挂接发现 / 负载均衡 / 故障转移的**唯一集成点**（connect 基于 `net/http`，不走 grpc-go 的 resolver/balancer 体系）。每个 Transport 绑定**单个服务**。

### 行为

1. **解析 + 选实例**：每次请求解析服务地址池，先剔除冷却中的实例构建候选集，池内按负载均衡策略（默认轮询）选实例。
2. **故障转移（仅拨号失败）**：拨号失败（`net.OpError{Op: "dial"}`，请求**确定未到达**服务端）→ 冷却该实例并转移到下一实例；同一请求内不重复尝试同一实例。
3. **不重复写**：收到任何响应（含 5xx）后**不再重试**，错误原样返回——写操作不会因重试产生重复写。非拨号错误（TLS 握手后错误 / 超时 / 响应中途中断等）同理，请求可能已被服务端处理。
4. **冷却不熔断**：全部实例处于冷却期时仍按轮询强制尝试，不因健康信息滞后拒绝请求（冷却只是优选信号而非熔断）。全部实例拨号失败时返回含 `failover` 的错误（不悬挂）。
5. **请求克隆**：每次尝试克隆请求发往指定实例，避免多次尝试间共享可变 URL；`req.Host`（占位值）被清除，报文 Host 头跟随实际目标；经 `req.GetBody` 重建 body，确保转移时请求体完整重放。

### 被动冷却

- 默认冷却窗口 `defaultCooldown = 5s`：拨号失败后的窗口期内不再选中该实例。k8s 部署后由平台 readinessProbe 负责主动健康检查，此处仅作滚动更新瞬间的兜底。
- `markCooling` 顺带清理已过期条目，防止动态端点（注册中心 / k8s）场景下 map 无界增长。

### 实例地址形式

`splitEndpoint` 支持 `scheme://host:port` 与 `host:port` 两种形式，后者沿用请求原始 scheme（connect 客户端 base URL 统一为 `http`）。实例地址只取 scheme 与 host，path/query 一律丢弃。

### 可选项

| 选项 | 说明 |
| --- | --- |
| `WithCooldown(d)` | 覆盖默认冷却时长（测试用） |
| `WithBaseTransport(rt)` | 覆盖底层 RoundTripper（测试注入用，默认 `http.DefaultTransport`） |
| `WithBalancer(b)` | 指定负载均衡策略（`nil` 忽略）。**有状态策略请每个 Transport 传入独立实例**，避免多传输层共享游标 |

`lf` 为可选 logger（`nil` 时静默丢弃日志），拨号失败转移时打 Warn。

## Balancer — 负载均衡策略

策略可插拔，在 `NewTransport` 时通过 `WithBalancer` 指定（默认轮询）。

约定：
- `Pick` 入参 candidates 保证**非空**，返回值必须是其中一个元素；
- 实现须**并发安全**（`RoundTrip` 并发调用）；
- 故障转移由 Transport 负责，策略只需关注"当下选谁"，无需感知重试。

| 实现 | 说明 |
| --- | --- |
| `RoundRobin`（`NewRoundRobin`，默认） | 游标递增依次命中候选实例，请求均匀分布在地址池上 |
| `Random` | 均匀随机选实例，无状态无锁；候选顺序固定导致轮询可预测时（如实例数远少于请求量）的替代选择 |
| `First` | 恒选候选集第一个 |

## 客户端构造

`newHTTPClient(provider, svc)` 构造走服务发现传输层的 `http.Client`：base URL 的 host 仅作占位（传输层按服务名解析重写），scheme 决定明文 / TLS。

| 函数 | 构造目标 | 占位 base URL |
| --- | --- | --- |
| `NewAuthClient(provider, timeout, maxInflight)` | `authv1connect.AuthServiceClient` | `http://auth` |
| `NewUserClient(provider, timeout, maxInflight)` | `userv1connect.UserServiceClient` | `http://user` |
| `NewMediaClient(provider, timeout, maxInflight)` | `mediav1connect.MediaServiceClient` | `http://media` |
| `NewPostClient(provider, timeout, maxInflight)` | `postv1connect.PostServiceClient` | `http://post` |
| `NewDiscoverClient(provider, timeout, maxInflight)` | `discoverv1connect.DiscoverServiceClient` | `http://discover` |

`clientOptions(timeout, maxInflight)` 下游调用统一拦截器：
- **bulkhead**（[舱壁限流](#舱壁限流调用端)）：per-service 在途并发上限（`<=0` 用默认 256，满舱快速失败）；
- **timeout**（[调用超时](#调用超时)）：单次调用超时（`<=0` 用默认 3s，只收紧不放宽）；
- **trace**（[trace](../trace/trace.md)）：透传 `trace_id`，串联跨服务调用链；
- **jwt**（[jwt](../jwt/jwt.md)）：把会话原始 token 以 `Authorization` 头透传给下游（RefreshTokens / Logout 等接口依赖下游从请求头自行解析会话）。

## 舱壁限流（调用端）

`bulkhead.go` `NewBulkheadInterceptor(maxInflight)`：per-service 在途请求并发上限（调用端限流）。
防单一慢/故障下游的在途请求无限堆积（雪崩的资源侧根因）——堆积资源上界 = `maxInflight × 调用超时`。

- **每客户端实例独立信号量** = per-service 一个配额（隔离舱语义：故障服务耗尽自己的额度，不拖累其他下游）；
- **快速失败不排队**：满舱 → `CodeResourceExhausted`（请求不发出）——排队会把过载变慢失败（与超时堆积同病）；
- `defer Release` 全路径归还额度（成功/失败/超时/panic）；`maxInflight <= 0` 不限制（调用方经 defaults 兜底）；
- 一次 Acquire 覆盖整条尝试链（transport 拨号失败转移在内层，不重复计数）；
- 溢出**不重试**（P2 保守策略："只对确认未处理的重试"）；consumer 侧溢出 → `classifyErr` → RETRY（吃 MaxDeliver 预算）。

配置：`rpc.max_inflight`（0 用默认 `defaults.RPCMaxInflight = 256`）。

位置说明：舱壁在**调用端**（发请求前拦，保护调用方资源）；与**服务端**入口限流（token bucket 全局/per-IP，
保护服务容量）是两个问题——后者搁置至公网/多实例需求明确（见 `tmp/rpc_resilience_plan.md` P3）。

## 调用超时

`timeout.go` `NewTimeoutInterceptor(timeout)`：unary 调用统一注入 `context.WithTimeout`：

- **只收紧不放宽**：调用方 ctx 已有更紧 deadline 时不叠加，尊重上游预算；
- per-attempt 计时（为 P2 失败转移预留：每次尝试独立计时、共享调用方总 deadline）；
- `timeout <= 0` 不注入（调用方应先经 `defaults.RPCDefaultTimeout` 兜底）；流式调用不注入（项目 RPC 均为 unary）。

超时语义：客户端超时 → `CodeDeadlineExceeded`——consumer 侧 `classifyErr` 判 RETRY 重投；
BFF 侧维持 `fail+InternalError` 兜底（面客不区分超时与内部错误，2026-11-15 拍板；不返连接层 error）。

配置：`rpc.timeout`（0 用默认 `defaults.RPCDefaultTimeout = 3s`）；
传输层兜底 `ResponseHeaderTimeout = defaults.RPCResponseHeaderTimeout = 10s`（防对端接收后不响应的黑洞绕过拦截器）。

## wire 装配

`cmd/gen/wire.go` 的 `Clients` set：

- `NewStaticProvider` → `wire.Bind` 到 `rpc.EndpointProvider`；
- `rpcTimeout(cfg)` 提取 `cfg.RPC.Timeout`、`rpcMaxInflight(cfg)` 提取 `cfg.RPC.MaxInflight`（0 由 client 层用默认值兜底）；
- `NewAuthClient` / `NewUserClient` / `NewMediaClient` / `NewPostClient` / `NewDiscoverClient` 构造五个下游客户端，注入 `v1.NewDownstreamRepo` 等下游调用方。

## MapServerError — 服务端错误出口映射

`MapServerError(err) error` 是各服务 handler 错误出口的统一映射（错误契约见 [proto](../../proto/proto.md)）：

| 入参 | 映射结果 |
| --- | --- |
| 已是 `connect.Error` | 原样透传（biz 可显式指定精确码：`CodeInternal` 表达不变量问题、`CodeUnimplemented` 表达占位） |
| 其余裸 error（DB/Redis/S3 等依赖故障） | `connect.CodeUnavailable`（瞬时故障语义，调用方按连接层码分类处理） |

用法：各服务本地 `mapErr` 先处理服务专属哨兵错误（如 `ErrNotImplemented` → `CodeUnimplemented`），再委托本函数兜底；handler 统一 `if err != nil { return nil, mapErr(err) }`。业务裁决错误不走本函数（直接 `fail + errmsg` 业务码，永不混入连接层）。
