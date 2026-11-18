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

		assetIDs, err := repo.DeletePostMedias(ctx, postID1)
		require.NoError(t, err)
		require.Len(t, assetIDs, 2, "返回被删关联的资产 ID 供计数回收")
		assert.ElementsMatch(t, []int64{6001, 6002}, assetIDs)

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

		assetIDs, err := repo.DeletePostMedias(ctx, postID1)
		require.NoError(t, err)
		assert.Empty(t, assetIDs, "重复删除无被删关联，计数回收天然幂等")
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

// ---------- 互动（点赞/收藏明细与条件计数） ----------

// TestInteractionDetailCRUD 明细幂等锚点（OnConflict DoNothing）+ 条件计数（GREATEST 防负）+ 浏览增量
func TestInteractionDetailCRUD(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()
	seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, 1000)
	const uid = int64(5001)

	// CreateLike：首次 true，重复 false（幂等锚点）
	inserted, err := repo.CreateLike(ctx, postID1, uid)
	require.NoError(t, err)
	assert.True(t, inserted)
	inserted, err = repo.CreateLike(ctx, postID1, uid)
	require.NoError(t, err)
	assert.False(t, inserted, "重复点赞不应新增明细")

	// DeleteLike：首次 true，重复 false
	deleted, err := repo.DeleteLike(ctx, postID1, uid)
	require.NoError(t, err)
	assert.True(t, deleted)
	deleted, err = repo.DeleteLike(ctx, postID1, uid)
	require.NoError(t, err)
	assert.False(t, deleted, "未赞时删除为 no-op")

	// 条件计数（seedPost 起点 like_count=1）：+1 → 2，-1 → 1
	info, err := repo.IncPostLikeCount(ctx, postID1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), info.RowsAffected, "PG 匹配行即计数")
	info, err = repo.DecPostLikeCount(ctx, postID1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), info.RowsAffected)

	// 防负条件更新：计数清 0 后再 -1 仍是 0
	require.NoError(t, db.Model(&models.PostDetail{}).Where("id = ?", postID1).Update("like_count", 0).Error)
	_, err = repo.DecPostLikeCount(ctx, postID1)
	require.NoError(t, err)
	var got models.PostDetail
	require.NoError(t, db.First(&got, postID1).Error)
	assert.Zero(t, got.LikeCount, "GREATEST 防负兜底")

	// 收藏明细同构
	inserted, err = repo.CreateFavourite(ctx, postID1, uid)
	require.NoError(t, err)
	assert.True(t, inserted)
	inserted, err = repo.CreateFavourite(ctx, postID1, uid)
	require.NoError(t, err)
	assert.False(t, inserted)
	deleted, err = repo.DeleteFavourite(ctx, postID1, uid)
	require.NoError(t, err)
	assert.True(t, deleted)

	// 容量计数：只数明细行
	cnt, err := repo.CountFavouritesByUser(ctx, uid)
	require.NoError(t, err)
	assert.Zero(t, cnt)

	// 浏览增量（seedPost 起点 view_count=4）：+3 → 7
	info, err = repo.IncPostViewCount(ctx, postID1, 3)
	require.NoError(t, err)
	assert.Equal(t, int64(1), info.RowsAffected)
	require.NoError(t, db.First(&got, postID1).Error)
	assert.Equal(t, int64(7), got.ViewCount)

	// 帖子不存在：条件更新 0 行
	info, err = repo.IncPostViewCount(ctx, 999999, 1)
	require.NoError(t, err)
	assert.Zero(t, info.RowsAffected)
}

