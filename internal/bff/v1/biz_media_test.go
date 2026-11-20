package v1

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	bffv1 "rednote/gen/go/bff/v1"
	commonv1 "rednote/gen/go/common/v1"
	mediav1 "rednote/gen/go/media/v1"
	"rednote/packages/defaults"
	"rednote/packages/errmsg"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBFFUploadMedia 上传媒体：uploader 会话注入 + 预签名 URL 透传
func TestBFFUploadMedia(t *testing.T) {
	meta := &mediav1.MediaMetaData{Format: mediav1.MediaFormat_MEDIA_FORMAT_JPG, SizeBytes: 100}

	t.Run("无会话返回 Unauthorized", func(t *testing.T) {
		u := newBFFForTest(&fakeUserClient{}, &fakeMediaClient{})
		resp, err := u.UploadMedia(context.Background(), &bffv1.UploadMediaRequest{Meta: meta})
		require.NoError(t, err)
		assert.True(t, resp.Fail)
		assert.Equal(t, errmsg.Unauthorized.Code, resp.Err.GetCode())
	})

	t.Run("成功：uploader 注入 + 三字段透传", func(t *testing.T) {
		media := &fakeMediaClient{upload: &mediav1.UploadMediaResponse{
			Asset: &mediav1.MediaAsset{
				Base:       &commonv1.BaseModel{Id: "fa1"},
				Status:     mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_UPLOADING,
				RefCount:   0,
				UploaderId: "u1",
			},
			SignedUrl: "https://s3/presigned",
			ExpireAt:  1700000000000,
		}}
		u := newBFFForTest(&fakeUserClient{}, media)

		resp, err := u.UploadMedia(ctxWithUID("u1"), &bffv1.UploadMediaRequest{Meta: meta})
		require.NoError(t, err)
		require.False(t, resp.Fail)
		assert.Nil(t, resp.Err)
		require.Len(t, media.gotUploadReqs, 1)
		assert.Equal(t, "u1", media.gotUploadReqs[0].GetUploaderId(), "uploader 必须来自会话")
		assert.Equal(t, meta, media.gotUploadReqs[0].GetMediaMeta(), "媒体元数据透传")
		assert.Equal(t, "https://s3/presigned", resp.GetSignedUrl())
		assert.Equal(t, int64(1700000000000), resp.GetExpireAt())
		// 全字段断言：Asset 逐字段透传
		assert.Equal(t, "fa1", resp.GetAsset().GetBase().GetId())
		assert.Equal(t, "u1", resp.GetAsset().GetUploaderId())
		assert.Equal(t, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_UPLOADING, resp.GetAsset().GetStatus())
		assert.Equal(t, int64(0), resp.GetAsset().GetRefCount())
	})

	t.Run("业务失败透传", func(t *testing.T) {
		media := &fakeMediaClient{uploadErr: errmsg.InvalidFormat}
		u := newBFFForTest(&fakeUserClient{}, media)

		resp, err := u.UploadMedia(ctxWithUID("u1"), &bffv1.UploadMediaRequest{Meta: meta})
		require.NoError(t, err)
		assert.True(t, resp.Fail)
		assert.Equal(t, errmsg.InvalidFormat.Code, resp.Err.GetCode())
	})

	t.Run("连接层错误归为 InternalError", func(t *testing.T) {
		media := &fakeMediaClient{uploadRPCErr: errors.New("dial tcp: connection refused")}
		u := newBFFForTest(&fakeUserClient{}, media)

		resp, err := u.UploadMedia(ctxWithUID("u1"), &bffv1.UploadMediaRequest{Meta: meta})
		require.NoError(t, err, "连接层错误在 biz 层包装为业务失败")
		assert.True(t, resp.Fail)
		assert.Equal(t, errmsg.InternalError.Code, resp.Err.GetCode())
	})
}

