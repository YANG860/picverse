// media 服务黑盒端到端测试：UploadMedia → 预签名直传 MinIO → 异步处理 → 查询变体 → 分页 → 封禁。
//
// 前置条件：media/consumer/poller 三个服务已部署，NATS/MinIO/PostgreSQL 可用。
// 运行：
//
//	go test ./test/e2e/... -count=1          # 默认 http://localhost:8080
//	MEDIA_E2E_ADDR=http://host:8080 go test ./test/e2e/...
//	go test -short ./...                     # -short 跳过
package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	mediav1 "rednote/gen/go/media/v1"
	"rednote/gen/go/media/v1/mediav1connect"
	"rednote/packages/errmsg"
	"rednote/packages/utils"
	"rednote/test/testsnowflake"

	"connectrpc.com/connect"
	"github.com/chai2010/webp"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	defaultMediaAddr = "http://localhost:8080"

	defaultS3Endpoint = "106.53.172.186:9000"
	defaultS3Key      = "rednote-3e2533eb" // 须与测试服务器容器 rednote-minio 的 MINIO_ROOT_USER 一致
	defaultS3Secret   = "LwFebgYHVFg2JRaO18iprgHPe5BQgpXv8okatw9"
	defaultS3Bucket   = "rednote"

	pollInterval      = 5 * time.Second
	processTimeout    = 2 * time.Minute // UploadMsgDelay 15s + 变体处理时间
	presignPutTimeout = 30 * time.Second
)

func mediaAddr() string { return e2eAddr("MEDIA_E2E_ADDR", defaultMediaAddr) }

// newS3Client 直连 MinIO 的客户端，用于验证对象真实存在且内容正确（仅测试用）
func newS3Client(t *testing.T) *minio.Client {
	t.Helper()
	mc, err := minio.New(e2eAddr("MEDIA_E2E_S3_ENDPOINT", defaultS3Endpoint), &minio.Options{
		Creds: credentials.NewStaticV4(
			e2eAddr("MEDIA_E2E_S3_ACCESS_KEY", defaultS3Key),
			e2eAddr("MEDIA_E2E_S3_SECRET_KEY", defaultS3Secret), ""),
		Secure: false,
	})
	require.NoError(t, err, "创建 S3 客户端")
	return mc
}

// downloadObject 从 S3 下载对象，返回内容与 Content-Type
func downloadObject(t *testing.T, mc *minio.Client, bucket, key string) ([]byte, string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	obj, err := mc.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	require.NoError(t, err, "Getobject %s", key)
	defer func() { _ = obj.Close() }()

	st, err := obj.Stat()
	require.NoError(t, err, "Stat %s", key)

	data, err := io.ReadAll(obj)
	require.NoError(t, err, "读取对象 %s", key)
	return data, st.ContentType
}

// assetImage 读取测试图片及其元数据
type assetImage struct {
	data   []byte
	sha256 string
	width  int
	height int
}

func loadAssetImage(t *testing.T) *assetImage {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "无法定位源文件路径")
	root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	path := filepath.Join(root, "assets", "ae2cde6ca7595fbadc4cff87734afe9b.jpg")

	data, err := os.ReadFile(path)
	require.NoError(t, err, "读取测试图片失败: %s", path)

	sum := sha256.Sum256(data)
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	require.NoError(t, err, "解码测试图片失败")
	assert.Contains(t, []string{"jpeg", "png"}, format, "测试图片应为 JPG/PNG（服务端只接受这两种格式）")

	return &assetImage{
		data:   data,
		sha256: hex.EncodeToString(sum[:]),
		width:  cfg.Width,
		height: cfg.Height,
	}
}

