package v1

import (
	"context"
	"errors"
	"fmt"

	commonv1 "rednote/gen/go/common/v1"
	postv1 "rednote/gen/go/post/v1"
	media "rednote/internal/media/models"

	post "rednote/internal/post/models"
	"rednote/packages/defaults"
	"rednote/packages/errmsg"
	"rednote/packages/mq"
	"rednote/packages/utils"

	"github.com/bwmarrin/snowflake"
	"gorm.io/datatypes"
)

// ErrNotImplemented 脚手架占位错误，具体实现落地后移除
var ErrNotImplemented = errors.New("post: not implemented")

// 包内 sentinel 业务分支错误：用 errors.Is 匹配，由各接口映射为可枚举业务码（见 doc/packages/errmsg/err.md）
var (
	// errNotFound 目标帖子不存在
	errNotFound = errors.New("post not found")
	// errNotAuthor 操作者非帖子作者
	errNotAuthor = errors.New("user is not the author of the post")
	// errNotEditable 更新目标帖子不可编辑（非 DRAFT）
	errNotEditable = errors.New("post: not editable")
)

// PostUsecase 帖子业务逻辑接口
type PostUsecase interface {
	// ==================== 草稿 ====================

	// CreateDraft 创建草稿（媒体列表必填：至少 1 张，首张即封面）
	CreateDraft(ctx context.Context, req *postv1.CreateDraftRequest) (*postv1.CreateDraftResponse, error)
	// UpdateDraft 更新草稿（文本字段部分更新：仅更新传入的字段，nil 字段不修改；仅 DRAFT 状态可更新）
	// title/full_text/metadata 为 nil 表示不修改；媒体更新见 UpdateDraftMedias
	UpdateDraft(ctx context.Context, req *postv1.UpdateDraftRequest) (*postv1.UpdateDraftResponse, error)
	// UpdateDraftMedias 更新草稿媒体（全量替换：至少 1 张，不支持清空，首张即封面；仅 DRAFT 状态可更新）
	UpdateDraftMedias(ctx context.Context, req *postv1.UpdateDraftMediasRequest) (*postv1.UpdateDraftMediasResponse, error)
	// PublishDraft 发布草稿（要求草稿内所有图片审核通过——占位：该校验暂未实现；成功后状态 DRAFT→AUDITING，异步审核通过后→PUBLISHED）
	PublishDraft(ctx context.Context, req *postv1.PublishDraftRequest) (*postv1.PublishDraftResponse, error)

	// ==================== 帖子查询 ====================

	// GetPostsByIDs 按ID批量获取帖子
	GetPostsByIDs(ctx context.Context, req *postv1.GetPostsByIDsRequest) (*postv1.GetPostsByIDsResponse, error)
	// ViewPostByAuthor 按作者游标分页获取帖子列表（空页返回空列表 + 空游标，不以 NotFound 表达翻页到底）
	ViewPostByAuthor(ctx context.Context, req *postv1.ViewPostByAuthorRequest) (*postv1.ViewPostByAuthorResponse, error)
	// GetPostStatus 获取帖子状态
	GetPostStatus(ctx context.Context, req *postv1.GetPostStatusRequest) (*postv1.GetPostStatusResponse, error)
	// ViewPost 查看帖子
	ViewPost(ctx context.Context, req *postv1.ViewPostRequest) (*postv1.ViewPostResponse, error)

	// ==================== 帖子管理 ====================

	// BanPost 封禁帖子
	BanPost(ctx context.Context, req *postv1.BanPostRequest) (*postv1.BanPostResponse, error)

	// DeletePost 删除帖子（用户删除自己的帖子，草稿与已发布均可删，软删；uid 必须为帖子作者）
	DeletePost(ctx context.Context, req *postv1.DeletePostRequest) (*postv1.DeletePostResponse, error)

	// SyncPostStat 同步帖子统计信息（内部管理，异步链路调用；逐字段部分更新：nil 字段不修改，设置则覆盖该统计列（含 0）；仅统计不改状态）
	SyncPostStat(ctx context.Context, req *postv1.SyncPostStatRequest) (*postv1.SyncPostStatResponse, error)
}