// TestBFFGetAndRefMediaFile 挂载点 helper：参数校验 → 双查 → ACTIVE 检查 → 计数 +1
func TestBFFGetAndRefMediaFile(t *testing.T) {
	activeAsset := &mediav1.MediaAsset{UploaderId: "u1", Status: mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE}

	t.Run("参数校验", func(t *testing.T) {
		u := newBFFForTest(&fakeUserClient{}, &fakeMediaClient{})

		f, errCode := u.GetAndRefMediaFile(context.Background(), "", mediav1.MediaFileType_MEDIA_FILE_TYPE_216)
		assert.Nil(t, f)
		assert.Equal(t, errmsg.InvalidID.Code, errCode.GetCode())

		f, errCode = u.GetAndRefMediaFile(context.Background(), "a1", mediav1.MediaFileType_MEDIA_FILE_TYPE_UNSPECIFIED)
		assert.Nil(t, f)
		assert.Equal(t, errmsg.InvalidArgument.Code, errCode.GetCode())
	})

	t.Run("连接层错误归为 InternalError", func(t *testing.T) {
		u := newBFFForTest(&fakeUserClient{}, &fakeMediaClient{assetRPCErr: errors.New("boom")})
		f, errCode := u.GetAndRefMediaFile(context.Background(), "a1", mediav1.MediaFileType_MEDIA_FILE_TYPE_216)
		assert.Nil(t, f)
		assert.Equal(t, errmsg.InternalError.Code, errCode.GetCode())
	})

	t.Run("资产不存在透传错误码", func(t *testing.T) {
		u := newBFFForTest(&fakeUserClient{}, &fakeMediaClient{assetErr: errmsg.NotFound})
		f, errCode := u.GetAndRefMediaFile(context.Background(), "a1", mediav1.MediaFileType_MEDIA_FILE_TYPE_216)
		assert.Nil(t, f)
		assert.Equal(t, errmsg.NotFound.Code, errCode.GetCode())
	})

	t.Run("资产非 ACTIVE 归为 InvalidMedia", func(t *testing.T) {
		media := &fakeMediaClient{asset: &mediav1.MediaAsset{Status: mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_UPLOADING}}
		u := newBFFForTest(&fakeUserClient{}, media)
		f, errCode := u.GetAndRefMediaFile(context.Background(), "a1", mediav1.MediaFileType_MEDIA_FILE_TYPE_216)
		assert.Nil(t, f)
		assert.Equal(t, errmsg.InvalidMedia.Code, errCode.GetCode())
		assert.Empty(t, media.refCalls, "状态不可用不得调整计数")
	})

	t.Run("文件缺失透传错误码且不计数", func(t *testing.T) {
		media := &fakeMediaClient{asset: activeAsset, fileErr: errmsg.NotFound}
		u := newBFFForTest(&fakeUserClient{}, media)
		f, errCode := u.GetAndRefMediaFile(context.Background(), "a1", mediav1.MediaFileType_MEDIA_FILE_TYPE_216)
		assert.Nil(t, f)
		assert.Equal(t, errmsg.NotFound.Code, errCode.GetCode())
		assert.Empty(t, media.refCalls)
	})

	t.Run("计数失败透传且整体失败", func(t *testing.T) {
		media := &fakeMediaClient{asset: activeAsset, file: &mediav1.MediaFile{}, refErr: errmsg.InvalidMedia}
		u := newBFFForTest(&fakeUserClient{}, media)
		f, errCode := u.GetAndRefMediaFile(context.Background(), "a1", mediav1.MediaFileType_MEDIA_FILE_TYPE_216)
		assert.Nil(t, f)
		assert.Equal(t, errmsg.InvalidMedia.Code, errCode.GetCode(), "挂载失败不得静默")
	})

	t.Run("成功：返回变体文件并计数 +1", func(t *testing.T) {
		file := &mediav1.MediaFile{Base: &commonv1.BaseModel{Id: "f1"}, Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_216}
		media := &fakeMediaClient{asset: activeAsset, file: file}
		u := newBFFForTest(&fakeUserClient{}, media)

		got, errCode := u.GetAndRefMediaFile(context.Background(), "a1", mediav1.MediaFileType_MEDIA_FILE_TYPE_216)
		require.Nil(t, errCode)
		assert.Equal(t, "f1", got.GetBase().GetId())
		assert.Equal(t, []refCall{{id: "a1", delta: 1}}, media.refCalls, "挂载点同步 +1")
	})
}

