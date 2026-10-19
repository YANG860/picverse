package utils

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	mediav1 "rednote/gen/go/media/v1"
	"rednote/packages/defaults"

	"github.com/chai2010/webp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------- 图片构造 helper ----------

// newTestImage 构造 w×h 的渐变测试图（非空内容，保证各编码器输出稳定）
func newTestImage(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x * 7), G: uint8(y * 13), B: uint8((x + y) * 3), A: 255})
		}
	}
	return img
}

// pngBytes 编码 w×h 的 PNG 图片
func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, newTestImage(w, h)))
	return buf.Bytes()
}

// jpegBytes 编码 w×h 的 JPEG 图片
func jpegBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, newTestImage(w, h), nil))
	return buf.Bytes()
}

// webpBytes 编码 w×h 的 WebP 图片（无损，保证尺寸字段稳定）
func webpBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, webp.Encode(&buf, newTestImage(w, h), &webp.Options{Lossless: true}))
	return buf.Bytes()
}

// metaFor 按真实图片字节构造完全一致的 MediaMetaData（尺寸/格式/摘要/大小全部匹配）
func metaFor(format mediav1.MediaFormat, w, h int, data []byte) *mediav1.MediaMetaData {
	sum := sha256.Sum256(data)
	return &mediav1.MediaMetaData{
		Format:     format,
		Width:      int64(w),
		Height:     int64(h),
		SizeBytes:  int64(len(data)),
		Sha_256Sum: hex.EncodeToString(sum[:]),
	}
}

// ---------- ValidateImage ----------

func TestValidateImage(t *testing.T) {
	t.Run("合法 PNG 全字段匹配返回 nil", func(t *testing.T) {
		// Arrange
		data := pngBytes(t, 8, 6)
		meta := metaFor(mediav1.MediaFormat_MEDIA_FORMAT_PNG, 8, 6, data)

		// Act
		err := ValidateImage(data, meta)

		// Assert
		assert.NoError(t, err)
	})

	t.Run("合法 JPEG 全字段匹配返回 nil", func(t *testing.T) {
		data := jpegBytes(t, 8, 6)
		meta := metaFor(mediav1.MediaFormat_MEDIA_FORMAT_JPG, 8, 6, data)

		err := ValidateImage(data, meta)

		assert.NoError(t, err)
	})

	t.Run("合法 WebP 全字段匹配返回 nil", func(t *testing.T) {
		data := webpBytes(t, 8, 6)
		meta := metaFor(mediav1.MediaFormat_MEDIA_FORMAT_WEBP, 8, 6, data)

		err := ValidateImage(data, meta)

		assert.NoError(t, err)
	})

	t.Run("size 与 sha256 未提供时跳过对应校验", func(t *testing.T) {
		// Arrange：只给格式与尺寸，摘要与大小留空
		data := pngBytes(t, 4, 4)
		meta := &mediav1.MediaMetaData{
			Format: mediav1.MediaFormat_MEDIA_FORMAT_PNG,
			Width:  4,
			Height: 4,
		}

		// Act
		err := ValidateImage(data, meta)

		// Assert
		assert.NoError(t, err)
	})

	t.Run("size 不匹配被拒绝", func(t *testing.T) {
		data := pngBytes(t, 8, 6)
		meta := metaFor(mediav1.MediaFormat_MEDIA_FORMAT_PNG, 8, 6, data)
		meta.SizeBytes = int64(len(data)) + 1

		err := ValidateImage(data, meta)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "size mismatch")
	})

	t.Run("sha256 不匹配被拒绝", func(t *testing.T) {
		data := pngBytes(t, 8, 6)
		meta := metaFor(mediav1.MediaFormat_MEDIA_FORMAT_PNG, 8, 6, data)
		meta.Sha_256Sum = strings.Repeat("0", 64)

		err := ValidateImage(data, meta)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "sha256 mismatch")
	})

	t.Run("声明格式与实际编码不符被拒绝", func(t *testing.T) {
		// Arrange：PNG 数据但声明为 JPEG
		data := pngBytes(t, 8, 6)
		meta := metaFor(mediav1.MediaFormat_MEDIA_FORMAT_JPG, 8, 6, data)

		// Act
		err := ValidateImage(data, meta)

		// Assert
		require.Error(t, err)
		assert.Contains(t, err.Error(), "format mismatch")
	})

	t.Run("宽度与元数据不符被拒绝", func(t *testing.T) {
		data := pngBytes(t, 8, 6)
		meta := metaFor(mediav1.MediaFormat_MEDIA_FORMAT_PNG, 8, 6, data)
		meta.Width = 9

		err := ValidateImage(data, meta)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "width mismatch")
	})

	t.Run("高度与元数据不符被拒绝", func(t *testing.T) {
		data := pngBytes(t, 8, 6)
		meta := metaFor(mediav1.MediaFormat_MEDIA_FORMAT_PNG, 8, 6, data)
		meta.Height = 7

		err := ValidateImage(data, meta)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "height mismatch")
	})

	t.Run("超出最大宽度被拒绝", func(t *testing.T) {
		// Arrange：宽度恰好越界（max+1）
		w := defaults.MaxImageWidth + 1
		data := pngBytes(t, w, 1)
		meta := metaFor(mediav1.MediaFormat_MEDIA_FORMAT_PNG, w, 1, data)

		// Act
		err := ValidateImage(data, meta)

		// Assert
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid image dimensions")
	})

	t.Run("最大允许尺寸的图片被接受", func(t *testing.T) {
		// Arrange：宽度恰好等于上限（边界值 max）
		w := defaults.MaxImageWidth
		data := pngBytes(t, w, 1)
		meta := metaFor(mediav1.MediaFormat_MEDIA_FORMAT_PNG, w, 1, data)

		// Act
		err := ValidateImage(data, meta)

		// Assert
		assert.NoError(t, err)
	})

	t.Run("非图片数据被拒绝", func(t *testing.T) {
		// Arrange
		data := []byte("这不是图片")
		sum := sha256.Sum256(data)
		meta := &mediav1.MediaMetaData{
			Format:     mediav1.MediaFormat_MEDIA_FORMAT_PNG,
			SizeBytes:  int64(len(data)),
			Sha_256Sum: hex.EncodeToString(sum[:]),
		}

		// Act
		err := ValidateImage(data, meta)

		// Assert
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to detect image format")
	})
}

