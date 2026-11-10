package consumer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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

	"github.com/bwmarrin/snowflake"
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
	c := &MediaConsumer{Repo: repo, S3: s3, Dedup: dedup}
	c.MessageLoop = NewMessageLoop(
		newConsumer(cfg.Consumer.Media.Name, cfg.Consumer.Media.MaxRetry, cfg.Consumer.Media.WorkingDir,
			mqClient, lf, "media_consumer"),
		defaults.MediaStream,
		func() *mediav1.MediaMessage { return &mediav1.MediaMessage{} },
		c.dispatch,
	)
	return c
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

	exist, err := c.S3.Exist(ctx, message.Key)
	if err != nil {
		return RETRY, err
	}

	if !exist {
		return c.SetMediaAssetFailed(ctx, assetID.Int64())
	}

	assetArr, err := c.Repo.GetMediaAssets(ctx, []int64{assetID.Int64()})
	if err != nil {
		return RETRY, err
	}

	if len(assetArr) == 0 {
		return ACK, fmt.Errorf("media asset does not exist")
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

	auditMsg := &mediav1.AuditMedia{
		AssetId: assetID.String(),
		Bucket:  message.GetBucket(),
		Key:     message.GetKey(),

		UploaderId:   message.GetUploaderId(),
		OriginFileId: snowflake.ParseInt64(file.ID).String(),
	}

	return c.SetMediaStatusAuditing(ctx, assetID.Int64(), auditMsg)
}

func (c *MediaConsumer) SetMediaAssetFailed(ctx context.Context, id int64) (ack bool, err error) {

	// 写失败态无下游副作用，无需用 RowsAffected 区分（未命中/值不变均幂等成功）
	_, err = c.Repo.SetMediaAssetStatus(ctx, id,
		mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_UPLOADING,
		mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_FAILED,
	)
	if err != nil {
		return RETRY, err
	}

	return ACK, nil
}

func (c *MediaConsumer) SetMediaStatusAuditing(ctx context.Context, assetID int64, msg *mediav1.AuditMedia) (ack bool, err error) {

	payload := &mediav1.MediaMessage{
		Payload: &mediav1.MediaMessage_AuditMedia{
			AuditMedia: msg,
		},
	}

	mqMsg, err := mq.NewMQMessage(defaults.MediaMessageStream, nil, payload)
	if err != nil {
		return RETRY, err
	}

	err = c.Repo.Transaction(func(repo v1.MediaRepository) error {
		info, err := repo.SetMediaAssetStatus(ctx, assetID,
			mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_UPLOADING,
			mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_AUDITING,
		)
		if err != nil {
			return err
		}

		if info.RowsAffected == 0 {
			return nil
		}

		return c.MQ.SendTX(ctx, mqMsg, repo.DB())
	})

	if err != nil {
		return RETRY, err
	}

	return ACK, nil
}

func (c *MediaConsumer) HandleAuditMedia(ctx context.Context, message *mediav1.AuditMedia) (ack bool, err error) {
	// Implementation for handling audit media message
	// TODO: 添加实际审查逻辑

	assetID, err := ParseSnowflakeID(message.GetAssetId(), "asset_id")
	if err != nil {
		return ACK, err
	}

	processMsg := &mediav1.ProcessMedia{
		AssetId:      message.AssetId,
		Bucket:       message.Bucket,
		Key:          message.Key,
		UploaderId:   message.UploaderId,
		OriginFileId: message.OriginFileId,
	}
	return c.SetMediaStatusProcessing(ctx, assetID.Int64(), processMsg)
}

func (c *MediaConsumer) SetMediaStatusProcessing(ctx context.Context, assetID int64, message *mediav1.ProcessMedia) (ack bool, err error) {
	mediaMsg := &mediav1.MediaMessage{
		Payload: &mediav1.MediaMessage_ProcessMedia{
			ProcessMedia: message,
		},
	}

	mqMsg, err := mq.NewMQMessage(defaults.MediaMessageStream, nil, mediaMsg)
	if err != nil {
		return RETRY, err
	}

	err = c.Repo.Transaction(func(repo v1.MediaRepository) error {
		info, err := repo.SetMediaAssetStatus(ctx, assetID,
			mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_AUDITING,
			mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_PROCESSING,
		)
		if err != nil {
			return err
		}

		if info.RowsAffected == 0 {
			return nil
		}

		return c.MQ.SendTX(ctx, mqMsg, repo.DB())
	})

	if err != nil {
		return RETRY, err
	}

	return ACK, nil
}

