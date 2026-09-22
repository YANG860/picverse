package v1

import (
	"context"
	"rednote/gen/query"
	common "rednote/internal/common/models"
	"rednote/packages/defaults"
	"time"

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