// ---------- ValidateEmail ----------

func TestValidateEmail(t *testing.T) {
	cases := []struct {
		name  string
		email string
		want  bool
	}{
		{"空串不合法", "", false},
		{"无@不合法", "plainstring", false},
		{"无域名不合法", "user@", false},
		{"无顶级域不合法", "user@localhost", false},
		{"顶级域仅1字符不合法", "user@host.c", false},
		{"缺@后部分不合法", "@example.com", false},
		{"缺@前部分不合法", "@example.com", false},
		{"双@不合法", "user@@example.com", false},
		{"域名以点开头不合法", "user@.com", false},
		{"域名以点结尾不合法", "user@example.", false},
		{"含空格不合法", "us er@example.com", false},
		{"中文本地名不合法", "用户@example.com", false},
		{"尾部空白不合法", "user@example.com ", false},
		{"合法普通邮箱", "user@example.com", true},
		{"合法最短邮箱", "u@e.co", true},
		{"合法含点与加号", "first.last+tag@example.com", true},
		{"合法多级域名", "user@mail.example.com", true},
		{"合法含连字符", "user_name@ex-ample.com", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ValidateEmail(tc.email))
		})
	}
}

// ---------- ValidatePassword ----------

func TestValidatePassword(t *testing.T) {
	// 边界：长度 7 / 8 / 32 / 33
	long32 := "AbcdefghijklmnopqrstuvwxyZ012345" // 32 位
	cases := []struct {
		name string
		pw   string
		want bool
	}{
		{"空串不合法", "", false},
		{"7位不合法", "Abc1234", false},
		{"8位合法", "Abc12345", true},
		{"32位合法", long32, true},
		{"33位不合法", long32 + "6", false},
		{"缺大写不合法", "abc12345", false},
		{"缺小写不合法", "ABC12345", false},
		{"缺数字不合法", "Abcdefgh", false},
		{"仅数字不合法", "12345678", false},
		{"含符号但三类齐全合法", "Abc1234!@#", true},
		{"空白密码不合法", "        ", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ValidatePassword(tc.pw))
		})
	}
}

