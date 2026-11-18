// 面客视图模型：字段对齐 BFF 契约（proto/bff/v1/views.proto）的 TS 镜像。
// mock 阶段直接消费本模型；对接真实 API 时仅需把 bff.v1.PostCard/PostDetail/CommentView 映射进来。
export interface MediaView {
  url: string
  width: number
  height: number
}

export interface UserBrief {
  uid: string
  name: string
  avatarUrl: string
}

/** 对齐 bff.v1.Relation（当前会话用户与目标用户的关系） */
export interface Relation {
  isFollowing: boolean
  isFollower: boolean
}

/** 对齐 user.v1.UserStat（BFF UserProfile.stat） */
export interface UserStat {
  followers: number
  followings: number
}

/** 对齐 post.v1.PostStat */
export interface PostStat {
  likeCount: number
  favouriteCount: number
  commentCount: number
  viewCount: number
}

export type FeedTab = 'recommend' | 'hot' | 'following'

/** 对齐 bff.v1.PostCard（信息流/列表页卡片） */
export interface PostCard {
  postId: string
  title: string
  cover: MediaView
  author: UserBrief
  stat: PostStat
  /** unix 毫秒 */
  createdAt: number
  /** 帖子状态：他人视角恒 PUBLISHED；“我的笔记”含草稿/审核中（对齐 post.v1.PostStatus） */
  status?: 'DRAFT' | 'AUDITING' | 'PUBLISHED'
}

/** 对齐 bff.v1.ProfileView（用户主页聚合视图） */
export interface ProfileView {
  brief: UserBrief
  bio: string
  stat: UserStat
  relation: Relation
  /** 获赞与收藏：BFF UserStat 不含此项，mock 合成占位（视觉用） */
  likeReceived: number
}

/** 对齐 bff.v1.PostDetail（帖子详情页） */
export interface PostDetail extends PostCard {
  fullText: string
  medias: MediaView[]
  relation: Relation
}

/** 对齐 bff.v1.CommentView（两级楼中楼：replies 平铺在楼层下） */
export interface CommentView {
  commentId: string
  /** 楼层 id（一级评论 = 自身） */
  rootId: string
  /** 被回复评论 id（一级评论为空串） */
  parentId: string
  user: UserBrief
  content: string
  /** unix 毫秒 */
  createdAt: number
  replyCount: number
  /** 被回复者（一级评论为空） */
  replyToUser?: UserBrief
  replies?: CommentView[]
}
