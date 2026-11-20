// 图片本地缓存：wallhaven 图片一次性下载到磁盘（images/<id>.<ext>），
// 重跑秒级跳过；入库前解码真实宽高并计算 SHA256（consumer 会校验元数据一致性）。
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // 注册 JPEG 解码器（DecodeConfig 用）
	_ "image/png"  // 注册 PNG 解码器
	"os"
	"path/filepath"

	"github.com/disintegration/imaging"

	mediav1 "rednote/gen/go/media/v1"
)

// cachedImage 已缓存图片的入库元信息（对应 media.v1.MediaMetaData）
type cachedImage struct {
	ID        string
	Path      string // 本地文件绝对/相对路径
	MIME      string
	Format    mediav1.MediaFormat
	Width     int64
	Height    int64
	SizeBytes int64
	SHA256    string
}

// whImageURL 按来源取图片直链：full=原图（数 MB 级）/ large=大缩略图（默认）/ small=小缩略图
func whImageURL(w whWallpaper, source string) (string, error) {
	switch source {
	case "full":
		return w.Path, nil
	case "large":
		if w.Thumbs.Large == "" {
			return "", fmt.Errorf("wallhaven %s 无 large 缩略图", w.ID)
		}
		return w.Thumbs.Large, nil
	case "small":
		if w.Thumbs.Small == "" {
			return "", fmt.Errorf("wallhaven %s 无 small 缩略图", w.ID)
		}
		return w.Thumbs.Small, nil
	}
	return "", fmt.Errorf("未知图片来源 %q（可选 full/large/small）", source)
}

// inspectImage 解码图片真实元信息：宽高/格式一律以字节为准（缩略图与声明的
// file_type 可能不一致——wallhaven 缩略图恒为 JPEG），不信任远端字段
func inspectImage(id, path string) (*cachedImage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取缓存图片 %s: %w", path, err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("解码缓存图片 %s（文件可能损坏，删除后重跑）: %w", path, err)
	}
	img := &cachedImage{
		ID:        id,
		Path:      path,
		Width:     int64(cfg.Width),
		Height:    int64(cfg.Height),
		SizeBytes: int64(len(data)),
		SHA256:    sha256Hex(data),
	}
	switch format {
	case "jpeg":
		img.MIME = "image/jpeg"
		img.Format = mediav1.MediaFormat_MEDIA_FORMAT_JPG
	case "png":
		img.MIME = "image/png"
		img.Format = mediav1.MediaFormat_MEDIA_FORMAT_PNG
	default:
		return nil, fmt.Errorf("缓存图片 %s 实际格式 %q 不受支持（仅接受 jpeg/png，consumer 只处理这两种）", path, format)
	}
	return img, nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// loadCachedDetail 单图详情磁盘缓存（details/<id>.json）：未命中调 API 并落盘，
// 重跑跳过限速调用；缓存写入失败仅告警不阻断（缓存是尽力而为）
func loadCachedDetail(ctx context.Context, wh *whClient, cacheDir, id string) (whWallpaper, error) {
	dir := filepath.Join(cacheDir, "details")
	path := filepath.Join(dir, id+".json")

	if data, err := os.ReadFile(path); err == nil {
		var w whWallpaper
		if err := json.Unmarshal(data, &w); err != nil {
			return whWallpaper{}, fmt.Errorf("解析详情缓存 %s（可删除后重跑）: %w", path, err)
		}
		return w, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return whWallpaper{}, fmt.Errorf("读取详情缓存 %s: %w", path, err)
	}

	w, err := wh.Detail(ctx, id)
	if err != nil {
		return whWallpaper{}, err
	}
	data, err := json.Marshal(w)
	if err != nil {
		fmt.Printf("警告: 序列化 wallhaven %s 详情缓存失败（不影响本次）: %v\n", id, err)
		return w, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Printf("警告: 创建详情缓存目录 %s 失败（不影响本次）: %v\n", dir, err)
		return w, nil
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		fmt.Printf("警告: 写入详情缓存 %s 失败（不影响本次）: %v\n", path, err)
	}
	return w, nil
}

