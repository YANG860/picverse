package utils

import (
	"fmt"
	mediav1 "rednote/gen/go/media/v1"
)

func BuildKey(id int64, fileType mediav1.MediaFileType, format mediav1.MediaFormat) string {
	var key string

	switch fileType {
	case mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN:
		key = fmt.Sprintf("media/%d/origin", id)
	case mediav1.MediaFileType_MEDIA_FILE_TYPE_1080:
		key = fmt.Sprintf("media/%d/1080", id)
	case mediav1.MediaFileType_MEDIA_FILE_TYPE_540:
		key = fmt.Sprintf("media/%d/540", id)
	case mediav1.MediaFileType_MEDIA_FILE_TYPE_216:
		key = fmt.Sprintf("media/%d/216", id)
	default:
		key = fmt.Sprintf("media/%d/unknown", id)
	}

	switch format {
	case mediav1.MediaFormat_MEDIA_FORMAT_JPG:
		key += ".jpg"
	case mediav1.MediaFormat_MEDIA_FORMAT_PNG:
		key += ".png"
	case mediav1.MediaFormat_MEDIA_FORMAT_WEBP:
		key += ".webp"
	default:
		key += ".unknown"
	}
	return key
}

func ContentTypeOf(format mediav1.MediaFormat) string {
	switch format {
	case mediav1.MediaFormat_MEDIA_FORMAT_JPG:
		return "image/jpeg"
	case mediav1.MediaFormat_MEDIA_FORMAT_PNG:
		return "image/png"
	case mediav1.MediaFormat_MEDIA_FORMAT_WEBP:
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}
