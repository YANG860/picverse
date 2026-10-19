package v1

import (
	"context"
	"errors"
	"testing"
	"time"

	mediav1 "rednote/gen/go/media/v1"
	postv1 "rednote/gen/go/post/v1"
	common "rednote/internal/common/models"
	media "rednote/internal/media/models"
	models "rednote/internal/post/models"
	"rednote/test/testdb"
	"rednote/test/testsnowflake"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// 固定测试 ID，避免依赖雪花生成结果，便于断言
const (
	authorID1 int64 = 3001
	authorID2 int64 = 3002

	postID1 int64 = 2001
	postID2 int64 = 2002
	postID3 int64 = 2003
	postID4 int64 = 2004
)

func newRepo(t *testing.T) (*PostRepoImpl, *gorm.DB) {
	t.Helper()
	testsnowflake.InitTestSnoflake(4) // 与 media(1)/user(3) 等测试错开 worker ID
	db := testdb.Setup(t)
	return NewPostRepo(db), db
}

// newMediaFile 构造完整媒体文件（post_medias 嵌入列的来源）
func newMediaFile(id, assetID int64) *media.MediaFile {
	return &media.MediaFile{
		BaseModel:     common.BaseModel{ID: id},
		AssetID:       assetID,
		Type:          mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN,
		ObjKey:        "post/obj",
		Bucket:        "rednote",
		Sha256Sum:     "abc",
		SizeBytes:     1024,
		Format:        mediav1.MediaFormat_MEDIA_FORMAT_WEBP,
		Height:        100,
		Width:         200,
		MediaMetaData: datatypes.JSONMap{"k": "v"},
	}
}

// seedPost 造一条全字段帖子（统计 1/2/3/4 便于全字段断言）
func seedPost(t *testing.T, db *gorm.DB, id, authorID int64, status postv1.PostStatus, createdAt int64) *models.PostDetail {
	t.Helper()
	p := &models.PostDetail{
		BaseModel:      common.BaseModel{ID: id, CreatedAt: createdAt, UpdatedAt: createdAt},
		AuthorID:       authorID,
		Title:          "标题",
		FullText:       "正文",
		Status:         status,
		LikeCount:      1,
		FavouriteCount: 2,
		CommentCount:   3,
		ViewCount:      4,
		MetaData:       datatypes.JSONMap{"k": "v"},
	}
	require.NoError(t, db.Create(p).Error)
	return p
}

// seedPostMedia 造一条帖子媒体关联（嵌入媒体列 + 展示顺序）
func seedPostMedia(t *testing.T, db *gorm.DB, rowID, postID, mediaID, assetID int64, sortOrder int32) *models.PostMedia {
	t.Helper()
	pm := &models.PostMedia{
		BaseModel: common.BaseModel{ID: rowID},
		PostID:    postID,
		Media:     newMediaFile(mediaID, assetID),
		SortOrder: sortOrder,
	}
	require.NoError(t, db.Create(pm).Error)
	return pm
}

// ---------- CreatePost ----------

func TestCreatePost(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	post := &models.PostDetail{
		AuthorID: authorID1,
		Title:    "新标题",
		FullText: "新正文",
		Status:   postv1.PostStatus_POST_STATUS_DRAFT,
		MetaData: datatypes.JSONMap{"theme": "dark"},
	}
	require.NoError(t, repo.CreatePost(ctx, post))

	// BeforeCreate 钩子应填充雪花 ID 与毫秒时间戳
	assert.NotZero(t, post.ID, "创建后应自动填充雪花 ID")
	assert.NotZero(t, post.CreatedAt, "创建后应自动填充 created_at")

	// DB：全字段落库（统计默认 0）
	var got models.PostDetail
	require.NoError(t, db.Where("id = ?", post.ID).First(&got).Error)
	assert.Equal(t, authorID1, got.AuthorID)
	assert.Equal(t, "新标题", got.Title)
	assert.Equal(t, "新正文", got.FullText)
	assert.Equal(t, postv1.PostStatus_POST_STATUS_DRAFT, got.Status)
	assert.Zero(t, got.LikeCount)
	assert.Zero(t, got.FavouriteCount)
	assert.Zero(t, got.CommentCount)
	assert.Zero(t, got.ViewCount)
	assert.Equal(t, datatypes.JSONMap{"theme": "dark"}, got.MetaData)
}

// ---------- DeletePost（物理删除，仅回收软删行） ----------

func TestDeletePost(t *testing.T) {
	ctx := context.Background()

	t.Run("仅物理删除软删行", func(t *testing.T) {
		// Arrange
		repo, db := newRepo(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DELETED, time.Now().UnixMilli())

		// Act
		info, err := repo.DeletePost(ctx, authorID1, postID1)

		// Assert
		require.NoError(t, err)
		assert.Equal(t, int64(1), info.RowsAffected)

		var cnt int64
		require.NoError(t, db.Model(&models.PostDetail{}).Where("id = ?", postID1).Count(&cnt).Error)
		assert.Zero(t, cnt, "软删行应被物理删除")
	})

	// 回归（P0-4）：无状态守卫时未软删的裸行会被误物理删，媒体关联随之丢失
	t.Run("未软删的帖子不被物理删除（回归）", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, time.Now().UnixMilli())

		info, err := repo.DeletePost(ctx, authorID1, postID1)

		require.NoError(t, err)
		assert.Zero(t, info.RowsAffected)

		var cnt int64
		require.NoError(t, db.Model(&models.PostDetail{}).Where("id = ?", postID1).Count(&cnt).Error)
		assert.Equal(t, int64(1), cnt, "未软删的帖子必须保留")
	})

	t.Run("作者不匹配不删除", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DELETED, time.Now().UnixMilli())

		info, err := repo.DeletePost(ctx, authorID2, postID1)

		require.NoError(t, err)
		assert.Zero(t, info.RowsAffected)

		var cnt int64
		require.NoError(t, db.Model(&models.PostDetail{}).Where("id = ?", postID1).Count(&cnt).Error)
		assert.Equal(t, int64(1), cnt)
	})

	t.Run("重复删除幂等（0 行）", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DELETED, time.Now().UnixMilli())

		_, err := repo.DeletePost(ctx, authorID1, postID1)
		require.NoError(t, err)

		info, err := repo.DeletePost(ctx, authorID1, postID1)
		require.NoError(t, err)
		assert.Zero(t, info.RowsAffected, "二次删除应为 0 行（幂等）")
	})
}

