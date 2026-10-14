package utils

import (
	"testing"

	mediav1 "rednote/gen/go/media/v1"

	"github.com/stretchr/testify/assert"
)

func TestBuildKey(t *testing.T) {
	cases := []struct {
		name     string
		id       int64
		fileType mediav1.MediaFileType
		format   mediav1.MediaFormat
		want     string
	}{
		{"原始图 JPG", 42, mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN, mediav1.MediaFormat_MEDIA_FORMAT_JPG, "media/42/origin.jpg"},
		{"原始图 PNG", 42, mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN, mediav1.MediaFormat_MEDIA_FORMAT_PNG, "media/42/origin.png"},
		{"原始图 WEBP", 42, mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN, mediav1.MediaFormat_MEDIA_FORMAT_WEBP, "media/42/origin.webp"},
		{"1080 变体", 42, mediav1.MediaFileType_MEDIA_FILE_TYPE_1080, mediav1.MediaFormat_MEDIA_FORMAT_WEBP, "media/42/1080.webp"},
		{"540 变体", 42, mediav1.MediaFileType_MEDIA_FILE_TYPE_540, mediav1.MediaFormat_MEDIA_FORMAT_WEBP, "media/42/540.webp"},
		{"216 变体", 42, mediav1.MediaFileType_MEDIA_FILE_TYPE_216, mediav1.MediaFormat_MEDIA_FORMAT_WEBP, "media/42/216.webp"},
		{"未知类型走 unknown", 42, mediav1.MediaFileType(999), mediav1.MediaFormat_MEDIA_FORMAT_PNG, "media/42/unknown.png"},
		{"未知格式走 .unknown", 42, mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN, mediav1.MediaFormat(999), "media/42/origin.unknown"},
		{"类型与格式都未知", 0, mediav1.MediaFileType(999), mediav1.MediaFormat(999), "media/0/unknown.unknown"},
		{"ID 为 0", 0, mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN, mediav1.MediaFormat_MEDIA_FORMAT_PNG, "media/0/origin.png"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, BuildKey(tc.id, tc.fileType, tc.format))
		})
	}
}

func TestContentTypeOf(t *testing.T) {
	cases := []struct {
		name   string
		format mediav1.MediaFormat
		want   string
	}{
		{"JPG", mediav1.MediaFormat_MEDIA_FORMAT_JPG, "image/jpeg"},
		{"PNG", mediav1.MediaFormat_MEDIA_FORMAT_PNG, "image/png"},
		{"WEBP", mediav1.MediaFormat_MEDIA_FORMAT_WEBP, "image/webp"},
		{"未知格式兜底", mediav1.MediaFormat(999), "application/octet-stream"},
		{"UNSPECIFIED 兜底", mediav1.MediaFormat_MEDIA_FORMAT_UNSPECIFIED, "application/octet-stream"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ContentTypeOf(tc.format))
		})
	}
}