// TestListLikedFavouritePostIDs 判态列表：(created_at DESC, id DESC) 序 + 游标翻页 + 用户隔离
func TestListLikedFavouritePostIDs(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()
	const uid = int64(5002)
	const other = int64(5003)

	// 显式 created_at/id：两条同毫秒由 id 决序（雪花口径：id 大者新）
	rows := []*models.PostLike{
		{BaseModel: common.BaseModel{ID: 9101, CreatedAt: 2000}, PostID: postID1, UserID: uid},
		{BaseModel: common.BaseModel{ID: 9102, CreatedAt: 1000}, PostID: postID2, UserID: uid},
		{BaseModel: common.BaseModel{ID: 9103, CreatedAt: 1000}, PostID: postID3, UserID: uid},
		{BaseModel: common.BaseModel{ID: 9105, CreatedAt: 500}, PostID: 2005, UserID: uid},
		{BaseModel: common.BaseModel{ID: 9104, CreatedAt: 3000}, PostID: postID4, UserID: other},
	}
	require.NoError(t, db.Create(rows).Error)

	got, err := repo.ListLikedPostIDs(ctx, uid, 99999, 10)
	require.NoError(t, err)
	require.Len(t, got, 4, "不串用户")
	assert.Equal(t, []int64{postID1, postID3, postID2, 2005},
		[]int64{got[0].PostID, got[1].PostID, got[2].PostID, got[3].PostID}, "created_at DESC，并列按 id DESC")

	// limit 翻页：首页 2 条，用末条 created_at 作 before 续翻。
	// 时间游标口径为严格小于边界（与 GetPostBriefsByAuthor 现状一致）：
	// 与页边界同毫秒的并列行（9102）落页外，判态场景可接受（见 comment 设计风险#4 同款口径）
	got, err = repo.ListLikedPostIDs(ctx, uid, 99999, 2)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, postID1, got[0].PostID)
	assert.Equal(t, postID3, got[1].PostID)
	next, err := repo.ListLikedPostIDs(ctx, uid, got[1].CreatedAt, 2)
	require.NoError(t, err)
	require.Len(t, next, 1)
	assert.Equal(t, int64(2005), next[0].PostID)

	// 收藏列表同构
	require.NoError(t, db.Create(&models.PostFavourite{
		BaseModel: common.BaseModel{ID: 9201, CreatedAt: 5000}, PostID: postID1, UserID: uid,
	}).Error)
	favs, err := repo.ListFavouritePostIDs(ctx, uid, 99999, 10)
	require.NoError(t, err)
	require.Len(t, favs, 1)
	assert.Equal(t, postID1, favs[0].PostID)

	// 空页
	empty, err := repo.ListLikedPostIDs(ctx, 999999, 99999, 10)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

// TestDeletePostLikes 删帖级联回收：物理删除该帖全部点赞明细（不串帖），重复 0 行幂等
func TestDeletePostLikes(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()
	seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DELETED, 1000)
	seedPost(t, db, postID2, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, 1000)

	require.NoError(t, db.Create([]*models.PostLike{
		{BaseModel: common.BaseModel{ID: 9601}, PostID: postID1, UserID: 6001},
		{BaseModel: common.BaseModel{ID: 9602}, PostID: postID1, UserID: 6002},
		{BaseModel: common.BaseModel{ID: 9603}, PostID: postID2, UserID: 6001},
	}).Error)

	info, err := repo.DeletePostLikes(ctx, postID1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), info.RowsAffected, "该帖点赞明细全部物理回收")

	info, err = repo.DeletePostLikes(ctx, postID1)
	require.NoError(t, err)
	assert.Zero(t, info.RowsAffected, "重复投递 0 行命中即幂等")

	var cnt int64
	require.NoError(t, db.Model(&models.PostLike{}).Where("post_id = ?", postID1).Count(&cnt).Error)
	assert.Zero(t, cnt, "该帖点赞明细清空（否则残留行会污染「我点赞的帖子」列表）")
	require.NoError(t, db.Model(&models.PostLike{}).Where("post_id = ?", postID2).Count(&cnt).Error)
	assert.Equal(t, int64(1), cnt, "他帖点赞明细不受影响")
}

// TestDeletePostFavourites 删帖级联回收：物理删除该帖全部收藏明细（不串帖），重复 0 行幂等
func TestDeletePostFavourites(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()
	seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DELETED, 1000)
	seedPost(t, db, postID2, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, 1000)

	require.NoError(t, db.Create([]*models.PostFavourite{
		{BaseModel: common.BaseModel{ID: 9701}, PostID: postID1, UserID: 6001},
		{BaseModel: common.BaseModel{ID: 9702}, PostID: postID2, UserID: 6001},
	}).Error)

	info, err := repo.DeletePostFavourites(ctx, postID1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), info.RowsAffected)

	info, err = repo.DeletePostFavourites(ctx, postID1)
	require.NoError(t, err)
	assert.Zero(t, info.RowsAffected, "重复投递 0 行命中即幂等")

	var cnt int64
	require.NoError(t, db.Model(&models.PostFavourite{}).Where("post_id = ?", postID1).Count(&cnt).Error)
	assert.Zero(t, cnt, "该帖收藏明细清空")
	require.NoError(t, db.Model(&models.PostFavourite{}).Where("post_id = ?", postID2).Count(&cnt).Error)
	assert.Equal(t, int64(1), cnt, "他帖收藏明细不受影响")
}

