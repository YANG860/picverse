package consumer

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	mediav1 "rednote/gen/go/media/v1"
	commonv1 "rednote/internal/common/v1"
	v1 "rednote/internal/media/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// fakeMediaRepo 覆写 HandleDerefMedia 所需方法（未覆写方法调用会 panic）
type fakeMediaRepo struct {
	v1.MediaRepository

	modifyErr   error
	modifyCalls int
	gotDeltas   []int64
}

func (f *fakeMediaRepo) WithTX(_ *gorm.DB) v1.MediaRepository { return f }

func (f *fakeMediaRepo) ModifyMediaAssetRefCount(_ context.Context, _ []int64, deltas []int64) error {
	f.modifyCalls++
	f.gotDeltas = deltas
	return f.modifyErr
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
