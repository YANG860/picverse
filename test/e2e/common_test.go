// e2e 黑盒测试共享工具：服务地址解析、可达性探测、业务错误断言、测试数据构造。
// 各服务用例文件（auth / media / post / user）只保留自身链路步骤与专属 helper。
//
// 通用约定：
//   - 目标地址用环境变量覆盖（AUTH_E2E_ADDR / MEDIA_E2E_ADDR / POST_E2E_ADDR / USER_E2E_ADDR），
//     默认指向本机对应端口；
//   - 服务不可达时跳过而非失败，便于本地/CI 按需运行；
//   - go test -short 跳过全部 e2e 用例。
package e2e

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	commonv1 "rednote/gen/go/common/v1"
	mediav1 "rednote/gen/go/media/v1"
	"rednote/gen/go/media/v1/mediav1connect"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// e2eAddr 解析 e2e 目标地址：环境变量优先，否则用默认值
func e2eAddr(envKey, fallback string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	return fallback
}

// mustReachable 探测服务可达性（GET 服务前缀路径），不可达则跳过当前测试
func mustReachable(t *testing.T, addr, servicePath string) {
	t.Helper()

	resp, err := http.Get(addr + servicePath)
	if err != nil {
		t.Skipf("e2e: 服务不可达 (%s)，跳过（恢复条件：部署该服务并可达后重跑）: %v", addr, err)
	}
	_ = resp.Body.Close()
}

// assertBizErr 断言业务失败及错误码
func assertBizErr(t *testing.T, fail bool, err *commonv1.Error, want *commonv1.Error, msg string) {
	t.Helper()
	require.True(t, fail, "%s 应业务失败", msg)
	require.NotNil(t, err, "%s 应返回错误信息", msg)
	assert.Equal(t, want.GetCode(), err.GetCode(), "%s 错误码不符: want=%s got=%s",
		msg, want.GetMsg(), err.GetMsg())
}

// activeAsset 通过 media 服务真实上传一张测试图并等到 ACTIVE（media consumer 处理完成），
// 返回可直接挂到帖子/用户上的 ORIGIN 媒体文件。
// 返回 error 而非就地 require：允许调用方在 goroutine 里并发上传，断言统一回测试 goroutine。
func activeAsset(ctx context.Context, client mediav1connect.MediaServiceClient, img *assetImage, uploaderID string) (*mediav1.MediaFile, error) {
	up, err := client.UploadMedia(ctx, connect.NewRequest(&mediav1.UploadMediaRequest{
		UploaderId: uploaderID,
		MediaMeta: &mediav1.MediaMetaData{
			Sha_256Sum: img.sha256,
			SizeBytes:  int64(len(img.data)),
			Format:     mediav1.MediaFormat_MEDIA_FORMAT_JPG,
			Height:     int64(img.height),
			Width:      int64(img.width),
		},
	}))
	if err != nil {
		return nil, fmt.Errorf("media.UploadMedia failed: %w", err)
	}
	if up.Msg.GetFail() {
		return nil, fmt.Errorf("media.UploadMedia biz fail: %s", up.Msg.GetErr().GetMsg())
	}

	assetID := up.Msg.GetAsset().GetBase().GetId()
	if assetID == "" {
		return nil, fmt.Errorf("media.UploadMedia returned empty asset id")
	}

	// 预签名直传（对象未落地时 media consumer 会在重投窗口内退避重试，最终判失败）
	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, up.Msg.GetSignedUrl(), bytes.NewReader(img.data))
	if err != nil {
		return nil, fmt.Errorf("build presigned PUT failed: %w", err)
	}
	putReq.Header.Set("Content-Type", "image/jpeg")
	putReq.Header.Set("Content-Length", fmt.Sprint(len(img.data)))
	resp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		return nil, fmt.Errorf("presigned PUT failed: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("presigned PUT status %d", resp.StatusCode)
	}

	// 等异步处理完成：UPLOADING → AUDITING → PROCESSING → ACTIVE
	deadline := time.Now().Add(processTimeout)
	for {
		got, err := client.GetMediaAssets(ctx, connect.NewRequest(&mediav1.GetMediaAssetsRequest{Ids: []string{assetID}}))
		if err == nil && len(got.Msg.GetResults()) == 1 && !got.Msg.GetResults()[0].GetFail() {
			status := got.Msg.GetResults()[0].GetAsset().GetStatus()
			if status == mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE {
				break
			}
			if status == mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_FAILED ||
				status == mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ERROR ||
				status == mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_BANNED ||
				status == mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_DELETED {
				return nil, fmt.Errorf("asset %s reached terminal status %s", assetID, status)
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("asset %s not ACTIVE within %s", assetID, processTimeout)
		}
		time.Sleep(time.Second)
	}

	// 取 ORIGIN 文件（含 base.id/obj_key/bucket/meta）直接作为帖子媒体使用
	files, err := client.GetMediaAssetFiles(ctx, connect.NewRequest(&mediav1.GetMediaAssetFilesRequest{
		Queries: []*mediav1.MediaAssetFileQuery{{
			Id:   assetID,
			Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN,
		}},
	}))
	if err != nil {
		return nil, fmt.Errorf("media.GetMediaAssetFiles failed: %w", err)
	}
	if len(files.Msg.GetFiles()) != 1 || files.Msg.GetFiles()[0].GetFail() {
		return nil, fmt.Errorf("media.GetMediaAssetFiles missing ORIGIN file for %s", assetID)
	}
	return files.Msg.GetFiles()[0].GetFile(), nil
}