// ---------- 评论（软删明细 + 条件计数 + 游标分页） ----------

// seedComment 造一条评论明细（一级评论 rootID/parentID 传 0）
func seedComment(t *testing.T, db *gorm.DB, id, postID, userID, rootID, parentID int64, status postv1.CommentStatus, createdAt int64) *models.PostComment {
	t.Helper()
	c := &models.PostComment{
		BaseModel:     common.BaseModel{ID: id, CreatedAt: createdAt, UpdatedAt: createdAt},
		PostID:        postID,
		UserID:        userID,
		RootID:        rootID,
		ParentID:      parentID,
		ReplyToUserID: userID,
		Content:       "评论内容",
		Status:        status,
	}
	require.NoError(t, db.Create(c).Error)
	return c
}

// TestCommentDetailCRUD 软删条件更新（幂等锚点）+ 回复/评论计数（防负）+ 行锁回读
func TestCommentDetailCRUD(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()
	seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, 1000)
	const uid = int64(6001)

	// CreateComment：BeforeCreate 钩子填雪花 ID 与时间戳
	c := &models.PostComment{
		PostID:  postID1,
		UserID:  uid,
		Content: "沙发",
		Status:  postv1.CommentStatus_COMMENT_STATUS_NORMAL,
	}
	require.NoError(t, repo.CreateComment(ctx, c))
	assert.NotZero(t, c.ID, "创建后应自动填充雪花 ID")
	assert.NotZero(t, c.CreatedAt, "创建后应自动填充 created_at")

	// GetComment：命中（含软删行）/ 未命中
	ok, got, err := repo.GetComment(ctx, c.ID)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "沙发", got.Content)
	assert.Equal(t, postv1.CommentStatus_COMMENT_STATUS_NORMAL, got.Status)
	ok, _, err = repo.GetComment(ctx, 999999)
	require.NoError(t, err)
	assert.False(t, ok, "不存在返回 ok=false")

	// LockCommentForUpdate：命中回读 / 未命中
	ok, locked, err := repo.LockCommentForUpdate(ctx, c.ID)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, uid, locked.UserID)
	assert.Equal(t, postID1, locked.PostID)
	ok, _, err = repo.LockCommentForUpdate(ctx, 999999)
	require.NoError(t, err)
	assert.False(t, ok)

	// SetCommentDeleted：条件软删，首次 1 行、重复 0 行（幂等锚点）；软删行保留可读
	info, err := repo.SetCommentDeleted(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), info.RowsAffected)
	info, err = repo.SetCommentDeleted(ctx, c.ID)
	require.NoError(t, err)
	assert.Zero(t, info.RowsAffected, "重复软删 0 行命中")
	ok, got, err = repo.GetComment(ctx, c.ID)
	require.NoError(t, err)
	assert.True(t, ok, "软删行保留可读")
	assert.Equal(t, postv1.CommentStatus_COMMENT_STATUS_DELETED, got.Status)

	// IncCommentReplyCount：仅 NORMAL 楼层生效（c 已软删 → 0 行）
	info, err = repo.IncCommentReplyCount(ctx, c.ID)
	require.NoError(t, err)
	assert.Zero(t, info.RowsAffected, "已删楼层不计回复数")

	// 楼层回复计数增减（NORMAL 楼层）
	root := seedComment(t, db, 9301, postID1, uid, 0, 0, postv1.CommentStatus_COMMENT_STATUS_NORMAL, 1000)
	info, err = repo.IncCommentReplyCount(ctx, root.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), info.RowsAffected)
	info, err = repo.IncCommentReplyCount(ctx, root.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), info.RowsAffected)
	info, err = repo.DecCommentReplyCount(ctx, root.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), info.RowsAffected)

	// 防负条件更新：计数清 0 后再 -1 仍是 0，按楼层存在性回报
	require.NoError(t, db.Model(&models.PostComment{}).Where("id = ?", root.ID).Update("reply_count", 0).Error)
	info, err = repo.DecCommentReplyCount(ctx, root.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), info.RowsAffected, "计数已 0 时按楼层存在性回报")
	var cntRow models.PostComment
	require.NoError(t, db.First(&cntRow, root.ID).Error)
	assert.Zero(t, cntRow.ReplyCount, "防负不减")

	// IncPostCommentCount：存在 1 行 / 不存在 0 行（seedPost 起点 comment_count=3）
	info, err = repo.IncPostCommentCount(ctx, postID1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), info.RowsAffected)
	info, err = repo.IncPostCommentCount(ctx, 999999)
	require.NoError(t, err)
	assert.Zero(t, info.RowsAffected)

	// DecPostCommentCount(postID, n)：按实删行数回收（现值 4 → 2）；计数不足 n 不减但按存在性回报
	info, err = repo.DecPostCommentCount(ctx, postID1, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(1), info.RowsAffected)
	var gotPost models.PostDetail
	require.NoError(t, db.First(&gotPost, postID1).Error)
	assert.Equal(t, int64(2), gotPost.CommentCount)
	info, err = repo.DecPostCommentCount(ctx, postID1, 5)
	require.NoError(t, err)
	assert.Equal(t, int64(1), info.RowsAffected, "计数不足 n 时按帖子存在性回报")
	require.NoError(t, db.First(&gotPost, postID1).Error)
	assert.Equal(t, int64(2), gotPost.CommentCount, "防负不减")
	info, err = repo.DecPostCommentCount(ctx, 999999, 1)
	require.NoError(t, err)
	assert.Zero(t, info.RowsAffected)

	// SetCommentsDeletedByRoot：级联仅删 NORMAL 回复，RowsAffected 即实删行数（幂等）
	seedComment(t, db, 9311, postID1, uid, 9301, 9301, postv1.CommentStatus_COMMENT_STATUS_NORMAL, 1000)
	seedComment(t, db, 9312, postID1, uid, 9301, 9311, postv1.CommentStatus_COMMENT_STATUS_NORMAL, 1000)
	seedComment(t, db, 9313, postID1, uid, 9301, 9301, postv1.CommentStatus_COMMENT_STATUS_DELETED, 1000)
	info, err = repo.SetCommentsDeletedByRoot(ctx, 9301)
	require.NoError(t, err)
	assert.Equal(t, int64(2), info.RowsAffected, "只级联 NORMAL 回复")
	info, err = repo.SetCommentsDeletedByRoot(ctx, 9301)
	require.NoError(t, err)
	assert.Zero(t, info.RowsAffected, "重复级联 0 行命中")
}