// PostUsecaseImpl 帖子业务逻辑实现
type PostUsecaseImpl struct {
	Repo PostRepository
	MQ   mq.MQClient
}

var _ PostUsecase = (*PostUsecaseImpl)(nil)

// NewPostUsecase 创建 PostUsecase 实例
func NewPostUsecase(repo PostRepository, mqClient mq.MQClient) *PostUsecaseImpl {
	return &PostUsecaseImpl{
		Repo: repo,
		MQ:   mqClient,
	}
}

// ==================== 草稿 ====================

// CreateDraft 创建草稿（媒体列表必填：所有帖子至少有一个封面图，首张即封面）
func (u *PostUsecaseImpl) CreateDraft(ctx context.Context, req *postv1.CreateDraftRequest) (*postv1.CreateDraftResponse, error) {

	authorID, err := snowflake.ParseString(req.GetUid())
	if err != nil {
		return &postv1.CreateDraftResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}
	if !utils.ValidateTitle(req.GetTitle()) {
		return &postv1.CreateDraftResponse{Fail: true, Err: errmsg.InvalidTitle}, nil
	}
	if !utils.ValidateContent(req.GetFullText()) {
		return &postv1.CreateDraftResponse{Fail: true, Err: errmsg.InvalidContent}, nil
	}

	// metadata 可选：空值等价于 {}（空对象），仅非法 JSON 才拒绝
	metadata, err := utils.UnmarshalJSON([]byte(req.GetMetadata().GetPayload()))
	if err != nil {
		return &postv1.CreateDraftResponse{Fail: true, Err: errmsg.InvalidArgument}, nil
	}

	// 媒体列表必填：所有帖子至少有一个封面图（首张即封面）
	if len(req.GetMedias()) == 0 {
		return &postv1.CreateDraftResponse{Fail: true, Err: errmsg.InvalidMedia}, nil
	}

	medias := make([]*media.MediaFile, 0, len(req.GetMedias()))
	for _, v := range req.GetMedias() {
		if !utils.ValidateMedia(v) {
			return &postv1.CreateDraftResponse{Fail: true, Err: errmsg.InvalidMedia}, nil
		}
		file, err := media.MediaFileFromProto(v)
		if err != nil {
			return &postv1.CreateDraftResponse{Fail: true, Err: errmsg.InvalidMedia}, nil
		}
		medias = append(medias, &file)
	}

	postDetail := &post.PostDetail{
		AuthorID: authorID.Int64(),
		Title:    req.GetTitle(),
		FullText: req.GetFullText(),

		Status: postv1.PostStatus_POST_STATUS_DRAFT,

		MetaData: metadata,
	}

	err = u.Repo.Transaction(func(repo PostRepository) error {
		err := repo.CreatePost(ctx, postDetail)
		if err != nil {
			return err
		}

		err = repo.CreatePostMedias(ctx, postDetail.ID, medias)
		if err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create draft: %w", err)
	}

	return &postv1.CreateDraftResponse{Draft: post.BuildPost(postDetail, medias)}, nil
}

// UpdateDraft 更新草稿（文本字段部分更新：仅更新传入的字段，nil 字段不修改；仅 DRAFT 状态可更新）
// 可空字段语义：title/full_text/metadata 为 nil 表示不修改；媒体更新见 UpdateDraftMedias
func (u *PostUsecaseImpl) UpdateDraft(ctx context.Context, req *postv1.UpdateDraftRequest) (*postv1.UpdateDraftResponse, error) {
	// 可空字段指示更新范围：nil 表示不修改
	updateTitle := req.GetTitle() != nil
	updateText := req.GetFullText() != nil
	updateMetadata := req.GetMetadata() != nil

	if !updateTitle && !updateText && !updateMetadata {
		return &postv1.UpdateDraftResponse{Fail: true, Err: errmsg.InvalidArgument}, nil
	}

	uid, err := snowflake.ParseString(req.GetUid())
	if err != nil {
		return &postv1.UpdateDraftResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}
	postID, err := snowflake.ParseString(req.GetPostId())
	if err != nil {
		return &postv1.UpdateDraftResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	if updateTitle && !utils.ValidateTitle(req.GetTitle().GetValue()) {
		return &postv1.UpdateDraftResponse{Fail: true, Err: errmsg.InvalidTitle}, nil
	}
	if updateText && !utils.ValidateContent(req.GetFullText().GetValue()) {
		return &postv1.UpdateDraftResponse{Fail: true, Err: errmsg.InvalidContent}, nil
	}

	var metadata datatypes.JSONMap
	if updateMetadata {
		// metadata 可选：空值等价于 {}（空对象），仅非法 JSON 才拒绝
		metadata, err = utils.UnmarshalJSON([]byte(req.GetMetadata().GetPayload()))
		if err != nil {
			return &postv1.UpdateDraftResponse{Fail: true, Err: errmsg.InvalidArgument}, nil
		}
	}

	var updated *post.PostDetail
	err = u.Repo.Transaction(func(repo PostRepository) error {
		// 锁定草稿行：SELECT ... FOR UPDATE 原子断言“存在且仍为 DRAFT”并持有行锁，
		// 避免与 PublishDraft/DeletePost 的并发状态流转竞态；不可编辑统一返回 ErrNotEditable
		ok, authorID, err := repo.GetAuthorID(ctx, postID.Int64())
		if err != nil {
			return err
		}
		if !ok {
			return errNotFound
		}

		if authorID != uid.Int64() {
			return errNotAuthor
		}

		ok, err = repo.LockDraftForUpdate(ctx, postID.Int64())
		if err != nil {
			return err
		}
		if !ok {
			return errNotEditable
		}

		if updateTitle {
			if _, err := repo.UpdateDraftTitle(ctx, postID.Int64(), req.GetTitle().GetValue()); err != nil {
				return err
			}
		}

		if updateText {
			if _, err := repo.UpdateDraftText(ctx, postID.Int64(), req.GetFullText().GetValue()); err != nil {
				return err
			}
		}

		if updateMetadata {
			if _, err := repo.UpdateDraftMetaData(ctx, postID.Int64(), metadata); err != nil {
				return err
			}
		}

		// 同事务回读完整帖子（含未在本接口修改的统计与状态，medias 按展示顺序）
		posts, err := repo.GetPosts(ctx, []int64{postID.Int64()})
		if err != nil {
			return err
		}
		if len(posts) == 0 {
			return fmt.Errorf("failed to get updated post: post %d not found after update", postID.Int64())
		}
		updated = posts[0]

		return nil
	})
	if errors.Is(err, errNotAuthor) {
		return &postv1.UpdateDraftResponse{Fail: true, Err: errmsg.Forbidden}, nil
	}
	if errors.Is(err, errNotFound) {
		return &postv1.UpdateDraftResponse{Fail: true, Err: errmsg.NotFound}, nil
	}
	if errors.Is(err, errNotEditable) {
		return &postv1.UpdateDraftResponse{Fail: true, Err: errmsg.NotEditable}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to update draft: %w", err)
	}

	return &postv1.UpdateDraftResponse{Post: updated.ToProto()}, nil
}

// UpdateDraftMedias 更新草稿媒体（全量替换：至少 1 张，不支持清空，首张即封面；仅 DRAFT 状态可更新）
// 返回的 old_asset_ids 为被替换的旧媒体所属资产 ID，由 BFF 据此调整旧资产引用计数；首次设置媒体时为空
func (u *PostUsecaseImpl) UpdateDraftMedias(ctx context.Context, req *postv1.UpdateDraftMediasRequest) (*postv1.UpdateDraftMediasResponse, error) {
	uid, err := snowflake.ParseString(req.GetUid())
	if err != nil {
		return &postv1.UpdateDraftMediasResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}
	postID, err := snowflake.ParseString(req.GetPostId())
	if err != nil {
		return &postv1.UpdateDraftMediasResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	// 媒体列表必填：所有帖子至少有一个封面图（首张即封面），不支持清空
	if len(req.GetMedias()) == 0 {
		return &postv1.UpdateDraftMediasResponse{Fail: true, Err: errmsg.InvalidMedia}, nil
	}

	medias := make([]*media.MediaFile, 0, len(req.GetMedias()))
	for _, v := range req.GetMedias() {
		if !utils.ValidateMedia(v) {
			return &postv1.UpdateDraftMediasResponse{Fail: true, Err: errmsg.InvalidMedia}, nil
		}
		file, err := media.MediaFileFromProto(v)
		if err != nil {
			return &postv1.UpdateDraftMediasResponse{Fail: true, Err: errmsg.InvalidMedia}, nil
		}
		medias = append(medias, &file)
	}

	var updated *post.PostDetail
	var oldMedias []*post.PostMedia
	err = u.Repo.Transaction(func(repo PostRepository) error {
		// 锁定草稿行：SELECT ... FOR UPDATE 原子断言“存在且仍为 DRAFT”并持有行锁，
		// 避免与 PublishDraft/DeletePost 的并发状态流转竞态；不可编辑统一返回 ErrNotEditable
		ok, authorID, err := repo.GetAuthorID(ctx, postID.Int64())
		if err != nil {
			return err
		}
		if !ok {
			return errNotFound
		}

		if authorID != uid.Int64() {
			return errNotAuthor
		}

		ok, err = repo.LockDraftForUpdate(ctx, postID.Int64())
		if err != nil {
			return err
		}
		if !ok {
			return errNotEditable
		}

		// 锁定现有媒体关联行后全量替换，串行化并发替换
		if err := repo.LockDraftMediasForUpdate(ctx, postID.Int64()); err != nil {
			return err
		}

		old, err := repo.GetPostMedias(ctx, []int64{postID.Int64()})
		if err != nil {
			return err
		}
		oldMedias = old

		if _, err := repo.DeletePostMedias(ctx, postID.Int64()); err != nil {
			return err
		}

		if err := repo.CreatePostMedias(ctx, postID.Int64(), medias); err != nil {
			return err
		}

		// 同事务回读完整帖子（含未在本接口修改的统计与状态，medias 按展示顺序）
		posts, err := repo.GetPosts(ctx, []int64{postID.Int64()})
		if err != nil {
			return err
		}
		if len(posts) == 0 {
			return fmt.Errorf("failed to get updated post: post %d not found after update", postID.Int64())
		}
		updated = posts[0]

		return nil
	})
	if errors.Is(err, errNotEditable) {
		return &postv1.UpdateDraftMediasResponse{Fail: true, Err: errmsg.NotEditable}, nil
	}
	if errors.Is(err, errNotAuthor) {
		return &postv1.UpdateDraftMediasResponse{Fail: true, Err: errmsg.Forbidden}, nil
	}
	if errors.Is(err, errNotFound) {
		return &postv1.UpdateDraftMediasResponse{Fail: true, Err: errmsg.NotFound}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to update draft medias: %w", err)
	}

	// 被替换的旧媒体所属资产 ID（与 UpdateUserAvatar.old_asset_id 语义一致）
	oldAssetIDs := make([]string, 0, len(oldMedias))
	for _, m := range oldMedias {
		if m.Media == nil {
			continue
		}
		oldAssetIDs = append(oldAssetIDs, snowflake.ParseInt64(m.Media.AssetID).String())
	}

	return &postv1.UpdateDraftMediasResponse{Post: updated.ToProto(), OldAssetIds: oldAssetIDs}, nil
}

// PublishDraft 发布草稿
func (u *PostUsecaseImpl) PublishDraft(ctx context.Context, req *postv1.PublishDraftRequest) (*postv1.PublishDraftResponse, error) {
	uid, err := snowflake.ParseString(req.GetUid())
	if err != nil {
		return &postv1.PublishDraftResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	postID, err := snowflake.ParseString(req.GetPostId())
	if err != nil {
		return &postv1.PublishDraftResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	ok, authorID, err := u.Repo.GetAuthorID(ctx, postID.Int64())
	if err != nil {
		return nil, fmt.Errorf("failed to get author ID when publish draft: %w", err)
	}
	if !ok {
		return &postv1.PublishDraftResponse{Fail: true, Err: errmsg.NotFound}, nil
	}
	if !(uid.Int64() == authorID) {
		return &postv1.PublishDraftResponse{Fail: true, Err: errmsg.Forbidden}, nil
	}

	auditMsg := &postv1.PostMessage{
		Payload: &postv1.PostMessage_AuditPost{
			AuditPost: &postv1.AuditPost{
				PostId:   req.GetPostId(),
				AuthorId: req.GetUid(),
			},
		},
	}

	err = u.Repo.Transaction(func(repo PostRepository) error {
		ok, status, err := repo.GetPostStatus(ctx, postID.Int64())
		if err != nil {
			return err
		}
		if !ok {
			return errNotFound
		}
		if status != postv1.PostStatus_POST_STATUS_DRAFT {
			return errNotEditable
		}

		// 条件更新（WHERE status=DRAFT）是原子守卫：预检 GetPostStatus 无锁，
		// 与并发的 PublishDraft/DeletePost/BanPost 竞态后状态可能已变；
		// RowsAffected=0 即状态已流转，绝不能继续发审核消息（否则重复审核/审核已删已封帖）
		info, err := repo.SetPostStatus(ctx, postID.Int64(),
			postv1.PostStatus_POST_STATUS_DRAFT,
			postv1.PostStatus_POST_STATUS_AUDITING,
		)
		if err != nil {
			return err
		}
		if info.RowsAffected == 0 {
			return errNotEditable
		}

		mqMsg, err := mq.NewMQMessage(defaults.PostMessageStream, nil, auditMsg)
		if err != nil {
			return err
		}

		err = u.MQ.SendTX(ctx, mqMsg, repo.DB())
		if err != nil {
			return err
		}

		return nil
	})
	if errors.Is(err, errNotFound) {
		return &postv1.PublishDraftResponse{Fail: true, Err: errmsg.NotFound}, nil
	}
	if errors.Is(err, errNotEditable) {
		return &postv1.PublishDraftResponse{Fail: true, Err: errmsg.NotEditable}, nil
	}

	if err != nil {
		return nil, fmt.Errorf("failed to publish draft: %w", err)
	}

	return &postv1.PublishDraftResponse{}, nil
}

// ==================== 帖子查询 ====================

// GetPostsByIDs 按ID批量获取帖子
func (u *PostUsecaseImpl) GetPostsByIDs(ctx context.Context, req *postv1.GetPostsByIDsRequest) (*postv1.GetPostsByIDsResponse, error) {
	ids := make([]int64, 0, len(req.GetPostIds()))
	results := make([]*postv1.PostResult, 0)
	for _, id := range req.GetPostIds() {
		parsedID, err := snowflake.ParseString(id)
		if err != nil {
			results = append(results, &postv1.PostResult{Fail: true, Err: errmsg.InvalidID, PostId: id})
			continue
		}
		ids = append(ids, parsedID.Int64())
	}

	posts, err := u.Repo.GetPosts(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("failed to get posts: %w", err)
	}

	found := make(map[int64]struct{}, len(posts))
	for _, post := range posts {
		results = append(results, &postv1.PostResult{PostId: snowflake.ParseInt64(post.ID).String(), Post: post.ToProto()})
		found[post.ID] = struct{}{}
	}

	// 逐条语义（PostResult 逐项 fail/err）：ID 合法但未命中的帖子逐条返回 NotFound，
	// 不静默丢弃，调用方按结果里的 post_id 对账；重复请求同一 id 也逐条出结果
	for _, id := range ids {
		if _, ok := found[id]; ok {
			continue
		}
		results = append(results, &postv1.PostResult{Fail: true, Err: errmsg.NotFound, PostId: snowflake.ParseInt64(id).String()})
	}

	return &postv1.GetPostsByIDsResponse{Results: results}, nil
}

// ViewPostByAuthor 按作者游标分页获取帖子列表
func (u *PostUsecaseImpl) ViewPostByAuthor(ctx context.Context, req *postv1.ViewPostByAuthorRequest) (*postv1.ViewPostByAuthorResponse, error) {
	viewerID, err := snowflake.ParseString(req.GetViewerId())
	if err != nil {
		return &postv1.ViewPostByAuthorResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	authorID, err := snowflake.ParseString(req.GetAuthorId())
	if err != nil {
		return &postv1.ViewPostByAuthorResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	before, limit := utils.ParseTimeCursor(req.GetCursor())

	var posts []*post.PostDetail
	if viewerID == authorID {
		posts, err = u.Repo.GetPostBriefsByAuthor(ctx, authorID.Int64(), before, limit)
	} else {
		posts, err = u.Repo.GetPublishedPostBriefsByAuthor(ctx, authorID.Int64(), before, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get post briefs: %w", err)
	}

	// 空页语义（与 user.GetFollowings 一致）：翻页到底/暂无内容返回空列表 + 空游标，
	// 不再以 NotFound 表达空页；调用方按 posts 为空终止翻页
	var cursor *commonv1.Cursor
	if len(posts) > 0 {
		cursor = utils.NewTimeCursor(posts[len(posts)-1].CreatedAt, limit)
	}
	briefs := make([]*postv1.PostBrief, 0, len(posts))
	for _, v := range posts {
		briefs = append(briefs, v.ToBrief())
	}

	return &postv1.ViewPostByAuthorResponse{Posts: briefs, Cursor: cursor}, nil
}

func (u *PostUsecaseImpl) ViewPost(ctx context.Context, req *postv1.ViewPostRequest) (*postv1.ViewPostResponse, error) {
	viewerID, err := snowflake.ParseString(req.GetViewerId())
	if err != nil {
		return &postv1.ViewPostResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}
	postID, err := snowflake.ParseString(req.GetPostId())
	if err != nil {
		return &postv1.ViewPostResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	posts, err := u.Repo.GetPosts(ctx, []int64{postID.Int64()})
	if err != nil {
		return nil, fmt.Errorf("failed to get post: %w", err)
	}

	if len(posts) == 0 {
		return &postv1.ViewPostResponse{Fail: true, Err: errmsg.NotFound}, nil
	}
	post := posts[0]

	switch post.Status {
	case postv1.PostStatus_POST_STATUS_PUBLISHED:
		return &postv1.ViewPostResponse{Post: post.ToProto()}, nil

	case postv1.PostStatus_POST_STATUS_AUDITING,
		postv1.PostStatus_POST_STATUS_DRAFT:
		if !(post.AuthorID == viewerID.Int64()) {
			return &postv1.ViewPostResponse{Fail: true, Err: errmsg.Forbidden}, nil
		}
		return &postv1.ViewPostResponse{Post: post.ToProto()}, nil

	// 封禁帖子对外显式返回 Banned，不再落到 default 兜底报 InternalError
	case postv1.PostStatus_POST_STATUS_BANNED:
		return &postv1.ViewPostResponse{Fail: true, Err: errmsg.Banned}, nil

	case postv1.PostStatus_POST_STATUS_DELETED:
		return &postv1.ViewPostResponse{Fail: true, Err: errmsg.NotFound}, nil

	default:
		return &postv1.ViewPostResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
}

func (u *PostUsecaseImpl) GetPostStatus(ctx context.Context, req *postv1.GetPostStatusRequest) (*postv1.GetPostStatusResponse, error) {

	postID, err := snowflake.ParseString(req.GetPostId())
	if err != nil {
		return &postv1.GetPostStatusResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}
	ok, status, err := u.Repo.GetPostStatus(ctx, postID.Int64())
	if err != nil {
		return nil, fmt.Errorf("failed to get post status: %w", err)
	}
	if !ok {
		return &postv1.GetPostStatusResponse{Fail: true, Err: errmsg.NotFound}, nil
	}

	return &postv1.GetPostStatusResponse{Status: status}, nil
}

// ==================== 帖子管理 ====================

// BanPost 封禁帖子（管理侧操作，请求无 uid，不做调用者身份校验；软封禁：状态→POST_STATUS_BANNED）
// 参照 DeletePost：单事务内查作者 + 状态流转 + outbox 写 BanPost 消息，保证状态与消息一致
func (u *PostUsecaseImpl) BanPost(ctx context.Context, req *postv1.BanPostRequest) (*postv1.BanPostResponse, error) {
	postID, err := snowflake.ParseString(req.GetPostId())
	if err != nil {
		return &postv1.BanPostResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	err = u.Repo.Transaction(func(repo PostRepository) error {
		ok, authorID, err := repo.GetAuthorID(ctx, postID.Int64())
		if err != nil {
			return fmt.Errorf("failed to get author ID: %w", err)
		}
		if !ok {
			return errNotFound
		}

		// 消息里的 author_id 只能来自库（请求不带 uid），须在事务内查到后填充
		banPost := &postv1.PostMessage{
			Payload: &postv1.PostMessage_BanPost{
				BanPost: &postv1.BanPost{
					PostId:   req.GetPostId(),
					AuthorId: snowflake.ParseInt64(authorID).String(),
				},
			},
		}

		info, err := repo.SetPostBanned(ctx, postID.Int64())
		if err != nil {
			return err
		}
		if info.RowsAffected == 0 {
			return nil
		}

		msg, err := mq.NewMQMessage(defaults.PostMessageStream, nil, banPost)
		if err != nil {
			return err
		}

		return u.MQ.SendTX(ctx, msg, repo.DB())
	})
	if errors.Is(err, errNotFound) {
		return &postv1.BanPostResponse{Fail: true, Err: errmsg.NotFound}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to ban post: %w", err)
	}

	return &postv1.BanPostResponse{}, nil
}

// DeletePost 删除帖子（用户删除自己的帖子，草稿与已发布均可删，软删）
func (u *PostUsecaseImpl) DeletePost(ctx context.Context, req *postv1.DeletePostRequest) (*postv1.DeletePostResponse, error) {
	uid, err := snowflake.ParseString(req.GetUid())
	if err != nil {
		return &postv1.DeletePostResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	postID, err := snowflake.ParseString(req.GetPostId())
	if err != nil {
		return &postv1.DeletePostResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	deletePost := &postv1.PostMessage{
		Payload: &postv1.PostMessage_DeletePost{
			DeletePost: &postv1.DeletePost{
				PostId:   req.GetPostId(),
				AuthorId: req.GetUid(),
			},
		},
	}

	err = u.Repo.Transaction(func(repo PostRepository) error {
		ok, authorID, err := repo.GetAuthorID(ctx, postID.Int64())
		if err != nil {
			return fmt.Errorf("failed to get author ID: %w", err)
		}

		if !ok {
			return errNotFound
		}

		if authorID != uid.Int64() {
			return errNotAuthor
		}

		// 必须用事务句柄 repo（而非 u.Repo）：软删与 outbox 消息同事务提交/回滚，
		// 避免事务回滚时删除不回滚导致状态与消息不一致
		info, err := repo.SetPostDeleted(ctx, postID.Int64())
		if err != nil {
			return err
		}
		// 已删除的帖子（RowsAffected=0）幂等成功，不重复投递 DeletePost 消息
		if info.RowsAffected == 0 {
			return nil
		}
		msg, err := mq.NewMQMessage(defaults.PostMessageStream, nil, deletePost)
		if err != nil {
			return err
		}

		err = u.MQ.SendTX(ctx, msg, repo.DB())
		if err != nil {
			return err
		}
		return nil
	})
	if errors.Is(err, errNotFound) {
		return &postv1.DeletePostResponse{Fail: true, Err: errmsg.NotFound}, nil
	}
	if errors.Is(err, errNotAuthor) {
		return &postv1.DeletePostResponse{Fail: true, Err: errmsg.Forbidden}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to delete post: %w", err)
	}

	return &postv1.DeletePostResponse{}, nil
}

// SyncPostStat 同步帖子统计信息（内部管理，异步链路调用）
// 逐字段部分更新（参考 UpdateUserProfile）：nil 字段不修改，设置则覆盖该统计列（含 0）；
// 仅统计字段，不改帖子状态
func (u *PostUsecaseImpl) SyncPostStat(ctx context.Context, req *postv1.SyncPostStatRequest) (*postv1.SyncPostStatResponse, error) {
	// 可空字段指示更新范围：nil 表示不修改
	updateLike := req.GetLikeCount() != nil
	updateFavourite := req.GetFavouriteCount() != nil
	updateComment := req.GetCommentCount() != nil
	updateView := req.GetViewCount() != nil

	if !updateLike && !updateFavourite && !updateComment && !updateView {
		return &postv1.SyncPostStatResponse{Fail: true, Err: errmsg.InvalidArgument}, nil
	}

	postID, err := snowflake.ParseString(req.GetPostId())
	if err != nil {
		return &postv1.SyncPostStatResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	if updateLike && !utils.ValidatePostStatCount(req.GetLikeCount().GetValue()) {
		return &postv1.SyncPostStatResponse{Fail: true, Err: errmsg.InvalidArgument}, nil
	}
	if updateFavourite && !utils.ValidatePostStatCount(req.GetFavouriteCount().GetValue()) {
		return &postv1.SyncPostStatResponse{Fail: true, Err: errmsg.InvalidArgument}, nil
	}
	if updateComment && !utils.ValidatePostStatCount(req.GetCommentCount().GetValue()) {
		return &postv1.SyncPostStatResponse{Fail: true, Err: errmsg.InvalidArgument}, nil
	}
	if updateView && !utils.ValidatePostStatCount(req.GetViewCount().GetValue()) {
		return &postv1.SyncPostStatResponse{Fail: true, Err: errmsg.InvalidArgument}, nil
	}

	// 单事务内逐字段更新，保证部分更新的原子性；
	// PG 中存在的行即使值不变 UPDATE 也计 1 行，故任一更新 0 行即视为帖子不存在，整体回滚
	err = u.Repo.Transaction(func(repo PostRepository) error {
		if updateLike {
			info, err := repo.UpdatePostLikeCount(ctx, postID.Int64(), req.GetLikeCount().GetValue())
			if err != nil {
				return err
			}
			if info.RowsAffected == 0 {
				return errNotFound
			}
		}

		if updateFavourite {
			info, err := repo.UpdatePostFavouriteCount(ctx, postID.Int64(), req.GetFavouriteCount().GetValue())
			if err != nil {
				return err
			}
			if info.RowsAffected == 0 {
				return errNotFound
			}
		}

		if updateComment {
			info, err := repo.UpdatePostCommentCount(ctx, postID.Int64(), req.GetCommentCount().GetValue())
			if err != nil {
				return err
			}
			if info.RowsAffected == 0 {
				return errNotFound
			}
		}

		if updateView {
			info, err := repo.UpdatePostViewCount(ctx, postID.Int64(), req.GetViewCount().GetValue())
			if err != nil {
				return err
			}
			if info.RowsAffected == 0 {
				return errNotFound
			}
		}

		return nil
	})
	if errors.Is(err, errNotFound) {
		return &postv1.SyncPostStatResponse{Fail: true, Err: errmsg.NotFound}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to sync post stat: %w", err)
	}

	return &postv1.SyncPostStatResponse{}, nil
}