func (c *MediaConsumer) HandleProcessMedia(ctx context.Context, message *mediav1.ProcessMedia) (ack bool, err error) {

	assetID, err := ParseSnowflakeID(message.GetAssetId(), "asset_id")
	if err != nil {
		return ACK, err
	}

	result, err := c.S3.Download(ctx, message.Key)
	if err != nil {
		return RETRY, err
	}
	defer func() { _ = result.Body.Close() }()

	originImageData, err := io.ReadAll(result.Body)
	if err != nil {
		return RETRY, fmt.Errorf("failed to read object body: %w", err)
	}

	// 需要生成的缩略图类型（216px、540px、1080px）
	thumbTypes := []mediav1.MediaFileType{
		mediav1.MediaFileType_MEDIA_FILE_TYPE_216,
		mediav1.MediaFileType_MEDIA_FILE_TYPE_540,
		mediav1.MediaFileType_MEDIA_FILE_TYPE_1080,
	}

	for _, thumbType := range thumbTypes {
		webpData, err := ConvertImage(originImageData, thumbType)
		if err != nil {
			return RETRY, err
		}

		// 生成对象键：原始路径基础上添加类型后缀
		thumbKey := utils.BuildKey(assetID.Int64(), thumbType, mediav1.MediaFormat_MEDIA_FORMAT_WEBP)

		// 上传转换后的 webp 到 S3
		_, err = c.S3.Upload(ctx, thumbKey, bytes.NewReader(webpData), int64(len(webpData)), "image/webp")
		if err != nil {
			return RETRY, err
		}

		// 计算 sha256
		hash := sha256.Sum256(webpData)
		sha256Sum := hex.EncodeToString(hash[:])

		// 解码获取尺寸
		cfg, err := webp.DecodeConfig(bytes.NewReader(webpData))
		if err != nil {
			return RETRY, err
		}

		// 创建媒体文件记录
		file := &media.MediaFile{
			AssetID:   assetID.Int64(),
			Bucket:    message.GetBucket(),
			ObjKey:    thumbKey,
			Type:      thumbType,
			Height:    int64(cfg.Height),
			Width:     int64(cfg.Width),
			SizeBytes: int64(len(webpData)),
			Sha256Sum: sha256Sum,
			Format:    mediav1.MediaFormat_MEDIA_FORMAT_WEBP,
		}

		err = c.Repo.CreateMediaFile(ctx, file)
		if errors.Is(err, gorm.ErrDuplicatedKey) {

		} else if err != nil {
			return RETRY, err
		}
	}

	c.log.Info("Media processed successfully", "asset_id", assetID.String())

	publishMsg := &mediav1.PublishMedia{
		AssetId:    assetID.String(),
		UploaderId: message.GetUploaderId(),
	}

	return c.SetMediaStatusActive(ctx, assetID.Int64(), publishMsg)
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

// ConvertImage 将图片按照指定类型缩放并转换为 webp 格式
// 尺寸小于目标宽度时不变
func ConvertImage(data []byte, imgType mediav1.MediaFileType) ([]byte, error) {
	// 解码原始图片
	img, err := imaging.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to decode image: %w", err)
	}

	// 获取目标宽度
	targetWidth := getTargetWidth(imgType)

	// 如果需要缩放
	if targetWidth > 0 {
		bounds := img.Bounds()
		origWidth := bounds.Dx()

		// 仅当原图宽度大于目标宽度时才缩放
		if origWidth > targetWidth {
			// 使用 Lanczos 滤波器进行高质量缩放
			img = imaging.Resize(img, targetWidth, 0, imaging.Lanczos)
		}
	}

	// 编码为 webp 格式（在内存中完成）
	var buf bytes.Buffer
	err = webp.Encode(&buf, img, &webp.Options{
		Quality: 85, // 质量 85，平衡质量和大小
	})
	if err != nil {
		return nil, fmt.Errorf("failed to encode webp: %w", err)
	}

	return buf.Bytes(), nil
}

func (c *MediaConsumer) SetMediaStatusActive(ctx context.Context, assetID int64, message *mediav1.PublishMedia) (ack bool, err error) {
	mediaMsg := &mediav1.MediaMessage{
		Payload: &mediav1.MediaMessage_PublishMedia{
			PublishMedia: message,
		},
	}

	mqMsg, err := mq.NewMQMessage(defaults.MediaMessageStream, nil, mediaMsg)
	if err != nil {
		return RETRY, err
	}

	err = c.Repo.Transaction(func(repo v1.MediaRepository) error {
		info, err := repo.SetMediaAssetStatus(ctx, assetID,
			mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_PROCESSING,
			mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE,
		)
		if err != nil {
			return err
		}

		if info.RowsAffected == 0 {
			return nil
		}

		return c.MQ.SendTX(ctx, mqMsg, repo.DB())
	})

	if err != nil {
		return RETRY, err
	}

	return ACK, nil
}

func (c *MediaConsumer) HandlePublishMedia(ctx context.Context, message *mediav1.PublishMedia) (ack bool, err error) {
	// Implementation for handling publish media messages
	// TODO:实现其他逻辑
	c.log.Info("media published",
		"asset_id", message.AssetId,
	)
	return ACK, nil
}

func (c *MediaConsumer) HandleDeleteMedia(ctx context.Context, message *mediav1.DeleteMedia) (ack bool, err error) {
	assetID, err := ParseSnowflakeID(message.GetAssetId(), "asset_id")
	if err != nil {
		return ACK, err
	}

	types := AllMediaTypes[:]

	ids := []int64{
		assetID.Int64(),
		assetID.Int64(),
		assetID.Int64(),
		assetID.Int64(),
	}

	files, err := c.Repo.GetMediaFiles(ctx, ids, types)
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

	files, err := c.Repo.GetMediaFiles(ctx, []int64{assetID.Int64()}, AllMediaTypes[:])
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
