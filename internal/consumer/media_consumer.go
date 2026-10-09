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
	"os"
	"rednote/config"
	mediav1 "rednote/gen/go/media/v1"
	"rednote/gen/query"
	media "rednote/internal/media/models"
	v1 "rednote/internal/media/v1"
	"rednote/packages/defaults"
	"rednote/packages/logger"
	"rednote/packages/mq"
	"rednote/packages/oss"
	"rednote/packages/trace"
	"rednote/packages/utils"
	"runtime/debug"

	"github.com/bwmarrin/snowflake"
	"github.com/chai2010/webp"
	"github.com/disintegration/imaging"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	_ "golang.org/x/image/webp"
)

type MediaConsumer struct {
	Consumer
	Repo       v1.MediaRepository
	ConsumeCtx jetstream.ConsumeContext
}



var (
	AllMediaTypes = [4]mediav1.MediaFileType{
		mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN,
		mediav1.MediaFileType_MEDIA_FILE_TYPE_216,
		mediav1.MediaFileType_MEDIA_FILE_TYPE_540,
		mediav1.MediaFileType_MEDIA_FILE_TYPE_1080,
	}
)

func NewMediaConsumer(db *gorm.DB, mqClient *mq.JetStreamClient, s3 oss.S3Repo, lf logger.LoggerFactory, cfg *config.Config, repo v1.MediaRepository) *MediaConsumer {
	ctx, cancel := context.WithCancel(context.Background())
	return &MediaConsumer{
		Consumer: Consumer{
			name:       cfg.Consumer.Media.Name,
			maxRetry:   cfg.Consumer.Media.MaxRetry,
			workingDir: cfg.Consumer.Media.WorkingDir,

			DB:  db,
			q:   query.Use(db),
			MQ:  mqClient,
			S3:  s3,
			log: lf.GetLoggerOf("media_consumer"),

			ctx:    ctx,
			cancel: cancel,
		},
		Repo: repo,
	}
}

func (c *MediaConsumer) Start() {

	c.log.Info("starting media consumer...")
	_, err := os.Stat(c.workingDir)
	if os.IsNotExist(err) {
		err := os.MkdirAll(c.workingDir, 0755)
		if err != nil {
			c.log.Error("failed to create working directory", "error", err)
			panic(err)
		}
	}

	consumer, err := c.MQ.GetStream().CreateOrUpdateConsumer(c.ctx,
		defaults.MediaStream,
		jetstream.ConsumerConfig{
			Name:    c.name,
			Durable: c.name,

			DeliverPolicy: jetstream.DeliverAllPolicy,
			AckPolicy:     jetstream.AckExplicitPolicy,

			MaxDeliver: c.maxRetry,
		})
	if err != nil {
		c.log.Error("failed to create or update consumer", "error", err)
		panic(err)
	}

	consumeCtx, err := consumer.Consume(c.HandleMessage)
	if err != nil {
		panic(err)
	}
	c.ConsumeCtx = consumeCtx
	c.log.Info("media consumer started")
}