// TestListCommentsReplies 一级评论/楼内回复游标分页：root_id 过滤 + 仅 NORMAL + (created_at DESC, id DESC)
func TestListCommentsReplies(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()
	seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, 1000)
	seedPost(t, db, postID2, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, 1000)
	const uid = int64(6001)

	// 一级评论：同毫秒由 id 决序（雪花口径：id 大者新）；软删/他帖/回复混入均须被过滤
	seedComment(t, db, 9501, postID1, uid, 0, 0, postv1.CommentStatus_COMMENT_STATUS_NORMAL, 2000)
	seedComment(t, db, 9502, postID1, uid, 0, 0, postv1.CommentStatus_COMMENT_STATUS_NORMAL, 1000)
	seedComment(t, db, 9503, postID1, uid, 0, 0, postv1.CommentStatus_COMMENT_STATUS_NORMAL, 1000)
	seedComment(t, db, 9504, postID1, uid, 0, 0, postv1.CommentStatus_COMMENT_STATUS_DELETED, 4000)
	seedComment(t, db, 9505, postID2, uid, 0, 0, postv1.CommentStatus_COMMENT_STATUS_NORMAL, 5000)
	seedComment(t, db, 9506, postID1, uid, 0, 0, postv1.CommentStatus_COMMENT_STATUS_NORMAL, 500)
	seedComment(t, db, 9511, postID1, uid, 9501, 9501, postv1.CommentStatus_COMMENT_STATUS_NORMAL, 6000)

	got, err := repo.ListComments(ctx, postID1, 99999, 10)
	require.NoError(t, err)
	require.Len(t, got, 4, "仅本帖一级评论且 NORMAL")
	assert.Equal(t, []int64{9501, 9503, 9502, 9506},
		[]int64{got[0].ID, got[1].ID, got[2].ID, got[3].ID}, "created_at DESC，并列按 id DESC")

	// limit 翻页：首页 2 条，用末条 created_at 作 before 续翻。
	// 时间游标口径为严格小于边界（与 GetPostBriefsByAuthor 现状一致）：
	// 与页边界同毫秒的并列行（9502）落页外，与判态列表同款口径
	got, err = repo.ListComments(ctx, postID1, 99999, 2)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, int64(9501), got[0].ID)
	assert.Equal(t, int64(9503), got[1].ID)
	next, err := repo.ListComments(ctx, postID1, got[1].CreatedAt, 2)
	require.NoError(t, err)
	require.Len(t, next, 1)
	assert.Equal(t, int64(9506), next[0].ID, "严格小于边界，同毫秒并列行落页外")

	// 空页
	empty, err := repo.ListComments(ctx, postID1, 500, 10)
	require.NoError(t, err)
	assert.Empty(t, empty)

	// 他帖一级评论互不串
	other, err := repo.ListComments(ctx, postID2, 99999, 10)
	require.NoError(t, err)
	require.Len(t, other, 1)
	assert.Equal(t, int64(9505), other[0].ID)

	// 楼内回复：root_id 过滤 + 仅 NORMAL + (created_at DESC, id DESC)；平铺含回复的回复
	seedComment(t, db, 9521, postID1, uid, 9501, 9501, postv1.CommentStatus_COMMENT_STATUS_NORMAL, 3000)
	seedComment(t, db, 9522, postID1, uid, 9501, 9521, postv1.CommentStatus_COMMENT_STATUS_NORMAL, 3000)
	seedComment(t, db, 9523, postID1, uid, 9501, 9501, postv1.CommentStatus_COMMENT_STATUS_DELETED, 7000)
	seedComment(t, db, 9524, postID1, uid, 9502, 9502, postv1.CommentStatus_COMMENT_STATUS_NORMAL, 8000)

	replies, err := repo.ListReplies(ctx, 9501, 99999, 10)
	require.NoError(t, err)
	require.Len(t, replies, 3, "仅本楼回复且 NORMAL（含首段埋的 9511）")
	assert.Equal(t, []int64{9511, 9522, 9521},
		[]int64{replies[0].ID, replies[1].ID, replies[2].ID}, "created_at DESC，并列按 id DESC")
	assert.Equal(t, int64(9521), replies[1].ParentID, "回复的回复平铺，parent 指向被回复者")

	// 空页
	emptyReplies, err := repo.ListReplies(ctx, 9501, 500, 10)
	require.NoError(t, err)
	assert.Empty(t, emptyReplies)
}

