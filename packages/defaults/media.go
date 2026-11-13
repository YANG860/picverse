package defaults

import "time"

var (
	UploadMsgDelay = 15 * time.Second
)

const (
	MediaMessageStream = "media"
	MediaStream        = "media"

	MaxImageHeight = 8192
	MaxImageWidth  = 8192
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
