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
