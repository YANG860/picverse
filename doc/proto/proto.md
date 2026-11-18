# proto 消息规范

proto 定义位于 `proto/`，生成代码位于 `gen/go/`。命名空间按 `common.v1` / `auth.v1` / `media.v1` / `post.v1` / `user.v1` / `bff.v1` 划分，其中 `bff.v1` 为面向前端与客户端的聚合网关（见 `doc/internal/bff/bff.md`）。

## ID 与时间表示

- **ID**：proto 层使用 `string`，为雪花 ID 的字符串形式（DB 层为 `int64`）。
- **时间**：proto 层使用 `int64`，为 unix 毫秒时间戳。
- **分页**：`common.v1.Cursor` 承载游标分页，`limit` 用 `buf.validate` 限制 `lt = 1000`。

## 错误约定（fail / err）

- **`err` 必须传递可枚举、有编号的错误**（见 `packages/errmsg`），否则直接使用 gRPC 原始错误（连接层 error）。服务端 handler 出口统一经 `rpc.MapServerError` 映射（见 [rpc](../packages/rpc/rpc.md)）：连接层错误原样透传，其余裸 error（依赖故障）→ `CodeUnavailable`；本地 `mapErr` 可先映射服务专属哨兵（如 `ErrNotImplemented` → `CodeUnimplemented`）。
- **BFF 例外（面向前端）**：BFF 不向客户端返回连接层 error——先内部记录日志，再返回 `fail + InternalError`；其余错误按惯例处理（业务可枚举码透传，见 [bff](../internal/bff/bff.md)）。
- **存储坐标不面客**（与"身份不面客"同一治理）：媒体的 `obj_key`/`bucket` 是内部存储坐标，不得出现在面客契约；面客统一 `bff.v1.MediaView`（含内嵌签发的预签名 URL），请求侧只传 `{asset_id, type}` 引用（见 [bff](../internal/bff/bff.md)）。
- 批量查询需要标识"部分成功"时，每个结果用 `fail` + `err` 标识该项失败。
- 异步 / best-effort 调用不在乎结果时，可不返回 `fail`/`err`。
- **任何情况下不保证返回顺序**，必须使用返回项里的 Key 信息或返回值进行定位。

## 批量返回规范（repeated results）

批量接口使用 `repeated Result results` 返回，每项携带可追溯回查询的 Key 信息。

```
rpc foo(Query) returns (Response);

message Query {
  int64  id  = 1;
  string str = 2;
}

message Response {
  repeated Result results = 1;
}
```

### Result 格式

```
message Result {
  // 错误信息，必须
  bool          fail = 1;
  common.v1.Error err = 2;

  // 查询 Key 信息：可多个 key，直接复制查询的类型和字段，
  // 或由 data 中已包含，保证字段一致、可追溯回查询
  Query query = 3;   // 或 int64 id = 3;

  // 对应这组 key 的返回数据
  dataType data = 4;
}
```

### 实例对照

| 服务 | 查询 Key | Result 类型 |
| --- | --- | --- |
| media | `id`（asset_id） | `MediaAssetResult{id, asset}` |
| media | `MediaAssetFileQuery{id, type}` | `MediaAssetFileResult{query, file}` |
| post | `uid` | `UserBriefResult{brief}` |
| post | `post_id` | `PostResult{post}` |

## 单值结果

单值接口直接在响应中携带 `fail` + `err` + 数据字段，如 `LoginResponse{fail, err, refresh_token, access_token}`、
`media.GetMediaAssetResponse{fail, err, asset}`、`media.GetMediaAssetFileResponse{fail, err, file}`；
请求侧仅携带查询 Key 字段（如 `GetMediaAssetRequest{id}`、`GetMediaAssetFileRequest{id, type}`）。

## 校验

使用 `buf.build/bufbuild/protovalidate`，字段规则以 `[(buf.validate.field)...]` 声明，例如：

- `string` 非空：`string.min_len = 1`
- `repeated string` 每项非空：`repeated.items.string.min_len = 1`
- `Cursor.limit` 上限：`int64.lt = 1000`

### 执行方式与错误形态分层

- **结构约束由传输层强制**：各服务 handler 可选挂 `connectrpc.com/validate` 拦截器（见 `cmd/*/main.go`），
  违反注解的请求在到达 biz 前被拒绝，返回连接层 `connect.CodeInvalidArgument`（不入 fail/err 响应体）
- **业务规则由 biz 层负责**：标题长度/正文长度/媒体合法性等语义校验（`packages/errmsg`），
  走既定的 `fail` + `common.v1.Error` 响应体约定，与注解校验互补不重叠
