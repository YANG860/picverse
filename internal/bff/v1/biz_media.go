package v1

import (
	"context"
	"slices"
	"time"

	bffv1 "rednote/gen/go/bff/v1"
	commonv1 "rednote/gen/go/common/v1"
	mediav1 "rednote/gen/go/media/v1"
	"rednote/packages/defaults"
	"rednote/packages/errmsg"
	"rednote/packages/jwt"
	"rednote/packages/mq"

	"connectrpc.com/connect"
	"golang.org/x/sync/errgroup"
)

// GetAndRefMediaFile 获取、校验并引用媒体文件（挂载点 +1）：按 (assetID, ftype) 取目标变体，
// 校验资产存在且 ACTIVE，再 ModifyMediaAssetRefCount +1；失败整体失败，不产生
// “挂载成功但计数漂移”的静默状态。
// 归属校验由调用方按场景比对 uploader_id（注册场景固定挂默认头像资产 defaults.AvatarAssetID，天然跳过）。
// 错误走第二返回值：业务可枚举错误透传，连接层/内部错误归为 InternalError。
func (u *BFFUsecaseImpl) GetAndRefMediaFile(ctx context.Context, assetID string, ftype mediav1.MediaFileType) (*mediav1.MediaFile, *commonv1.Error) {
	if assetID == "" {
		return nil, errmsg.InvalidID
	}
	if ftype == mediav1.MediaFileType_MEDIA_FILE_TYPE_UNSPECIFIED {
		return nil, errmsg.InvalidArgument
	}

	// 资产元数据与文件元数据互不依赖，并行获取
	var assetResp *connect.Response[mediav1.GetMediaAssetResponse]
	var fileResp *connect.Response[mediav1.GetMediaAssetFileResponse]
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		var err error
		assetResp, err = u.Repo.Media.GetMediaAsset(gctx, connect.NewRequest(&mediav1.GetMediaAssetRequest{Id: assetID}))
		return err
	})
	g.Go(func() error {
		var err error
		fileResp, err = u.Repo.Media.GetMediaAssetFile(gctx, connect.NewRequest(&mediav1.GetMediaAssetFileRequest{Id: assetID, Type: ftype}))
		return err
	})
	if err := g.Wait(); err != nil {
		u.log.Error("media.GetMediaAsset/GetMediaAssetFile failed", "asset_id", assetID, "type", ftype, "error", err)
		return nil, errmsg.InternalError
	}

	// 状态不可用归为 InvalidMedia
	if assetResp.Msg.GetFail() {
		return nil, assetResp.Msg.GetErr()
	}
	if assetResp.Msg.GetAsset().GetStatus() != mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE {
		return nil, errmsg.InvalidMedia
	}
	if fileResp.Msg.GetFail() {
		return nil, fileResp.Msg.GetErr()
	}

	refResp, err := u.Repo.Media.ModifyMediaAssetRefCount(ctx, connect.NewRequest(&mediav1.ModifyMediaAssetRefCountRequest{
		Queries: []*mediav1.ModifyMediaAssetRefCountQuery{{Id: assetID, Delta: 1}},
	}))
	if err != nil {
		u.log.Error("media.ModifyMediaAssetRefCount failed", "asset_id", assetID, "delta", 1, "error", err)
		return nil, errmsg.InternalError
	}
	if refResp.Msg.GetFail() {
		return nil, refResp.Msg.GetErr()
	}

	return fileResp.Msg.GetFile(), nil
}