// ---------- SetPostStatus（CAS 流转） ----------

func TestSetPostStatus(t *testing.T) {
	ctx := context.Background()

	t.Run("from 匹配时流转成功", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DRAFT, time.Now().UnixMilli())

		info, err := repo.SetPostStatus(ctx, postID1,
			postv1.PostStatus_POST_STATUS_DRAFT, postv1.PostStatus_POST_STATUS_AUDITING)

		require.NoError(t, err)
		assert.Equal(t, int64(1), info.RowsAffected)

		var got models.PostDetail
		require.NoError(t, db.Where("id = ?", postID1).First(&got).Error)
		assert.Equal(t, postv1.PostStatus_POST_STATUS_AUDITING, got.Status)
	})

	t.Run("from 不匹配时不流转（CAS 语义）", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, time.Now().UnixMilli())

		info, err := repo.SetPostStatus(ctx, postID1,
			postv1.PostStatus_POST_STATUS_DRAFT, postv1.PostStatus_POST_STATUS_AUDITING)

		require.NoError(t, err)
		assert.Zero(t, info.RowsAffected)

		var got models.PostDetail
		require.NoError(t, db.Where("id = ?", postID1).First(&got).Error)
		assert.Equal(t, postv1.PostStatus_POST_STATUS_PUBLISHED, got.Status, "状态不应被改变")
	})
}

// ---------- LockDraftForUpdate ----------

func TestLockDraftForUpdate(t *testing.T) {
	ctx := context.Background()

	t.Run("草稿命中", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DRAFT, time.Now().UnixMilli())

		ok, err := repo.LockDraftForUpdate(ctx, postID1)
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("非草稿不命中", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, time.Now().UnixMilli())

		ok, err := repo.LockDraftForUpdate(ctx, postID1)
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("不存在不命中", func(t *testing.T) {
		repo, _ := newRepo(t)

		ok, err := repo.LockDraftForUpdate(ctx, 999999)
		require.NoError(t, err)
		assert.False(t, ok)
	})
}

// ---------- UpdateDraftTitle / UpdateDraftText / UpdateDraftMetaData ----------