// TestMediaServiceE2E 黑盒冒烟全链路。步骤之间有依赖（同一资产），因此在一个测试内按序执行。
func TestMediaServiceE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: -short 模式跳过依赖外部部署服务的测试")
	}

	// 服务未部署时跳过而不是失败，便于本地/CI 环境按需运行
	mustReachable(t, mediaAddr(), "/media.v1.MediaService/")

	testsnowflake.InitTestSnoflake(1)
	client := mediav1connect.NewMediaServiceClient(http.DefaultClient, mediaAddr())
	img := loadAssetImage(t)

	// ---- 1. UploadMedia：创建资产 + 预签名 URL ----
	up, err := client.UploadMedia(context.Background(), connect.NewRequest(&mediav1.UploadMediaRequest{
		UploaderId: utils.GenID().String(),
		MediaMeta: &mediav1.MediaMetaData{
			Sha_256Sum: img.sha256,
			SizeBytes:  int64(len(img.data)),
			Format:     mediav1.MediaFormat_MEDIA_FORMAT_JPG,
			Height:     int64(img.height),
			Width:      int64(img.width),
		},
	}))
	require.NoError(t, err, "UploadMedia RPC")
	require.False(t, up.Msg.GetFail(), "UploadMedia 业务失败: %v", up.Msg.GetErr())

	assetID := up.Msg.GetAsset().GetBase().GetId()
	require.NotEmpty(t, assetID, "应返回资产ID")
	require.NotEmpty(t, up.Msg.GetSignedUrl(), "应返回预签名上传URL")
	assert.Equal(t, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_UPLOADING, up.Msg.GetAsset().GetStatus())
	t.Logf("asset=%s signed_url 有效期至 %d", assetID, up.Msg.GetExpireAt())

	// ---- 2. 直传 MinIO ----
	putReq, err := http.NewRequest(http.MethodPut, up.Msg.GetSignedUrl(), bytes.NewReader(img.data))
	require.NoError(t, err)
	putReq.Header.Set("Content-Type", "image/jpeg")
	putReq.Header.Set("Content-Length", fmt.Sprint(len(img.data)))

	putCtx, cancel := context.WithTimeout(context.Background(), presignPutTimeout)
	defer cancel()
	httpResp, err := http.DefaultClient.Do(putReq.WithContext(putCtx))
	require.NoError(t, err, "PUT 预签名URL")
	_, _ = io.Copy(io.Discard, httpResp.Body)
	_ = httpResp.Body.Close()
	require.Equal(t, http.StatusOK, httpResp.StatusCode, "直传 MinIO 应返回 200")

	// ---- 3. 轮询直到异步处理完成（UPLOADING → PROCESSING → ACTIVE）----
	var finalStatus mediav1.MediaAssetStatus
	require.Eventually(t, func() bool {
		got, err := client.GetMediaAssets(context.Background(),
			connect.NewRequest(&mediav1.GetMediaAssetsRequest{Ids: []string{assetID}}))
		if err != nil {
			return false
		}
		if got.Msg.GetResults()[0].GetFail() {
			return false
		}
		finalStatus = got.Msg.GetResults()[0].GetAsset().GetStatus()
		return finalStatus == mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE
	}, processTimeout, pollInterval, "资产应在 %v 内到达 ACTIVE，最终状态: %s", processTimeout, finalStatus)

	// ---- 4. 查询 4 种变体文件 ----
	types := []mediav1.MediaFileType{
		mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN,
		mediav1.MediaFileType_MEDIA_FILE_TYPE_216,
		mediav1.MediaFileType_MEDIA_FILE_TYPE_540,
		mediav1.MediaFileType_MEDIA_FILE_TYPE_1080,
	}
	queries := make([]*mediav1.MediaAssetFileQuery, 0, len(types))
	for _, ft := range types {
		queries = append(queries, &mediav1.MediaAssetFileQuery{Id: assetID, Type: ft})
	}

	files, err := client.GetMediaAssetFiles(context.Background(),
		connect.NewRequest(&mediav1.GetMediaAssetFilesRequest{Queries: queries}))
	require.NoError(t, err, "GetMediaAssetFiles RPC")
	require.Len(t, files.Msg.GetFiles(), len(types), "每种类型应各返回一条结果")

	byType := map[mediav1.MediaFileType]*mediav1.MediaFile{}
	for _, r := range files.Msg.GetFiles() {
		require.False(t, r.GetFail(), "type=%v 查询失败: %v", r.GetQuery().GetType(), r.GetErr())
		assert.NotEmpty(t, r.GetFile().GetBase().GetId(), "文件应有自身ID")
		byType[r.GetFile().GetType()] = r.GetFile()
	}
	require.Len(t, byType, len(types), "4 种类型都应存在")

	for _, ft := range types {
		f, ok := byType[ft]
		require.True(t, ok, "缺少类型 %v 的文件", ft)
		assert.Equal(t, assetID, f.GetAssetId(), "asset_id 应与请求一致")
		assert.NotEmpty(t, f.GetObjKey())
		assert.NotEmpty(t, f.GetMeta().GetSha_256Sum())
		assert.NotEmpty(t, f.GetMeta().GetSizeBytes())
		if ft != mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN {
			// 变体是 webp 缩略图：尺寸不超过原图且不超过对应档位
			assert.LessOrEqual(t, f.GetMeta().GetWidth(), int64(img.width))
			assert.LessOrEqual(t, f.GetMeta().GetHeight(), int64(img.height))
		}
	}
	// 原图元数据应与上传时一致
	origin := byType[mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN]
	assert.Equal(t, int64(img.width), origin.GetMeta().GetWidth())
	assert.Equal(t, int64(img.height), origin.GetMeta().GetHeight())
	assert.Equal(t, int64(len(img.data)), origin.GetMeta().GetSizeBytes())
	assert.Equal(t, img.sha256, origin.GetMeta().GetSha_256Sum(), "原图 sha256 应与上传时一致")

	// ---- 4.5 从 S3 实际下载对象，验证内容与元数据一致 ----
	mc := newS3Client(t)
	bucket := e2eAddr("MEDIA_E2E_S3_BUCKET", defaultS3Bucket)

	for _, ft := range types {
		f := byType[ft]
		t.Run("下载"+ft.String(), func(t *testing.T) {
			data, contentType := downloadObject(t, mc, bucket, f.GetObjKey())

			// 内容完整性：字节数与 sha256 与元数据一致
			assert.Equal(t, f.GetMeta().GetSizeBytes(), int64(len(data)), "对象字节数应与元数据一致")
			sum := sha256.Sum256(data)
			assert.Equal(t, f.GetMeta().GetSha_256Sum(), hex.EncodeToString(sum[:]), "对象 sha256 应与元数据一致")

			// Content-Type：origin 为 image/jpeg，变体为 image/webp
			if ft == mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN {
				assert.Equal(t, "image/jpeg", contentType)
			} else {
				assert.Equal(t, "image/webp", contentType)
			}

			// 实际解码验证图像有效
			if ft == mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN {
				// 原图应与上传文件逐字节一致
				assert.True(t, bytes.Equal(img.data, data), "原图应与上传的文件完全一致")
				decoded, err := jpeg.Decode(bytes.NewReader(data))
				require.NoError(t, err, "原图应为有效 JPEG")
				assert.Equal(t, img.width, decoded.Bounds().Dx())
				assert.Equal(t, img.height, decoded.Bounds().Dy())
			} else {
				// 变体应为有效 webp，且实际尺寸与元数据一致
				decoded, err := webp.Decode(bytes.NewReader(data))
				require.NoError(t, err, "变体应为有效 webp")
				assert.Equal(t, f.GetMeta().GetWidth(), int64(decoded.Bounds().Dx()), "webp 实际宽度应与元数据一致")
				assert.Equal(t, f.GetMeta().GetHeight(), int64(decoded.Bounds().Dy()), "webp 实际高度应与元数据一致")
			}
		})
	}

	// ---- 5. SignMediaFileURLs：签发展示 URL（TTL 分级）+ 签名 URL 真实下载校验 ----
	file216 := byType[mediav1.MediaFileType_MEDIA_FILE_TYPE_216]
	sign, err := client.SignMediaFileURLs(context.Background(), connect.NewRequest(&mediav1.SignMediaFileURLsRequest{
		Refs:       []*mediav1.MediaAssetFileQuery{{Id: assetID, Type: file216.GetType()}},
		TtlSeconds: 3600, // 显式 1h
	}))
	require.NoError(t, err, "SignMediaFileURLs RPC")
	require.False(t, sign.Msg.GetFail(), "SignMediaFileURLs 业务失败: %v", sign.Msg.GetErr())
	require.Len(t, sign.Msg.GetUrls(), 1)
	signed := sign.Msg.GetUrls()[0]
	require.False(t, signed.GetFail())
	require.NotEmpty(t, signed.GetSignedUrl(), "应返回预签名展示 URL")
	assert.Equal(t, assetID, signed.GetAssetId())
	// TTL 约束：expire_at ≈ now+1h（±2min）
	signExpire := time.UnixMilli(signed.GetExpireAt())
	assert.WithinDuration(t, time.Now().Add(time.Hour), signExpire, 2*time.Minute, "显式 TTL 1h")

	// 签名 URL 真实下载：内容与 S3 直下载一致
	downResp, err := http.Get(signed.GetSignedUrl())
	require.NoError(t, err, "GET 签名 URL")
	defer downResp.Body.Close()
	require.Equal(t, http.StatusOK, downResp.StatusCode)
	signedData, err := io.ReadAll(downResp.Body)
	require.NoError(t, err)
	s3Data, _ := downloadObject(t, mc, bucket, file216.GetObjKey())
	require.Equal(t, s3Data, signedData, "签名 URL 下载内容应与 S3 直下载一致")

	// TTL 超 7d 上限：InvalidArgument，不签发
	over, err := client.SignMediaFileURLs(context.Background(), connect.NewRequest(&mediav1.SignMediaFileURLsRequest{
		Refs:       []*mediav1.MediaAssetFileQuery{{Id: assetID, Type: file216.GetType()}},
		TtlSeconds: 8 * 24 * 3600,
	}))
	require.NoError(t, err)
	assert.True(t, over.Msg.GetFail())
	assert.Equal(t, errmsg.InvalidArgument.Code, over.Msg.GetErr().GetCode())
	assert.Empty(t, over.Msg.GetUrls(), "TTL 非法时不得签发")

	// ---- 6. 游标分页 ----
	list, err := client.ListMediaAssetsBefore(context.Background(),
		connect.NewRequest(&mediav1.ListMediaAssetsBeforeRequest{}))
	require.NoError(t, err, "ListMediaAssetsBefore RPC")
	assert.NotEmpty(t, list.Msg.GetAssets(), "应能列举到资产")

	// ---- 7. 封禁 ----
	ban, err := client.BanMedia(context.Background(),
		connect.NewRequest(&mediav1.BanMediaRequest{AssetId: assetID}))
	require.NoError(t, err, "BanMedia RPC")
	require.False(t, ban.Msg.GetFail(), "BanMedia 业务失败: %v", ban.Msg.GetErr())

	got, err := client.GetMediaAssets(context.Background(),
		connect.NewRequest(&mediav1.GetMediaAssetsRequest{Ids: []string{assetID}}))
	require.NoError(t, err)
	require.False(t, got.Msg.GetResults()[0].GetFail())
	assert.Equal(t, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_BANNED,
		got.Msg.GetResults()[0].GetAsset().GetStatus(), "封禁后状态应为 BANNED")
}