// GetAndRefMediaFiles 批量获取、校验并引用媒体文件（挂载点 +1）：批量归属校验（uploader_id == owner）
// → 并行取各槽位变体文件 → ModifyMediaAssetRefCount 原子批量 +1。任一环节失败整体失败，
// 计数不发生变化（+1 是最后一步原子调用，之前无副作用）。
// 计数语义：一个媒体槽位 = 1 引用（同资产多变体各占一槽，各自 +1/-1），替换时旧槽 -1、新槽 +1 自动对齐。
func (u *BFFUsecaseImpl) GetAndRefMediaFiles(ctx context.Context, refs []*bffv1.AssetRef, owner string) ([]*mediav1.MediaFile, *commonv1.Error) {
	if len(refs) == 0 {
		return nil, errmsg.InvalidArgument
	}
	// 去重后的ID 用于批量查询media_asset
	ids := make([]string, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, r := range refs {
		if r.GetAssetId() == "" {
			return nil, errmsg.InvalidID
		}
		if r.GetType() == mediav1.MediaFileType_MEDIA_FILE_TYPE_UNSPECIFIED {
			return nil, errmsg.InvalidArgument
		}
		if _, ok := seen[r.GetAssetId()]; !ok {
			seen[r.GetAssetId()] = struct{}{}
			ids = append(ids, r.GetAssetId())
		}
	}

	// 归属 + 状态批量校验：逐项 fail 透传，非本用户资产 Forbidden，状态不可用 InvalidMedia
	assetResp, err := u.Repo.Media.GetMediaAssets(ctx, connect.NewRequest(&mediav1.GetMediaAssetsRequest{Ids: ids}))
	if err != nil {
		u.log.Error("media.GetMediaAssets failed", "ids", ids, "error", err)
		return nil, errmsg.InternalError
	}
	assets := make(map[string]*mediav1.MediaAsset, len(ids))
	for _, r := range assetResp.Msg.GetResults() {
		if r.GetFail() {
			return nil, r.GetErr()
		}
		assets[r.GetId()] = r.GetAsset()
	}
	for _, id := range ids {
		a := assets[id]
		if a == nil {
			u.log.Error("media.GetMediaAssets missing item", "asset_id", id)
			return nil, errmsg.InternalError
		}
		if a.GetUploaderId() != owner {
			return nil, errmsg.Forbidden
		}
		if a.GetStatus() != mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE {
			return nil, errmsg.InvalidMedia
		}
	}

	// 并行取各槽位变体文件（结果与 refs 对齐）
	files := make([]*mediav1.MediaFile, len(refs))
	errCodes := make([]*commonv1.Error, len(refs))
	g, gctx := errgroup.WithContext(ctx)
	for i := range refs {
		g.Go(func() error {
			resp, err := u.Repo.Media.GetMediaAssetFile(gctx, connect.NewRequest(&mediav1.GetMediaAssetFileRequest{Id: refs[i].GetAssetId(), Type: refs[i].GetType()}))
			if err != nil {
				return err
			}
			if resp.Msg.GetFail() {
				errCodes[i] = resp.Msg.GetErr()
				return nil
			}
			files[i] = resp.Msg.GetFile()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		u.log.Error("media.GetMediaAssetFile failed", "count", len(refs), "error", err)
		return nil, errmsg.InternalError
	}
	for _, ec := range errCodes {
		if ec != nil {
			return nil, ec
		}
	}

	// 引用：原子批量 +1（全部成功或全部失败）
	queries := make([]*mediav1.ModifyMediaAssetRefCountQuery, 0, len(refs))
	for _, r := range refs {
		queries = append(queries, &mediav1.ModifyMediaAssetRefCountQuery{Id: r.GetAssetId(), Delta: 1})
	}
	refResp, err := u.Repo.Media.ModifyMediaAssetRefCount(ctx, connect.NewRequest(&mediav1.ModifyMediaAssetRefCountRequest{Queries: queries}))
	if err != nil {
		u.log.Error("media.ModifyMediaAssetRefCount failed", "ids", ids, "delta", 1, "error", err)
		return nil, errmsg.InternalError
	}
	if refResp.Msg.GetFail() {
		return nil, refResp.Msg.GetErr()
	}
	return files, nil
}

// rollbackRefs 挂载回滚：对已挂载槽位发卸载消息（-1，MQ 异步）；发送失败记 ERROR 待闲置回收兜底，
// 不改变调用方的失败返回（回滚 best-effort，主错误已确定）。
func (u *BFFUsecaseImpl) rollbackRefs(ctx context.Context, refs []*bffv1.AssetRef, uid string) {
	for _, r := range refs {
		if rb := u.DeRefMediaFile(ctx, r.GetAssetId()); rb != nil {
			u.log.Error("rollback media ref count failed", "uid", uid, "asset_id", r.GetAssetId(), "err_code", rb.GetCode())
		}
	}
}

// DeRefMediaFile 解引用媒体资产（卸载点 -1）：卸载走 MQ 异步（media consumer 消费递减），
// 卸载可最终一致（挂载 +1 仍同步）；发送失败仍返回错误，不静默。
func (u *BFFUsecaseImpl) DeRefMediaFile(ctx context.Context, assetID string) *commonv1.Error {
	if assetID == "" {
		return errmsg.InvalidID
	}

	msg, err := mq.NewMQMessage(defaults.MediaMessageStream, nil, &mediav1.MediaMessage{
		Payload: &mediav1.MediaMessage_DerefMedia{
			DerefMedia: &mediav1.DerefMedia{AssetId: assetID},
		},
	})
	if err != nil {
		u.log.Error("mq.NewMQMessage failed", "asset_id", assetID, "error", err)
		return errmsg.InternalError
	}
	if err := u.MQ.Send(ctx, msg); err != nil {
		u.log.Error("mq send deref media failed", "asset_id", assetID, "error", err)
		return errmsg.InternalError
	}
	return nil
}

// ==================== 媒体 ====================

// GetMediaAssetStatus 批量查询媒体资产状态（供前端上传后轮询 UPLOADING→ACTIVE）：
// 逐项 fail 标识该项失败；纯读，不改引用计数。
func (u *BFFUsecaseImpl) GetMediaAssetStatus(ctx context.Context, req *bffv1.GetMediaAssetStatusRequest) (*bffv1.GetMediaAssetStatusResponse, error) {
	if len(req.GetIds()) == 0 || slices.Contains(req.GetIds(), "") {
		return &bffv1.GetMediaAssetStatusResponse{Fail: true, Err: errmsg.InvalidArgument}, nil
	}

	resp, err := u.Repo.Media.GetMediaAssets(ctx, connect.NewRequest(&mediav1.GetMediaAssetsRequest{Ids: req.GetIds()}))
	if err != nil {
		u.log.Error("media.GetMediaAssets failed", "ids", req.GetIds(), "error", err)
		return &bffv1.GetMediaAssetStatusResponse{Fail: true, Err: errmsg.InternalError}, nil
	}

	statuses := make([]*bffv1.MediaAssetStatus, 0, len(resp.Msg.GetResults()))
	for _, r := range resp.Msg.GetResults() {
		st := &bffv1.MediaAssetStatus{
			Fail:    r.GetFail(),
			Err:     r.GetErr(),
			AssetId: r.GetId(),
		}
		if !r.GetFail() {
			st.Status = r.GetAsset().GetStatus()
		}
		statuses = append(statuses, st)
	}
	return &bffv1.GetMediaAssetStatusResponse{Statuses: statuses}, nil
}

// ==================== MediaView 组装（BFF 返回前内嵌签发） ====================

// 面客 URL TTL 分级（详见 doc/internal/bff/bff.md）：公开内容 7d 上限（未来可切 CDN），私有内容 24h，过期重拉原接口。
var (
	publicMediaTTL  = defaults.MediaUrlMaxTTL
	privateMediaTTL = defaults.MediaUrlTTL
)

// fallbackMediaView 构造默认失败占位图视图（签名失败项替换用）：保留原资产坐标供追溯，
// URL 指向面客前端托管的静态占位图（零依赖，不走签名链路，避免占位再失败）
func fallbackMediaView(f *mediav1.MediaFile) *bffv1.MediaView {
	return &bffv1.MediaView{
		AssetId: f.GetAssetId(),
		Type:    f.GetType(),
		Url:     defaults.FallbackCoverURL,
		Width:   f.GetMeta().GetWidth(),
		Height:  f.GetMeta().GetHeight(),
	}
}

// 展示尺寸策略：转换链路已生成 216/540/1080 三档变体，面客按用途签发——
// 卡片封面用 540（~30KB），详情/编辑回显用 1080（~90KB）。历史上帖子挂载的是 ORIGIN（~317KB），
// 一页 10 张就是 3.2MB，是图片加载慢的主因；ORIGIN 只保留给"看原图"场景。
const (
	cardDisplayType   = mediav1.MediaFileType_MEDIA_FILE_TYPE_540
	detailDisplayType = mediav1.MediaFileType_MEDIA_FILE_TYPE_1080
)

// signMediaViews 批量把下游 MediaFile 降维为面客 MediaView（批量签发 + 尺寸抄入）。
// 按文件自带 type 签发（原语义，页面需要原图时用）；展示场景见 signMediaViewsAt。
func (u *BFFUsecaseImpl) signMediaViews(ctx context.Context, ttl time.Duration, files []*mediav1.MediaFile,
	failOpen bool,
) ([]*bffv1.MediaView, *commonv1.Error) {
	return u.signMediaViewsAt(ctx, ttl, files, failOpen, mediav1.MediaFileType_MEDIA_FILE_TYPE_UNSPECIFIED)
}

// signMediaViewsAt 按指定展示尺寸签发（want 为空/ORIGIN 即原语义）。
//
// want 为 540/1080 时先按该尺寸批量签；**变体缺失**（历史数据、转换失败）的项再用 ORIGIN 补一轮：
// 不让详情页因个别缺变体整页报错，也不静默丢图。变体齐全的常见路径只有 1 次 RPC。
func (u *BFFUsecaseImpl) signMediaViewsAt(ctx context.Context, ttl time.Duration, files []*mediav1.MediaFile,
	failOpen bool, want mediav1.MediaFileType,
) ([]*bffv1.MediaView, *commonv1.Error) {
	views, pending, errCode := u.signMediaViewsPass(ctx, ttl, files, want)
	if errCode != nil {
		return nil, errCode
	}

	// 目标尺寸缺失 → 回退原文件类型（通常 ORIGIN）
	if len(pending) > 0 && want != mediav1.MediaFileType_MEDIA_FILE_TYPE_UNSPECIFIED &&
		want != mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN {
		u.log.Warn("media display variant missing，回退原图签发",
			"display_type", want.String(), "count", len(pending))
		retry := make([]*mediav1.MediaFile, 0, len(pending))
		for _, i := range pending {
			retry = append(retry, files[i])
		}
		fallback, stillPending, errCode := u.signMediaViewsPass(ctx, ttl, retry,
			mediav1.MediaFileType_MEDIA_FILE_TYPE_UNSPECIFIED)
		if errCode != nil {
			return nil, errCode
		}
		for j, i := range pending {
			views[i] = fallback[j]
		}
		// stillPending 是 retry 的下标，映射回原 files 下标
		remap := make([]int, 0, len(stillPending))
		for _, j := range stillPending {
			remap = append(remap, pending[j])
		}
		pending = remap
	}

	if len(pending) == 0 {
		return views, nil
	}
	if failOpen {
		for _, i := range pending {
			u.log.Warn("media sign missing item，替换失败占位图",
				"asset_id", files[i].GetAssetId(), "display_type", want.String())
			views[i] = fallbackMediaView(files[i])
		}
		return views, nil
	}
	u.log.Error("media sign failed for display type", "display_type", want.String(), "missing", len(pending))
	return nil, errmsg.InternalError
}

// signMediaViewsPass 单轮签发：want 非空时把请求 type 改写成 want。
// 返回与 files 对齐的 views（未签成功处为 nil）与失败的 files 下标（缺失或逐项失败）。
func (u *BFFUsecaseImpl) signMediaViewsPass(ctx context.Context, ttl time.Duration, files []*mediav1.MediaFile,
	want mediav1.MediaFileType,
) ([]*bffv1.MediaView, []int, *commonv1.Error) {
	views := make([]*bffv1.MediaView, len(files))

	refs := make([]*mediav1.MediaAssetFileQuery, 0, len(files))
	refIndex := make([]int, 0, len(files)) // refs 下标 → files 下标
	for i, f := range files {
		if f == nil {
			continue
		}
		fileType := f.GetType()
		if want != mediav1.MediaFileType_MEDIA_FILE_TYPE_UNSPECIFIED {
			fileType = want
		}
		refs = append(refs, &mediav1.MediaAssetFileQuery{Id: f.GetAssetId(), Type: fileType})
		refIndex = append(refIndex, i)
	}
	if len(refs) == 0 {
		return views, nil, nil
	}

	resp, err := u.Repo.Media.SignMediaFileURLs(ctx, connect.NewRequest(&mediav1.SignMediaFileURLsRequest{
		Refs:       refs,
		TtlSeconds: int64(ttl / time.Second),
	}))
	if err != nil {
		u.log.Error("media.SignMediaFileURLs failed", "count", len(refs), "error", err)
		return nil, nil, errmsg.InternalError
	}

	// 契约：结果不保证顺序，按 asset_id + type 定位
	signed := make(map[string]*mediav1.MediaFileURL, len(resp.Msg.GetUrls()))
	for _, s := range resp.Msg.GetUrls() {
		if s.GetFail() {
			u.log.Warn("media sign item failed", "asset_id", s.GetAssetId(),
				"type", s.GetType().String(), "err_code", s.GetErr().GetCode())
			continue
		}
		signed[s.GetAssetId()+"/"+s.GetType().String()] = s
	}

	var pending []int
	for j, ref := range refs {
		i := refIndex[j]
		s := signed[ref.GetId()+"/"+ref.GetType().String()]
		if s == nil {
			pending = append(pending, i)
			continue
		}
		views[i] = &bffv1.MediaView{
			AssetId:   s.GetAssetId(),
			Type:      s.GetType(),
			Url:       s.GetSignedUrl(),
			ExpiresAt: s.GetExpireAt(),
			Width:     files[i].GetMeta().GetWidth(),
			Height:    files[i].GetMeta().GetHeight(),
		}
	}
	return views, pending, nil
}

// UploadMedia 上传媒体：uploader 即会话用户，下游创建资产并返回预签名直传 URL（客户端直传对象存储）。
// 新建资产引用计数为 0，挂载时才 +1；闲置资产由下游 DeleteUnusedMedia 定期回收。
func (u *BFFUsecaseImpl) UploadMedia(ctx context.Context, req *bffv1.UploadMediaRequest) (*bffv1.UploadMediaResponse, error) {
	claims := jwt.ClaimsFromContext(ctx)
	if claims == nil {
		return &bffv1.UploadMediaResponse{Fail: true, Err: errmsg.Unauthorized}, nil
	}

	resp, err := u.Repo.Media.UploadMedia(ctx, connect.NewRequest(&mediav1.UploadMediaRequest{
		UploaderId: claims.UID,
		MediaMeta:  req.GetMeta(),
	}))
	if err != nil {
		u.log.Error("media.UploadMedia failed", "uid", claims.UID, "error", err)
		return &bffv1.UploadMediaResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.Fail {
		return &bffv1.UploadMediaResponse{Fail: resp.Msg.Fail, Err: resp.Msg.Err}, nil
	}

	return &bffv1.UploadMediaResponse{
		Asset:     resp.Msg.GetAsset(),
		SignedUrl: resp.Msg.GetSignedUrl(),
		ExpireAt:  resp.Msg.GetExpireAt(),
	}, nil
}