func TestUpdateDraftFields(t *testing.T) {
	ctx := context.Background()

	t.Run("标题更新生效", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DRAFT, time.Now().UnixMilli())

		info, err := repo.UpdateDraftTitle(ctx, postID1, "新标题")
		require.NoError(t, err)
		assert.Equal(t, int64(1), info.RowsAffected)

		var got models.PostDetail
		require.NoError(t, db.Where("id = ?", postID1).First(&got).Error)
		assert.Equal(t, "新标题", got.Title)
		assert.Equal(t, "正文", got.FullText, "其他列不应被修改")
	})

	t.Run("正文更新生效", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DRAFT, time.Now().UnixMilli())

		info, err := repo.UpdateDraftText(ctx, postID1, "新正文")
		require.NoError(t, err)
		assert.Equal(t, int64(1), info.RowsAffected)

		var got models.PostDetail
		require.NoError(t, db.Where("id = ?", postID1).First(&got).Error)
		assert.Equal(t, "新正文", got.FullText)
	})

	t.Run("metadata 更新生效（含空 map 落库 {}）", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DRAFT, time.Now().UnixMilli())

		info, err := repo.UpdateDraftMetaData(ctx, postID1, datatypes.JSONMap{})
		require.NoError(t, err)
		assert.Equal(t, int64(1), info.RowsAffected)

		var got models.PostDetail
		require.NoError(t, db.Where("id = ?", postID1).First(&got).Error)
		assert.Empty(t, got.MetaData, "空 map 应落库为 {}")
	})

	// 状态守卫：非 DRAFT 不可改（WHERE 带 status = DRAFT）
	t.Run("非草稿不被修改（守卫生效）", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, time.Now().UnixMilli())

		infoTitle, err := repo.UpdateDraftTitle(ctx, postID1, "新标题")
		require.NoError(t, err)
		assert.Zero(t, infoTitle.RowsAffected)

		infoText, err := repo.UpdateDraftText(ctx, postID1, "新正文")
		require.NoError(t, err)
		assert.Zero(t, infoText.RowsAffected)

		infoMeta, err := repo.UpdateDraftMetaData(ctx, postID1, datatypes.JSONMap{"a": "b"})
		require.NoError(t, err)
		assert.Zero(t, infoMeta.RowsAffected)

		var got models.PostDetail
		require.NoError(t, db.Where("id = ?", postID1).First(&got).Error)
		assert.Equal(t, "标题", got.Title, "非草稿的任何列都不应被修改")
		assert.Equal(t, "正文", got.FullText)
		assert.Equal(t, datatypes.JSONMap{"k": "v"}, got.MetaData)
	})
}

// ---------- UpdatePostLikeCount / FavouriteCount / CommentCount / ViewCount ----------

func TestUpdatePostStatCounts(t *testing.T) {
	ctx := context.Background()

	t.Run("各计数列逐字段更新生效", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, time.Now().UnixMilli())

		infoLike, err := repo.UpdatePostLikeCount(ctx, postID1, 11)
		require.NoError(t, err)
		assert.Equal(t, int64(1), infoLike.RowsAffected)

		infoFav, err := repo.UpdatePostFavouriteCount(ctx, postID1, 12)
		require.NoError(t, err)
		assert.Equal(t, int64(1), infoFav.RowsAffected)

		infoCmt, err := repo.UpdatePostCommentCount(ctx, postID1, 13)
		require.NoError(t, err)
		assert.Equal(t, int64(1), infoCmt.RowsAffected)

		infoView, err := repo.UpdatePostViewCount(ctx, postID1, 14)
		require.NoError(t, err)
		assert.Equal(t, int64(1), infoView.RowsAffected)

		var got models.PostDetail
		require.NoError(t, db.Where("id = ?", postID1).First(&got).Error)
		assert.Equal(t, int64(11), got.LikeCount)
		assert.Equal(t, int64(12), got.FavouriteCount)
		assert.Equal(t, int64(13), got.CommentCount)
		assert.Equal(t, int64(14), got.ViewCount)
		assert.Equal(t, "标题", got.Title, "统计更新不应影响内容列")
	})

	// 回归（P1-4）：0 值必须覆盖旧值（set 0 ≠ 未设置）
	t.Run("0 值覆盖旧值（回归）", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, time.Now().UnixMilli())

		info, err := repo.UpdatePostLikeCount(ctx, postID1, 0)
		require.NoError(t, err)
		assert.Equal(t, int64(1), info.RowsAffected)

		var got models.PostDetail
		require.NoError(t, db.Where("id = ?", postID1).First(&got).Error)
		assert.Zero(t, got.LikeCount, "显式写 0 应覆盖旧值 1")
	})

	t.Run("帖子不存在返回 0 行", func(t *testing.T) {
		repo, _ := newRepo(t)

		info, err := repo.UpdatePostLikeCount(ctx, 999999, 5)
		require.NoError(t, err)
		assert.Zero(t, info.RowsAffected)
	})
}

