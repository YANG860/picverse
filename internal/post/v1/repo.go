package v1

import (
	"context"

	postv1 "rednote/gen/go/post/v1"
	"rednote/gen/query"
	media "rednote/internal/media/models"
	models "rednote/internal/post/models"
	"rednote/packages/defaults"

	"gorm.io/datatypes"
	"gorm.io/gen"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PostRepository 帖子存储库接口（gorm gen 查询，见 gen/query）
type PostRepository interface {
	// 使用事务
	WithTX(tx *gorm.DB) PostRepository
	// Transaction 在事务内执行 fn，fn 拿到绑定事务的 repo
	Transaction(fn func(repo PostRepository) error) error
	// DB 返回当前绑定的 *gorm.DB（事务上下文中即事务句柄，供 MQ.SendTX 等事务性写入共享同一事务）
	DB() *gorm.DB

	// ==================== 草稿 ====================

	// CreatePost 创建帖子（草稿）
	CreatePost(ctx context.Context, post *models.PostDetail) error
	// DeletePost 物理删除软删（DELETED 状态）的帖子行，供删帖消息的下游回收链路调用
	DeletePost(ctx context.Context, authorID, postID int64) (gen.ResultInfo, error)
	// SetPostStatus 流转帖子状态（条件更新，from 为当前状态）
	SetPostStatus(ctx context.Context, postID int64, from, to postv1.PostStatus) (gen.ResultInfo, error)
	// LockDraftForUpdate 对草稿行加行锁并原子校验“存在且仍为 DRAFT”；ok 为 false 表示不存在或已非 DRAFT
	LockDraftForUpdate(ctx context.Context, postID int64) (bool, error)
	// UpdateDraftTitle 更新草稿标题（仅 DRAFT 状态的帖子生效）
	UpdateDraftTitle(ctx context.Context, postID int64, title string) (gen.ResultInfo, error)
	// UpdateDraftText 更新草稿正文（仅 DRAFT 状态的帖子生效）
	UpdateDraftText(ctx context.Context, postID int64, text string) (gen.ResultInfo, error)
	// UpdateDraftMetaData 更新草稿元数据（仅 DRAFT 状态的帖子生效）
	UpdateDraftMetaData(ctx context.Context, postID int64, metaData datatypes.JSONMap) (gen.ResultInfo, error)

	// ==================== 帖子统计 ====================
	// 逐字段更新统计列（仅更新传入的列）；WHERE 仅按 id 匹配（PG 匹配行即计数，值不变也算命中），
	// RowsAffected 为 0 即帖子不存在，NotFound 由上层判定
	// UpdatePostLikeCount 更新点赞数
	UpdatePostLikeCount(ctx context.Context, postID int64, count int64) (gen.ResultInfo, error)
	// UpdatePostFavouriteCount 更新收藏数
	UpdatePostFavouriteCount(ctx context.Context, postID int64, count int64) (gen.ResultInfo, error)
	// UpdatePostCommentCount 更新评论数
	UpdatePostCommentCount(ctx context.Context, postID int64, count int64) (gen.ResultInfo, error)
	// UpdatePostViewCount 更新浏览数
	UpdatePostViewCount(ctx context.Context, postID int64, count int64) (gen.ResultInfo, error)
	// ==================== 帖子查询 ====================

	// 获取作者id
	GetAuthorID(ctx context.Context, postID int64) (bool, int64, error)
	// GetPosts 根据ID批量获取帖子
	GetPosts(ctx context.Context, postIDs []int64) ([]*models.PostDetail, error)
	// GetPostStat 批量获取帖子统计（列裁剪：仅 id/4 个计数列；不存在的 id 不返回）
	GetPostStat(ctx context.Context, postIDs []int64) ([]*models.PostStat, error)
	// GetPostStatus 获取帖子状态；ok 为 false 表示帖子不存在
	GetPostStatus(ctx context.Context, postID int64) (bool, postv1.PostStatus, error)

	// GetPostBriefsByAuthor 按作者游标分页获取帖子列表，包括审核中和草稿
	GetPostBriefsByAuthor(ctx context.Context, authorID int64, before int64, limit int64) ([]*models.PostDetail, error)
	// GetPublishedPostBriefsByAuthor 按作者游标分页获取帖子列表，已发布的
	GetPublishedPostBriefsByAuthor(ctx context.Context, authorID int64, before int64, limit int64) ([]*models.PostDetail, error)

	// ==================== 帖子管理 ====================

	// SetPostBanned 封禁帖子
	SetPostBanned(ctx context.Context, postID int64) (gen.ResultInfo, error)
	// SetPostDeleted 删除帖子，标记删除
	SetPostDeleted(ctx context.Context, postID int64) (gen.ResultInfo, error)

	// ==================== 媒体关联 ====================
	// LockDraftMediasForUpdate 对草稿现有媒体关联行加行锁，串行化并发替换；无关联行不视为失败（媒体可后补）
	LockDraftMediasForUpdate(ctx context.Context, postID int64) error
	// CreatePostMedias 创建帖子与媒体的关联
	CreatePostMedias(ctx context.Context, postID int64, medias []*media.MediaFile) error
	// GetPostMedias 获取帖子下的媒体关联
	GetPostMedias(ctx context.Context, postIDs []int64) ([]*models.PostMedia, error)
	// GetPostCover 获取帖子封面，首个媒体关联
	GetPostCover(ctx context.Context, postIDs []int64) ([]*models.PostMedia, error)
	// DeletePostMedias 删除帖子与媒体的关联，返回被删关联的媒体资产 ID（供调用方回收引用计数）
	DeletePostMedias(ctx context.Context, postID int64) ([]int64, error)

	// ==================== 互动（点赞/收藏明细） ====================
	// CreateLike 点赞明细（OnConflict DoNothing 幂等）；ok=false 表示已赞过（未新增）
	CreateLike(ctx context.Context, postID int64, userID int64) (bool, error)
	// DeleteLike 删除点赞明细；ok=true 表示确有删除（未赞时 false，幂等）
	DeleteLike(ctx context.Context, postID int64, userID int64) (bool, error)
	// CreateFavourite 收藏明细（幂等）；ok=false 表示已收藏
	CreateFavourite(ctx context.Context, postID int64, userID int64) (bool, error)
	// DeleteFavourite 删除收藏明细；ok=true 表示确有删除
	DeleteFavourite(ctx context.Context, postID int64, userID int64) (bool, error)
	// CountFavouritesByUser 用户收藏总数（收藏夹容量校验用）
	CountFavouritesByUser(ctx context.Context, userID int64) (int64, error)
	// IncPostLikeCount 点赞数 +1（条件增量；RowsAffected=0 即帖子不存在，下层同库事务内调用）
	IncPostLikeCount(ctx context.Context, postID int64) (gen.ResultInfo, error)
	// DecPostLikeCount 点赞数 -1（防负条件更新：仅 like_count > 0 才减；计数已 0 时按存在性回报）
	DecPostLikeCount(ctx context.Context, postID int64) (gen.ResultInfo, error)
	// IncPostFavouriteCount 收藏数 +1（条件增量）
	IncPostFavouriteCount(ctx context.Context, postID int64) (gen.ResultInfo, error)
	// DecPostFavouriteCount 收藏数 -1（防负条件更新：仅计数 > 0 才减）
	DecPostFavouriteCount(ctx context.Context, postID int64) (gen.ResultInfo, error)
	// ListLikedPostIDs 用户点赞的帖子明细（created_at DESC, id DESC；返回行供游标取 stamp）
	ListLikedPostIDs(ctx context.Context, userID int64, before int64, limit int64) ([]*models.PostLike, error)
	// ListFavouritePostIDs 用户收藏的帖子明细（同构）
	ListFavouritePostIDs(ctx context.Context, userID int64, before int64, limit int64) ([]*models.PostFavourite, error)
}

var _ PostRepository = (*PostRepoImpl)(nil)

// PostRepoImpl 帖子存储库实现（gorm gen 查询）
type PostRepoImpl struct {
	db *gorm.DB
	q  *query.Query
}

func NewPostRepo(db *gorm.DB) *PostRepoImpl {
	return &PostRepoImpl{db: db, q: query.Use(db)}
}

func (r *PostRepoImpl) WithTX(tx *gorm.DB) PostRepository {
	return &PostRepoImpl{db: tx, q: query.Use(tx)}
}

// Transaction 在事务内执行 fn，fn 拿到绑定事务的 repo
func (r *PostRepoImpl) Transaction(fn func(repo PostRepository) error) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		return fn(r.WithTX(tx))
	})
}