// activeMediaFiles 并发上传 n 张测试图并全部等到 ACTIVE（发布用例的媒体前置），任一张失败即 require 失败。
// 媒体服务不可达时跳过当前测试（发布链路依赖它）。
func activeMediaFiles(t *testing.T, n int, uploaderSeed int64) []*mediav1.MediaFile {
	t.Helper()

	mustReachable(t, mediaAddr(), "/media.v1.MediaService/")
	client := mediav1connect.NewMediaServiceClient(http.DefaultClient, mediaAddr())
	img := loadAssetImage(t)

	files := make([]*mediav1.MediaFile, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			files[i], errs[i] = activeAsset(context.Background(), client, img, e2eID(uploaderSeed+int64(i)))
		}(i)
	}
	wg.Wait()

	for i := range n {
		require.NoError(t, errs[i], "上传第 %d 张测试图并等到 ACTIVE", i+1)
		require.NotNil(t, files[i])
	}
	return files
}

// e2eID 生成测试用雪花 ID 字符串（业务服务只做 ID 解析，不校验实体存在）
func e2eID(seed int64) string { return fmt.Sprintf("%d", seed) }

// e2eMediaFile 构造"仅在库中落关联"的媒体文件（业务写入路径不校验资产存在性，obj_key 非空即可）。
// 注意：发布链路（audit_post）会校验帖内媒体资产 ACTIVE，超时即回退 DRAFT——
// **凡是会走到发布的用例必须用 activeMediaFiles 真上传**，伪造 asset_id 只适用于不经过审核的路径
// （如用户头像、仅建草稿/删帖回收）。
// base.id 为文件雪花 ID，须各不相同
func e2eMediaFile(fileSeed, assetID, objKey string) *mediav1.MediaFile {
	return &mediav1.MediaFile{
		Base:    &commonv1.BaseModel{Id: fileSeed},
		AssetId: assetID,
		Type:    mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN,
		ObjKey:  objKey,
		Bucket:  "rednote",
		Meta:    &mediav1.MediaMetaData{Format: mediav1.MediaFormat_MEDIA_FORMAT_JPG, SizeBytes: 1024, Sha_256Sum: "e2e-sha"},
	}
}