// ---------- GetAuthorID ----------

func TestGetAuthorID(t *testing.T) {
	ctx := context.Background()

	t.Run("命中返回作者", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DRAFT, time.Now().UnixMilli())

		ok, authorID, err := repo.GetAuthorID(ctx, postID1)
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, authorID1, authorID)
	})

	t.Run("不存在返回 ok=false", func(t *testing.T) {
		repo, _ := newRepo(t)

		ok, authorID, err := repo.GetAuthorID(ctx, 999999)
		require.NoError(t, err)
		assert.False(t, ok)
		assert.Zero(t, authorID)
	})
}

// ---------- GetPosts ----------

func TestGetPosts(t *testing.T) {
	ctx := context.Background()

	t.Run("命中返回全字段且媒体按 sort_order 排序", func(t *testing.T) {
		repo, db := newRepo(t)
		createdAt := time.Now().UnixMilli()
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, createdAt)
		seedPostMedia(t, db, 5002, postID1, 4002, 6002, 1) // 后插，验证排序
		seedPostMedia(t, db, 5001, postID1, 4001, 6001, 0)

		posts, err := repo.GetPosts(ctx, []int64{postID1})
		require.NoError(t, err)
		require.Len(t, posts, 1)

		got := posts[0]
		assert.Equal(t, postID1, got.ID)
		assert.Equal(t, authorID1, got.AuthorID)
		assert.Equal(t, "标题", got.Title)
		assert.Equal(t, "正文", got.FullText)
		assert.Equal(t, postv1.PostStatus_POST_STATUS_PUBLISHED, got.Status)
		assert.Equal(t, int64(1), got.LikeCount)
		assert.Equal(t, int64(2), got.FavouriteCount)
		assert.Equal(t, int64(3), got.CommentCount)
		assert.Equal(t, int64(4), got.ViewCount)
		assert.Equal(t, datatypes.JSONMap{"k": "v"}, got.MetaData)
		assert.Equal(t, createdAt, got.CreatedAt)

		require.Len(t, got.Medias, 2, "媒体关联应全部加载")
		assert.Equal(t, int32(0), got.Medias[0].SortOrder, "媒体应按 sort_order 升序")
		assert.Equal(t, int32(1), got.Medias[1].SortOrder)
		require.NotNil(t, got.Medias[0].Media)
		assert.Equal(t, int64(4001), got.Medias[0].Media.ID)
		assert.Equal(t, int64(6001), got.Medias[0].Media.AssetID)
		assert.Equal(t, "post/obj", got.Medias[0].Media.ObjKey)
	})

	t.Run("部分命中只返回存在的", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DRAFT, time.Now().UnixMilli())

		posts, err := repo.GetPosts(ctx, []int64{postID1, 999999})
		require.NoError(t, err)
		require.Len(t, posts, 1)
		assert.Equal(t, postID1, posts[0].ID)
	})

	t.Run("空入参返回空集", func(t *testing.T) {
		repo, _ := newRepo(t)

		posts, err := repo.GetPosts(ctx, []int64{})
		require.NoError(t, err)
		assert.Empty(t, posts)
	})
}

// ---------- GetPostStatus ----------

func TestGetPostStatus(t *testing.T) {
	ctx := context.Background()

	t.Run("命中返回状态", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_AUDITING, time.Now().UnixMilli())

		ok, status, err := repo.GetPostStatus(ctx, postID1)
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, postv1.PostStatus_POST_STATUS_AUDITING, status)
	})

	t.Run("不存在返回 ok=false", func(t *testing.T) {
		repo, _ := newRepo(t)

		ok, status, err := repo.GetPostStatus(ctx, 999999)
		require.NoError(t, err)
		assert.False(t, ok)
		assert.Equal(t, postv1.PostStatus_POST_STATUS_UNSPECIFIED, status)
	})
}

