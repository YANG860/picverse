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
	AvatarAssetID = "default-avatar" //TODO: 上传替换为默认头像的asset
)
