import { useMemo, useState } from 'react'
import { Heart, MessageCircle, Star } from 'lucide-react'
import { Link, useParams } from 'react-router-dom'
import AppHeader from '@/components/layout/AppHeader'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import CommentItem from '@/features/post/CommentItem'
import MediaGallery from '@/features/post/MediaGallery'
import { getPostDetail, listComments } from '@/mocks/data'
import { formatDate, fromNow } from '@/lib/time'

/** mock 帖子详情页：纯合成数据，仅用于视觉效果验证 */
export default function MockPostPage() {
  const { id = '' } = useParams()
  const detail = useMemo(() => getPostDetail(id), [id])
  const comments = useMemo(() => listComments(id), [id])

  // 交互态仅本地生效（不接 API）
  const [liked, setLiked] = useState(false)
  const [collected, setCollected] = useState(false)
  const [following, setFollowing] = useState(detail.relation.isFollowing)

  const likeCount = detail.stat.likeCount + (liked ? 1 : 0)
  const favCount = detail.stat.favouriteCount + (collected ? 1 : 0)

  return (
    // 桌面（lg）：视口高度定高布局——左图固定，右侧内容+评论独立滚动；移动端整页滚动
    <div className="min-h-svh bg-muted/30 lg:flex lg:h-svh lg:flex-col lg:overflow-hidden">
      <AppHeader />
      <main className="mx-auto grid w-full max-w-6xl gap-6 px-4 py-6 lg:min-h-0 lg:flex-1 lg:grid-cols-[minmax(0,1fr)_400px] lg:py-4">
        {/* 左：图集（桌面固定，不随右侧滚动） */}
        <MediaGallery medias={detail.medias} />

        {/* 右：信息 + 评论流（桌面独立滚动） */}
        <div className="flex min-w-0 flex-col lg:min-h-0 lg:overflow-y-auto lg:pr-1">
          <div className="flex items-center gap-3">
            <img src={detail.author.avatarUrl} alt={detail.author.name} className="size-10 rounded-full object-cover" />
            <Link to="/mock" className="text-sm font-medium hover:underline">
              {detail.author.name}
            </Link>
            <Button
              size="sm"
              variant={following ? 'outline' : 'default'}
              className="ml-auto rounded-full"
              onClick={() => setFollowing((v) => !v)}
            >
              {following ? '已关注' : '关注'}
            </Button>
          </div>

          <h1 className="mt-4 text-lg leading-7 font-semibold">{detail.title}</h1>
          <p className="mt-3 text-sm leading-7 whitespace-pre-wrap">{detail.fullText}</p>
          <p className="mt-3 text-xs text-muted-foreground">
            {formatDate(detail.createdAt)} · {fromNow(detail.createdAt)} · {detail.stat.viewCount} 次浏览
          </p>

          <div className="mt-4 border-t pt-4">
            <p className="text-sm font-medium">共 {comments.length} 条评论</p>
            <div className="mt-2 flex gap-2">
              <Input placeholder="说点什么…" className="rounded-full" />
              <Button variant="secondary" className="rounded-full">
                发送
              </Button>
            </div>
            <div className="mt-2 divide-y">
              {comments.map((c) => (
                <CommentItem key={c.commentId} comment={c} />
              ))}
            </div>
          </div>

          {/* 底部互动栏（sticky 贴右列滚动容器底部） */}
          <div className="sticky bottom-0 -mx-1 mt-4 flex items-center gap-2 rounded-full border bg-background/90 p-1.5 pl-4 shadow-sm backdrop-blur">
            <Input placeholder="说点什么…" className="h-9 rounded-full border-none bg-muted/60 shadow-none" />
            <Button
              variant="ghost"
              size="sm"
              className={liked ? 'text-red-500' : 'text-muted-foreground'}
              onClick={() => setLiked((v) => !v)}
            >
              <Heart className={liked ? 'fill-current' : ''} />
              {likeCount}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              className={collected ? 'text-amber-500' : 'text-muted-foreground'}
              onClick={() => setCollected((v) => !v)}
            >
              <Star className={collected ? 'fill-current' : ''} />
              {favCount}
            </Button>
            <Button variant="ghost" size="sm" className="text-muted-foreground">
              <MessageCircle />
              {detail.stat.commentCount}
            </Button>
          </div>
        </div>
      </main>
    </div>
  )
}
