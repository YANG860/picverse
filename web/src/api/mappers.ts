// bff.v1 面客视图 → 前端视图模型（features/models.ts）的映射层。
// 契约见 proto/bff/v1/views.proto；bigint（int64）统一降为 number（计数/尺寸在安全整数范围内）。
import type {
  CommentView as PbCommentView,
  MediaView as PbMediaView,
  PostCard as PbPostCard,
  PostDetail as PbPostDetail,
  ProfileView as PbProfileView,
  UserBrief as PbUserBrief,
} from '@gen/bff/v1/views_pb.js'
import { PostStatus } from '@gen/post/v1/post_pb.js'
import type { CommentView, MediaView, PostCard, PostDetail, ProfileView, UserBrief } from '@/features/models'

export function mapMedia(m: PbMediaView | undefined): MediaView {
  // 尺寸缺失（部分历史测试数据无 meta）兑底 3:4 竖图：避免 0/0 导致 aspect-ratio 与分列估算失效
  const width = Number(m?.width ?? 0)
  const height = Number(m?.height ?? 0)
  return { url: m?.url ?? '', width: width > 0 ? width : 3, height: height > 0 ? height : 4 }
}

export function mapUserBrief(u: PbUserBrief | undefined): UserBrief {
  return { uid: u?.uid ?? '', name: u?.name ?? '', avatarUrl: u?.avatar?.url ?? '' }
}

/** post.v1.PostStatus → 前端状态（他人视角恒 PUBLISHED，仅“我的帖子”含草稿/审核中） */
function mapStatus(s: PostStatus): NonNullable<PostCard['status']> {
  if (s === PostStatus.DRAFT) return 'DRAFT'
  if (s === PostStatus.AUDITING) return 'AUDITING'
  return 'PUBLISHED'
}

export function mapPostCard(c: PbPostCard): PostCard {
  return {
    postId: c.postId,
    title: c.title,
    cover: mapMedia(c.cover),
    author: mapUserBrief(c.author),
    stat: {
      likeCount: Number(c.stat?.likeCount ?? 0),
      favouriteCount: Number(c.stat?.favouriteCount ?? 0),
      commentCount: Number(c.stat?.commentCount ?? 0),
      viewCount: Number(c.stat?.viewCount ?? 0),
    },
    // created_at 为 unix 毫秒时间戳的字符串形式（views.proto 约定）
    createdAt: Number(c.createdAt),
    status: mapStatus(c.status),
  }
}

export function mapPostDetail(d: PbPostDetail): PostDetail {
  return {
    postId: d.postId,
    title: d.title,
    cover: mapMedia(d.medias[0]),
    author: mapUserBrief(d.author),
    stat: {
      likeCount: Number(d.stat?.likeCount ?? 0),
      favouriteCount: Number(d.stat?.favouriteCount ?? 0),
      commentCount: Number(d.stat?.commentCount ?? 0),
      viewCount: Number(d.stat?.viewCount ?? 0),
    },
    createdAt: Number(d.createdAt),
    status: mapStatus(d.status),
    fullText: d.fullText,
    medias: d.medias.map(mapMedia),
    relation: {
      isFollowing: d.relation?.isFollowing ?? false,
      isFollower: d.relation?.isFollower ?? false,
    },
  }
}

export function mapComment(c: PbCommentView): CommentView {
  return {
    commentId: c.commentId,
    rootId: c.rootId,
    parentId: c.parentId,
    user: mapUserBrief(c.user),
    content: c.content,
    createdAt: Number(c.createdAt),
    replyCount: Number(c.replyCount),
    // 一级评论无被回复者（parent_id 空）
    replyToUser: c.parentId ? mapUserBrief(c.replyToUser) : undefined,
  }
}

/** bff.v1.ProfileView（GetUserProfile 聚合视图）→ 前端模型；获赞数 BFF 不提供，真实数据传 0（前端隐藏） */
export function mapProfileView(v: PbProfileView): ProfileView {
  const p = v.profile
  return {
    brief: mapUserBrief(p?.brief),
    bio: p?.bio ?? '',
    stat: {
      followers: Number(p?.stat?.followers ?? 0),
      followings: Number(p?.stat?.followings ?? 0),
    },
    relation: {
      isFollowing: v.relation?.isFollowing ?? false,
      isFollower: v.relation?.isFollower ?? false,
    },
    likeReceived: 0,
  }
}
