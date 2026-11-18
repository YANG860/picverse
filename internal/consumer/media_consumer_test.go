package consumer

import (
	"bytes"
	"context"
	"errors"
	"image/color"
	imagepkg "image/jpeg"
	"io"
	"log/slog"
	"testing"
	"time"

	mediav1 "rednote/gen/go/media/v1"
	commonv1 "rednote/internal/common/v1"
	models "rednote/internal/media/models"
	v1 "rednote/internal/media/v1"
	"rednote/packages/oss"

	"github.com/disintegration/imaging"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gen"
	"gorm.io/gorm"
)

// fakeMediaRepo 覆写 HandleDerefMedia/HandleUploadMedia 所需方法（未覆写方法调用会 panic）
type fakeMediaRepo struct {
	v1.MediaRepository

	modifyErr   error
	modifyCalls int
	gotDeltas   []int64

	// HandleUploadMedia 重试判定/状态守卫用：资产返回集与状态转移记录 (from, to)
	assets        []*models.MediaAsset
	statusChanges [][2]mediav1.MediaAssetStatus
}

func (f *fakeMediaRepo) WithTX(_ *gorm.DB) v1.MediaRepository { return f }

func (f *fakeMediaRepo) ModifyMediaAssetRefCount(_ context.Context, _ []int64, deltas []int64) error {
	f.modifyCalls++
	f.gotDeltas = deltas
	return f.modifyErr
}

func (f *fakeMediaRepo) GetMediaAssets(_ context.Context, _ []int64) ([]*models.MediaAsset, error) {
	return f.assets, nil
}

func (f *fakeMediaRepo) SetMediaAssetStatus(_ context.Context, _ int64, from, to mediav1.MediaAssetStatus) (gen.ResultInfo, error) {
	f.statusChanges = append(f.statusChanges, [2]mediav1.MediaAssetStatus{from, to})
	return gen.ResultInfo{RowsAffected: 1}, nil
}

// fakeUploadS3 覆写 HandleUploadMedia 判定阶段所需方法（Download 等未覆写方法调用 panic=反证未触达）
type fakeUploadS3 struct {
	oss.S3Repo
	exist      bool
	existCalls int
}

func (f *fakeUploadS3) Exist(_ context.Context, _ string) (bool, error) {
	f.existCalls++
	return f.exist, nil
}

// fakeDedup 模拟共用去重机制（幂等执行模板），未覆写方法调用会 panic
type fakeDedup struct {
	commonv1.ProcessedMessageRepository

	applied    bool // 注入去重结果：true=首次 / false=重复投递
	execErr    error
	gotID      int64
	gotHandler string
	execCalls  int
}

func (f *fakeDedup) ExecuteIdempotent(_ context.Context, messageID int64, handler string, fn func(tx *gorm.DB) error) (bool, error) {
	f.execCalls++
	f.gotID = messageID
	f.gotHandler = handler
	if f.execErr != nil {
		return false, f.execErr
	}
	if !f.applied {
		return false, nil
	}
	if err := fn(nil); err != nil {
		return false, err
	}
	return true, nil
}

func newDerefMediaConsumer(repo v1.MediaRepository, dedup commonv1.ProcessedMessageRepository) *MediaConsumer {
	return &MediaConsumer{
		MessageLoop: &MessageLoop[*mediav1.MediaMessage]{Consumer: Consumer{log: slog.Default()}},
		Repo:        repo,
		Dedup:       dedup,
	}
}

// newUploadMediaConsumer HandleUploadMedia 专用基座（maxRetry=5 对齐 config consumer.media.max_retry）
func newUploadMediaConsumer(repo v1.MediaRepository, s3 oss.S3Repo) *MediaConsumer {
	return &MediaConsumer{
		MessageLoop: &MessageLoop[*mediav1.MediaMessage]{Consumer: Consumer{log: slog.Default(), maxRetry: 5}},
		Repo:        repo,
		S3:          s3,
	}
}

