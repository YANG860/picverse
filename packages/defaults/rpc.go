package defaults

import "time"

var (
	// RPCDefaultTimeout 下游 RPC 单次调用超时默认值（配置未指定时使用）。
	// 保守拍值（下游查询均为毫秒级）：超时即故障信号（P1 熔断/P2 转移的输入），上线后按日志调优
	RPCDefaultTimeout = 3 * time.Second
	// RPCConsumerTimeout consumer 单条消息处理超时默认值（防单条挂死阻塞消息循环）。
	// 覆盖最坏路径（fanout 500×N 页拉取 + 批量写库）并留足余量；超时 Nak 重投
	RPCConsumerTimeout = 30 * time.Second
	// RPCResponseHeaderTimeout 传输层响应头等待上限（最后防线：防对端接收后不响应的黑洞，
	// 绕过拦截器调用超时的场景）；主约束是 RPCConfig.Timeout 注入的调用超时
	RPCResponseHeaderTimeout = 10 * time.Second
	// RPCMaxInflight 每下游服务在途请求并发上限默认值（舱壁限流，见 packages/rpc/bulkhead.go）。
	// 堆积资源上界 = maxInflight × 调用超时；无生产数据先拍后调
	RPCMaxInflight int64 = 256
)
