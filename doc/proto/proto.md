# proto 消息规范

proto 定义位于 `proto/`，生成代码位于 `gen/go/`。命名空间按 `common.v1` / `auth.v1` / `media.v1` / `post.v1` 划分。

## ID 与时间表示

- **ID**：proto 层使用 `string`，为雪花 ID 的字符串形式（DB 层为 `int64`）。
- **时间**：proto 层使用 `int64`，为 unix 毫秒时间戳。
- **分页**：`common.v1.Cursor` 承载游标分页，`limit` 用 `buf.validate` 限制 `lt = 1000`。

## 错误约定（fail / err）

- **`err` 必须传递可枚举、有编号的错误**（见 `packages/errmsg`），否则直接使用 gRPC 原始错误（连接层 error）。
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

单值接口直接在响应中携带 `fail` + `err` + 数据字段，如 `LoginResponse{fail, err, refresh_token, access_token}`。

## 校验

使用 `buf.build/bufbuild/protovalidate`，字段规则以 `[(buf.validate.field)...]` 声明，例如：

- `string` 非空：`string.min_len = 1`
- `repeated string` 每项非空：`repeated.items.string.min_len = 1`
- `Cursor.limit` 上限：`int64.lt = 1000`
