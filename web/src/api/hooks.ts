// 真实 API 数据层（TanStack Query）：信息流 / 帖子详情 / 评论 / 互动（点赞·收藏·关注）。
// 语义对齐 proto/bff/v1/*：游标分页「游标为空即到底」；互动幂等、响应回填确定态。
import { useInfiniteQuery, useMutation, useQuery, useQueryClient, type InfiniteData, type QueryClient } from '@tanstack/react-query'
import type { SearchSort } from '@gen/discover/v1/requests_pb'
import { bff } from '@/api/transport'
import { unwrap, type FailShape } from '@/api/errors'
import { mapComment, mapPostCard, mapPostDetail, mapProfileView, mapUserBrief } from '@/api/mappers'
import { uploadMedia, waitAssetsActive } from '@/api/publish'
import { MediaFileType } from '@gen/media/v1/media_pb'
import type { CommentView, FeedTab, PostDetail } from '@/features/models'

/** 游标分页页大小（契约 limit < 1000；int64 → bigint） */
const PAGE_SIZE = 20n

export const qk = {
  feed: (tab: FeedTab) => ['feed', tab] as const,
  post: (postId: string) => ['post', postId] as const,
  comments: (postId: string) => ['comments', postId] as const,
  replies: (rootId: string) => ['replies', rootId] as const,
  myInteraction: ['my-interaction'] as const,
  myProfile: ['my-profile'] as const,
  search: (keyword: string, sort: SearchSort) => ['search', keyword, sort] as const,
}

/** 帖子搜索（offset 游标：相关度排序无稳定全序，游标编码 offset；cursor 为空才到底） */
export function useSearchPosts(keyword: string, sort: SearchSort) {
  return useInfiniteQuery({
    queryKey: qk.search(keyword, sort),
    initialPageParam: '',
    queryFn: async ({ pageParam }) => {
      const ok = unwrap(await bff.searchPosts({ keyword, sort, cursor: { str: pageParam, limit: PAGE_SIZE } }))
      return { posts: ok.posts.map(mapPostCard), next: ok.cursor?.str ?? '' }
    },
    getNextPageParam: (last) => last.next || undefined,
    enabled: keyword.length > 0,
    // 搜索是用户主动行为：失败快速反馈（默认重试 3 次会让「加载中」占住 UI 约 7 秒）
    retry: 1,
  })
}

/** 更新我的资料（部分更新：仅传变更字段，undefined 不修改）；成功后失效资料缓存驱动重拉 */
export function useUpdateProfile() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (patch: { name?: string; bio?: string }) => {
      const ok = unwrap(await bff.updateMyProfile({ name: patch.name, bio: patch.bio }))
      return ok.profile
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.myProfile })
      void qc.invalidateQueries({ queryKey: ['user-profile'] })
    },
  })
}

/** 更新我的头像：UploadMedia 直传 → 等 ACTIVE → UpdateMyAvatar（BFF 校验归属 + 旧资产引用计数卸载） */
export function useUpdateAvatar() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (file: File) => {
      const assetId = await uploadMedia(file)
      await waitAssetsActive([assetId])
      const ok = unwrap(await bff.updateMyAvatar({ avatar: { assetId, type: MediaFileType.MEDIA_FILE_TYPE_ORIGIN } }))
      return ok.profile
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.myProfile })
      void qc.invalidateQueries({ queryKey: ['user-profile'] })
    },
  })
}

/** 我的资料（判断评论归属/删除权限；uid='me' 路由解析） */
export function useMyProfile() {
  return useQuery({
    queryKey: qk.myProfile,
    queryFn: async () => unwrap(await bff.getMyProfile({})).profile ?? null,
    staleTime: 60_000,
  })
}

/** 他人主页资料（GetUserProfile 聚合关注关系 → ProfileView） */
export function useUserProfile(uid: string) {
  return useQuery({
    queryKey: ['user-profile', uid],
    queryFn: async () => {
      const ok = unwrap(await bff.getUserProfile({ uid }))
      return ok.view ? mapProfileView(ok.view) : null
    },
    enabled: Boolean(uid),
    staleTime: 30_000,
  })
}