// prepareUpload 上传前压图：长边 > maxDim 时 Lanczos 缩放，统一重编码 JPEG。
// 目的：控制直传体积，适配慢链路下 consumer 的上传守卫窗口
// （对象须在 ~21s 内落桶，否则资产判 FAILED，见 internal/consumer/media_consumer.go）。
// 产物落 uploads/ 缓存（文件名含参数），断点续跑不重复编码。
func prepareUpload(src *cachedImage, cacheDir string, maxDim, quality int) (*cachedImage, error) {
	cachePath := filepath.Join(cacheDir, "uploads",
		fmt.Sprintf("%s.m%dq%d.jpg", src.ID, maxDim, quality))
	if img, err := inspectImage(src.ID, cachePath); err == nil {
		return img, nil
	}

	f, err := os.Open(src.Path)
	if err != nil {
		return nil, fmt.Errorf("打开待压缩图片 %s: %w", src.Path, err)
	}
	decoded, _, err := image.Decode(f)
	_ = f.Close()
	if err != nil {
		return nil, fmt.Errorf("解码待压缩图片 %s: %w", src.Path, err)
	}

	// 长边超限才缩放（等比，短边按比例），否则仅重编码瘦身
	out := decoded
	if b := decoded.Bounds(); b.Dx() > maxDim || b.Dy() > maxDim {
		if b.Dx() >= b.Dy() {
			out = imaging.Resize(decoded, maxDim, 0, imaging.Lanczos) // 横图：宽 = maxDim
		} else {
			out = imaging.Resize(decoded, 0, maxDim, imaging.Lanczos) // 竖图：高 = maxDim
		}
	}

	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return nil, fmt.Errorf("创建上传缓存目录 %s: %w", filepath.Dir(cachePath), err)
	}
	outf, err := os.Create(cachePath)
	if err != nil {
		return nil, fmt.Errorf("创建压缩产物文件 %s: %w", cachePath, err)
	}
	encErr := imaging.Encode(outf, out, imaging.JPEG, imaging.JPEGQuality(quality))
	closeErr := outf.Close()
	if encErr != nil {
		_ = os.Remove(cachePath)
		return nil, fmt.Errorf("编码压缩图片 %s: %w", cachePath, encErr)
	}
	if closeErr != nil {
		_ = os.Remove(cachePath)
		return nil, fmt.Errorf("关闭压缩产物文件 %s: %w", cachePath, closeErr)
	}
	img, err := inspectImage(src.ID, cachePath)
	if err != nil {
		_ = os.Remove(cachePath)
		return nil, err
	}
	return img, nil
}

// ensureImage 下载并缓存 wallhaven 图片；已缓存则直接读取元信息（幂等）。
// 先落临时文件再按实际格式改扩展名，保证扩展名/MIME/格式枚举三者一致。
func ensureImage(ctx context.Context, wh *whClient, cacheDir string, w whWallpaper, source string) (*cachedImage, error) {
	dir := filepath.Join(cacheDir, "images")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建图片缓存目录 %s: %w", dir, err)
	}

	// 命中缓存：按实际字节识别格式（扩展名可能与实际不符，故扫描两种）
	for _, ext := range []string{".jpg", ".png"} {
		path := filepath.Join(dir, w.ID+ext)
		if _, err := os.Stat(path); err == nil {
			return inspectImage(w.ID, path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("检查缓存图片 %s: %w", path, err)
		}
	}

	src, err := whImageURL(w, source)
	if err != nil {
		return nil, err
	}
	tmp := filepath.Join(dir, w.ID+".tmp")
	if err := wh.Download(ctx, src, tmp); err != nil {
		return nil, fmt.Errorf("下载 wallhaven %s 图片: %w", w.ID, err)
	}
	img, err := inspectImage(w.ID, tmp)
	if err != nil {
		_ = os.Remove(tmp) // 损坏/不支持的文件清掉，下次重下
		return nil, err
	}
	ext := ".jpg"
	if img.MIME == "image/png" {
		ext = ".png"
	}
	path := filepath.Join(dir, w.ID+ext)
	if err := os.Rename(tmp, path); err != nil {
		return nil, fmt.Errorf("缓存图片定名 %s → %s: %w", tmp, path, err)
	}
	img.Path = path
	return img, nil
}