// ---------- GetPostBriefsByAuthor / GetPublishedPostBriefsByAuthor ----------

func TestGetPostBriefsByAuthor(t *testing.T) {
	ctx := context.Background()
	base := time.Now().Add(-time.Hour).UnixMilli()

	t.Run("作者视角含草稿/审核中/已发布，排除封禁/已删", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DRAFT, base-5000)
		seedPost(t, db, postID2, authorID1, postv1.PostStatus_POST_STATUS_AUDITING, base-4000)
		seedPost(t, db, postID3, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, base-3000)
		seedPost(t, db, postID4, authorID1, postv1.PostStatus_POST_STATUS_BANNED, base-2000)
		seedPost(t, db, 2005, authorID1, postv1.PostStatus_POST_STATUS_DELETED, base-1000)
		seedPost(t, db, 2006, authorID2, postv1.PostStatus_POST_STATUS_PUBLISHED, base-6000) // 他人帖子

		posts, err := repo.GetPostBriefsByAuthor(ctx, authorID1, base, 10)
		require.NoError(t, err)

		ids := make([]int64, 0, len(posts))
		for _, p := range posts {
			ids = append(ids, p.ID)
		}
		assert.Equal(t, []int64{postID3, postID2, postID1}, ids,
			"应按 created_at DESC 且只含 DRAFT/AUDITING/PUBLISHED")
	})

	t.Run("游标严格小于且 limit 生效", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, base-5000)
		seedPost(t, db, postID2, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, base-4000)
		seedPost(t, db, postID3, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, base-3000)

		// 第一页（limit 2）
		page1, err := repo.GetPostBriefsByAuthor(ctx, authorID1, base, 2)
		require.NoError(t, err)
		require.Len(t, page1, 2)
		assert.Equal(t, []int64{postID3, postID2}, []int64{page1[0].ID, page1[1].ID})

		// 第二页：游标 = 第一页最后一条 created_at（严格小于，不含自身）
		page2, err := repo.GetPostBriefsByAuthor(ctx, authorID1, page1[1].CreatedAt, 2)
		require.NoError(t, err)
		require.Len(t, page2, 1)
		assert.Equal(t, postID1, page2[0].ID)
	})

	t.Run("只预载封面（sort_order=0）", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, base-5000)
		seedPostMedia(t, db, 5001, postID1, 4001, 6001, 0)
		seedPostMedia(t, db, 5002, postID1, 4002, 6002, 1)

		posts, err := repo.GetPostBriefsByAuthor(ctx, authorID1, base, 10)
		require.NoError(t, err)
		require.Len(t, posts, 1)
		require.Len(t, posts[0].Medias, 1, "brief 只带封面")
		assert.Equal(t, int32(0), posts[0].Medias[0].SortOrder)
	})

	t.Run("无数据返回空集", func(t *testing.T) {
		repo, _ := newRepo(t)

		posts, err := repo.GetPostBriefsByAuthor(ctx, authorID1, base, 10)
		require.NoError(t, err)
		assert.Empty(t, posts)
	})
}

func TestGetPublishedPostBriefsByAuthor(t *testing.T) {
	ctx := context.Background()
	base := time.Now().Add(-time.Hour).UnixMilli()

	t.Run("仅返回已发布", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DRAFT, base-5000)
		seedPost(t, db, postID2, authorID1, postv1.PostStatus_POST_STATUS_AUDITING, base-4000)
		seedPost(t, db, postID3, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, base-3000)

		posts, err := repo.GetPublishedPostBriefsByAuthor(ctx, authorID1, base, 10)
		require.NoError(t, err)
		require.Len(t, posts, 1)
		assert.Equal(t, postID3, posts[0].ID)
	})

	t.Run("limit 生效", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, base-5000)
		seedPost(t, db, postID2, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, base-4000)

		posts, err := repo.GetPublishedPostBriefsByAuthor(ctx, authorID1, base, 1)
		require.NoError(t, err)
		require.Len(t, posts, 1)
		assert.Equal(t, postID2, posts[0].ID)
	})
}

// ---------- SetPostBanned / SetPostDeleted ----------