// TestBFFSignMediaViews 面客 MediaView 组装：批量签发 + 尺寸回填 + 分级 TTL + 失败不静默
func TestBFFSignMediaViews(t *testing.T) {
	meta := &mediav1.MediaMetaData{Width: 540, Height: 960}
	file := &mediav1.MediaFile{AssetId: "a1", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_540, Meta: meta}

	t.Run("成功：MediaView 全字段组装（URL/尺寸回填）", func(t *testing.T) {
		media := &fakeMediaClient{}
		u := newBFFForTest(&fakeUserClient{}, media)

		views, errCode := u.signMediaViews(context.Background(), publicMediaTTL, []*mediav1.MediaFile{file}, false)
		require.Nil(t, errCode)
		require.Len(t, views, 1)
		assert.Equal(t, "a1", views[0].GetAssetId())
		assert.Equal(t, mediav1.MediaFileType_MEDIA_FILE_TYPE_540, views[0].GetType())
		assert.Equal(t, "https://signed/a1", views[0].GetUrl())
		assert.Equal(t, int64(1700000000000), views[0].GetExpiresAt())
		assert.Equal(t, int64(540), views[0].GetWidth(), "尺寸从媒体元数据回填")
		assert.Equal(t, int64(960), views[0].GetHeight())
	})

	t.Run("TTL 分级透传：公开 7d / 私有 24h", func(t *testing.T) {
		media := &fakeMediaClient{}
		u := newBFFForTest(&fakeUserClient{}, media)

		_, errCode := u.signMediaViews(context.Background(), publicMediaTTL, []*mediav1.MediaFile{file}, false)
		require.Nil(t, errCode)
		_, errCode = u.signMediaViews(context.Background(), privateMediaTTL, []*mediav1.MediaFile{file}, false)
		require.Nil(t, errCode)

		require.Len(t, media.gotSignTTLs, 2)
		assert.Equal(t, int64(7*24*3600), media.gotSignTTLs[0], "公开内容长 TTL（7d 上限）")
		assert.Equal(t, int64(24*3600), media.gotSignTTLs[1], "私有内容 24h")
	})

	t.Run("nil 文件占位 nil 视图（无头像用户）", func(t *testing.T) {
		media := &fakeMediaClient{}
		u := newBFFForTest(&fakeUserClient{}, media)

		views, errCode := u.signMediaViews(context.Background(), publicMediaTTL, []*mediav1.MediaFile{nil}, false)
		require.Nil(t, errCode)
		require.Len(t, views, 1)
		assert.Nil(t, views[0])
		assert.Empty(t, media.gotSignReqs, "无引用不调下游")
	})

	t.Run("逐项签发失败整体报错不静默", func(t *testing.T) {
		media := &fakeMediaClient{signFailIDs: map[string]*commonv1.Error{"a1": errmsg.NotFound}}
		u := newBFFForTest(&fakeUserClient{}, media)

		views, errCode := u.signMediaViews(context.Background(), publicMediaTTL, []*mediav1.MediaFile{file}, false)
		assert.Nil(t, views)
		assert.Equal(t, errmsg.InternalError.Code, errCode.GetCode(), "签发失败不得返回半残 URL")
	})

	t.Run("failOpen：单项失败替换默认失败占位图，好项保留（关键回归：脏资产不拖垮整页）", func(t *testing.T) {
		bad := &mediav1.MediaFile{AssetId: "bad", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_540, Meta: meta}
		media := &fakeMediaClient{signFailIDs: map[string]*commonv1.Error{"bad": errmsg.NotFound}}
		u := newBFFForTest(&fakeUserClient{}, media)

		views, errCode := u.signMediaViews(context.Background(), publicMediaTTL,
			[]*mediav1.MediaFile{file, bad}, true)
		require.Nil(t, errCode, "单项脏资产不得整体报错")
		require.Len(t, views, 2)
		require.NotNil(t, views[0], "好项保留")
		assert.Equal(t, "https://signed/a1", views[0].GetUrl())
		require.NotNil(t, views[1], "失败项不缺格")
		assert.Equal(t, defaults.FallbackCoverURL, views[1].GetUrl(), "失败项替换默认失败占位图")
		assert.Equal(t, "bad", views[1].GetAssetId(), "保留原资产坐标供追溯")
	})

	t.Run("failOpen：RPC 级失败仍整体报错（非单项降级范围）", func(t *testing.T) {
		media := &fakeMediaClient{signRPCErr: errors.New("dial fail")}
		u := newBFFForTest(&fakeUserClient{}, media)

		views, errCode := u.signMediaViews(context.Background(), publicMediaTTL, []*mediav1.MediaFile{file}, true)
		assert.Nil(t, views)
		assert.Equal(t, errmsg.InternalError.Code, errCode.GetCode())
	})

	t.Run("连接层错误归为 InternalError", func(t *testing.T) {
		media := &fakeMediaClient{signRPCErr: errors.New("dial fail")}
		u := newBFFForTest(&fakeUserClient{}, media)

		views, errCode := u.signMediaViews(context.Background(), publicMediaTTL, []*mediav1.MediaFile{file}, false)
		assert.Nil(t, views)
		assert.Equal(t, errmsg.InternalError.Code, errCode.GetCode())
	})
}

