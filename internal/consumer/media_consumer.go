package consumer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"io"
	"rednote/config"
	mediav1 "rednote/gen/go/media/v1"
	commonv1 "rednote/internal/common/v1"
	media "rednote/internal/media/models"
	v1 "rednote/internal/media/v1"
	"rednote/packages/defaults"
	"rednote/packages/logger"
	"rednote/packages/mq"
	"rednote/packages/oss"
	"rednote/packages/utils"
	"strconv"
	"time"

	"github.com/chai2010/webp"
	"github.com/disintegration/imaging"
	"gorm.io/gorm"

	_ "golang.org/x/image/webp"
)

type MediaConsumer struct {
	*MessageLoop[*mediav1.MediaMessage]
	Repo  v1.MediaRepository
	S3    oss.S3Repo
	Dedup commonv1.ProcessedMessageRepository

	// waitObjectTimeout 上传确认消息在 handler 内短轮询等直传落地的时长（0 表示只查一次）。
	// upload_media 消息在 UploadMedia 事务里就发出，通常比客户端 PUT 完成早几百毫秒；
	// 短等待能就地接住对象，省掉一次 Nak 退避重投（首档退避 1s，是上传时延的大头）
	waitObjectTimeout time.Duration
}

var (
	AllMediaTypes = [4]mediav1.MediaFileType{
		mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN,
		mediav1.MediaFileType_MEDIA_FILE_TYPE_216,
		mediav1.MediaFileType_MEDIA_FILE_TYPE_540,
		mediav1.MediaFileType_MEDIA_FILE_TYPE_1080,
	}
)

func NewMediaConsumer(mqClient mq.MQClient, s3 oss.S3Repo, lf logger.LoggerFactory, cfg *config.Config, repo v1.MediaRepository, dedup commonv1.ProcessedMessageRepository) *MediaConsumer {
	c := &MediaConsumer{
		Repo:              repo,
		S3:                s3,
		Dedup:             dedup,
		waitObjectTimeout: defaults.UploadObjectWait,
	}
	c.MessageLoop = NewMessageLoop(
		newConsumer(cfg.Consumer.Media.Name, cfg.Consumer.Media.MaxRetry, cfg.Consumer.Media.WorkingDir, cfg.Consumer.HandleTimeout,
			mqClient, lf, "media_consumer"),
		defaults.MediaStream,
		func() *mediav1.MediaMessage { return &mediav1.MediaMessage{} },
		c.dispatch,
	)
	return c
}