func (c *MediaConsumer) HandleMessage(msg jetstream.Msg) {

	traceID := msg.Headers().Get(defaults.TraceIDKey)
	sendAt := msg.Headers().Get(defaults.SendAtKey)
	id := msg.Headers().Get(defaults.MessageIDKey)
	ctx := trace.WithTraceID(c.ctx, traceID)

	defer func() {
		if r := recover(); r != nil {
			c.log.Error("panic occurred",
				"trace_id", traceID,
				"send_at", sendAt,
				"id", id,
				"error", r,
				"stack", debug.Stack(),
			)
		}
	}()

	var err error
	var ack bool

	m := &mediav1.MediaMessage{}
	err = proto.Unmarshal(msg.Data(), m)
	if err != nil {
		_ = msg.Term()
		c.log.Error("failed to unmarshal media message",
			"id", id,
			"trace_id", traceID,
			"send_at", sendAt,
			"error", err,
		)

		return
	}

	switch message := m.Payload.(type) {
	case *mediav1.MediaMessage_DeleteMedia:
		ack, err = c.HandleDeleteMedia(ctx, message.DeleteMedia)
	case *mediav1.MediaMessage_UploadMedia:
		ack, err = c.HandleUploadMedia(ctx, message.UploadMedia)
	case *mediav1.MediaMessage_AuditMedia:
		ack, err = c.HandleAuditMedia(ctx, message.AuditMedia)
	case *mediav1.MediaMessage_ProcessMedia:
		ack, err = c.HandleProcessMedia(ctx, message.ProcessMedia)
	case *mediav1.MediaMessage_PublishMedia:
		ack, err = c.HandlePublishMedia(ctx, message.PublishMedia)

	default:
		ack = true
		err = fmt.Errorf("unhandled media message")
	}

	if err != nil {
		c.log.Error("failed to handle media message",
			"id", id,
			"trace_id", traceID,
			"send_at", sendAt,
			"error", err,
		)
	} else {
		c.log.Info("successfully handled media message",
			"id", id,
			"trace_id", traceID,
			"send_at", sendAt,
		)
	}

	if ack {
		_ = msg.Ack()
	} else {
		_ = msg.Nak()
		c.log.Error("message nak",
			"id", id,
			"trace_id", traceID,
			"send_at", sendAt,
		)
	}

}

func (c *MediaConsumer) Close() {
	c.cancel()
	c.ConsumeCtx.Stop()
}

func (c *MediaConsumer) HandleUploadMedia(ctx context.Context, message *mediav1.UploadMedia) (ack bool, err error) {

	assetID, err := snowflake.ParseString(message.GetAssetId())
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

	err = ValidateImage(data, message.GetMeta())
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

	info, err := c.Repo.SetMediaAssetStatus(ctx, id,
		mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_UPLOADING,
		mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_FAILED,
	)
	if err != nil {
		return RETRY, err
	}

	if info.RowsAffected == 0 {
		return ACK, nil
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

	err = c.DB.Transaction(func(tx *gorm.DB) error {
		info, err := c.Repo.WithTX(tx).SetMediaAssetStatus(ctx, assetID,
			mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_UPLOADING,
			mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_AUDITING,
		)
		if err != nil {
			return err
		}

		if info.RowsAffected == 0 {
			return nil
		}

		return c.MQ.SendTX(ctx, mqMsg, tx)
	})

	if err != nil {
		return RETRY, err
	}

	return ACK, nil
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

func (c *MediaConsumer) HandleAuditMedia(ctx context.Context, message *mediav1.AuditMedia) (ack bool, err error) {
	// Implementation for handling audit media message
	// TODO: 添加实际审查逻辑

	assetID, err := snowflake.ParseString(message.GetAssetId())
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

	err = c.DB.Transaction(func(tx *gorm.DB) error {
		info, err := c.Repo.WithTX(tx).SetMediaAssetStatus(ctx, assetID,
			mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_AUDITING,
			mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_PROCESSING,
		)
		if err != nil {
			return err
		}

		if info.RowsAffected == 0 {
			return nil
		}

		return c.MQ.SendTX(ctx, mqMsg, tx)
	})

	if err != nil {
		return RETRY, err
	}

	return ACK, nil
}

func (c *MediaConsumer) HandleProcessMedia(ctx context.Context, message *mediav1.ProcessMedia) (ack bool, err error) {

	assetID, err := snowflake.ParseString(message.GetAssetId())
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

	err = c.DB.Transaction(func(tx *gorm.DB) error {
		info, err := c.Repo.WithTX(tx).SetMediaAssetStatus(ctx, assetID,
			mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_PROCESSING,
			mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE,
		)
		if err != nil {
			return err
		}

		if info.RowsAffected == 0 {
			return nil
		}

		return c.MQ.SendTX(ctx, mqMsg, tx)
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
	assetID, err := snowflake.ParseString(message.GetAssetId())
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

	err = c.DB.Transaction(func(tx *gorm.DB) error {
		_, err := c.Repo.WithTX(tx).DeleteMediaFiles(ctx, assetID.Int64())
		if err != nil {
			return err
		}
		_, err = c.Repo.WithTX(tx).DeleteMediaAssets(ctx, []int64{assetID.Int64()})
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
	assetID, err := snowflake.ParseString(message.GetAssetId())
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
