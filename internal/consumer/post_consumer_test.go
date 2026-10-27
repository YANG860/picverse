package consumer

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	mediav1 "rednote/gen/go/media/v1"
	postv1 "rednote/gen/go/post/v1"
	v1 "rednote/internal/post/v1"
	"rednote/packages/defaults"
	"rednote/packages/mq"
	"rednote/test/testmq"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gen"
	"gorm.io/gorm"
)

// fakePostRepo 覆写 handleDeletePost 所需方法（未覆写方法调用会 panic）；
// DeletePostMedias 返回一次被删关联后置空，模拟重复投递时 0 行命中（幂等守卫）
type fakePostRepo struct {
	v1.PostRepository

	deletePostErr   error
	deleteMediasErr error
	assetIDs        []int64

	gotDeletePost   bool
	gotDeleteMedias bool
}

func (f *fakePostRepo) Transaction(fn func(repo v1.PostRepository) error) error {
	return fn(f)
}

func (f *fakePostRepo) DB() *gorm.DB { return nil }

func (f *fakePostRepo) DeletePost(_ context.Context, _, _ int64) (gen.ResultInfo, error) {
	f.gotDeletePost = true
	if f.deletePostErr != nil {
		return gen.ResultInfo{}, f.deletePostErr
	}
	return gen.ResultInfo{}, nil
}

func (f *fakePostRepo) DeletePostMedias(_ context.Context, _ int64) ([]int64, error) {
	f.gotDeleteMedias = true
	if f.deleteMediasErr != nil {
		return nil, f.deleteMediasErr
	}
	ids := f.assetIDs
	f.assetIDs = nil
	return ids, nil
}

// derefAssetIDs 解出 media 流上的 DerefMedia 消息资产 ID 序列
func derefAssetIDs(t *testing.T, c *testmq.Client) []string {
	t.Helper()
	var ids []string
	for _, m := range c.SentBySubject(defaults.MediaMessageStream) {
		var msg mediav1.MediaMessage
		require.NoError(t, proto.Unmarshal(m.PayLoad, &msg))
		require.NotNil(t, msg.GetDerefMedia(), "删帖回收必须发 DerefMedia 卸载消息")
		ids = append(ids, msg.GetDerefMedia().GetAssetId())
	}
	return ids
}

func newDeletePostConsumer(repo v1.PostRepository, mqClient mq.MQClient) *PostConsumer {
	return &PostConsumer{
		Consumer: Consumer{MQ: mqClient, log: slog.Default()},
		Repo:     repo,
	}
}

// TestHandleDeletePost 删帖回收：物理删除 + 按被删关联逐槽位写 outbox 卸载消息（计数回收必达不漏）
func TestHandleDeletePost(t *testing.T) {
	msg := &postv1.DeletePost{PostId: "9001", AuthorId: "8001"}

	t.Run("删除并发卸载消息：删几槽发几条", func(t *testing.T) {
		repo := &fakePostRepo{assetIDs: []int64{6001, 6002}}
		mqc := testmq.New()
		c := newDeletePostConsumer(repo, mqc)

		ack, err := c.handleDeletePost(context.Background(), msg)
		require.NoError(t, err)
		assert.True(t, ack)
		assert.True(t, repo.gotDeletePost)
		assert.True(t, repo.gotDeleteMedias)
		assert.Equal(t, []string{"6001", "6002"}, derefAssetIDs(t, mqc))

		sent := mqc.SentBySubject(defaults.MediaMessageStream)
		require.Len(t, sent, 2)
		assert.NotEqual(t, sent[0].ID, sent[1].ID, "每条消息唯一 ID，媒体侧幂等去重")
	})

	t.Run("重复投递幂等：无被删关联不再发消息", func(t *testing.T) {
		repo := &fakePostRepo{assetIDs: []int64{6001}}
		mqc := testmq.New()
		c := newDeletePostConsumer(repo, mqc)

		_, err := c.handleDeletePost(context.Background(), msg)
		require.NoError(t, err)
		require.Len(t, mqc.SentBySubject(defaults.MediaMessageStream), 1)

		ack, err := c.handleDeletePost(context.Background(), msg)
		require.NoError(t, err)
		assert.True(t, ack)
		assert.Len(t, mqc.SentBySubject(defaults.MediaMessageStream), 1, "第二次不重复发卸载消息")
	})

	t.Run("物理删除失败 RETRY 不发消息", func(t *testing.T) {
		repo := &fakePostRepo{assetIDs: []int64{6001}, deletePostErr: errors.New("db down")}
		mqc := testmq.New()
		c := newDeletePostConsumer(repo, mqc)

		ack, err := c.handleDeletePost(context.Background(), msg)
		assert.False(t, ack, "RETRY")
		assert.Error(t, err)
		assert.Empty(t, mqc.Sent())
	})

	t.Run("关联删除失败 RETRY 不发消息", func(t *testing.T) {
		repo := &fakePostRepo{assetIDs: []int64{6001}, deleteMediasErr: errors.New("db down")}
		mqc := testmq.New()
		c := newDeletePostConsumer(repo, mqc)

		ack, err := c.handleDeletePost(context.Background(), msg)
		assert.False(t, ack, "RETRY")
		assert.Error(t, err)
		assert.Empty(t, mqc.Sent())
	})

	t.Run("outbox 写入失败 RETRY（事务回滚，不漏减）", func(t *testing.T) {
		repo := &fakePostRepo{assetIDs: []int64{6001}}
		mqc := testmq.New()
		mqc.SetSendError(errors.New("outbox write fail"))
		c := newDeletePostConsumer(repo, mqc)

		ack, err := c.handleDeletePost(context.Background(), msg)
		assert.False(t, ack, "RETRY")
		assert.Error(t, err)
	})
}
