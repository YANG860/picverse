package utils

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"

	// 注册图片解码器：保证 ValidateImage 不依赖调用方是否恰好引入了对应编解码包
	_ "image/jpeg"
	_ "image/png"
	mediav1 "rednote/gen/go/media/v1"
	"rednote/packages/defaults"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	_ "github.com/chai2010/webp"
)

func ValidateEmail(email string) bool {
	if len(email) == 0 {
		return false
	}
	// RFC 5322 邮箱格式正则表达式
	emailRegex := regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)
	return emailRegex.MatchString(email)
}

func ValidatePassword(pw string) bool {
	if len(pw) == 0 {
		return false
	}
	// 密码最少8位
	if len(pw) < 8 {
		return false
	}
	if len(pw) > 32 {
		return false
	}
	// 检查是否包含大小写字母和数字
	hasUpper := regexp.MustCompile(`[A-Z]`).MatchString(pw)
	hasLower := regexp.MustCompile(`[a-z]`).MatchString(pw)
	hasDigit := regexp.MustCompile(`[0-9]`).MatchString(pw)
	return (hasUpper || hasLower) && hasDigit
}

func ValidateImage(ImgData []byte, meta *mediav1.MediaMetaData) (err error) {

	// 校验文件大小
	if meta.GetSizeBytes() > 0 && int64(len(ImgData)) != meta.GetSizeBytes() {
		return fmt.Errorf("size mismatch: actual=%d, meta=%d", len(ImgData), meta.GetSizeBytes())
	}

	// 校验 SHA256
	if sum := meta.GetSha_256Sum(); sum != "" {
		hash := sha256.Sum256(ImgData)
		actual := hex.EncodeToString(hash[:])
		if actual != sum {
			return fmt.Errorf("sha256 mismatch: actual=%s, meta=%s", actual, sum)
		}
	}

	// 校验实际文件格式
	cfg, actualFmt, err := image.DecodeConfig(bytes.NewReader(ImgData))
	if err != nil {
		return fmt.Errorf("failed to detect image format: %w", err)
	}

	if convertFormat(meta.GetFormat()) != actualFmt {
		return fmt.Errorf("format mismatch: actual=%s, meta=%v", actualFmt, convertFormat(meta.GetFormat()))
	}

	width := int64(cfg.Width)
	height := int64(cfg.Height)

	if width <= 0 || height <= 0 || width > defaults.MaxImageWidth || height > defaults.MaxImageHeight {
		return fmt.Errorf("invalid image dimensions: %dx%d", width, height)
	}

	// 校验元数据中的尺寸是否与实际图片一致
	if meta.GetWidth() != width {
		return fmt.Errorf("width mismatch: meta=%d, actual=%d", meta.GetWidth(), width)
	}
	if meta.GetHeight() != height {
		return fmt.Errorf("height mismatch: meta=%d, actual=%d", meta.GetHeight(), height)
	}

	// 校验格式是否受支持
	switch meta.GetFormat() {

	// 受支持的格式
	case mediav1.MediaFormat_MEDIA_FORMAT_PNG,
		mediav1.MediaFormat_MEDIA_FORMAT_JPG,
		mediav1.MediaFormat_MEDIA_FORMAT_WEBP:

	default:
		return fmt.Errorf("unsupported media format: %v", meta.GetFormat())
	}

	return nil
}

func convertFormat(format mediav1.MediaFormat) string {
	switch format {
	case mediav1.MediaFormat_MEDIA_FORMAT_JPG:
		return "jpeg"
	case mediav1.MediaFormat_MEDIA_FORMAT_PNG:
		return "png"
	case mediav1.MediaFormat_MEDIA_FORMAT_WEBP:
		return "webp"
	default:
		return "unknown"
	}
}

// ValidateTitle 标题校验：非空、长度 ≤ 100（rune 计数，与 ValidateName/Bio 及 VARCHAR 字符语义一致）
func ValidateTitle(title string) bool {
	// TODO:检验标题
	n := utf8.RuneCountInString(title)
	return n > 0 && n <= 100
}

// ValidateContent 正文校验：非空、长度 ≤ 1000（rune 计数，与 ValidateName/Bio 一致）
func ValidateContent(content string) bool {
	// TODO:检验内容
	n := utf8.RuneCountInString(content)
	return n > 0 && n <= 1000
}

// CommentMaxLen 评论内容长度上限（rune 计数，与 VARCHAR(512) 列宽对齐），评论比正文短故独立上限
const CommentMaxLen = 500

// ValidateComment 评论校验：去除首尾空白后非空、长度 ≤ 500、除换行/回车/制表符外不含控制字符（对齐 ValidateBio 手法）
func ValidateComment(content string) bool {
	if strings.TrimSpace(content) == "" {
		return false
	}
	if utf8.RuneCountInString(content) > CommentMaxLen {
		return false
	}
	return !strings.ContainsFunc(content, func(r rune) bool {
		return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t'
	})
}
func ValidateMedia(media *mediav1.MediaFile) bool {
	// 仅做对象键非空的基础校验：媒体文件的存在性、归属校验与资产引用计数由 BFF 侧统一维护
	// （写入口返回 old_asset_ids 供 BFF 调整计数），业务服务只存关联，不在写入路径校验
	return media != nil && len(media.ObjKey) > 0
}

// 昵称/简介长度上限（rune 计数，与 PostgreSQL VARCHAR(n) 的字符语义一致），
// 与 user_profiles 表列宽 VARCHAR(64)/VARCHAR(512) 对齐，避免落库时被截断/报错
const (
	NameMaxLen = 64
	BioMaxLen  = 512
)

// ValidateName 昵称校验：去除首尾空白后非空、长度 1~64、不含控制字符
func ValidateName(name string) bool {
	if strings.TrimSpace(name) == "" {
		return false
	}
	if utf8.RuneCountInString(name) > NameMaxLen {
		return false
	}
	return !strings.ContainsFunc(name, unicode.IsControl)
}

// ValidateBio 简介校验：可为空、长度 ≤ 512、除换行/回车/制表符外不含控制字符
// （多行简介合法；媒体内容审核由审核服务负责，此处仅做格式校验）
func ValidateBio(bio string) bool {
	if utf8.RuneCountInString(bio) > BioMaxLen {
		return false
	}
	return !strings.ContainsFunc(bio, func(r rune) bool {
		return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t'
	})
}

// ValidatePostStatCount 统计计数校验：单列计数必须非负（0 合法）。
// 逐字段更新语义下无“未设置”歧义：字段是否修改由调用方的 nil 判断决定，本函数只校验取值
func ValidatePostStatCount(count int64) bool {
	return count >= 0
}
