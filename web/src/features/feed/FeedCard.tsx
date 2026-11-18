import { Heart } from 'lucide-react'
import { Link, useNavigate } from 'react-router-dom'
import type { PostCard } from '@/features/models'

/** 状态角标：草稿/审核中（仅“我的笔记”场景出现） */
const STATUS_BADGE: Record<string, { label: string; className: string }> = {
  DRAFT: { label: '草稿', className: 'bg-foreground/70 text-background' },
  AUDITING: { label: '审核中', className: 'bg-amber-500/90 text-white' },
}

/** 瀑布流卡片：封面（按原始比例占位防抖动）+ 标题 + 作者/点赞行 */
export default function FeedCard({ post, basePath = '/mock/post/' }: { post: PostCard; basePath?: string }) {
  const navigate = useNavigate()
  const badge = post.status && post.status !== 'PUBLISHED' ? STATUS_BADGE[post.status] : undefined
  return (
    <Link
      to={`${basePath}${post.postId}`}
      className="group mb-4 block rounded-xl bg-card shadow-sm ring-1 ring-border/60 transition-shadow hover:shadow-md"
    >
      <div className="relative overflow-hidden rounded-t-xl bg-muted" style={{ aspectRatio: `${post.cover.width} / ${post.cover.height}` }}>
        <img
          src={post.cover.url || undefined}
          alt=""
          loading="lazy"
          className="size-full object-cover transition-transform duration-300 group-hover:scale-[1.03]"
          onError={(e) => {
            e.currentTarget.style.visibility = 'hidden'
          }}
        />
        {badge ? (
          <span className={`absolute top-2 left-2 rounded-full px-2 py-0.5 text-xs ${badge.className}`}>{badge.label}</span>
        ) : null}
      </div>
      <div className="space-y-2 p-3">
        <p className="line-clamp-2 text-sm leading-5 font-medium">{post.title}</p>
        <div className="flex items-center justify-between">
            {/* 作者主页入口（阻止冒泡避免触发卡片跳转） */}
            <button
              type="button"
              onClick={(e) => {
                e.preventDefault()
                e.stopPropagation()
                navigate(`/user/${post.author.uid}`)
              }}
              className="flex min-w-0 items-center gap-1.5 text-left"
            >
              <img
                src={post.author.avatarUrl || undefined}
                alt=""
                className="size-5 rounded-full bg-muted object-cover"
                onError={(e) => {
                  e.currentTarget.style.visibility = 'hidden'
                }}
              />
              <span className="truncate text-xs text-muted-foreground">{post.author.name}</span>
            </button>
          <div className="flex shrink-0 items-center gap-1 text-xs text-muted-foreground">
            <Heart className="size-3.5" />
            {post.stat.likeCount}
          </div>
        </div>
      </div>
    </Link>
  )
}