// waitObject 等直传对象落地：≤waitObjectTimeout 内按 100ms 轮询一次。
// 在 handler 内等（而非立刻 Nak）是因为消息比 PUT 早到，退避重投的最小档就是 1s，
// 短等几百毫秒即可接住，链路时延直接少 1s；仍未见则交给重投窗口兜底。
func (c *MediaConsumer) waitObject(ctx context.Context, key string) (bool, error) {
	deadline := time.Now().Add(c.waitObjectTimeout)
	for {
		exist, err := c.S3.Exist(ctx, key)
		if err != nil || exist || c.waitObjectTimeout <= 0 || time.Now().After(deadline) {
			return exist, err
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// dispatch media 消息分发（oneof → 业务 handler）；错误分类语义见 utils.go
func (c *MediaConsumer) dispatch(ctx context.Context, m *mediav1.MediaMessage, messageID string) (ack bool, err error) {
	switch message := m.Payload.(type) {
	case *mediav1.MediaMessage_DeleteMedia:
		return c.HandleDeleteMedia(ctx, message.DeleteMedia)
	case *mediav1.MediaMessage_UploadMedia:
		return c.HandleUploadMedia(ctx, message.UploadMedia)
	case *mediav1.MediaMessage_AuditMedia:
		return c.HandleAuditMedia(ctx, message.AuditMedia)
	case *mediav1.MediaMessage_ProcessMedia:
		return c.HandleProcessMedia(ctx, message.ProcessMedia)
	case *mediav1.MediaMessage_PublishMedia:
		return c.HandlePublishMedia(ctx, message.PublishMedia)
	case *mediav1.MediaMessage_BanMedia:
		return c.HandleBanMedia(ctx, message.BanMedia)
	case *mediav1.MediaMessage_DerefMedia:
		return c.HandleDerefMedia(ctx, message.DerefMedia, messageID)
	default:
		return ACK, fmt.Errorf("unhandled media message")
	}
}

func (c *MediaConsumer) HandleUploadMedia(ctx context.Context, message *mediav1.UploadMedia) (ack bool, err error) {

	assetID, err := ParseSnowflakeID(message.GetAssetId(), "asset_id")
	if err != nil {
		return ACK, err
	}

	exist, err := c.waitObject(ctx, message.Key)
	if err != nil {
		return RETRY, err
	}

	if !exist {
		// 短等待后仍未见对象（直传仍在进行/失败）：退避重投，由 NakWithDelay 逐条调度；
		// 窗口耗尽（MaxDeliver）仍未到则判上传失败（守卫转移 UPLOADING→FAILED，不误伤已推进资产）
		if DeliveredFrom(ctx) >= uint64(c.maxRetry) {
			return c.SetMediaAssetFailed(ctx, assetID.Int64())
		}
		return RETRY, nil
	}

	assetArr, err := c.Repo.GetMediaAssets(ctx, []int64{assetID.Int64()})
	if err != nil {
		return RETRY, err
	}

	if len(assetArr) == 0 {
		return ACK, fmt.Errorf("media asset does not exist")
	}

	// 状态守卫：仅 UPLOADING 可推进——At-least-once 重复投递不重复处理已推进资产
	if assetArr[0].Status != mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_UPLOADING {
		return ACK, nil
	}

	result, err := c.S3.Download(ctx, message.Key)
	if err != nil {
		return RETRY, err
	}
	defer func() { _ = result.Body.Close() }()

	// 读取全部内容用于校验
	data, err := io.ReadAll(result.Body)
	if err != nil {
		return RETRY, fmt.Errorf("failed to read object body: %w", err)
	}

	err = utils.ValidateImage(data, message.GetMeta())
	if err != nil {
		return c.SetMediaAssetFailed(ctx, assetID.Int64())
	}

	file := &media.MediaFile{
		AssetID: assetID.Int64(),
		Bucket:  message.GetBucket(),
		ObjKey:  message.GetKey(),
		Type:    mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN,

		Height:    message.GetMeta().Height,
		Width:     message.GetMeta().Width,
		SizeBytes: message.GetMeta().SizeBytes,

		Sha256Sum: message.GetMeta().Sha_256Sum,
		Format:    message.GetMeta().Format,
	}
	err = c.Repo.CreateMediaFile(ctx, file)

	if errors.Is(err, gorm.ErrDuplicatedKey) {

	} else if err != nil {
		return RETRY, err
	}

	// 同一次投递内联推进后续状态：审核（占位）→ 处理 → ACTIVE。
	// 历史实现把这三步拆成 audit/process/publish 三次消息接力，每跳都要等 outbox poller 的 tick
	// （秒级），实测把「PUT 完成 → ACTIVE」拖到 11s+，而三跳里审核与发布都只有占位逻辑。
	return c.ProcessAsset(ctx, assetID.Int64(), message.GetBucket(), message.GetKey(), message.GetUploaderId(), data)
}

// SetMediaAssetFailed 上传确认阶段的失败落点（窗口耗尽仍未见原图）：委托 failAsset。
func (c *MediaConsumer) SetMediaAssetFailed(ctx context.Context, id int64) (ack bool, err error) {
	return c.failAsset(ctx, id, errMediaUploadMissing)
}

// errMediaUploadMissing 上传确认窗口耗尽（直传对象始终未到）的判失败原因
var errMediaUploadMissing = errors.New("media origin object missing after retry window")

// processFail 处理链失败出口：窗口内按 NakWithDelay 退避重投；投递次数耗尽则判失败（在途→FAILED + ACK）。
//
// 没有这层兜底时，MaxDeliver 耗尽后 JetStream 静默丢弃消息（本服务未配死信），
// 资产会永久卡在 PROCESSING/AUDITING——前端一直显示「处理中」，也没有任何告警信号。
func (c *MediaConsumer) processFail(ctx context.Context, assetID int64, cause error) (bool, error) {
	if DeliveredFrom(ctx) < uint64(c.maxRetry) {
		return RETRY, cause
	}
	return c.failAsset(ctx, assetID, cause)
}

// failAsset 把在途资产判为 FAILED（守卫转移，幂等）。已 ACTIVE / 已终态无需处理。
// 查询或转移失败时 ACK + 带上下文错误：此时重投预算已耗尽，RETRY 等价于丢弃，
// 交给 ERROR 日志人工介入（错误信息里写明需人工确认）。
func (c *MediaConsumer) failAsset(ctx context.Context, assetID int64, cause error) (bool, error) {
	assets, err := c.Repo.GetMediaAssets(ctx, []int64{assetID})
	if err != nil {
		return ACK, fmt.Errorf("mark media failed: asset status unreadable, 需人工确认 asset_id=%d: %w (cause: %v)", assetID, err, cause)
	}
	if len(assets) == 0 {
		return ACK, fmt.Errorf("mark media failed: asset not found asset_id=%d (cause: %v)", assetID, cause)
	}

	from := assets[0].Status
	switch from {
	case mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_UPLOADING,
		mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_AUDITING,
		mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_PROCESSING:
		// 在途才能判失败；ACTIVE/终态保持原样（重复投递/已推进）
	default:
		return ACK, nil
	}

	if _, err = c.Repo.SetMediaAssetStatus(ctx, assetID, from,
		mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_FAILED,
	); err != nil {
		return ACK, fmt.Errorf("mark media failed: guard transition %s→FAILED error, 需人工确认 asset_id=%d: %w (cause: %v)",
			from, from, err, cause)
	}

	c.log.Error("media asset marked failed after retry window exhausted",
		"asset_id", strconv.FormatInt(assetID, 10),
		"from", from.String(),
		"cause", cause,
	)
	return ACK, fmt.Errorf("media asset failed after retry window exhausted asset_id=%d from=%s: %w", assetID, from, cause)
}

// ProcessAsset 资产处理主流程（一次投递内联跑完审核占位 + 变体转换 + 置 ACTIVE）。
//
// 状态迁移：UPLOADING → AUDITING（真实审核的接入点，当前占位）→ PROCESSING → 转换三档 → ACTIVE。
// 只写状态、不产生后续消息——链路里已无需要异步解耦的步骤，消息接力纯属多付 poller tick 时延。
//
// 幂等：入口按状态白名单放行（在途才推进），变体落库容忍重复键；At-least-once 重投 / 服务端
// AckWait 到期重投都不会重复上传变体（转换是纯函数，重跑也只会覆盖同内容对象）。
// origin 非空则复用调用方已下载的原图字节（上传确认路径已读过一次，省掉第二次对象下载）。
func (c *MediaConsumer) ProcessAsset(ctx context.Context, assetID int64, bucket, key, uploaderID string, origin []byte) (ack bool, err error) {
	assets, err := c.Repo.GetMediaAssets(ctx, []int64{assetID})
	if err != nil {
		return c.processFail(ctx, assetID, err)
	}
	if len(assets) == 0 {
		return ACK, fmt.Errorf("media asset does not exist")
	}

	// 在途白名单：UPLOADING/AUDITING/PROCESSING 可推进（首次投递与重投都落在这里）；
	// ACTIVE 与各终态一律 ACK 跳过——重复投递不得重跑转换、不得把已终态资产拉回中间态
	var from mediav1.MediaAssetStatus
	switch assets[0].Status {
	case mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_UPLOADING:
		// 审核占位（TODO: 接入真实审核，不可用时走 SetMediaAssetFailed 守卫转移）：落到 AUDITING
		if err = c.advanceStatus(ctx, assetID,
			mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_UPLOADING,
			mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_AUDITING,
		); err != nil {
			return c.processFail(ctx, assetID, err)
		}
		from = mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_AUDITING
	case mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_AUDITING:
		from = mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_AUDITING
	case mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_PROCESSING:
		from = mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_PROCESSING // 在途重投：转换阶段幂等，直接续跑
	default:
		return ACK, nil
	}

	if from != mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_PROCESSING {
		if err = c.advanceStatus(ctx, assetID, from, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_PROCESSING); err != nil {
			return c.processFail(ctx, assetID, err)
		}
	}

	originImageData := origin
	if len(originImageData) == 0 {
		result, err := c.S3.Download(ctx, key)
		if err != nil {
			return c.processFail(ctx, assetID, err)
		}
		defer func() { _ = result.Body.Close() }()

		originImageData, err = io.ReadAll(result.Body)
		if err != nil {
			return c.processFail(ctx, assetID, fmt.Errorf("failed to read object body: %w", err))
		}
	}

	// 原图只解码一次，三档变体都在同一张解码后的图上裁剪缩放（历史实现每档各解码一遍原图）
	originImage, err := imaging.Decode(bytes.NewReader(originImageData))
	if err != nil {
		return c.processFail(ctx, assetID, fmt.Errorf("failed to decode origin image: %w", err))
	}

	for _, thumbType := range []mediav1.MediaFileType{
		mediav1.MediaFileType_MEDIA_FILE_TYPE_216,
		mediav1.MediaFileType_MEDIA_FILE_TYPE_540,
		mediav1.MediaFileType_MEDIA_FILE_TYPE_1080,
	} {
		webpData, err := EncodeThumb(originImage, thumbType)
		if err != nil {
			return c.processFail(ctx, assetID, err)
		}

		// 生成对象键：原始路径基础上添加类型后缀
		thumbKey := utils.BuildKey(assetID, thumbType, mediav1.MediaFormat_MEDIA_FORMAT_WEBP)

		// 上传转换后的 webp 到 S3
		if _, err = c.S3.Upload(ctx, thumbKey, bytes.NewReader(webpData), int64(len(webpData)), "image/webp"); err != nil {
			return c.processFail(ctx, assetID, err)
		}

		// 计算 sha256
		hash := sha256.Sum256(webpData)
		sha256Sum := hex.EncodeToString(hash[:])

		// 解码获取尺寸
		cfg, err := webp.DecodeConfig(bytes.NewReader(webpData))
		if err != nil {
			return c.processFail(ctx, assetID, err)
		}

		// 创建媒体文件记录（重投重复写入由唯一键拦下，容忍）
		file := &media.MediaFile{
			AssetID:   assetID,
			Bucket:    bucket,
			ObjKey:    thumbKey,
			Type:      thumbType,
			Height:    int64(cfg.Height),
			Width:     int64(cfg.Width),
			SizeBytes: int64(len(webpData)),
			Sha256Sum: sha256Sum,
			Format:    mediav1.MediaFormat_MEDIA_FORMAT_WEBP,
		}
		if err = c.Repo.CreateMediaFile(ctx, file); err != nil && !errors.Is(err, gorm.ErrDuplicatedKey) {
			return c.processFail(ctx, assetID, err)
		}
	}

	c.log.Info("Media processed successfully", "asset_id", strconv.FormatInt(assetID, 10))

	// 置 ACTIVE：PROCESSING→ACTIVE 守卫转移，重投(已是 ACTIVE)时 RowsAffected=0 幂等成功
	if _, err = c.Repo.SetMediaAssetStatus(ctx, assetID,
		mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_PROCESSING,
		mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE,
	); err != nil {
		return c.processFail(ctx, assetID, err)
	}

	c.log.Info("media published", "asset_id", strconv.FormatInt(assetID, 10), "uploader_id", uploaderID)
	return ACK, nil
}

// advanceStatus 状态守卫转移（from→to）。0 行受影响表示已被其它投递推进，幂等继续。
func (c *MediaConsumer) advanceStatus(ctx context.Context, assetID int64, from, to mediav1.MediaAssetStatus) error {
	_, err := c.Repo.SetMediaAssetStatus(ctx, assetID, from, to)
	return err
}

// HandleAuditMedia / HandleProcessMedia 历史消息兼容入口：旧版本把链路拆成 audit→process→publish
// 三次消息接力，部署切换期间流里可能残留这两类消息，统一收敛到 ProcessAsset（状态守卫保证幂等）。
func (c *MediaConsumer) HandleAuditMedia(ctx context.Context, message *mediav1.AuditMedia) (ack bool, err error) {
	assetID, err := ParseSnowflakeID(message.GetAssetId(), "asset_id")
	if err != nil {
		return ACK, err
	}
	return c.ProcessAsset(ctx, assetID.Int64(), message.GetBucket(), message.GetKey(), message.GetUploaderId(), nil)
}

func (c *MediaConsumer) HandleProcessMedia(ctx context.Context, message *mediav1.ProcessMedia) (ack bool, err error) {
	assetID, err := ParseSnowflakeID(message.GetAssetId(), "asset_id")
	if err != nil {
		return ACK, err
	}
	return c.ProcessAsset(ctx, assetID.Int64(), message.GetBucket(), message.GetKey(), message.GetUploaderId(), nil)
}

// 根据 MediaFileType 获取目标宽度
func getTargetWidth(imgType mediav1.MediaFileType) int {
	switch imgType {
	case mediav1.MediaFileType_MEDIA_FILE_TYPE_216:
		return 216
	case mediav1.MediaFileType_MEDIA_FILE_TYPE_540:
		return 540
	case mediav1.MediaFileType_MEDIA_FILE_TYPE_1080:
		return 1080
	case mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN:
		return 0 // 原始尺寸，不缩放
	default:
		return 0
	}
}

// EncodeThumb 把已解码的原图缩放到指定档位并编码为 webp（原图宽度未超目标宽度时不放大）。
// 调用方复用同一份解码结果：解码约占单档耗时的三分之一，历史实现每档都重解一遍原图。
func EncodeThumb(origin image.Image, imgType mediav1.MediaFileType) ([]byte, error) {
	targetWidth := getTargetWidth(imgType)

	img := origin
	if targetWidth > 0 && origin.Bounds().Dx() > targetWidth {
		// 使用 Lanczos 滤波器进行高质量缩放
		img = imaging.Resize(origin, targetWidth, 0, imaging.Lanczos)
	}

	// 编码为 webp 格式（在内存中完成）
	var buf bytes.Buffer
	err := webp.Encode(&buf, img, &webp.Options{
		Quality: 85, // 质量 85，平衡质量和大小
	})
	if err != nil {
		return nil, fmt.Errorf("failed to encode webp: %w", err)
	}

	return buf.Bytes(), nil
}

// HandlePublishMedia 历史消息兼容入口：媒体置 ACTIVE 的消息接力已取消（见 ProcessAsset），
// 流中残留的 publish_media 消息直接 ACK——无待办副作用。
func (c *MediaConsumer) HandlePublishMedia(ctx context.Context, message *mediav1.PublishMedia) (ack bool, err error) {
	c.log.Info("media published", "asset_id", message.GetAssetId(), "legacy_message", true)
	return ACK, nil
}

// assetFiles 取资产下全部类型的文件记录。
// GetMediaFiles 要求 ids/types 等长且一一对应（zip 语义，非笛卡尔积）：这里按类型数把 asset_id 复制成等长切片。
// 历史实现给 GetMediaFiles 传 1 个 id + 4 个 type，必然返回 "IDs and types length mismatch"，
// 使 ban_media 消息每次重投都失败（封禁的 S3 清理从未执行）。
func (c *MediaConsumer) assetFiles(ctx context.Context, assetID int64) ([]*media.MediaFile, error) {
	ids := make([]int64, len(AllMediaTypes))
	for i := range ids {
		ids[i] = assetID
	}
	return c.Repo.GetMediaFiles(ctx, ids, AllMediaTypes[:])
}

func (c *MediaConsumer) HandleDeleteMedia(ctx context.Context, message *mediav1.DeleteMedia) (ack bool, err error) {
	assetID, err := ParseSnowflakeID(message.GetAssetId(), "asset_id")
	if err != nil {
		return ACK, err
	}

	files, err := c.assetFiles(ctx, assetID.Int64())
	if err != nil {
		return RETRY, err
	}

	for _, file := range files {
		ok, err := c.S3.Exist(ctx, file.ObjKey)
		if err != nil {
			return RETRY, err
		}

		if !ok {
			continue
		}

		err = c.S3.DeleteObject(ctx, file.ObjKey)
		if err != nil {
			return RETRY, err
		}
	}

	err = c.Repo.Transaction(func(repo v1.MediaRepository) error {
		_, err := repo.DeleteMediaFiles(ctx, assetID.Int64())
		if err != nil {
			return err
		}
		_, err = repo.DeleteMediaAssets(ctx, []int64{assetID.Int64()})
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return RETRY, err
	}

	return ACK, nil
}

func (c *MediaConsumer) HandleBanMedia(ctx context.Context, message *mediav1.BanMedia) (ack bool, err error) {
	assetID, err := ParseSnowflakeID(message.GetAssetId(), "asset_id")
	if err != nil {
		return ACK, err
	}

	assets, err := c.Repo.GetMediaAssets(ctx, []int64{assetID.Int64()})
	if err != nil {
		return RETRY, err
	}
	if len(assets) == 0 {
		return ACK, nil
	}
	if assets[0].Status != mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_BANNED {
		return ACK, nil
	}

	files, err := c.assetFiles(ctx, assetID.Int64())
	if err != nil {
		return RETRY, err
	}

	for _, f := range files {
		exist, err := c.S3.Exist(ctx, f.ObjKey)
		if err != nil {
			return RETRY, err
		}
		if !exist {
			continue
		}
		err = c.S3.DeleteObject(ctx, f.ObjKey)
		if err != nil {
			return RETRY, err
		}
	}
	return ACK, nil

}

// HandleDerefMedia 异步卸载：引用计数 -1（BFF 挂载点替换/删帖回收时发出，替代同步 RPC）。
// 投递语义 AtLeastOnce：经 consumer_processed_messages 同事务去重登记（ApplyDerefIdempotent），
// 重复投递跳过、乱序无害（delta 可交换），恰好一次效果。
func (c *MediaConsumer) HandleDerefMedia(ctx context.Context, message *mediav1.DerefMedia, messageID string) (ack bool, err error) {
	msgID, err := ParseMessageID(messageID)
	if err != nil {
		// message_id 缺失/非法：不减数不登记，RETRY 等正确消息（宁可重试不可错减）
		return RETRY, err
	}
	assetID, err := ParseSnowflakeID(message.GetAssetId(), "asset_id")
	if err != nil {
		// 非法资产 ID 不可恢复，ACK 丢弃（与 HandleDeleteMedia 一致）
		return ACK, err
	}

	// 幂等执行（共用去重机制）：去重登记与计数变更同事务，恰好一次效果；重复投递跳过、乱序无害
	applied, err := c.Dedup.ExecuteIdempotent(ctx, msgID, "deref_media", func(tx *gorm.DB) error {
		return c.Repo.WithTX(tx).ModifyMediaAssetRefCount(ctx, []int64{assetID.Int64()}, []int64{-1})
	})
	if err != nil {
		return RETRY, err
	}
	if !applied {
		c.log.Info("deref skipped duplicate", "id", messageID, "asset_id", message.GetAssetId())
		return ACK, nil
	}
	c.log.Info("media ref decremented", "asset_id", message.GetAssetId())
	return ACK, nil
}