func TestSetPostBanned(t *testing.T) {
	ctx := context.Background()

	t.Run("正常封禁", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, time.Now().UnixMilli())

		info, err := repo.SetPostBanned(ctx, postID1)
		require.NoError(t, err)
		assert.Equal(t, int64(1), info.RowsAffected)

		var got models.PostDetail
		require.NoError(t, db.Where("id = ?", postID1).First(&got).Error)
		assert.Equal(t, postv1.PostStatus_POST_STATUS_BANNED, got.Status)
	})

	t.Run("已封禁 0 行（幂等守卫）", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_BANNED, time.Now().UnixMilli())

		info, err := repo.SetPostBanned(ctx, postID1)
		require.NoError(t, err)
		assert.Zero(t, info.RowsAffected)
	})

	t.Run("已软删不被封禁", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DELETED, time.Now().UnixMilli())

		info, err := repo.SetPostBanned(ctx, postID1)
		require.NoError(t, err)
		assert.Zero(t, info.RowsAffected)

		var got models.PostDetail
		require.NoError(t, db.Where("id = ?", postID1).First(&got).Error)
		assert.Equal(t, postv1.PostStatus_POST_STATUS_DELETED, got.Status, "已删状态不应被覆盖")
	})
}

func TestSetPostDeleted(t *testing.T) {
	ctx := context.Background()

	t.Run("正常软删", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, time.Now().UnixMilli())

		info, err := repo.SetPostDeleted(ctx, postID1)
		require.NoError(t, err)
		assert.Equal(t, int64(1), info.RowsAffected)

		var got models.PostDetail
		require.NoError(t, db.Where("id = ?", postID1).First(&got).Error)
		assert.Equal(t, postv1.PostStatus_POST_STATUS_DELETED, got.Status)
	})

	t.Run("已软删 0 行（幂等守卫）", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DELETED, time.Now().UnixMilli())

		info, err := repo.SetPostDeleted(ctx, postID1)
		require.NoError(t, err)
		assert.Zero(t, info.RowsAffected)
	})
}

// ---------- 媒体关联 ----------

func TestCreatePostMedias(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	medias := []*media.MediaFile{newMediaFile(4001, 6001), newMediaFile(4002, 6002)}
	require.NoError(t, repo.CreatePostMedias(ctx, postID1, medias))

	var rows []models.PostMedia
	require.NoError(t, db.Where("post_id = ?", postID1).Order("sort_order").Find(&rows).Error)
	require.Len(t, rows, 2)

	assert.Equal(t, int32(0), rows[0].SortOrder, "展示顺序应按传入顺序从 0 递增")
	assert.Equal(t, int32(1), rows[1].SortOrder)
	assert.NotZero(t, rows[0].ID, "关联行应有独立雪花 ID")

	// 嵌入媒体列完整落库
	require.NotNil(t, rows[0].Media)
	assert.Equal(t, int64(4001), rows[0].Media.ID)
	assert.Equal(t, int64(6001), rows[0].Media.AssetID)
	assert.Equal(t, "post/obj", rows[0].Media.ObjKey)
	assert.Equal(t, "rednote", rows[0].Media.Bucket)
	assert.Equal(t, "abc", rows[0].Media.Sha256Sum)
	assert.Equal(t, int64(1024), rows[0].Media.SizeBytes)
	assert.Equal(t, mediav1.MediaFormat_MEDIA_FORMAT_WEBP, rows[0].Media.Format)
	assert.Equal(t, int64(100), rows[0].Media.Height)
	assert.Equal(t, int64(200), rows[0].Media.Width)
	assert.Equal(t, datatypes.JSONMap{"k": "v"}, rows[0].Media.MediaMetaData)
}

func TestGetPostMediasAndCover(t *testing.T) {
	ctx := context.Background()

	t.Run("GetPostMedias 全量按 sort_order", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPostMedia(t, db, 5002, postID1, 4002, 6002, 1)
		seedPostMedia(t, db, 5001, postID1, 4001, 6001, 0)
		seedPostMedia(t, db, 5003, postID2, 4003, 6003, 0) // 其他帖子

		rows, err := repo.GetPostMedias(ctx, []int64{postID1})
		require.NoError(t, err)
		require.Len(t, rows, 2)
		assert.Equal(t, int32(0), rows[0].SortOrder)
		assert.Equal(t, int32(1), rows[1].SortOrder)
	})

	t.Run("GetPostCover 仅 sort_order=0", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPostMedia(t, db, 5001, postID1, 4001, 6001, 0)
		seedPostMedia(t, db, 5002, postID1, 4002, 6002, 1)

		rows, err := repo.GetPostCover(ctx, []int64{postID1})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, int32(0), rows[0].SortOrder)
	})

	t.Run("无关联行返回空集", func(t *testing.T) {
		repo, _ := newRepo(t)

		rows, err := repo.GetPostMedias(ctx, []int64{postID1})
		require.NoError(t, err)
		assert.Empty(t, rows)
	})
}

