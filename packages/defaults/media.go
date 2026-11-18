package defaults

import "time"

var (
	// ConsumerRetryBackOff 全消费组统一 Nak 退避重投节奏（防瞬时错误重投风暴挤占串行消费循环）。
	// 超出长度沿用最后一个值；投递总次数仍由各消费组 max_retry（MaxDeliver）控制。
	// 上传确认场景：UploadMedia 消息立即发送，对象未到按此节奏重试，耗尽判失败（原延时 15s 等待已废除）
	ConsumerRetryBackOff = []time.Duration{time.Second, 2 * time.Second, 3 * time.Second, 5 * time.Second}
)

const (
	MediaMessageStream = "media"
	MediaStream        = "media"

	MaxImageHeight = 8192
	MaxImageWidth  = 8192

	// UploadObjectWait 上传确认消息在 handler 内等直传对象落地的时长。
	// 消息在 UploadMedia 事务里即发出，常比客户端 PUT 早到几百毫秒；短等即可接住，
	// 省掉一次 Nak 退避重投（最小档 1s，是上传链路时延的大头）。仍未见再交由重投窗口兜底
	UploadObjectWait = 2 * time.Second
)

var (
	// MediaUrlTTL 展示 URL 默认有效期：私有内容（草稿配图）24h，过期重拉原接口
	MediaUrlTTL = 24 * time.Hour
	// MediaUrlMaxTTL 展示 URL 有效期上限：MinIO 预签名上限 7d（公开内容可放宽至此）
	MediaUrlMaxTTL = 7 * 24 * time.Hour
)

var (
	// AvatarAssetID 默认头像资产 ID（部署预置的真实资产，注册时统一挂 216 变体）
	AvatarAssetID = "2115637161121284096"
)

// FallbackCoverURL 媒体签名失败时的默认失败占位图（前端静态资源，面客展示兜底）。
// 签发逐项非原子（部分失败正常语义）：失败项替换为占位图而非缺格，
// 由面客前端在自身域名托管该路径静态资源
const FallbackCoverURL = "/static/img/fallback-cover.png"
