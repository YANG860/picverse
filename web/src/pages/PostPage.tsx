import { useMemo, useState, type FormEvent } from 'react'
import { Link, useParams } from 'react-router-dom'
import { Bookmark, Heart, MessageCircle, Send } from 'lucide-react'
import { describeError } from '@/api/errors'
import {
  useAddComment,
  useDeleteComment,
  useInfiniteComments,
  useMyInteraction,
  useMyProfile,
  usePost,
  useReplies,
  useReplyComment,
  useToggleFavourite,
  useToggleFollow,
  useToggleLike,
} from '@/api/hooks'
import AppHeader from '@/components/layout/AppHeader'
import { buttonVariants } from '@/components/ui/button'
import CommentItem from '@/features/post/CommentItem'
import MediaGallery from '@/features/post/MediaGallery'
import type { CommentView } from '@/features/models'
import { fromNow } from '@/lib/time'

/** 一楼评论 + 楼内回复懒加载 + 回复/删除（展开时才请求 GetReplies） */
function CommentThread({ comment, postId, canDelete }: { comment: CommentView; postId: string; canDelete: (uid: string) => boolean }) {
  const [expanded, setExpanded] = useState(false)
  const { data: replies, isLoading } = useReplies(comment.commentId, expanded)
  const reply = useReplyComment(postId, comment.commentId)
  const del = useDeleteComment(postId)
  return (
    <CommentItem
      comment={{ ...comment, replies: expanded ? (replies ?? []) : undefined }}
      onLoadReplies={() => setExpanded(true)}
      repliesLoading={isLoading}
      onReply={(parentId, content) => {
        // 回复成功后展开楼层，看到新回复（mutation 内同步 replies 缓存）
        reply.mutate(
          { parentId, content },
          {
            onSuccess: () => setExpanded(true),
          },
        )
      }}
      onDelete={(commentId, isRoot) => del.mutate({ commentId, rootId: comment.commentId, isRoot })}
      canDelete={canDelete(comment.user.uid)}
    />
  )
}