// TestBFFGetAndRefMediaFiles 批量挂载边界：空入参拒绝、同资产多槽位逐槽 +1、文件与槽位顺序对齐
func TestBFFGetAndRefMediaFiles(t *testing.T) {
	active := &mediav1.MediaAsset{UploaderId: "u1", Status: mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE}

	t.Run("空入参拒绝且不调下游", func(t *testing.T) {
		media := &fakeMediaClient{}
		u := newBFFForTest(&fakeUserClient{}, media)

		files, errCode := u.GetAndRefMediaFiles(context.Background(), nil, "u1")
		assert.Nil(t, files)
		assert.Equal(t, errmsg.InvalidArgument.Code, errCode.GetCode())
		assert.Empty(t, media.refCalls)
	})

	t.Run("同资产多槽位：查询去重一次、逐槽 +1、文件与槽位对齐", func(t *testing.T) {
		file216 := &mediav1.MediaFile{Base: &commonv1.BaseModel{Id: "f216"}, Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_216}
		file540 := &mediav1.MediaFile{Base: &commonv1.BaseModel{Id: "f540"}, Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_540}
		media := &fakeMediaClient{
			batch:      []*mediav1.MediaAssetResult{{Id: "a1", Asset: active}},
			fileByType: map[mediav1.MediaFileType]*mediav1.MediaFile{file216.GetType(): file216, file540.GetType(): file540},
		}
		u := newBFFForTest(&fakeUserClient{}, media)

		files, errCode := u.GetAndRefMediaFiles(context.Background(), []*bffv1.AssetRef{
			{AssetId: "a1", Type: file216.GetType()},
			{AssetId: "a1", Type: file540.GetType()},
		}, "u1")
		require.Nil(t, errCode)
		require.Len(t, files, 2)
		assert.Equal(t, []string{"a1"}, media.gotBatchIDs, "同资产去重后批量查询一次")
		assert.Equal(t, []refCall{{id: "a1", delta: 1}, {id: "a1", delta: 1}}, media.refCalls,
			"同资产多变体各占一槽，逐槽 +1")
		assert.Equal(t, "f216", files[0].GetBase().GetId(), "文件与请求槽位顺序对齐")
		assert.Equal(t, "f540", files[1].GetBase().GetId())
	})

	t.Run("任一变体查询失败：整体失败不计数", func(t *testing.T) {
		media := &fakeMediaClient{
			batch:   []*mediav1.MediaAssetResult{{Id: "a1", Asset: active}},
			fileErr: errmsg.NotFound,
		}
		u := newBFFForTest(&fakeUserClient{}, media)

		files, errCode := u.GetAndRefMediaFiles(context.Background(), []*bffv1.AssetRef{
			{AssetId: "a1", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_216},
			{AssetId: "a1", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_540},
		}, "u1")
		assert.Nil(t, files)
		assert.Equal(t, errmsg.NotFound.Code, errCode.GetCode())
		assert.Empty(t, media.refCalls, "挂载前失败不得产生计数副作用")
	})
}

