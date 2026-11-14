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

// ErrNotImplemented 尚未实现的 RPC 占位错误
var ErrNotImplemented = errors.New("post: not implemented")

// 包内 sentinel 业务分支错误：用 errors.Is 匹配，由各接口映射为可枚举业务码（见 doc/packages/errmsg/err.md）
var (
	// errNotFound 目标帖子不存在
	errNotFound = errors.New("post not found")
	// errFavLimit 收藏夹容量上限（默认 defaults.FavouriteCap，超限回滚新增）
	errFavLimit = errors.New("favourite limit exceeded")
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
	// ViewPostsByIDs 面客批量查看（仅 PUBLISHED，可见性裁决收敛服务端）
	ViewPostsByIDs(ctx context.Context, req *postv1.ViewPostsByIDsRequest) (*postv1.ViewPostsByIDsResponse, error)

	// ==================== 帖子管理 ====================

	// BanPost 封禁帖子
	BanPost(ctx context.Context, req *postv1.BanPostRequest) (*postv1.BanPostResponse, error)

	// DeletePost 删除帖子（用户删除自己的帖子，草稿与已发布均可删，软删；uid 必须为帖子作者）
	DeletePost(ctx context.Context, req *postv1.DeletePostRequest) (*postv1.DeletePostResponse, error)

	// SyncPostStat 同步帖子统计信息（内部管理，异步链路调用；逐字段部分更新：nil 字段不修改，设置则覆盖该统计列（含 0）；仅统计不改状态）
	SyncPostStat(ctx context.Context, req *postv1.SyncPostStatRequest) (*postv1.SyncPostStatResponse, error)

	// ==================== 互动（点赞/收藏） ====================

	// LikePost 点赞（幂等：重复点赞成功且不重复计数；仅 PUBLISHED 可互动，非 PUBLISHED → NotFound）
	LikePost(ctx context.Context, req *postv1.LikePostRequest) (*postv1.LikePostResponse, error)
	// UnlikePost 取消点赞（幂等：未赞时成功且计数不变）
	UnlikePost(ctx context.Context, req *postv1.UnlikePostRequest) (*postv1.UnlikePostResponse, error)
	// FavouritePost 收藏（幂等；新增收藏校验容量上限 FavouriteCap，超限 FavouriteLimitExceeded）
	FavouritePost(ctx context.Context, req *postv1.FavouritePostRequest) (*postv1.FavouritePostResponse, error)
	// UnfavouritePost 取消收藏（幂等）
	UnfavouritePost(ctx context.Context, req *postv1.UnfavouritePostRequest) (*postv1.UnfavouritePostResponse, error)
	// ListLikedPostIDs 最近点赞的帖子 ID（created_at DESC 游标；前端预取判态，空页 = 空列表 + 空游标）
	ListLikedPostIDs(ctx context.Context, req *postv1.ListLikedPostIDsRequest) (*postv1.ListLikedPostIDsResponse, error)
	// ListFavouritePostIDs 收藏的帖子 ID（同构；FavouriteCap 内一页可拉全）
	ListFavouritePostIDs(ctx context.Context, req *postv1.ListFavouritePostIDsRequest) (*postv1.ListFavouritePostIDsResponse, error)

	// ==================== 评论 ====================

	// CreateComment 发表一级评论（同步链路：参数校验 + 内容审核 + 状态校验均在请求内完成，写入即可见；
	// 仅 PUBLISHED 可评论，非 PUBLISHED → NotFound）
	CreateComment(ctx context.Context, req *postv1.CreateCommentRequest) (*postv1.CreateCommentResponse, error)
	// ReplyComment 回复（楼中楼：挂 parent 所在楼层，parent 可以是回复；同步链路同 CreateComment；
	// parent 必须存在、同帖、未删，否则 NotFound）
	ReplyComment(ctx context.Context, req *postv1.ReplyCommentRequest) (*postv1.ReplyCommentResponse, error)
	// DeleteComment 删除评论（软删；评论作者或帖子作者可删，否则 Forbidden；删一级评论级联软删整楼；
	// 幂等：重复删除成功且不重复减计数）
	DeleteComment(ctx context.Context, req *postv1.DeleteCommentRequest) (*postv1.DeleteCommentResponse, error)
	// ListComments 帖子一级评论分页（created_at DESC 游标；每条带楼层 reply_count；游标为空即到底（末页可带数据））
	ListComments(ctx context.Context, req *postv1.ListCommentsRequest) (*postv1.ListCommentsResponse, error)
	// ListReplies 楼内回复分页（root_id = 楼层 id；平铺含回复的回复；空页语义同上）
	ListReplies(ctx context.Context, req *postv1.ListRepliesRequest) (*postv1.ListRepliesResponse, error)
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

// ViewPostsByIDs 面客批量查看（信息流等组装口径）：仅 PUBLISHED 可见，
// 可见性裁决收敛服务端（与 GetPostsByIDs 内部取数（全状态）成对）。
// 逐条结果（不保证与输入同序），key = post_id 定位：fail = 不可见/不存在（NotFound，
// 不暴露状态存在性）/ 非法 id（InvalidID）；调用方按 post_id 对账，不依赖顺序
func (u *PostUsecaseImpl) ViewPostsByIDs(ctx context.Context, req *postv1.ViewPostsByIDsRequest) (*postv1.ViewPostsByIDsResponse, error) {
	ids := make([]int64, 0, len(req.GetPostIds()))
	for _, id := range req.GetPostIds() {
		parsedID, err := snowflake.ParseString(id)
		if err != nil {
			continue
		}
		ids = append(ids, parsedID.Int64())
	}

	posts, err := u.Repo.GetPosts(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("failed to get posts: %w", err)
	}

	// 命中且 PUBLISHED 的出 brief；其余（不存在/非 PUBLISHED）统一 NotFound——不暴露状态存在性
	results := make([]*postv1.PostBriefResult, 0, len(ids))
	visible := make(map[int64]struct{}, len(posts))
	for _, p := range posts {
		if p.Status != postv1.PostStatus_POST_STATUS_PUBLISHED {
			continue
		}
		results = append(results, &postv1.PostBriefResult{
			PostId: snowflake.ParseInt64(p.ID).String(),
			Post:   p.ToBrief(),
		})
		visible[p.ID] = struct{}{}
	}

	// ID 合法但不可见（不存在/非 PUBLISHED）逐条 fail 提示定位
	for _, id := range ids {
		if _, ok := visible[id]; !ok {
			results = append(results, &postv1.PostBriefResult{PostId: snowflake.ParseInt64(id).String(), Fail: true, Err: errmsg.NotFound})
		}
	}
	for _, id := range req.GetPostIds() {
		if _, err := snowflake.ParseString(id); err != nil {
			results = append(results, &postv1.PostBriefResult{PostId: id, Fail: true, Err: errmsg.InvalidID})
		}
	}
	return &postv1.ViewPostsByIDsResponse{Results: results}, nil
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

// ==================== 互动（点赞/收藏） ====================

// assertInteractable 校验帖子可互动：存在且 PUBLISHED，否则 errNotFound（口径：非 PUBLISHED 一律 NotFound，不泄露状态）
func assertInteractable(ctx context.Context, repo PostRepository, postID int64) error {
	ok, status, err := repo.GetPostStatus(ctx, postID)
	if err != nil {
		return err
	}
	if !ok || status != postv1.PostStatus_POST_STATUS_PUBLISHED {
		return errNotFound
	}
	return nil
}

// interactionStat 取帖子最新计数（事务内读自身写入，同事务返回免二次查询）
func interactionStat(ctx context.Context, repo PostRepository, postID int64) (*post.PostStat, error) {
	stats, err := repo.GetPostStat(ctx, []int64{postID})
	if err != nil {
		return nil, err
	}
	if len(stats) == 0 {
		return nil, errNotFound
	}
	return stats[0], nil
}

// auditComment 评论内容同步审核（占位：当前直接放行，对齐 posts 审核占位口径）。
// 口径：评论不走 outbox 异步审核链路，创建/回复请求内同步完成校验 + 审核，写入即可见（无 AUDITING 态）；
// 后续接入内容审核服务时原地替换本函数即可，不改调用方
func auditComment(content string) error {
	// TODO: 接入内容审核服务后在此同步调用，拒绝时返回 errNotAudited 类错误
	return nil
}

// LikePost 点赞（幂等：重复点赞返回成功且不重复计数；明细为准 + 同事务条件计数，强一致）
func (u *PostUsecaseImpl) LikePost(ctx context.Context, req *postv1.LikePostRequest) (*postv1.LikePostResponse, error) {
	uid, err := snowflake.ParseString(req.GetUid())
	if err != nil {
		return &postv1.LikePostResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}
	postID, err := snowflake.ParseString(req.GetPostId())
	if err != nil {
		return &postv1.LikePostResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	var stat *post.PostStat
	err = u.Repo.Transaction(func(repo PostRepository) error {
		if err := assertInteractable(ctx, repo, postID.Int64()); err != nil {
			return err
		}
		inserted, err := repo.CreateLike(ctx, postID.Int64(), uid.Int64())
		if err != nil {
			return err
		}
		// 明细为准：仅实际插入才 +1（OnConflict DoNothing 幂等锚点）
		if inserted {
			info, err := repo.IncPostLikeCount(ctx, postID.Int64())
			if err != nil {
				return err
			}
			if info.RowsAffected == 0 {
				return errNotFound
			}
		}
		stat, err = interactionStat(ctx, repo, postID.Int64())
		return err
	})
	if errors.Is(err, errNotFound) {
		return &postv1.LikePostResponse{Fail: true, Err: errmsg.NotFound}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to like post: %w", err)
	}

	return &postv1.LikePostResponse{LikeCount: stat.LikeCount, Liked: true}, nil
}

// UnlikePost 取消点赞（幂等：未赞时成功且计数不变；GREATEST 防负兑底）
func (u *PostUsecaseImpl) UnlikePost(ctx context.Context, req *postv1.UnlikePostRequest) (*postv1.UnlikePostResponse, error) {
	uid, err := snowflake.ParseString(req.GetUid())
	if err != nil {
		return &postv1.UnlikePostResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}
	postID, err := snowflake.ParseString(req.GetPostId())
	if err != nil {
		return &postv1.UnlikePostResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	var stat *post.PostStat
	err = u.Repo.Transaction(func(repo PostRepository) error {
		if err := assertInteractable(ctx, repo, postID.Int64()); err != nil {
			return err
		}
		deleted, err := repo.DeleteLike(ctx, postID.Int64(), uid.Int64())
		if err != nil {
			return err
		}
		if deleted {
			info, err := repo.DecPostLikeCount(ctx, postID.Int64())
			if err != nil {
				return err
			}
			if info.RowsAffected == 0 {
				return errNotFound
			}
		}
		stat, err = interactionStat(ctx, repo, postID.Int64())
		return err
	})
	if errors.Is(err, errNotFound) {
		return &postv1.UnlikePostResponse{Fail: true, Err: errmsg.NotFound}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to unlike post: %w", err)
	}

	return &postv1.UnlikePostResponse{LikeCount: stat.LikeCount, Liked: false}, nil
}

// FavouritePost 收藏（幂等：已藏返回成功不重复计数；新增收藏同事务校验容量上限，超限整体回滚）
func (u *PostUsecaseImpl) FavouritePost(ctx context.Context, req *postv1.FavouritePostRequest) (*postv1.FavouritePostResponse, error) {
	uid, err := snowflake.ParseString(req.GetUid())
	if err != nil {
		return &postv1.FavouritePostResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}
	postID, err := snowflake.ParseString(req.GetPostId())
	if err != nil {
		return &postv1.FavouritePostResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	var stat *post.PostStat
	err = u.Repo.Transaction(func(repo PostRepository) error {
		if err := assertInteractable(ctx, repo, postID.Int64()); err != nil {
			return err
		}
		inserted, err := repo.CreateFavourite(ctx, postID.Int64(), uid.Int64())
		if err != nil {
			return err
		}
		if !inserted {
			// 幂等命中：已收藏，不占容量、不报满
			stat, err = interactionStat(ctx, repo, postID.Int64())
			return err
		}
		// 新增收藏：容量校验（计数含自身，超上限回滚本事务，插入一并撤销）
		count, err := repo.CountFavouritesByUser(ctx, uid.Int64())
		if err != nil {
			return err
		}
		if count > int64(defaults.FavouriteCap) {
			return errFavLimit
		}
		info, err := repo.IncPostFavouriteCount(ctx, postID.Int64())
		if err != nil {
			return err
		}
		if info.RowsAffected == 0 {
			return errNotFound
		}
		stat, err = interactionStat(ctx, repo, postID.Int64())
		return err
	})
	if errors.Is(err, errFavLimit) {
		return &postv1.FavouritePostResponse{Fail: true, Err: errmsg.FavouriteLimitExceeded}, nil
	}
	if errors.Is(err, errNotFound) {
		return &postv1.FavouritePostResponse{Fail: true, Err: errmsg.NotFound}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to favourite post: %w", err)
	}

	return &postv1.FavouritePostResponse{FavouriteCount: stat.FavouriteCount, Favourited: true}, nil
}

// UnfavouritePost 取消收藏（幂等）
func (u *PostUsecaseImpl) UnfavouritePost(ctx context.Context, req *postv1.UnfavouritePostRequest) (*postv1.UnfavouritePostResponse, error) {
	uid, err := snowflake.ParseString(req.GetUid())
	if err != nil {
		return &postv1.UnfavouritePostResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}
	postID, err := snowflake.ParseString(req.GetPostId())
	if err != nil {
		return &postv1.UnfavouritePostResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	var stat *post.PostStat
	err = u.Repo.Transaction(func(repo PostRepository) error {
		if err := assertInteractable(ctx, repo, postID.Int64()); err != nil {
			return err
		}
		deleted, err := repo.DeleteFavourite(ctx, postID.Int64(), uid.Int64())
		if err != nil {
			return err
		}
		if deleted {
			info, err := repo.DecPostFavouriteCount(ctx, postID.Int64())
			if err != nil {
				return err
			}
			if info.RowsAffected == 0 {
				return errNotFound
			}
		}
		stat, err = interactionStat(ctx, repo, postID.Int64())
		return err
	})
	if errors.Is(err, errNotFound) {
		return &postv1.UnfavouritePostResponse{Fail: true, Err: errmsg.NotFound}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to unfavourite post: %w", err)
	}

	return &postv1.UnfavouritePostResponse{FavouriteCount: stat.FavouriteCount, Favourited: false}, nil
}

// ListLikedPostIDs 最近点赞的帖子 ID（created_at DESC 游标；前端预取判态）
func (u *PostUsecaseImpl) ListLikedPostIDs(ctx context.Context, req *postv1.ListLikedPostIDsRequest) (*postv1.ListLikedPostIDsResponse, error) {
	uid, err := snowflake.ParseString(req.GetUid())
	if err != nil {
		return &postv1.ListLikedPostIDsResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	before, limit := utils.ParseTimeCursor(req.GetCursor())
	rows, err := u.Repo.ListLikedPostIDs(ctx, uid.Int64(), before, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list liked post IDs: %w", err)
	}

	// 空页语义（与 ViewPostByAuthor 一致）：列表为空且 cursor 为空，调用方按空列表终止翻页
	ids := make([]string, 0, len(rows))
	for _, v := range rows {
		ids = append(ids, snowflake.ParseInt64(v.PostID).String())
	}
	var cursor *commonv1.Cursor
	if len(rows) > 0 {
		cursor = utils.NewTimeCursor(rows[len(rows)-1].CreatedAt, limit)
	}

	return &postv1.ListLikedPostIDsResponse{PostIds: ids, Cursor: cursor}, nil
}

// ListFavouritePostIDs 收藏的帖子 ID（同构；FavouriteCap 内一页可拉全）
func (u *PostUsecaseImpl) ListFavouritePostIDs(ctx context.Context, req *postv1.ListFavouritePostIDsRequest) (*postv1.ListFavouritePostIDsResponse, error) {
	uid, err := snowflake.ParseString(req.GetUid())
	if err != nil {
		return &postv1.ListFavouritePostIDsResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	before, limit := utils.ParseTimeCursor(req.GetCursor())
	rows, err := u.Repo.ListFavouritePostIDs(ctx, uid.Int64(), before, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list favourite post IDs: %w", err)
	}

	ids := make([]string, 0, len(rows))
	for _, v := range rows {
		ids = append(ids, snowflake.ParseInt64(v.PostID).String())
	}
	var cursor *commonv1.Cursor
	if len(rows) > 0 {
		cursor = utils.NewTimeCursor(rows[len(rows)-1].CreatedAt, limit)
	}

	return &postv1.ListFavouritePostIDsResponse{PostIds: ids, Cursor: cursor}, nil
}

// ==================== 评论 ====================

// CreateComment 发表一级评论（同步链路：格式校验 + 同步审核 + 状态校验均在请求内完成，写入即可见）；
// 明细为准 + 同事务条件计数（强一致，与点赞同口径）
func (u *PostUsecaseImpl) CreateComment(ctx context.Context, req *postv1.CreateCommentRequest) (*postv1.CreateCommentResponse, error) {
	uid, err := snowflake.ParseString(req.GetUid())
	if err != nil {
		return &postv1.CreateCommentResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}
	postID, err := snowflake.ParseString(req.GetPostId())
	if err != nil {
		return &postv1.CreateCommentResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	// 同步链路：格式校验 + 内容审核（占位）在请求内完成，不走 outbox 异步审核
	if !utils.ValidateComment(req.GetContent()) {
		return &postv1.CreateCommentResponse{Fail: true, Err: errmsg.InvalidComment}, nil
	}
	if err := auditComment(req.GetContent()); err != nil {
		return &postv1.CreateCommentResponse{Fail: true, Err: errmsg.InvalidComment}, nil
	}

	comment := &post.PostComment{
		PostID:  postID.Int64(),
		UserID:  uid.Int64(),
		Content: req.GetContent(),
		Status:  postv1.CommentStatus_COMMENT_STATUS_NORMAL,
	}

	var stat *post.PostStat
	err = u.Repo.Transaction(func(repo PostRepository) error {
		if err := assertInteractable(ctx, repo, postID.Int64()); err != nil {
			return err
		}
		if err := repo.CreateComment(ctx, comment); err != nil {
			return err
		}
		info, err := repo.IncPostCommentCount(ctx, postID.Int64())
		if err != nil {
			return err
		}
		if info.RowsAffected == 0 {
			return errNotFound
		}
		stat, err = interactionStat(ctx, repo, postID.Int64())
		return err
	})
	if errors.Is(err, errNotFound) {
		return &postv1.CreateCommentResponse{Fail: true, Err: errmsg.NotFound}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create comment: %w", err)
	}

	return &postv1.CreateCommentResponse{Comment: comment.ToProto(), CommentCount: stat.CommentCount}, nil
}

// ReplyComment 回复（楼中楼：挂 parent 所在楼层，parent 可以是回复；同步链路同 CreateComment）。
// 楼层行锁串行化与删楼的竞态：锁内断言楼层 NORMAL，防向已删楼插孤儿回复
func (u *PostUsecaseImpl) ReplyComment(ctx context.Context, req *postv1.ReplyCommentRequest) (*postv1.ReplyCommentResponse, error) {
	uid, err := snowflake.ParseString(req.GetUid())
	if err != nil {
		return &postv1.ReplyCommentResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}
	postID, err := snowflake.ParseString(req.GetPostId())
	if err != nil {
		return &postv1.ReplyCommentResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}
	parentID, err := snowflake.ParseString(req.GetParentId())
	if err != nil {
		return &postv1.ReplyCommentResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	if !utils.ValidateComment(req.GetContent()) {
		return &postv1.ReplyCommentResponse{Fail: true, Err: errmsg.InvalidComment}, nil
	}
	if err := auditComment(req.GetContent()); err != nil {
		return &postv1.ReplyCommentResponse{Fail: true, Err: errmsg.InvalidComment}, nil
	}

	comment := &post.PostComment{
		PostID:   postID.Int64(),
		UserID:   uid.Int64(),
		ParentID: parentID.Int64(),
		Content:  req.GetContent(),
		Status:   postv1.CommentStatus_COMMENT_STATUS_NORMAL,
	}

	var stat *post.PostStat
	err = u.Repo.Transaction(func(repo PostRepository) error {
		if err := assertInteractable(ctx, repo, postID.Int64()); err != nil {
			return err
		}
		// parent 非锁定读：存在、同帖、未删（并发被删时回复仍成立，@展示降级，设计口径）
		ok, parent, err := repo.GetComment(ctx, parentID.Int64())
		if err != nil {
			return err
		}
		if !ok || parent.PostID != postID.Int64() || parent.Status != postv1.CommentStatus_COMMENT_STATUS_NORMAL {
			return errNotFound
		}
		rootID := parent.RootID
		if rootID == 0 {
			rootID = parent.ID
		}
		// 楼层行锁（锁序恒为单行）：与删楼（DeleteComment 级联）串行化，锁内断言 NORMAL
		locked, root, err := repo.LockCommentForUpdate(ctx, rootID)
		if err != nil {
			return err
		}
		if !locked || root.Status != postv1.CommentStatus_COMMENT_STATUS_NORMAL {
			return errNotFound
		}

		comment.RootID = rootID
		comment.ReplyToUserID = parent.UserID
		if err := repo.CreateComment(ctx, comment); err != nil {
			return err
		}
		info, err := repo.IncCommentReplyCount(ctx, rootID)
		if err != nil {
			return err
		}
		if info.RowsAffected == 0 {
			return errNotFound
		}
		info, err = repo.IncPostCommentCount(ctx, postID.Int64())
		if err != nil {
			return err
		}
		if info.RowsAffected == 0 {
			return errNotFound
		}
		stat, err = interactionStat(ctx, repo, postID.Int64())
		return err
	})
	if errors.Is(err, errNotFound) {
		return &postv1.ReplyCommentResponse{Fail: true, Err: errmsg.NotFound}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to reply comment: %w", err)
	}

	return &postv1.ReplyCommentResponse{Comment: comment.ToProto(), CommentCount: stat.CommentCount}, nil
}

// DeleteComment 删除评论（软删；评论作者或帖子作者可删；删一级评论级联软删整楼）。
// 计数回收以实际删除行数为准（条件软删 RowsAffected），重复删除 0 行命中即幂等不减
func (u *PostUsecaseImpl) DeleteComment(ctx context.Context, req *postv1.DeleteCommentRequest) (*postv1.DeleteCommentResponse, error) {
	uid, err := snowflake.ParseString(req.GetUid())
	if err != nil {
		return &postv1.DeleteCommentResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}
	commentID, err := snowflake.ParseString(req.GetCommentId())
	if err != nil {
		return &postv1.DeleteCommentResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	var commentCount int64
	err = u.Repo.Transaction(func(repo PostRepository) error {
		ok, target, err := repo.GetComment(ctx, commentID.Int64())
		if err != nil {
			return err
		}
		if !ok {
			return errNotFound
		}
		if target.Status == postv1.CommentStatus_COMMENT_STATUS_DELETED {
			// 幂等：重复删除不重复减计数，回读当前计数
			stat, err := interactionStat(ctx, repo, target.PostID)
			if err != nil {
				return err
			}
			commentCount = stat.CommentCount
			return nil
		}

		// 权限：评论作者或帖子作者（楼主删评），否则 Forbidden
		isAuthor, authorID, err := repo.GetAuthorID(ctx, target.PostID)
		if err != nil {
			return err
		}
		if !isAuthor {
			return errNotFound
		}
		if uid.Int64() != target.UserID && uid.Int64() != authorID {
			return errNotAuthor
		}

		if target.RootID == 0 {
			// 删一级评论：行锁串行化与发回复 → 级联软删整楼，按实际删除行数回收计数
			locked, root, err := repo.LockCommentForUpdate(ctx, target.ID)
			if err != nil {
				return err
			}
			if !locked || root.Status != postv1.CommentStatus_COMMENT_STATUS_NORMAL {
				// 并发已删：幂等成功（锁内重读为准）
				stat, err := interactionStat(ctx, repo, target.PostID)
				if err != nil {
					return err
				}
				commentCount = stat.CommentCount
				return nil
			}
			info, err := repo.SetCommentDeleted(ctx, target.ID)
			if err != nil {
				return err
			}
			total := info.RowsAffected
			info, err = repo.SetCommentsDeletedByRoot(ctx, target.ID)
			if err != nil {
				return err
			}
			total += info.RowsAffected
			info, err = repo.DecPostCommentCount(ctx, target.PostID, total)
			if err != nil {
				return err
			}
			if info.RowsAffected == 0 {
				return errNotFound
			}
		} else {
			// 删回复：条件软删自身，实际删中才回收楼层/帖子计数（并发已删即幂等）
			info, err := repo.SetCommentDeleted(ctx, target.ID)
			if err != nil {
				return err
			}
			if info.RowsAffected > 0 {
				if _, err := repo.DecCommentReplyCount(ctx, target.RootID); err != nil {
					return err
				}
				info, err = repo.DecPostCommentCount(ctx, target.PostID, 1)
				if err != nil {
					return err
				}
				if info.RowsAffected == 0 {
					return errNotFound
				}
			}
		}

		stat, err := interactionStat(ctx, repo, target.PostID)
		if err != nil {
			return err
		}
		commentCount = stat.CommentCount
		return nil
	})
	if errors.Is(err, errNotFound) {
		return &postv1.DeleteCommentResponse{Fail: true, Err: errmsg.NotFound}, nil
	}
	if errors.Is(err, errNotAuthor) {
		return &postv1.DeleteCommentResponse{Fail: true, Err: errmsg.Forbidden}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to delete comment: %w", err)
	}

	return &postv1.DeleteCommentResponse{CommentCount: commentCount, Deleted: true}, nil
}

// ListComments 帖子一级评论分页（created_at DESC 时间游标；状态裁决复用互动口径，非 PUBLISHED 一律 NotFound）
func (u *PostUsecaseImpl) ListComments(ctx context.Context, req *postv1.ListCommentsRequest) (*postv1.ListCommentsResponse, error) {
	postID, err := snowflake.ParseString(req.GetPostId())
	if err != nil {
		return &postv1.ListCommentsResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	if err := assertInteractable(ctx, u.Repo, postID.Int64()); err != nil {
		if errors.Is(err, errNotFound) {
			return &postv1.ListCommentsResponse{Fail: true, Err: errmsg.NotFound}, nil
		}
		return nil, fmt.Errorf("failed to list comments: %w", err)
	}

	before, limit := utils.ParseTimeCursor(req.GetCursor())
	rows, err := u.Repo.ListComments(ctx, postID.Int64(), before, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list comments: %w", err)
	}

	// 游标语义与信息流一致：短取即到底——短页（< limit）带数据回空游标，满页才回 next 游标
	comments := make([]*postv1.Comment, 0, len(rows))
	for _, v := range rows {
		comments = append(comments, v.ToProto())
	}
	var cursor *commonv1.Cursor
	if int64(len(rows)) >= limit {
		cursor = utils.NewTimeCursor(rows[len(rows)-1].CreatedAt, limit)
	}

	return &postv1.ListCommentsResponse{Comments: comments, Cursor: cursor}, nil
}

// ListReplies 楼内回复分页（created_at DESC 时间游标；平铺含回复的回复，带 reply_to_user_id 供 @展示）。
// 楼层不存在/已删 → NotFound（读侧不泄露已删内容），帖子状态裁决同 ListComments
func (u *PostUsecaseImpl) ListReplies(ctx context.Context, req *postv1.ListRepliesRequest) (*postv1.ListRepliesResponse, error) {
	rootID, err := snowflake.ParseString(req.GetRootId())
	if err != nil {
		return &postv1.ListRepliesResponse{Fail: true, Err: errmsg.InvalidID}, nil
	}

	ok, root, err := u.Repo.GetComment(ctx, rootID.Int64())
	if err != nil {
		return nil, fmt.Errorf("failed to list replies: %w", err)
	}
	if !ok || root.RootID != 0 || root.Status != postv1.CommentStatus_COMMENT_STATUS_NORMAL {
		// root_id 必须指向一级评论（楼层）；不存在/已删/传了回复 id 一律 NotFound
		return &postv1.ListRepliesResponse{Fail: true, Err: errmsg.NotFound}, nil
	}
	if err := assertInteractable(ctx, u.Repo, root.PostID); err != nil {
		if errors.Is(err, errNotFound) {
			return &postv1.ListRepliesResponse{Fail: true, Err: errmsg.NotFound}, nil
		}
		return nil, fmt.Errorf("failed to list replies: %w", err)
	}

	before, limit := utils.ParseTimeCursor(req.GetCursor())
	rows, err := u.Repo.ListReplies(ctx, rootID.Int64(), before, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list replies: %w", err)
	}

	// 游标语义与信息流一致：短取即到底（同 ListComments）
	replies := make([]*postv1.Comment, 0, len(rows))
	for _, v := range rows {
		replies = append(replies, v.ToProto())
	}
	var cursor *commonv1.Cursor
	if int64(len(rows)) >= limit {
		cursor = utils.NewTimeCursor(rows[len(rows)-1].CreatedAt, limit)
	}

	return &postv1.ListRepliesResponse{Replies: replies, Cursor: cursor}, nil
}