// TestHandleUploadMedia 上传确认重试语义（需求变更：延时 15s 等待改为立即投递+退避重试，耗尽判失败）
func TestHandleUploadMedia(t *testing.T) {
	msg := &mediav1.UploadMedia{AssetId: "7001", Key: "origin"}

	t.Run("对象未到且重试窗口未耗尽：RETRY 退避重投不判失败", func(t *testing.T) {
		repo := &fakeMediaRepo{}
		s3 := &fakeUploadS3{exist: false}
		c := newUploadMediaConsumer(repo, s3)

		ctx := withDelivered(context.Background(), &jetstream.MsgMetadata{NumDelivered: 1})
		ack, err := c.HandleUploadMedia(ctx, msg)
		require.NoError(t, err)
		assert.False(t, ack, "窗口内 RETRY 退避重投（直传进行中）")
		assert.Empty(t, repo.statusChanges, "窗口内不得置失败态")
	})

	t.Run("对象未到且窗口耗尽：ACK + 判失败（UPLOADING→FAILED 守卫转移）", func(t *testing.T) {
		repo := &fakeMediaRepo{assets: []*models.MediaAsset{{
			Status: mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_UPLOADING,
		}}}
		s3 := &fakeUploadS3{exist: false}
		c := newUploadMediaConsumer(repo, s3)

		ctx := withDelivered(context.Background(), &jetstream.MsgMetadata{NumDelivered: 5})
		ack, err := c.HandleUploadMedia(ctx, msg)
		// 判失败是真实失败（不是业务裁决）：ACK 结束消息，同时带出原因让消息循环记 ERROR 可追溯
		require.Error(t, err)
		assert.Contains(t, err.Error(), "media origin object missing")
		assert.True(t, ack, "耗尽判死后 ACK（否则 MaxDeliver 耗尽后资产永久卡 UPLOADING）")
		require.Len(t, repo.statusChanges, 1)
		assert.Equal(t, [2]mediav1.MediaAssetStatus{
			mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_UPLOADING,
			mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_FAILED,
		}, repo.statusChanges[0])
	})

	t.Run("对象已到但资产非 UPLOADING：ACK 跳过防重复处理", func(t *testing.T) {
		repo := &fakeMediaRepo{assets: []*models.MediaAsset{{
			Status: mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_AUDITING,
		}}}
		s3 := &fakeUploadS3{exist: true}
		c := newUploadMediaConsumer(repo, s3)

		ack, err := c.HandleUploadMedia(context.Background(), msg)
		require.NoError(t, err)
		assert.True(t, ack, "At-least-once 重复投递 ACK 跳过")
		assert.Empty(t, repo.statusChanges, "不重复推进状态")
		assert.Equal(t, 1, s3.existCalls)
		// Download 等处理链路方法未覆写（nil 桩），触达会 panic——反证守卫已拦断
	})
}

// TestHandleDerefMedia 幂等卸载：重复投递跳过、失败 RETRY 可重投、坏消息不误减
func TestHandleDerefMedia(t *testing.T) {
	msg := &mediav1.DerefMedia{AssetId: "7001"}

	t.Run("首次消息执行减数", func(t *testing.T) {
		repo := &fakeMediaRepo{}
		dedup := &fakeDedup{applied: true}
		c := newDerefMediaConsumer(repo, dedup)

		ack, err := c.HandleDerefMedia(context.Background(), msg, "5001")
		require.NoError(t, err)
		assert.True(t, ack)
		assert.Equal(t, 1, dedup.execCalls)
		assert.Equal(t, int64(5001), dedup.gotID)
		assert.Equal(t, "deref_media", dedup.gotHandler)
		assert.Equal(t, 1, repo.modifyCalls, "副作用应经去重模板在事务内执行")
		assert.Equal(t, []int64{-1}, repo.gotDeltas)
	})

	t.Run("重复投递 ACK 跳过不减数", func(t *testing.T) {
		repo := &fakeMediaRepo{}
		dedup := &fakeDedup{applied: false}
		c := newDerefMediaConsumer(repo, dedup)

		ack, err := c.HandleDerefMedia(context.Background(), msg, "5001")
		require.NoError(t, err)
		assert.True(t, ack, "重复消息也应 ACK（幂等跳过，不重投）")
		assert.Zero(t, repo.modifyCalls, "重复投递不得再次减数")
	})

	t.Run("副作用失败 RETRY 可重投", func(t *testing.T) {
		repo := &fakeMediaRepo{modifyErr: errors.New("db down")}
		dedup := &fakeDedup{applied: true}
		c := newDerefMediaConsumer(repo, dedup)

		ack, err := c.HandleDerefMedia(context.Background(), msg, "5001")
		assert.False(t, ack, "RETRY")
		assert.Error(t, err)
	})

	t.Run("去重登记失败 RETRY 不减数", func(t *testing.T) {
		repo := &fakeMediaRepo{}
		dedup := &fakeDedup{execErr: errors.New("db down")}
		c := newDerefMediaConsumer(repo, dedup)

		ack, err := c.HandleDerefMedia(context.Background(), msg, "5001")
		assert.False(t, ack, "RETRY")
		assert.Error(t, err)
		assert.Zero(t, repo.modifyCalls)
	})

	t.Run("非法资产 ID ACK 丢弃不触达计数", func(t *testing.T) {
		repo := &fakeMediaRepo{}
		dedup := &fakeDedup{applied: true}
		c := newDerefMediaConsumer(repo, dedup)

		ack, err := c.HandleDerefMedia(context.Background(), &mediav1.DerefMedia{AssetId: "bad"}, "5001")
		assert.True(t, ack)
		assert.Error(t, err)
		assert.Zero(t, dedup.execCalls)
	})

	t.Run("非法 message_id RETRY 不减数（宁可重试不可错减）", func(t *testing.T) {
		repo := &fakeMediaRepo{}
		dedup := &fakeDedup{applied: true}
		c := newDerefMediaConsumer(repo, dedup)

		ack, err := c.HandleDerefMedia(context.Background(), msg, "not-a-number")
		assert.False(t, ack, "RETRY")
		assert.Error(t, err)
		assert.Zero(t, dedup.execCalls)
	})
}