/** 真实帖子详情：媒体画廊（左固定）+ 正文/互动/评论流（右独立滚动） */
export default function PostPage() {
  const { id = '' } = useParams()
  const { data: post, isLoading, isError, error } = usePost(id)
  const { data: interaction } = useMyInteraction()
  const toggleLike = useToggleLike()
  const toggleFav = useToggleFavourite()
  const toggleFollow = useToggleFollow()

  const commentsQuery = useInfiniteComments(id)
  const comments = useMemo(() => commentsQuery.data?.pages.flatMap((p) => p.comments) ?? [], [commentsQuery.data])

  // 评论发布/删除
  const { data: me } = useMyProfile()
  const addComment = useAddComment(id)
  const [draft, setDraft] = useState('')
  const myUid = me?.brief?.uid ?? ''
  // 删除权限：评论作者或帖子作者（契约 DeleteComment）
  const canDelete = (uid: string) => Boolean(myUid) && (uid === myUid || post?.author.uid === myUid)

  // 互动态统一预取集合本地判态（点赞/收藏/关注，不逐请求查询）
  const liked = interaction?.liked.has(id) ?? false
  const favourited = interaction?.favourited.has(id) ?? false
  const following = post ? (interaction?.following.has(post.author.uid) ?? false) : false

  if (isLoading) {
    return (
      <div className="flex min-h-svh items-center justify-center text-muted-foreground">
        <p className="text-sm">加载中…</p>
      </div>
    )
  }
  if (isError || !post) {
    return (
      <div className="flex min-h-svh flex-col items-center justify-center gap-2 text-muted-foreground">
        <p className="text-sm text-destructive">{describeError(error)}</p>
        <a href="/" className={buttonVariants({ variant: 'outline' })}>
          返回信息流
        </a>
      </div>
    )
  }

  return (
    // 桌面（lg）：视口定高——左图固定，右侧内容+评论独立滚动；移动端整页滚动
    <div className="min-h-svh bg-muted/30 lg:flex lg:h-svh lg:flex-col lg:overflow-hidden">
      <AppHeader />
      <main className="mx-auto grid w-full max-w-6xl gap-6 px-4 py-6 lg:min-h-0 lg:flex-1 lg:grid-cols-[minmax(0,1fr)_400px] lg:py-4">
        {/* 左：图集（桌面固定） */}
        <MediaGallery medias={post.medias} />

        {/* 右：信息 + 评论流（桌面独立滚动） */}
        <div className="flex min-w-0 flex-col lg:min-h-0 lg:overflow-y-auto lg:pr-1">
          {/* 作者行（点击进作者主页） */}
          <header className="flex items-center gap-3">
            <Link to={`/user/${post.author.uid}`} className="flex min-w-0 flex-1 items-center gap-3">
              {post.author.avatarUrl ? (
                <img src={post.author.avatarUrl} alt={post.author.name} className="size-10 rounded-full object-cover" />
              ) : (
                <div className="flex size-10 items-center justify-center rounded-full bg-primary/10 text-primary">{post.author.name.slice(0, 1)}</div>
              )}
              <div className="min-w-0 flex-1">
                <p className="truncate text-sm font-medium">{post.author.name}</p>
                <p className="text-xs text-muted-foreground">{fromNow(post.createdAt)}</p>
              </div>
            </Link>
            <button
              type="button"
              onClick={() => toggleFollow.mutate(post.author.uid)}
              disabled={toggleFollow.isPending}
              className={buttonVariants({ variant: following ? 'outline' : 'default', size: 'sm', className: 'rounded-full' })}
            >
              {following ? '已关注' : '关注'}
            </button>
          </header>

          {/* 正文 */}
          <section className="mt-4">
            <h1 className="text-lg leading-7 font-semibold">{post.title}</h1>
            <p className="mt-2 text-sm leading-6 whitespace-pre-wrap">{post.fullText}</p>
          </section>

          {/* 评论流 */}
          <section className="mt-6 flex-1">
            <h2 className="text-sm font-medium text-muted-foreground">共 {post.stat.commentCount} 条评论</h2>
            <div className="mt-1 divide-y">
              {comments.map((c) => (
                <CommentThread key={c.commentId} comment={c} postId={id} canDelete={canDelete} />
              ))}
            </div>
            {commentsQuery.hasNextPage ? (
              <button
                type="button"
                onClick={() => void commentsQuery.fetchNextPage()}
                disabled={commentsQuery.isFetchingNextPage}
                className="w-full py-4 text-sm text-muted-foreground transition-colors hover:text-foreground disabled:opacity-60"
              >
                {commentsQuery.isFetchingNextPage ? '加载中…' : '加载更多评论'}
              </button>
            ) : null}
            {!commentsQuery.hasNextPage && comments.length > 0 ? (
              <p className="py-4 text-center text-xs text-muted-foreground">— 没有更多了 —</p>
            ) : null}
          </section>

          {/* 底部互动栏（sticky 贴右列滚动容器底部） */}
          <div className="sticky bottom-0 -mx-1 mt-4 flex items-center gap-2 rounded-full border bg-background/90 p-1.5 pl-4 shadow-sm backdrop-blur">
            {/* 评论发布（Enter/发送按钮） */}
            <form
              className="flex min-w-0 flex-1 items-center gap-2"
              onSubmit={(e: FormEvent) => {
                e.preventDefault()
                const content = draft.trim()
                if (!content) return
                addComment.mutate(content, {
                  onSuccess: () => setDraft(''),
                })
              }}
            >
              <input
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
                placeholder="说点什么…"
                disabled={addComment.isPending}
                className="min-w-0 flex-1 bg-transparent text-sm outline-none placeholder:text-muted-foreground disabled:opacity-60"
              />
              <button
                type="submit"
                disabled={!draft.trim() || addComment.isPending}
                aria-label="发布评论"
                className="px-1 text-muted-foreground transition-colors hover:text-primary disabled:opacity-40"
              >
                <Send className="size-4" />
              </button>
            </form>
            <button
              type="button"
              onClick={() => toggleLike.mutate(id)}
              disabled={toggleLike.isPending}
              className="flex items-center gap-1.5 px-2 text-sm text-muted-foreground transition-colors hover:text-foreground disabled:opacity-60"
            >
              <Heart className={`size-4 ${liked ? 'fill-primary text-primary' : ''}`} />
              {post.stat.likeCount}
            </button>
            <button
              type="button"
              onClick={() => toggleFav.mutate(id)}
              disabled={toggleFav.isPending}
              className="flex items-center gap-1.5 px-2 text-sm text-muted-foreground transition-colors hover:text-foreground disabled:opacity-60"
            >
              <Bookmark className={`size-4 ${favourited ? 'fill-primary text-primary' : ''}`} />
              {post.stat.favouriteCount}
            </button>
            <span className="flex items-center gap-1.5 px-2 text-sm text-muted-foreground">
              <MessageCircle className="size-4" />
              {post.stat.commentCount}
            </span>
          </div>
        </div>
      </main>
    </div>
  )
}