// TestBFFGetMediaAssetStatus 资产状态批量查询：逐项结果 + 只读（供前端轮询上传完成）
func TestBFFGetMediaAssetStatus(t *testing.T) {
	t.Run("参数校验：空 ids / 含空串拒绝", func(t *testing.T) {
		u := newBFFForTest(&fakeUserClient{}, &fakeMediaClient{})

		r, err := u.GetMediaAssetStatus(context.Background(), &bffv1.GetMediaAssetStatusRequest{})
		require.NoError(t, err)
		assert.Equal(t, errmsg.InvalidArgument.Code, r.Err.GetCode())

		r, err = u.GetMediaAssetStatus(context.Background(), &bffv1.GetMediaAssetStatusRequest{Ids: []string{"a1", ""}})
		require.NoError(t, err)
		assert.Equal(t, errmsg.InvalidArgument.Code, r.Err.GetCode())
	})

	t.Run("连接层错误归为 InternalError", func(t *testing.T) {
		u := newBFFForTest(&fakeUserClient{}, &fakeMediaClient{batchRPCErr: errors.New("dial fail")})
		r, err := u.GetMediaAssetStatus(context.Background(), &bffv1.GetMediaAssetStatusRequest{Ids: []string{"a1"}})
		require.NoError(t, err)
		assert.True(t, r.Fail)
		assert.Equal(t, errmsg.InternalError.Code, r.Err.GetCode())
	})

	t.Run("逐项结果：状态透传 + 查无逐项 fail", func(t *testing.T) {
		media := &fakeMediaClient{batch: []*mediav1.MediaAssetResult{
			{Fail: false, Id: "a1", Asset: &mediav1.MediaAsset{Status: mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_UPLOADING}},
			{Fail: true, Err: errmsg.NotFound, Id: "gone"},
		}}
		u := newBFFForTest(&fakeUserClient{}, media)

		r, err := u.GetMediaAssetStatus(context.Background(), &bffv1.GetMediaAssetStatusRequest{Ids: []string{"a1", "gone"}})
		require.NoError(t, err)
		require.False(t, r.Fail)
		require.Len(t, r.Statuses, 2)
		assert.Equal(t, []string{"a1", "gone"}, media.gotBatchIDs, "ids 透传")

		assert.False(t, r.Statuses[0].GetFail())
		assert.Equal(t, "a1", r.Statuses[0].GetAssetId())
		assert.Equal(t, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_UPLOADING, r.Statuses[0].GetStatus())

		assert.True(t, r.Statuses[1].GetFail())
		assert.Equal(t, errmsg.NotFound.Code, r.Statuses[1].GetErr().GetCode())
		assert.Equal(t, "gone", r.Statuses[1].GetAssetId())
		assert.Equal(t, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_UNSPECIFIED, r.Statuses[1].GetStatus())
	})
}