// fakeProcessS3 处理链专用的 S3 桩：原图下载返回一张可解码的小图，变体上传只计数
type fakeProcessS3 struct {
	oss.S3Repo
	origin      []byte
	downloadErr error
	uploads     int
	keys        []string
}

func (f *fakeProcessS3) Download(_ context.Context, _ string) (*oss.DownloadResult, error) {
	if f.downloadErr != nil {
		return nil, f.downloadErr
	}
	return &oss.DownloadResult{Body: io.NopCloser(bytes.NewReader(f.origin))}, nil
}

func (f *fakeProcessS3) Upload(_ context.Context, key string, r io.Reader, _ int64, _ string) (*oss.UploadResult, error) {
	if _, err := io.Copy(io.Discard, r); err != nil {
		return nil, err
	}
	f.uploads++
	f.keys = append(f.keys, key)
	return &oss.UploadResult{}, nil
}

// fakeProcessRepo ProcessAsset 专用：记录状态转移与变体落库次数
type fakeProcessRepo struct {
	v1.MediaRepository

	status        mediav1.MediaAssetStatus
	statusChanges [][2]mediav1.MediaAssetStatus
	files         []*models.MediaFile
}

func (f *fakeProcessRepo) WithTX(_ *gorm.DB) v1.MediaRepository { return f }

func (f *fakeProcessRepo) GetMediaAssets(_ context.Context, _ []int64) ([]*models.MediaAsset, error) {
	return []*models.MediaAsset{{Status: f.status}}, nil
}

func (f *fakeProcessRepo) SetMediaAssetStatus(_ context.Context, _ int64, from, to mediav1.MediaAssetStatus) (gen.ResultInfo, error) {
	// 守卫语义：只有 from 与当前状态一致才生效（模拟 WHERE status=from）
	if f.status != from {
		return gen.ResultInfo{RowsAffected: 0}, nil
	}
	f.statusChanges = append(f.statusChanges, [2]mediav1.MediaAssetStatus{from, to})
	f.status = to
	return gen.ResultInfo{RowsAffected: 1}, nil
}

func (f *fakeProcessRepo) CreateMediaFile(_ context.Context, file *models.MediaFile) error {
	f.files = append(f.files, file)
	return nil
}

func newProcessAssetConsumer(repo v1.MediaRepository, s3 oss.S3Repo) *MediaConsumer {
	return &MediaConsumer{
		MessageLoop: &MessageLoop[*mediav1.MediaMessage]{Consumer: Consumer{log: slog.Default(), maxRetry: 5}},
		Repo:        repo,
		S3:          s3,
	}
}