/** 主页笔记流（自己：GetMyPosts 含草稿/审核中；他人：GetUserPosts 仅已发布；游标分页） */
export function useUserPosts(uid: string, isSelf: boolean) {
  return useInfiniteQuery({
    queryKey: ['user-posts', uid, isSelf],
    initialPageParam: '',
    queryFn: async ({ pageParam }) => {
      const cursor = { str: pageParam, limit: PAGE_SIZE }
      const ok = unwrap(isSelf ? await bff.getMyPosts({ cursor }) : await bff.getUserPosts({ uid, cursor }))
      return { posts: ok.posts.map(mapPostCard), next: ok.cursor?.str ?? '' }
    },
    getNextPageParam: (last) => last.next || undefined,
    enabled: Boolean(uid),
  })
}

/** 我的收藏帖子（契约无批量卡片接口：ID 全量预取后并发 GetPost 聚合；收藏夹容量上限内可全量） */
export function useFavouritePosts() {
  return useQuery({
    queryKey: ['favourite-posts'],
    queryFn: async () => {
      const ids = await fetchAllIds((cursor) => bff.getMyFavouritePostIDs({ cursor }), (ok) => ok.postIds)
      const list = await Promise.all(
        [...ids].map(async (postId) => {
          const ok = unwrap(await bff.getPost({ postId }))
          return ok.post ? mapPostDetail(ok.post) : null
        }),
      )
      return list.filter((p): p is PostDetail => p !== null)
    },
  })
}

/** 关注/粉丝列表（契约仅“我的”列表：本人主页可看；游标分页） */
export function useFollowingList() {
  return useInfiniteQuery({
    queryKey: ['following-list'],
    initialPageParam: '',
    queryFn: async ({ pageParam }) => {
      const ok = unwrap(await bff.getFollowingList({ cursor: { str: pageParam, limit: 30n } }))
      return { users: ok.userBriefs.map(mapUserBrief), next: ok.cursor?.str ?? '' }
    },
    getNextPageParam: (last) => last.next || undefined,
  })
}

export function useFollowerList() {
  return useInfiniteQuery({
    queryKey: ['follower-list'],
    initialPageParam: '',
    queryFn: async ({ pageParam }) => {
      const ok = unwrap(await bff.getFollowerList({ cursor: { str: pageParam, limit: 30n } }))
      return { users: ok.userBriefs.map(mapUserBrief), next: ok.cursor?.str ?? '' }
    },
    getNextPageParam: (last) => last.next || undefined,
  })
}

/** 信息流（推荐/热门/关注）。翻页到底只看游标（推荐流短取≠到底） */
export function useInfiniteFeed(tab: FeedTab) {
  return useInfiniteQuery({
    queryKey: qk.feed(tab),
    initialPageParam: '',
    queryFn: async ({ pageParam }) => {
      let str = pageParam
      // 推荐流曝光去重可能整页为空但游标仍有后续（契约：短取≠到底，翻页只看游标）——循环跳过空页；
      // 上限内未见数据即视为到底（推荐池被刷尽的极端场景），UI 显示“没有更多了”
      for (let i = 0; i < 30; i++) {
        const cursor = { str, limit: PAGE_SIZE }
        const res =
          tab === 'hot'
            ? await bff.getHotPosts({ cursor })
            : tab === 'following'
              ? await bff.getFollowingFeed({ cursor })
              : await bff.getRecommendFeed({ cursor })
        const ok = unwrap(res)
        const next = ok.cursor?.str ?? ''
        if (ok.posts.length > 0 || !next) return { posts: ok.posts.map(mapPostCard), next }
        str = next
      }
      return { posts: [], next: '' }
    },
    getNextPageParam: (last) => last.next || undefined,
  })
}

/** 帖子详情 */
export function usePost(postId: string) {
  return useQuery({
    queryKey: qk.post(postId),
    queryFn: async () => {
      const ok = unwrap(await bff.getPost({ postId }))
      return ok.post ? mapPostDetail(ok.post) : null
    },
    enabled: Boolean(postId),
  })
}

/** 帖子一级评论（游标分页，created_at DESC） */
export function useInfiniteComments(postId: string) {
  return useInfiniteQuery({
    queryKey: qk.comments(postId),
    initialPageParam: '',
    queryFn: async ({ pageParam }) => {
      const ok = unwrap(await bff.getComments({ postId, cursor: { str: pageParam, limit: PAGE_SIZE } }))
      return { comments: ok.comments.map(mapComment), next: ok.cursor?.str ?? '' }
    },
    getNextPageParam: (last) => last.next || undefined,
    enabled: Boolean(postId),
  })
}

