# testmq — MQ 本地内存模拟

代码：`test/testmq/testmq.go`。实现 `mq.MQClient`，单元测试注入替代 `*mq.JetStreamClient`。

| 方法 | 模拟行为 |
| --- | --- |
| `Send` / `SendTX` | 记录消息供断言；`SendTX` **不写 outbox 表**（outbox 事务语义由真实现 + testdb 集成测试覆盖） |
| `GetStream` | 直接 panic（消费面不做本地模拟，误用即暴露） |
| `Close` | 空实现 |

测试辅助：`Sent()` / `SentBySubject(sub)` / `Count()` 消息快照、`SetSendError(err)` 故障注入、`Reset()` 清空。

```go
mqc := testmq.New()
usecase := postv1.NewPostUsecase(repo, mqc, db)
// ... 断言
msgs := mqc.SentBySubject(defaults.PostMessageStream)
```