// DB 返回当前绑定的 *gorm.DB（事务上下文中即事务句柄）
func (r *PostRepoImpl) DB() *gorm.DB {
	return r.db
}

// ==================== 草稿 ====================

// CreatePost 创建帖子（草稿）
func (r *PostRepoImpl) CreatePost(ctx context.Context, post *models.PostDetail) error {
	err := r.q.PostDetail.WithContext(ctx).Create(post)
	return err
}

// DeletePost 物理删除软删（DELETED 状态）的帖子行；
// WHERE 带 status = DELETED 守卫：仅回收软删行，避免误物理删未删帖（含已发布）的裸行，
// 与 handleDeletePost 的 DeletePostMedias 配套；重复投递时 0 行命中即幂等成功
func (r *PostRepoImpl) DeletePost(ctx context.Context, authorID, postID int64) (gen.ResultInfo, error) {
	info, err := r.q.PostDetail.WithContext(ctx).
		Where(
			r.q.PostDetail.ID.Eq(postID),
			r.q.PostDetail.AuthorID.Eq(authorID),
			r.q.PostDetail.Status.Eq(int32(postv1.PostStatus_POST_STATUS_DELETED)),
		).
		Delete()
	return info, err
}

// SetPostStatus 流转帖子状态
func (r *PostRepoImpl) SetPostStatus(ctx context.Context, postID int64, from, to postv1.PostStatus) (gen.ResultInfo, error) {
	info, err := r.q.PostDetail.WithContext(ctx).
		Where(
			r.q.PostDetail.ID.Eq(postID),
			r.q.PostDetail.Status.Eq(int32(from)),
		).
		Update(
			r.q.PostDetail.Status, int32(to),
		)
	return info, err
}