/** 楼内回复（展开时懒加载；回复条数有限，一页拉全，短取即到底） */
export function useReplies(rootId: string, enabled: boolean) {
  return useQuery({
    queryKey: qk.replies(rootId),
    queryFn: async () => {
      const ok = unwrap(await bff.getReplies({ rootId, cursor: { str: '', limit: 999n } }))
      return ok.replies.map(mapComment)
    },
    enabled,
  })
}

/** 循环翻页拉全 ID 列表（互动态预取：点赞/收藏/关注均本地集合判态，不逐请求查询） */
async function fetchAllIds<T extends FailShape & { cursor?: { str?: string } | null }>(
  request: (cursor: { str: string; limit: bigint }) => Promise<T>,
  pick: (ok: T) => string[],
): Promise<Set<string>> {
  const ids = new Set<string>()
  let str = ''
  for (;;) {
    const ok = unwrap(await request({ str, limit: 999n }))
    for (const id of pick(ok)) ids.add(id)
    if (!ok.cursor?.str) break
    str = ok.cursor.str
  }
  return ids
}

/**
 * 我的互动态：点赞 / 收藏 / 关注 三个集合一次预取，前端本地判态。
 * 契约：视图不回填 liked/favourited（关注同理，relation 不作按钮判态依据）；
 * 写操作（赞/藏/关注）幂等且响应回填确定态，直接同步本地集合。
 */
export function useMyInteraction() {
  return useQuery({
    queryKey: qk.myInteraction,
    queryFn: async () => {
      const [liked, favourited, following] = await Promise.all([
        fetchAllIds((cursor) => bff.getMyLikedPostIDs({ cursor }), (ok) => ok.postIds),
        fetchAllIds((cursor) => bff.getMyFavouritePostIDs({ cursor }), (ok) => ok.postIds),
        fetchAllIds((cursor) => bff.getFollowingList({ cursor }), (ok) => ok.userBriefs.map((u) => u.uid)),
      ])
      return { liked, favourited, following }
    },
    staleTime: 30_000,
  })
}

function patchPostStat(qc: QueryClient, postId: string, patch: Partial<{ likeCount: number; favouriteCount: number; commentCount: number }>) {
  qc.setQueryData<PostDetail | null>(qk.post(postId), (old) => (old ? { ...old, stat: { ...old.stat, ...patch } } : old))
}

function patchInteractionSet(qc: QueryClient, key: 'liked' | 'favourited' | 'following', id: string, on: boolean) {
  qc.setQueryData<{ liked: Set<string>; favourited: Set<string>; following: Set<string> }>(qk.myInteraction, (old) => {
    if (!old) return old
    const next = new Set(old[key])
    if (on) next.add(id)
    else next.delete(id)
    return { ...old, [key]: next }
  })
}

/** 点赞/取消点赞（幂等；响应带回最新计数与确定态，直接回填） */
export function useToggleLike() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (postId: string) => {
      const liked = qc.getQueryData<{ liked: Set<string> }>(qk.myInteraction)?.liked.has(postId) ?? false
      const ok = unwrap(liked ? await bff.unlikePost({ postId }) : await bff.likePost({ postId }))
      return { postId, liked: ok.liked, likeCount: Number(ok.likeCount) }
    },
    onSuccess: ({ postId, liked, likeCount }) => {
      patchPostStat(qc, postId, { likeCount })
      patchInteractionSet(qc, 'liked', postId, liked)
    },
  })
}

/** 收藏/取消收藏（幂等；超收藏夹上限报 FavouriteLimitExceeded） */
export function useToggleFavourite() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (postId: string) => {
      const favourited = qc.getQueryData<{ favourited: Set<string> }>(qk.myInteraction)?.favourited.has(postId) ?? false
      const ok = unwrap(favourited ? await bff.unfavouritePost({ postId }) : await bff.favouritePost({ postId }))
      return { postId, favourited: ok.favourited, favouriteCount: Number(ok.favouriteCount) }
    },
    onSuccess: ({ postId, favourited, favouriteCount }) => {
      patchPostStat(qc, postId, { favouriteCount })
      patchInteractionSet(qc, 'favourited', postId, favourited)
    },
  })
}