// TestBFFDeRefMediaFile 卸载点 helper：发 MQ 异步卸载消息（media consumer 递减）
func TestBFFDeRefMediaFile(t *testing.T) {
	t.Run("空资产 ID 拒绝", func(t *testing.T) {
		u := newBFFForTest(&fakeUserClient{}, &fakeMediaClient{})
		assert.Equal(t, errmsg.InvalidID.Code, u.DeRefMediaFile(context.Background(), "").GetCode())
	})

	t.Run("成功：发 DerefMedia 消息", func(t *testing.T) {
		u := newBFFForTest(&fakeUserClient{}, &fakeMediaClient{})
		require.Nil(t, u.DeRefMediaFile(context.Background(), "a9"))
		assert.Equal(t, []string{"a9"}, derefSent(t, u))
	})

	t.Run("发送失败归为 InternalError", func(t *testing.T) {
		u := &BFFUsecaseImpl{
			Repo: &DownstreamRepo{},
			MQ:   &fakeMQClient{sendErr: errors.New("nats: no responders")},
			log:  slog.Default(),
		}
		assert.Equal(t, errmsg.InternalError.Code, u.DeRefMediaFile(context.Background(), "a9").GetCode())
	})
}

// TestBFFGetMediaAssetStatusMixedBatch 混合批量顺序对齐（G12 边界）：成功/失败交错 5 项，
// 逐项状态与请求 ids 顺序严格对齐（防批量结果错位）
func TestBFFGetMediaAssetStatusMixedBatch(t *testing.T) {
	media := &fakeMediaClient{batch: []*mediav1.MediaAssetResult{
		{Fail: false, Id: "s1", Asset: &mediav1.MediaAsset{Status: mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE}},
		{Fail: true, Err: errmsg.NotFound, Id: "m1"},
		{Fail: false, Id: "s2", Asset: &mediav1.MediaAsset{Status: mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_UPLOADING}},
		{Fail: true, Err: errmsg.InvalidID, Id: "m2"},
		{Fail: false, Id: "s3", Asset: &mediav1.MediaAsset{Status: mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_FAILED}},
	}}
	u := newBFFForTest(&fakeUserClient{}, media)

	ids := []string{"s1", "m1", "s2", "m2", "s3"}
	r, err := u.GetMediaAssetStatus(context.Background(), &bffv1.GetMediaAssetStatusRequest{Ids: ids})
	require.NoError(t, err)
	require.False(t, r.Fail, "整体不失败，逐项表达结果")
	assert.Equal(t, ids, media.gotBatchIDs, "ids 顺序透传")
	require.Len(t, r.Statuses, 5)

	// 逐项位置严格对齐（成功/失败交错不串位）
	assert.Equal(t, "s1", r.Statuses[0].GetAssetId())
	assert.False(t, r.Statuses[0].GetFail())
	assert.Equal(t, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, r.Statuses[0].GetStatus())

	assert.Equal(t, "m1", r.Statuses[1].GetAssetId())
	assert.True(t, r.Statuses[1].GetFail())
	assert.Equal(t, errmsg.NotFound.Code, r.Statuses[1].GetErr().GetCode())

	assert.Equal(t, "s2", r.Statuses[2].GetAssetId())
	assert.False(t, r.Statuses[2].GetFail())
	assert.Equal(t, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_UPLOADING, r.Statuses[2].GetStatus())

	assert.Equal(t, "m2", r.Statuses[3].GetAssetId())
	assert.True(t, r.Statuses[3].GetFail())
	assert.Equal(t, errmsg.InvalidID.Code, r.Statuses[3].GetErr().GetCode())

	assert.Equal(t, "s3", r.Statuses[4].GetAssetId())
	assert.False(t, r.Statuses[4].GetFail())
	assert.Equal(t, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_FAILED, r.Statuses[4].GetStatus())
}