// LockDraftForUpdate 对草稿行加行锁（SELECT ... FOR UPDATE）并原子校验“存在且仍为 DRAFT”；
// ok 为 false 表示帖子不存在或已非 DRAFT。
// 注意：聚合查询不能携带 FOR UPDATE（PG 报 SQLSTATE 0A000），必须用非聚合查询锁行后按结果判定
func (r *PostRepoImpl) LockDraftForUpdate(ctx context.Context, postID int64) (bool, error) {
	rows, err := r.q.PostDetail.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where(
			r.q.PostDetail.ID.Eq(postID),
			r.q.PostDetail.Status.Eq(int32(postv1.PostStatus_POST_STATUS_DRAFT)),
		).
		Find()
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

// UpdateDraftMetaData 更新草稿元数据（空 map 落库为 {}）；
// WHERE 带 status = DRAFT 条件，非草稿帖子不被修改（RowsAffected 为 0）
func (r *PostRepoImpl) UpdateDraftMetaData(ctx context.Context, postID int64, metadata datatypes.JSONMap) (gen.ResultInfo, error) {
	return r.q.PostDetail.WithContext(ctx).
		Where(
			r.q.PostDetail.ID.Eq(postID),
			r.q.PostDetail.Status.Eq(int32(postv1.PostStatus_POST_STATUS_DRAFT)),
		).
		Update(r.q.PostDetail.MetaData, metadata)
}

// UpdateDraftText 更新草稿正文；WHERE 带 status = DRAFT 条件，非草稿帖子不被修改
func (r *PostRepoImpl) UpdateDraftText(ctx context.Context, postID int64, text string) (gen.ResultInfo, error) {
	return r.q.PostDetail.WithContext(ctx).
		Where(
			r.q.PostDetail.ID.Eq(postID),
			r.q.PostDetail.Status.Eq(int32(postv1.PostStatus_POST_STATUS_DRAFT)),
		).
		Update(r.q.PostDetail.FullText, text)
}

// UpdateDraftTitle 更新草稿标题；WHERE 带 status = DRAFT 条件，非草稿帖子不被修改
func (r *PostRepoImpl) UpdateDraftTitle(ctx context.Context, postID int64, title string) (gen.ResultInfo, error) {
	return r.q.PostDetail.WithContext(ctx).
		Where(
			r.q.PostDetail.ID.Eq(postID),
			r.q.PostDetail.Status.Eq(int32(postv1.PostStatus_POST_STATUS_DRAFT)),
		).
		Update(r.q.PostDetail.Title, title)
}

// UpdatePostLikeCount 更新点赞数
func (r *PostRepoImpl) UpdatePostLikeCount(ctx context.Context, postID int64, count int64) (gen.ResultInfo, error) {
	return r.q.PostDetail.WithContext(ctx).
		Where(r.q.PostDetail.ID.Eq(postID)).
		Update(r.q.PostDetail.LikeCount, count)
}

// UpdatePostFavouriteCount 更新收藏数
func (r *PostRepoImpl) UpdatePostFavouriteCount(ctx context.Context, postID int64, count int64) (gen.ResultInfo, error) {
	return r.q.PostDetail.WithContext(ctx).
		Where(r.q.PostDetail.ID.Eq(postID)).
		Update(r.q.PostDetail.FavouriteCount, count)
}

// UpdatePostCommentCount 更新评论数
func (r *PostRepoImpl) UpdatePostCommentCount(ctx context.Context, postID int64, count int64) (gen.ResultInfo, error) {
	return r.q.PostDetail.WithContext(ctx).
		Where(r.q.PostDetail.ID.Eq(postID)).
		Update(r.q.PostDetail.CommentCount, count)
}

// UpdatePostViewCount 更新浏览数
func (r *PostRepoImpl) UpdatePostViewCount(ctx context.Context, postID int64, count int64) (gen.ResultInfo, error) {
	return r.q.PostDetail.WithContext(ctx).
		Where(r.q.PostDetail.ID.Eq(postID)).
		Update(r.q.PostDetail.ViewCount, count)
}

// ==================== 帖子查询 ====================

// GetAuthorID 获取作者id；帖子不存在时返回 0
func (r *PostRepoImpl) GetAuthorID(ctx context.Context, postID int64) (bool, int64, error) {
	// Pluck 需要切片 dest，且空结果不报错，须用结果长度判断不存在
	var authorIDs []int64
	err := r.q.PostDetail.WithContext(ctx).
		Where(
			r.q.PostDetail.ID.Eq(postID),
		).
		Pluck(
			r.q.PostDetail.AuthorID, &authorIDs,
		)
	if err != nil {
		return false, 0, err
	}
	if len(authorIDs) == 0 {
		return false, 0, nil
	}

	return true, authorIDs[0], nil
}

// GetPosts 根据ID批量获取帖子（媒体关联按展示顺序 sort_order 加载）
func (r *PostRepoImpl) GetPosts(ctx context.Context, postIDs []int64) ([]*models.PostDetail, error) {
	posts, err := r.q.PostDetail.WithContext(ctx).
		Where(r.q.PostDetail.ID.In(postIDs...)).
		Preload(r.q.PostDetail.Medias.Order(r.q.PostMedia.SortOrder)).
		Find()
	return posts, err
}

// GetPostStat 批量获取帖子统计：列裁剪查询（id/点赞/收藏/评论/浏览）
// 不加载其余列；不存在的 id 不返回（与 GetPosts 的部分命中语义一致）
func (r *PostRepoImpl) GetPostStat(ctx context.Context, postIDs []int64) ([]*models.PostStat, error) {
	rows, err := r.q.PostDetail.WithContext(ctx).
		Select(
			r.q.PostDetail.ID,
			r.q.PostDetail.LikeCount,
			r.q.PostDetail.FavouriteCount,
			r.q.PostDetail.CommentCount,
			r.q.PostDetail.ViewCount,
		).
		Where(r.q.PostDetail.ID.In(postIDs...)).
		Find()
	if err != nil {
		return nil, err
	}

	stats := make([]*models.PostStat, 0, len(rows))
	for _, row := range rows {
		stats = append(stats, &models.PostStat{
			ID:             row.ID,
			LikeCount:      row.LikeCount,
			FavouriteCount: row.FavouriteCount,
			CommentCount:   row.CommentCount,
			ViewCount:      row.ViewCount,
		})
	}
	return stats, nil
}

// GetPostStatus 获取帖子状态；ok 为 false 表示帖子不存在
func (r *PostRepoImpl) GetPostStatus(ctx context.Context, postID int64) (bool, postv1.PostStatus, error) {
	// 状态列 Pluck 到基础类型，gorm 对自定义类型 dest 支持不稳定，再转换为枚举
	var statuses []int32
	err := r.q.PostDetail.WithContext(ctx).
		Where(
			r.q.PostDetail.ID.Eq(postID),
		).
		Pluck(
			r.q.PostDetail.Status, &statuses,
		)
	if err != nil {
		return false, postv1.PostStatus_POST_STATUS_UNSPECIFIED, err
	}
	// Pluck 空结果不报错，ok 须用结果长度判断，否则不存在时会误报存在
	if len(statuses) == 0 {
		return false, postv1.PostStatus_POST_STATUS_UNSPECIFIED, nil
	}

	return true, postv1.PostStatus(statuses[0]), nil
}

// GetPostBriefsByAuthor 按作者游标分页获取帖子列表
func (r *PostRepoImpl) GetPostBriefsByAuthor(ctx context.Context, authorID int64, before int64, limit int64) ([]*models.PostDetail, error) {
	posts, err := r.q.PostDetail.WithContext(ctx).
		Where(
			r.q.PostDetail.AuthorID.Eq(authorID),
			r.q.PostDetail.CreatedAt.Lt(before),
			r.q.PostDetail.Status.In(
				int32(postv1.PostStatus_POST_STATUS_PUBLISHED),
				int32(postv1.PostStatus_POST_STATUS_AUDITING),
				int32(postv1.PostStatus_POST_STATUS_DRAFT),
			),
		).
		Preload(r.q.PostDetail.Medias.
			// gen 的 relation 条件必须用 On：Where 是独立关联查询的 DAO 方法，
			// Preload 只消费 RelationField 状态，传 Where 会被静默忽略（封面过滤失效）
			On(r.q.PostMedia.SortOrder.Eq(0)),
		).
		// 游标翻页依赖稳定顺序：无 ORDER BY 时 LIMIT 结果不确定，会漏项/重复；
		// created_at 并列时用 id 二级排序保证确定性
		Order(r.q.PostDetail.CreatedAt.Desc(), r.q.PostDetail.ID.Desc()).
		Limit(int(limit)).
		Find()

	return posts, err
}

// GetPublishedPostBriefsByAuthor 按作者游标分页获取帖子列表
func (r *PostRepoImpl) GetPublishedPostBriefsByAuthor(ctx context.Context, authorID int64, before int64, limit int64) ([]*models.PostDetail, error) {
	posts, err := r.q.PostDetail.WithContext(ctx).
		Where(
			r.q.PostDetail.AuthorID.Eq(authorID),
			r.q.PostDetail.CreatedAt.Lt(before),
			r.q.PostDetail.Status.Eq(
				int32(postv1.PostStatus_POST_STATUS_PUBLISHED),
			),
		).
		Preload(r.q.PostDetail.Medias.
			// gen 的 relation 条件必须用 On：Where 是独立关联查询的 DAO 方法，
			// Preload 只消费 RelationField 状态，传 Where 会被静默忽略（封面过滤失效）
			On(r.q.PostMedia.SortOrder.Eq(0)),
		).
		// 游标翻页依赖稳定顺序：无 ORDER BY 时 LIMIT 结果不确定，会漏项/重复；
		// created_at 并列时用 id 二级排序保证确定性
		Order(r.q.PostDetail.CreatedAt.Desc(), r.q.PostDetail.ID.Desc()).
		Limit(int(limit)).
		Find()
	return posts, err
}

// ==================== 帖子管理 ====================

// SetPostBanned 封禁帖子
func (r *PostRepoImpl) SetPostBanned(ctx context.Context, postID int64) (gen.ResultInfo, error) {
	return r.q.PostDetail.WithContext(ctx).
		Where(
			r.q.PostDetail.ID.Eq(postID),
			r.q.PostDetail.Status.Neq(int32(postv1.PostStatus_POST_STATUS_DELETED)),
			r.q.PostDetail.Status.Neq(int32(postv1.PostStatus_POST_STATUS_BANNED)),
		).
		Update(r.q.PostDetail.Status, postv1.PostStatus_POST_STATUS_BANNED)
}

// SetPostDeleted 删除帖子，标记删除；
// WHERE 带 status != DELETED 条件，已删除帖子不被修改（RowsAffected 为 0），
// 上层据此跳过重复投递 DeletePost 消息（PG 计匹配行，无守卫时值不变也计 1）
func (r *PostRepoImpl) SetPostDeleted(ctx context.Context, postID int64) (gen.ResultInfo, error) {
	return r.q.PostDetail.WithContext(ctx).
		Where(
			r.q.PostDetail.ID.Eq(postID),
			r.q.PostDetail.Status.Neq(int32(postv1.PostStatus_POST_STATUS_DELETED)),
		).
		Update(r.q.PostDetail.Status, postv1.PostStatus_POST_STATUS_DELETED)
}

// ==================== 媒体关联 ====================

// LockDraftMediasForUpdate 对草稿现有媒体关联行加行锁（SELECT ... FOR UPDATE），串行化并发替换；
// 无关联行不视为失败（媒体可后补），草稿状态守卫由 LockDraftForUpdate 负责。
func (r *PostRepoImpl) LockDraftMediasForUpdate(ctx context.Context, postID int64) error {
	_, err := r.q.PostMedia.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where(r.q.PostMedia.PostID.Eq(postID)).
		Find()
	return err
}

// CreatePostMedias 创建帖子与媒体的关联
func (r *PostRepoImpl) CreatePostMedias(ctx context.Context, postID int64, medias []*media.MediaFile) error {
	postMedias := make([]*models.PostMedia, 0, len(medias))
	for i, v := range medias {
		postMedias = append(postMedias, &models.PostMedia{
			PostID:    postID,
			Media:     v,
			SortOrder: int32(i),
		})
	}

	err := r.q.PostMedia.WithContext(ctx).CreateInBatches(postMedias, defaults.CreateBatchSize)
	return err
}

// GetPostCover 获取帖子封面，首个媒体关联
func (r *PostRepoImpl) GetPostCover(ctx context.Context, postIDs []int64) ([]*models.PostMedia, error) {
	return r.q.WithContext(ctx).PostMedia.
		Where(
			r.q.PostMedia.PostID.In(postIDs...),
			r.q.PostMedia.SortOrder.Eq(0),
		).Find()
}

// GetPostMedias 获取帖子下的媒体关联
func (r *PostRepoImpl) GetPostMedias(ctx context.Context, postIDs []int64) ([]*models.PostMedia, error) {
	return r.q.WithContext(ctx).PostMedia.
		Where(r.q.PostMedia.PostID.In(postIDs...)).
		Order(r.q.PostMedia.SortOrder.Asc()).
		Find()
}

// DeletePostMedias 删除帖子下的媒体关联（物理删除），返回被删关联的媒体资产 ID 列表
// （调用方据此逐槽位回收引用计数：删几行减几槽）；先查后删，重复删除返回空列表，
// 计数回收天然幂等。
func (r *PostRepoImpl) DeletePostMedias(ctx context.Context, postID int64) ([]int64, error) {
	rows, err := r.q.PostMedia.WithContext(ctx).
		Where(r.q.PostMedia.PostID.Eq(postID)).
		Find()
	if err != nil {
		return nil, err
	}
	if _, err := r.q.PostMedia.WithContext(ctx).
		Where(r.q.PostMedia.PostID.Eq(postID)).
		Delete(); err != nil {
		return nil, err
	}

	assetIDs := make([]int64, 0, len(rows))
	for _, row := range rows {
		if row.Media != nil {
			assetIDs = append(assetIDs, row.Media.AssetID)
		}
	}
	return assetIDs, nil
}

// ==================== 互动（点赞/收藏明细） ====================

// CreateLike 点赞明细（OnConflict DoNothing 幂等：重复点赞不报错、不新增）；
// 经 gen WithResult 取回 RowsAffected 判定是否实际插入
func (r *PostRepoImpl) CreateLike(ctx context.Context, postID int64, userID int64) (bool, error) {
	var createErr error
	res := r.q.PostLike.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		WithResult(func(tx gen.Dao) {
			createErr = tx.Create(&models.PostLike{PostID: postID, UserID: userID})
		})
	if createErr != nil {
		return false, createErr
	}
	return res.RowsAffected > 0, nil
}

// DeleteLike 删除点赞明细（未赞时 0 行，幂等）
func (r *PostRepoImpl) DeleteLike(ctx context.Context, postID int64, userID int64) (bool, error) {
	info, err := r.q.PostLike.WithContext(ctx).
		Where(r.q.PostLike.PostID.Eq(postID), r.q.PostLike.UserID.Eq(userID)).
		Delete()
	if err != nil {
		return false, err
	}
	return info.RowsAffected > 0, nil
}

// CreateFavourite 收藏明细（幂等；同 CreateLike，WithResult 判定是否实际插入）
func (r *PostRepoImpl) CreateFavourite(ctx context.Context, postID int64, userID int64) (bool, error) {
	var createErr error
	res := r.q.PostFavourite.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		WithResult(func(tx gen.Dao) {
			createErr = tx.Create(&models.PostFavourite{PostID: postID, UserID: userID})
		})
	if createErr != nil {
		return false, createErr
	}
	return res.RowsAffected > 0, nil
}

// DeleteFavourite 删除收藏明细（幂等）
func (r *PostRepoImpl) DeleteFavourite(ctx context.Context, postID int64, userID int64) (bool, error) {
	info, err := r.q.PostFavourite.WithContext(ctx).
		Where(r.q.PostFavourite.PostID.Eq(postID), r.q.PostFavourite.UserID.Eq(userID)).
		Delete()
	if err != nil {
		return false, err
	}
	return info.RowsAffected > 0, nil
}

// CountFavouritesByUser 用户收藏总数（收藏夹容量校验）
func (r *PostRepoImpl) CountFavouritesByUser(ctx context.Context, userID int64) (int64, error) {
	return r.q.PostFavourite.WithContext(ctx).
		Where(r.q.PostFavourite.UserID.Eq(userID)).
		Count()
}

// IncPostLikeCount 点赞数 +1（条件增量；PG 匹配行即计数，RowsAffected=0 即帖子不存在）
func (r *PostRepoImpl) IncPostLikeCount(ctx context.Context, postID int64) (gen.ResultInfo, error) {
	return r.q.PostDetail.WithContext(ctx).
		Where(r.q.PostDetail.ID.Eq(postID)).
		UpdateSimple(r.q.PostDetail.LikeCount.Add(1))
}

// DecPostLikeCount 点赞数 -1（防负条件更新：仅 like_count > 0 才减，等价 GREATEST(…, 0) 语义；
// 计数已 0 时不减但按帖子存在性回报 RowsAffected，保持"匹配行即计数"口径）
func (r *PostRepoImpl) DecPostLikeCount(ctx context.Context, postID int64) (gen.ResultInfo, error) {
	info, err := r.q.PostDetail.WithContext(ctx).
		Where(r.q.PostDetail.ID.Eq(postID), r.q.PostDetail.LikeCount.Gt(0)).
		UpdateSimple(r.q.PostDetail.LikeCount.Sub(1))
	if err != nil || info.RowsAffected > 0 {
		return info, err
	}
	// 0 行两可：帖子不存在（→0）或计数已 0（防负不减，存在 →1）
	n, err := r.q.PostDetail.WithContext(ctx).Where(r.q.PostDetail.ID.Eq(postID)).Count()
	if err != nil {
		return gen.ResultInfo{}, err
	}
	return gen.ResultInfo{RowsAffected: n}, nil
}

// IncPostFavouriteCount 收藏数 +1（条件增量）
func (r *PostRepoImpl) IncPostFavouriteCount(ctx context.Context, postID int64) (gen.ResultInfo, error) {
	return r.q.PostDetail.WithContext(ctx).
		Where(r.q.PostDetail.ID.Eq(postID)).
		UpdateSimple(r.q.PostDetail.FavouriteCount.Add(1))
}

// DecPostFavouriteCount 收藏数 -1（防负条件更新，同 DecPostLikeCount）
func (r *PostRepoImpl) DecPostFavouriteCount(ctx context.Context, postID int64) (gen.ResultInfo, error) {
	info, err := r.q.PostDetail.WithContext(ctx).
		Where(r.q.PostDetail.ID.Eq(postID), r.q.PostDetail.FavouriteCount.Gt(0)).
		UpdateSimple(r.q.PostDetail.FavouriteCount.Sub(1))
	if err != nil || info.RowsAffected > 0 {
		return info, err
	}
	n, err := r.q.PostDetail.WithContext(ctx).Where(r.q.PostDetail.ID.Eq(postID)).Count()
	if err != nil {
		return gen.ResultInfo{}, err
	}
	return gen.ResultInfo{RowsAffected: n}, nil
}

// ListLikedPostIDs 用户点赞的帖子明细（created_at DESC, id DESC 游标翻页）
func (r *PostRepoImpl) ListLikedPostIDs(ctx context.Context, userID int64, before int64, limit int64) ([]*models.PostLike, error) {
	return r.q.PostLike.WithContext(ctx).
		Where(
			r.q.PostLike.UserID.Eq(userID),
			r.q.PostLike.CreatedAt.Lt(before),
		).
		Order(r.q.PostLike.CreatedAt.Desc(), r.q.PostLike.ID.Desc()).
		Limit(int(limit)).
		Find()
}

// ListFavouritePostIDs 用户收藏的帖子明细（同构）
func (r *PostRepoImpl) ListFavouritePostIDs(ctx context.Context, userID int64, before int64, limit int64) ([]*models.PostFavourite, error) {
	return r.q.PostFavourite.WithContext(ctx).
		Where(
			r.q.PostFavourite.UserID.Eq(userID),
			r.q.PostFavourite.CreatedAt.Lt(before),
		).
		Order(r.q.PostFavourite.CreatedAt.Desc(), r.q.PostFavourite.ID.Desc()).
		Limit(int(limit)).
		Find()
}