/** 关注/取消关注（幂等空响应；本地关注集合判态并回写，不逐请求查询关系） */
export function useToggleFollow() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (targetUid: string) => {
      const following = qc.getQueryData<{ following: Set<string> }>(qk.myInteraction)?.following.has(targetUid) ?? false
      unwrap(following ? await bff.unfollow({ targetUid }) : await bff.follow({ targetUid }))
      return { targetUid, follow: !following }
    },
    onSuccess: ({ targetUid, follow }) => {
      patchInteractionSet(qc, 'following', targetUid, follow)
    },
  })
}

/** 评论列表无限数据结构（TanStack InfiniteData：{ pages, pageParams }，非数组） */
type CommentPages = InfiniteData<{ comments: CommentView[]; next: string }>

/** 发表一级评论：响应评论插入列表头部，comment_count 回填帖子 */
export function useAddComment(postId: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (content: string) => {
      const ok = unwrap(await bff.addComment({ postId, content }))
      return { comment: ok.comment ? mapComment(ok.comment) : null, commentCount: Number(ok.commentCount) }
    },
    onSuccess: ({ comment, commentCount }) => {
      if (comment) {
        // 新评论插入首页头部（GetComments 为 created_at DESC），发布后立即可见
        qc.setQueryData<CommentPages>(qk.comments(postId), (old) => {
          if (!old || old.pages.length === 0) return old
          return { ...old, pages: [{ ...old.pages[0], comments: [comment, ...old.pages[0].comments] }, ...old.pages.slice(1)] }
        })
      }
      patchPostStat(qc, postId, { commentCount })
    },
  })
}

/**
 * 楼中楼回复（parent 可为一级评论或楼内回复）：回复追加楼层缓存，
 * 楼层 reply_count +1（契约：回复自身的 reply_count 恒 0，计数在楼层）。
 */
export function useReplyComment(postId: string, rootId: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ parentId, content }: { parentId: string; content: string }) => {
      const ok = unwrap(await bff.replyComment({ postId, parentId, content }))
      return { comment: ok.comment ? mapComment(ok.comment) : null, commentCount: Number(ok.commentCount) }
    },
    onSuccess: ({ comment, commentCount }) => {
      if (comment) {
        qc.setQueryData<CommentView[]>(qk.replies(rootId), (old) => (old ? [...old, comment] : [comment]))
        qc.setQueryData<CommentPages>(qk.comments(postId), (old) =>
          old
            ? {
                ...old,
                pages: old.pages.map((page) => ({
                  ...page,
                  comments: page.comments.map((c) => (c.commentId === rootId ? { ...c, replyCount: c.replyCount + 1 } : c)),
                })),
              }
            : old,
        )
      }
      patchPostStat(qc, postId, { commentCount })
    },
  })
}

/**
 * 删除评论（软删，幂等）：删一级评论级联整楼，删回复只移除自身。
 * 响应回填最新 comment_count。
 */
export function useDeleteComment(postId: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ commentId }: { commentId: string; rootId: string; isRoot: boolean }) => {
      const ok = unwrap(await bff.deleteComment({ commentId }))
      return { commentId, commentCount: Number(ok.commentCount) }
    },
    onSuccess: ({ commentId, commentCount }, { rootId, isRoot }) => {
      if (isRoot) {
        // 删一级评论：整楼移除
        qc.setQueryData<CommentPages>(qk.comments(postId), (old) =>
          old
            ? { ...old, pages: old.pages.map((page) => ({ ...page, comments: page.comments.filter((c) => c.commentId !== commentId) })) }
            : old,
        )
        qc.removeQueries({ queryKey: qk.replies(rootId) })
      } else {
        // 删回复：楼层 reply_count -1
        qc.setQueryData<CommentView[]>(qk.replies(rootId), (old) => (old ? old.filter((c) => c.commentId !== commentId) : old))
        qc.setQueryData<CommentPages>(qk.comments(postId), (old) =>
          old
            ? {
                ...old,
                pages: old.pages.map((page) => ({
                  ...page,
                  comments: page.comments.map((c) => (c.commentId === rootId ? { ...c, replyCount: Math.max(0, c.replyCount - 1) } : c)),
                })),
              }
            : old,
        )
      }
      patchPostStat(qc, postId, { commentCount })
    },
  })
}