// testOriginImage 构造一张宽度大于最大档位的可解码图片，覆盖"缩放"与"编码"两条路径
func testOriginImage(t *testing.T) []byte {
	t.Helper()
	img := imaging.New(1200, 800, color.NRGBA{R: 0x20, G: 0x60, B: 0xa0, A: 0xff})
	var buf bytes.Buffer
	require.NoError(t, imagepkg.Encode(&buf, img, &imagepkg.Options{Quality: 90}))
	return buf.Bytes()
}

// TestProcessAsset 一次投递内联跑完 审核占位→变体转换→ACTIVE：
// 状态链不再依赖 audit/process/publish 三次消息接力（每跳都要等 outbox poller tick）
func TestProcessAsset(t *testing.T) {
	const assetID = int64(7001)

	t.Run("首次投递：UPLOADING→AUDITING→PROCESSING→ACTIVE，三档变体各落库一次", func(t *testing.T) {
		repo := &fakeProcessRepo{status: mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_UPLOADING}
		s3 := &fakeProcessS3{origin: testOriginImage(t)}
		c := newProcessAssetConsumer(repo, s3) // MQ 为 nil：若仍发消息会 panic，反证链路已不再产生消息

		ack, err := c.ProcessAsset(context.Background(), assetID, "rednote", "origin.jpg", "9001", nil)
		require.NoError(t, err)
		assert.True(t, ack, "处理完成 ACK")

		assert.Equal(t, [][2]mediav1.MediaAssetStatus{
			{mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_UPLOADING, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_AUDITING},
			{mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_AUDITING, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_PROCESSING},
			{mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_PROCESSING, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE},
		}, repo.statusChanges, "状态链：审核占位 → 处理 → ACTIVE")
		assert.Equal(t, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE, repo.status)

		require.Len(t, repo.files, 3, "216/540/1080 三档变体")
		assert.Equal(t, 3, s3.uploads, "每档一次 S3 上传")
		for i, want := range []mediav1.MediaFileType{
			mediav1.MediaFileType_MEDIA_FILE_TYPE_216,
			mediav1.MediaFileType_MEDIA_FILE_TYPE_540,
			mediav1.MediaFileType_MEDIA_FILE_TYPE_1080,
		} {
			assert.Equal(t, want, repo.files[i].Type)
			assert.Equal(t, mediav1.MediaFormat_MEDIA_FORMAT_WEBP, repo.files[i].Format)
			assert.Positive(t, repo.files[i].SizeBytes)
			assert.Equal(t, int64(216), repo.files[0].Width, "216 档按目标宽度缩放")
		}
	})

	t.Run("重复投递（已 ACTIVE）：ACK 跳过不重跑转换", func(t *testing.T) {
		repo := &fakeProcessRepo{status: mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE}
		s3 := &fakeProcessS3{origin: testOriginImage(t)}
		c := newProcessAssetConsumer(repo, s3)

		ack, err := c.ProcessAsset(context.Background(), assetID, "rednote", "origin.jpg", "9001", nil)
		require.NoError(t, err)
		assert.True(t, ack)
		assert.Zero(t, s3.uploads, "已 ACTIVE 不得重复上传变体")
		assert.Empty(t, repo.statusChanges)
	})

	t.Run("在途重投（PROCESSING）：续跑转换并置 ACTIVE", func(t *testing.T) {
		repo := &fakeProcessRepo{status: mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_PROCESSING}
		s3 := &fakeProcessS3{origin: testOriginImage(t)}
		c := newProcessAssetConsumer(repo, s3)

		ack, err := c.ProcessAsset(context.Background(), assetID, "rednote", "origin.jpg", "9001", nil)
		require.NoError(t, err)
		assert.True(t, ack)
		assert.Equal(t, 3, s3.uploads, "在途重投续跑")
		assert.Equal(t, [][2]mediav1.MediaAssetStatus{
			{mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_PROCESSING, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE},
		}, repo.statusChanges, "不重复推进 AUDITING/PROCESSING")
	})

	t.Run("终态（FAILED）：ACK 跳过", func(t *testing.T) {
		repo := &fakeProcessRepo{status: mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_FAILED}
		s3 := &fakeProcessS3{origin: testOriginImage(t)}
		c := newProcessAssetConsumer(repo, s3)

		ack, err := c.ProcessAsset(context.Background(), assetID, "rednote", "origin.jpg", "9001", nil)
		require.NoError(t, err)
		assert.True(t, ack)
		assert.Zero(t, s3.uploads)
		assert.Empty(t, repo.statusChanges)
	})
}