// TestDeletePostComments 删帖回收：物理删除帖子下全部评论行（软删/正常一并清），按帖隔离，重复 0 行幂等
func TestDeletePostComments(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()
	seedPost(t, db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DELETED, 1000)
	seedPost(t, db, postID2, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, 1000)

	seedComment(t, db, 9801, postID1, 6001, 0, 0, postv1.CommentStatus_COMMENT_STATUS_NORMAL, 1000)
	seedComment(t, db, 9802, postID1, 6002, 9801, 9801, postv1.CommentStatus_COMMENT_STATUS_DELETED, 2000)
	seedComment(t, db, 9803, postID1, 6002, 9801, 9801, postv1.CommentStatus_COMMENT_STATUS_NORMAL, 3000)
	seedComment(t, db, 9804, postID2, 6001, 0, 0, postv1.CommentStatus_COMMENT_STATUS_NORMAL, 4000)

	info, err := repo.DeletePostComments(ctx, postID1)
	require.NoError(t, err)
	assert.Equal(t, int64(3), info.RowsAffected, "软删/正常一并物理回收")

	info, err = repo.DeletePostComments(ctx, postID1)
	require.NoError(t, err)
	assert.Zero(t, info.RowsAffected, "重复投递 0 行命中即幂等")

	var cnt int64
	require.NoError(t, db.Model(&models.PostComment{}).Where("post_id = ?", postID1).Count(&cnt).Error)
	assert.Zero(t, cnt, "该帖评论物理清空")
	require.NoError(t, db.Model(&models.PostComment{}).Where("post_id = ?", postID2).Count(&cnt).Error)
	assert.Equal(t, int64(1), cnt, "他帖评论不受影响")
}