// ---------- ValidateName ----------

func TestValidateName(t *testing.T) {
	rune64 := strings.Repeat("好", NameMaxLen)
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"空串不合法", "", false},
		{"纯空白不合法", "   ", false},
		{"含控制字符不合法", "ab\x00cd", false},
		{"含换行不合法", "ab\ncd", false},
		{"64字符合法（边界）", rune64, true},
		{"65字符不合法（越界）", rune64 + "好", false},
		{"单字符合法", "薯", true},
		{"含空格的名称合法", "小 红薯", true},
		{"ASCII 64字符合法（边界）", strings.Repeat("a", NameMaxLen), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ValidateName(tc.in))
		})
	}
}

// ---------- ValidateBio ----------

func TestValidateBio(t *testing.T) {
	rune512 := strings.Repeat("好", BioMaxLen)
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"空简介合法", "", true},
		{"512字符合法（边界）", rune512, true},
		{"513字符不合法（越界）", rune512 + "好", false},
		{"换行回车制表合法", "第一行\n第二行\r\n\t缩进", true},
		{"含NUL不合法", "ab\x00cd", false},
		{"含其他控制字符不合法", "ab\x01cd", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ValidateBio(tc.in))
		})
	}
}

// ---------- ValidateTitle / ValidateContent / ValidateMedia ----------

// TestValidateTitle 标题校验（rune 计数，与 ValidateName/Bio 一致；
// 回归：曾用字节计数导致中文标题提前超限）
func TestValidateTitle(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"空标题不合法", "", false},
		{"1个汉字合法（边界）", "标", true},
		{"100个汉字合法（边界）", strings.Repeat("标", 100), true},
		{"101个汉字不合法（越界）", strings.Repeat("标", 101), false},
		{"100字符 ASCII 合法（边界）", strings.Repeat("a", 100), true},
		{"101字符 ASCII 不合法（越界）", strings.Repeat("a", 101), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ValidateTitle(tc.in))
		})
	}
}

// TestValidateContent 正文校验（rune 计数，回归同 ValidateTitle）
func TestValidateContent(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"空内容不合法", "", false},
		{"1个汉字合法（边界）", "字", true},
		{"1000个汉字合法（边界）", strings.Repeat("字", 1000), true},
		{"1001个汉字不合法（越界）", strings.Repeat("字", 1001), false},
		{"1000字符 ASCII 合法（边界）", strings.Repeat("a", 1000), true},
		{"1001字符 ASCII 不合法（越界）", strings.Repeat("a", 1001), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ValidateContent(tc.in))
		})
	}
}

func TestValidateMedia(t *testing.T) {
	t.Run("nil 不合法", func(t *testing.T) {
		assert.False(t, ValidateMedia(nil))
	})

	t.Run("对象键为空不合法", func(t *testing.T) {
		assert.False(t, ValidateMedia(&mediav1.MediaFile{}))
	})

	t.Run("对象键非空合法", func(t *testing.T) {
		assert.True(t, ValidateMedia(&mediav1.MediaFile{ObjKey: "media/1/origin.png"}))
	})
}

// ---------- ValidatePostStatCount ----------

// TestValidatePostStatCount 统计计数校验：非负合法（含 0 边界），负数不合法
func TestValidatePostStatCount(t *testing.T) {
	cases := []struct {
		name string
		in   int64
		want bool
	}{
		{"0 合法（边界）", 0, true},
		{"正数合法", 100, true},
		{"负数不合法", -1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ValidatePostStatCount(tc.in))
		})
	}
}