// fakeDeleteS3 只覆写封禁/删除路径所需方法
type fakeDeleteS3 struct {
	oss.S3Repo
	exist   bool
	deleted []string
}

func (f *fakeDeleteS3) Exist(_ context.Context, _ string) (bool, error) { return f.exist, nil }

func (f *fakeDeleteS3) DeleteObject(_ context.Context, key string) error {
	f.deleted = append(f.deleted, key)
	return nil
}

// fakeFilesRepo 记录 GetMediaFiles 入参，验证 ids/types 等长配对（zip 语义）
type fakeFilesRepo struct {
	v1.MediaRepository

	gotIDs   []int64
	gotTypes []mediav1.MediaFileType
	files    []*models.MediaFile
}

func (f *fakeFilesRepo) GetMediaFiles(_ context.Context, ids []int64, types []mediav1.MediaFileType) ([]*models.MediaFile, error) {
	f.gotIDs, f.gotTypes = ids, types
	if len(ids) != len(types) {
		return nil, errors.New("IDs and types length mismatch")
	}
	return f.files, nil
}

func (f *fakeFilesRepo) GetMediaAssets(_ context.Context, _ []int64) ([]*models.MediaAsset, error) {
	return []*models.MediaAsset{{Status: mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_BANNED}}, nil
}

// TestHandleBanMedia 封禁清理：必须按 (asset_id, type) 等长配对取全部变体。
// 历史实现传 1 个 id + 4 个 type，GetMediaFiles 必报 mismatch，该消息每次投递都失败（poison），
// 封禁的 S3 清理从未执行。
func TestHandleBanMedia(t *testing.T) {
	repo := &fakeFilesRepo{files: []*models.MediaFile{{ObjKey: "media/7001/540.webp"}}}
	s3 := &fakeDeleteS3{exist: true}
	c := &MediaConsumer{
		MessageLoop: &MessageLoop[*mediav1.MediaMessage]{Consumer: Consumer{log: slog.Default()}},
		Repo:        repo,
		S3:          s3,
	}

	ack, err := c.HandleBanMedia(context.Background(), &mediav1.BanMedia{AssetId: "7001"})
	require.NoError(t, err, "不得再出现 IDs and types length mismatch")
	assert.True(t, ack)
	assert.Len(t, repo.gotIDs, len(repo.gotTypes), "ids/types 必须等长")
	assert.Len(t, repo.gotIDs, len(AllMediaTypes), "覆盖全部媒体类型")
	for _, id := range repo.gotIDs {
		assert.Equal(t, int64(7001), id, "每个类型都配同一资产")
	}
	assert.Equal(t, []string{"media/7001/540.webp"}, s3.deleted, "封禁应删掉变体对象")
}

// fakeExistSeq 按调用次序返回 Exist 结果（末尾重复最后一个值）
type fakeExistSeq struct {
	oss.S3Repo
	seq   []bool
	calls int
}

func (f *fakeExistSeq) Exist(_ context.Context, _ string) (bool, error) {
	f.calls++
	if f.calls > len(f.seq) {
		return f.seq[len(f.seq)-1], nil
	}
	return f.seq[f.calls-1], nil
}

