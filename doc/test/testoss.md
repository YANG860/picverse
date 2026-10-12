# testoss — S3 本地内存模拟

代码：`test/testoss/testoss.go`。实现 `oss.S3Repo`，单元测试注入替代 `*oss.MinioClient`。

| 能力 | 说明 |
| --- | --- |
| 对象存储 | 内存 map：`Upload` / `Download` / `DownloadToWriter` / `Exist` / `DeleteObject(s)` |
| 预签名 URL | `PresignedPutURL` / `PresignedGetURL` 返回 `https://testoss.local/<bucket>/<key>?sig=test`（可断言对象键） |
| 测试辅助 | `SetObject` / `Object` 造数据与断言、`Ops()` 调用记录、`SetError()` 故障注入、`Reset()` |
| 未实现 | `PresignedDeleteURL` / `PresignedURL` 直接 panic（业务未使用）；`HealthCheck` 空实现恒成功 |

对象不存在返回 `testoss.ErrNotFound`。

```go
s3 := testoss.New()
usecase := mediav1.NewMediaUsecase(repo, mq, db, s3)
```