// TestBFFSignMediaViews_展示尺寸 展示尺寸策略：卡片 540 / 详情 1080；变体缺失回退原图
func TestBFFSignMediaViews_展示尺寸(t *testing.T) {
	meta := &mediav1.MediaMetaData{Width: 1920, Height: 1080}
	file := &mediav1.MediaFile{AssetId: "a1", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN, Meta: meta}

	t.Run("卡片：请求 540 且回填实际签发尺寸", func(t *testing.T) {
		media := &fakeMediaClient{}
		u := newBFFForTest(&fakeUserClient{}, media)

		views, errCode := u.signMediaViewsAt(context.Background(), publicMediaTTL,
			[]*mediav1.MediaFile{file}, true, cardDisplayType)
		require.Nil(t, errCode)
		require.Len(t, views, 1)
		assert.Equal(t, mediav1.MediaFileType_MEDIA_FILE_TYPE_540, media.gotSignReqs[0].GetType(), "应请求 540 变体")
		assert.Equal(t, mediav1.MediaFileType_MEDIA_FILE_TYPE_540, views[0].GetType())
		assert.Equal(t, "https://signed/a1", views[0].GetUrl())
		assert.Len(t, media.gotSignReqs, 1, "变体齐全时只签一轮")
	})

	t.Run("详情：请求 1080", func(t *testing.T) {
		media := &fakeMediaClient{}
		u := newBFFForTest(&fakeUserClient{}, media)

		_, errCode := u.signMediaViewsAt(context.Background(), publicMediaTTL,
			[]*mediav1.MediaFile{file}, false, detailDisplayType)
		require.Nil(t, errCode)
		assert.Equal(t, mediav1.MediaFileType_MEDIA_FILE_TYPE_1080, media.gotSignReqs[0].GetType())
	})

	t.Run("变体缺失：回退 ORIGIN 签发（failOpen 详情也不报错）", func(t *testing.T) {
		media := &fakeMediaClient{signFailTypes: map[mediav1.MediaFileType]bool{
			mediav1.MediaFileType_MEDIA_FILE_TYPE_540: true,
		}}
		u := newBFFForTest(&fakeUserClient{}, media)

		views, errCode := u.signMediaViewsAt(context.Background(), publicMediaTTL,
			[]*mediav1.MediaFile{file}, false, cardDisplayType)
		require.Nil(t, errCode, "缺变体应回退原图而不是整页报错")
		require.Len(t, views, 1)
		assert.Equal(t, mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN, views[0].GetType(), "回退到原图")
		require.Len(t, media.gotSignReqs, 2, "第一轮 540 失败 → 第二轮 ORIGIN")
		assert.Equal(t, mediav1.MediaFileType_MEDIA_FILE_TYPE_540, media.gotSignReqs[0].GetType())
		assert.Equal(t, mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN, media.gotSignReqs[1].GetType())
	})

	t.Run("两轮都失败：failOpen=false 报错，failOpen=true 占位图", func(t *testing.T) {
		failAll := func() *fakeMediaClient {
			return &fakeMediaClient{signFailTypes: map[mediav1.MediaFileType]bool{
				mediav1.MediaFileType_MEDIA_FILE_TYPE_540:    true,
				mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN: true,
			}}
		}
		u := newBFFForTest(&fakeUserClient{}, failAll())
		_, errCode := u.signMediaViewsAt(context.Background(), publicMediaTTL,
			[]*mediav1.MediaFile{file}, false, cardDisplayType)
		assert.NotNil(t, errCode, "强字段两轮都失败 → 整体报错")

		u = newBFFForTest(&fakeUserClient{}, failAll())
		views, errCode := u.signMediaViewsAt(context.Background(), publicMediaTTL,
			[]*mediav1.MediaFile{file}, true, cardDisplayType)
		require.Nil(t, errCode)
		require.Len(t, views, 1)
		assert.Equal(t, defaults.FallbackCoverURL, views[0].GetUrl(), "弱字段降级占位图")
	})

	t.Run("want=ORIGIN：保持原语义，只签一轮", func(t *testing.T) {
		media := &fakeMediaClient{}
		u := newBFFForTest(&fakeUserClient{}, media)

		_, errCode := u.signMediaViewsAt(context.Background(), publicMediaTTL,
			[]*mediav1.MediaFile{file}, false, mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN)
		require.Nil(t, errCode)
		assert.Equal(t, mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN, media.gotSignReqs[0].GetType())
		assert.Len(t, media.gotSignReqs, 1)
	})
}