func TestDeletePostMedias(t *testing.T) {
	ctx := context.Background()

	t.Run("删除帖子全部关联", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPostMedia(t, db, 5001, postID1, 4001, 6001, 0)
		seedPostMedia(t, db, 5002, postID1, 4002, 6002, 1)
		seedPostMedia(t, db, 5003, postID2, 4003, 6003, 0) // 其他帖子

		info, err := repo.DeletePostMedias(ctx, postID1)
		require.NoError(t, err)
		assert.Equal(t, int64(2), info.RowsAffected)

		var rows []models.PostMedia
		require.NoError(t, db.Where("post_id IN ?", []int64{postID1, postID2}).Find(&rows).Error)
		require.Len(t, rows, 1, "仅保留其他帖子的关联")
		assert.Equal(t, postID2, rows[0].PostID)
	})

	t.Run("重复删除幂等（0 行）", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPostMedia(t, db, 5001, postID1, 4001, 6001, 0)

		_, err := repo.DeletePostMedias(ctx, postID1)
		require.NoError(t, err)

		info, err := repo.DeletePostMedias(ctx, postID1)
		require.NoError(t, err)
		assert.Zero(t, info.RowsAffected)
	})
}

func TestLockDraftMediasForUpdate(t *testing.T) {
	ctx := context.Background()

	t.Run("有无关联行均成功", func(t *testing.T) {
		repo, db := newRepo(t)
		seedPostMedia(t, db, 5001, postID1, 4001, 6001, 0)

		require.NoError(t, repo.LockDraftMediasForUpdate(ctx, postID1), "有行应成功")
		require.NoError(t, repo.LockDraftMediasForUpdate(ctx, postID2), "无关联行不视为失败")
	})
}

// ---------- Transaction / WithTX / DB ----------

func TestTransaction(t *testing.T) {
	ctx := context.Background()

	t.Run("提交后全部可见", func(t *testing.T) {
		repo, db := newRepo(t)

		err := repo.Transaction(func(r PostRepository) error {
			return r.CreatePost(ctx, &models.PostDetail{
				BaseModel: common.BaseModel{ID: postID1},
				AuthorID:  authorID1,
				Title:     "事务内",
				FullText:  "正文",
				Status:    postv1.PostStatus_POST_STATUS_DRAFT,
			})
		})
		require.NoError(t, err)

		var cnt int64
		require.NoError(t, db.Model(&models.PostDetail{}).Where("id = ?", postID1).Count(&cnt).Error)
		assert.Equal(t, int64(1), cnt)
	})

	t.Run("回滚后全部不可见", func(t *testing.T) {
		repo, db := newRepo(t)

		err := repo.Transaction(func(r PostRepository) error {
			if err := r.CreatePost(ctx, &models.PostDetail{
				BaseModel: common.BaseModel{ID: postID1},
				AuthorID:  authorID1,
				Title:     "事务内",
				FullText:  "正文",
				Status:    postv1.PostStatus_POST_STATUS_DRAFT,
			}); err != nil {
				return err
			}
			return errors.New("boom") // 触发整体回滚
		})
		require.Error(t, err)

		var cnt int64
		require.NoError(t, db.Model(&models.PostDetail{}).Where("id = ?", postID1).Count(&cnt).Error)
		assert.Zero(t, cnt, "事务回滚后不应残留")
	})

	t.Run("DB 返回绑定句柄", func(t *testing.T) {
		repo, _ := newRepo(t)
		assert.NotNil(t, repo.DB())

		var cnt int64
		require.NoError(t, repo.Transaction(func(r PostRepository) error {
			return r.DB().Model(&models.PostDetail{}).Count(&cnt).Error
		}))
	})
}
