package v1

import (
	"context"
	"rednote/gen/query"
	common "rednote/internal/common/models"
	"rednote/packages/defaults"
	"time"

	"gorm.io/gen"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type OutBoxRepository interface {
	WithTX(tx *gorm.DB) OutBoxRepository
	Poll(ctx context.Context, batchSize int) ([]*common.OutBoxEntry, error)
	SetEntryStatusDone(ctx context.Context, ids []int64) error
	AddEntryRetryCount(ctx context.Context, ids []int64) error
}

var _ OutBoxRepository = (*OutBoxRepoImpl)(nil)

type OutBoxRepoImpl struct {
	DB *gorm.DB
	q  *query.Query
}

func NewOutBoxRepo(db *gorm.DB) *OutBoxRepoImpl {
	return &OutBoxRepoImpl{
		DB: db,
		q:  query.Use(db),
	}
}

func (r *OutBoxRepoImpl) WithTX(tx *gorm.DB) OutBoxRepository {
	return &OutBoxRepoImpl{
		DB: tx,
		q:  query.Use(tx),
	}
}

func (r *OutBoxRepoImpl) Poll(ctx context.Context, batchSize int) ([]*common.OutBoxEntry, error) {
	entries, err := r.q.OutBoxEntry.WithContext(ctx).
		Clauses(clause.Locking{
			Strength: clause.LockingStrengthUpdate,
			Options:  clause.LockingOptionsSkipLocked,
		}).
		Where(
			r.q.OutBoxEntry.SendAt.Lt(time.Now().UnixMilli()),
			r.q.OutBoxEntry.Status.Eq(int32(common.STATUS_PENDING)),
			r.q.OutBoxEntry.RetryCount.Lt(defaults.MaxRetry),
		).
		Limit(
			batchSize,
		).
		Find()
	return entries, err
}

func (r *OutBoxRepoImpl) SetEntryStatusDone(ctx context.Context, ids []int64) error {
	_, err := r.q.OutBoxEntry.WithContext(ctx).
		Where(
			r.q.OutBoxEntry.ID.In(ids...),
		).
		Update(
			r.q.OutBoxEntry.Status, int32(common.STATUS_DONE),
		)
	return err
}

func (r *OutBoxRepoImpl) AddEntryRetryCount(ctx context.Context, ids []int64) error {
	_, err := r.q.OutBoxEntry.WithContext(ctx).
		Where(
			r.q.OutBoxEntry.ID.In(ids...),
		).
		UpdateSimple(
			r.q.OutBoxEntry.RetryCount.Add(1),
		)
	return err
}

// ProcessedMessageRepository 消费端消息幂等去重（共用机制，所有 AtLeastOnce 消费点复用）
type ProcessedMessageRepository interface {
	WithTX(tx *gorm.DB) ProcessedMessageRepository
	// ExecuteIdempotent 幂等执行：同事务"去重登记 + 业务副作用 fn"，恰好一次效果。
	// applied=false 表示消息已处理过，fn 不执行；fn 失败整体回滚（去重记录一并回滚）。
	ExecuteIdempotent(ctx context.Context, messageID int64, handler string, fn func(tx *gorm.DB) error) (applied bool, err error)
}

var _ ProcessedMessageRepository = (*ProcessedMessageRepoImpl)(nil)

type ProcessedMessageRepoImpl struct {
	DB *gorm.DB
	q  *query.Query
}

func NewProcessedMessageRepo(db *gorm.DB) *ProcessedMessageRepoImpl {
	return &ProcessedMessageRepoImpl{
		DB: db,
		q:  query.Use(db),
	}
}

func (r *ProcessedMessageRepoImpl) WithTX(tx *gorm.DB) ProcessedMessageRepository {
	return &ProcessedMessageRepoImpl{
		DB: tx,
		q:  query.Use(tx),
	}
}

// ExecuteIdempotent 幂等执行：同事务内"去重登记 + 业务副作用"。
// 去重记录与副作用原子提交，崩溃回滚后重投从头执行（exactly-once effect）；
// 重复投递按 message_id 主键冲突识别，返回 applied=false 不执行副作用。
func (r *ProcessedMessageRepoImpl) ExecuteIdempotent(ctx context.Context, messageID int64, handler string, fn func(tx *gorm.DB) error) (bool, error) {
	var applied bool

	err := r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		q := query.Use(tx)

		// ON CONFLICT DO NOTHING：RowsAffected=0 识别重复（幂等登记）
		info := q.WithContext(ctx).ProcessedMessage.WithResult(func(dao gen.Dao) {
			_ = dao.Clauses(clause.OnConflict{DoNothing: true}).Create(&common.ProcessedMessage{
				MessageID:   messageID,
				Handler:     handler,
				ProcessedAt: time.Now().UnixMilli(),
			})
		})
		if info.Error != nil {
			return info.Error
		}
		if info.RowsAffected == 0 {
			applied = false
			return nil
		}

		if err := fn(tx); err != nil {
			return err
		}
		applied = true
		return nil
	})

	return applied, err
}