// TestWaitObject 上传确认的就地短等待：消息常比客户端 PUT 早到，短轮询接住即可省掉一次 Nak 重投（最小档 1s）
func TestWaitObject(t *testing.T) {
	t.Run("对象已落地：一次查询即返回", func(t *testing.T) {
		s3 := &fakeExistSeq{seq: []bool{true}}
		c := &MediaConsumer{S3: s3, waitObjectTimeout: time.Second}

		exist, err := c.waitObject(context.Background(), "origin")
		require.NoError(t, err)
		assert.True(t, exist)
		assert.Equal(t, 1, s3.calls)
	})

	t.Run("直传进行中：短等待内二次查询接住", func(t *testing.T) {
		s3 := &fakeExistSeq{seq: []bool{false, true}}
		c := &MediaConsumer{S3: s3, waitObjectTimeout: 2 * time.Second}

		start := time.Now()
		exist, err := c.waitObject(context.Background(), "origin")
		require.NoError(t, err)
		assert.True(t, exist, "第二轮应接住刚 PUT 完成的对象")
		assert.Equal(t, 2, s3.calls)
		assert.Less(t, time.Since(start), time.Second, "短等待不得退化成重投级别的时延")
	})

	t.Run("超时仍未见：返回 false 交由重投窗口兜底", func(t *testing.T) {
		s3 := &fakeExistSeq{seq: []bool{false}}
		c := &MediaConsumer{S3: s3, waitObjectTimeout: 200 * time.Millisecond}

		exist, err := c.waitObject(context.Background(), "origin")
		require.NoError(t, err)
		assert.False(t, exist)
		assert.Greater(t, s3.calls, 1, "应轮询多次后再放弃")
	})

	t.Run("未配置等待（直接构造的测试基座）：只查一次", func(t *testing.T) {
		s3 := &fakeExistSeq{seq: []bool{false}}
		c := &MediaConsumer{S3: s3}

		exist, err := c.waitObject(context.Background(), "origin")
		require.NoError(t, err)
		assert.False(t, exist)
		assert.Equal(t, 1, s3.calls)
	})
}

// TestProcessAsset_RetryExhausted 处理链重试耗尽兜底：MaxDeliver 用尽后若仍 RETRY，JetStream 会静默丢弃
// 消息（本服务未配死信），资产将永久卡在 PROCESSING（前端一直显示处理中、无告警）。
// 因此耗尽时必须把在途资产判为 FAILED 并 ACK。
func TestProcessAsset_RetryExhausted(t *testing.T) {
	const assetID = int64(7001)

	t.Run("窗口内失败：RETRY 且资产留在 PROCESSING", func(t *testing.T) {
		repo := &fakeProcessRepo{status: mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_PROCESSING}
		s3 := &fakeProcessS3{downloadErr: errors.New("s3 unreachable")}
		c := newProcessAssetConsumer(repo, s3)

		ctx := withDelivered(context.Background(), &jetstream.MsgMetadata{NumDelivered: 1})
		ack, err := c.ProcessAsset(ctx, assetID, "rednote", "origin.jpg", "9001", nil)
		require.Error(t, err)
		assert.False(t, ack, "窗口内应 RETRY 退避重投")
		assert.Equal(t, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_PROCESSING, repo.status, "窗口内不得判失败")
		assert.Empty(t, repo.statusChanges)
	})

	t.Run("投递次数耗尽：ACK + PROCESSING→FAILED", func(t *testing.T) {
		repo := &fakeProcessRepo{status: mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_PROCESSING}
		s3 := &fakeProcessS3{downloadErr: errors.New("s3 unreachable")}
		c := newProcessAssetConsumer(repo, s3)

		ctx := withDelivered(context.Background(), &jetstream.MsgMetadata{NumDelivered: 5})
		ack, err := c.ProcessAsset(ctx, assetID, "rednote", "origin.jpg", "9001", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "retry window exhausted", "错误信息须带判失败原因（ERROR 日志追溯）")
		assert.True(t, ack, "判失败后 ACK，避免无谓重投")
		assert.Equal(t, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_FAILED, repo.status)
		assert.Equal(t, [][2]mediav1.MediaAssetStatus{{
			mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_PROCESSING,
			mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_FAILED,
		}}, repo.statusChanges)
	})

	t.Run("耗尽时资产已是 ACTIVE（并发推进）：ACK 不改状态", func(t *testing.T) {
		repo := &fakeProcessRepo{status: mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE}
		s3 := &fakeProcessS3{downloadErr: errors.New("s3 unreachable")}
		c := newProcessAssetConsumer(repo, s3)

		ctx := withDelivered(context.Background(), &jetstream.MsgMetadata{NumDelivered: 5})
		ack, err := c.ProcessAsset(ctx, assetID, "rednote", "origin.jpg", "9001", nil)
		require.NoError(t, err)
		assert.True(t, ack)
		assert.Empty(t, repo.statusChanges, "已 ACTIVE 不得被判失败")
	})
}
