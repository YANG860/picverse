import { useState } from 'react'
import { CornerDownRight } from 'lucide-react'
import type { CommentView } from '@/features/models'
import { fromNow } from '@/lib/time'

interface Props {
  comment: CommentView
  /** 懒加载楼内回复（真实 API：GetReplies）；缺省时展示 comment.replies（mock 全量模式） */
  onLoadReplies?: () => void
  repliesLoading?: boolean
  /** 回复某条评论（parentId = 被回复评论 id）；缺省不显示回复入口（mock） */
  onReply?: (parentId: string, content: string) => void
  /** 删除本条评论（自己的评论/帖子作者可见） */
  onDelete?: (commentId: string, isRoot: boolean) => void
  /** 是否展示删除入口（帖子作者可删任意评论） */
  canDelete?: boolean
}

/** 回复输入（小尺寸内联输入） */
function ReplyBox({ onSubmit, onCancel }: { onSubmit: (content: string) => void; onCancel: () => void }) {
  const [content, setContent] = useState('')
  return (
    <div className="mt-2 flex items-center gap-2">
      <input
        autoFocus
        value={content}
        onChange={(e) => setContent(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && content.trim()) {
            onSubmit(content.trim())
            setContent('')
          }
          if (e.key === 'Escape') onCancel()
        }}
        placeholder="回复…"
        className="min-w-0 flex-1 rounded-full border bg-background px-3 py-1.5 text-xs outline-none focus:ring-1 focus:ring-primary"
      />
      <button
        type="button"
        disabled={!content.trim()}
        onClick={() => {
          onSubmit(content.trim())
          setContent('')
        }}
        className="text-xs text-primary transition-opacity hover:opacity-80 disabled:opacity-40"
      >
        发送
      </button>
      <button type="button" onClick={onCancel} className="text-xs text-muted-foreground transition-colors hover:text-foreground">
        取消
      </button>
    </div>
  )
}

/** 单条评论（含楼中楼回复，对齐 CommentView 两级语义） */
export default function CommentItem({ comment, onLoadReplies, repliesLoading, onReply, onDelete, canDelete }: Props) {
  // 回复目标：'' 表示未展开；'root' 回复楼层，否则回复楼内某条
  const [replyTo, setReplyTo] = useState('')
  return (
    <div className="flex gap-3 py-3">
      <img
        src={comment.user.avatarUrl || undefined}
        alt=""
        className="size-8 shrink-0 rounded-full bg-muted object-cover"
        onError={(e) => {
          e.currentTarget.style.visibility = 'hidden'
        }}
      />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="text-sm font-medium">{comment.user.name}</span>
          <span className="text-xs text-muted-foreground">{fromNow(comment.createdAt)}</span>
        </div>
        <p className="mt-1 text-sm leading-6 break-words whitespace-pre-wrap">
          {comment.replyToUser ? (
            <span className="text-muted-foreground">
              回复 <span className="text-primary">@{comment.replyToUser.name}</span>：
            </span>
          ) : null}
          {comment.content}
        </p>
        <div className="mt-1.5 flex items-center gap-4 text-xs text-muted-foreground">
          {/* 评论点赞契约未提供（无 LikeComment/like_count）——按“没有就放弃”不展示假入口 */}
          {comment.replyCount > 0 && !comment.replies?.length ? (
            <button type="button" onClick={onLoadReplies} disabled={repliesLoading} className="transition-colors hover:text-foreground disabled:opacity-60">
              {repliesLoading ? '回复加载中…' : `共 ${comment.replyCount} 条回复`}
            </button>
          ) : null}
          {onReply ? (
            <button type="button" onClick={() => setReplyTo(replyTo ? '' : 'root')} className="transition-colors hover:text-foreground">
              回复
            </button>
          ) : null}
          {onDelete && canDelete ? (
            <button type="button" onClick={() => onDelete(comment.commentId, true)} className="transition-colors hover:text-destructive">
              删除
            </button>
          ) : null}
        </div>

        {replyTo === 'root' && onReply ? <ReplyBox onSubmit={(content) => onReply(comment.commentId, content)} onCancel={() => setReplyTo('')} /> : null}

        {comment.replies?.length ? (
          <div className="mt-2 space-y-2 rounded-lg bg-muted/60 p-3">
            {comment.replies.map((r) => (
              <div key={r.commentId} className="flex gap-2">
                <CornerDownRight className="mt-1 size-3.5 shrink-0 text-muted-foreground" />
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span className="text-xs font-medium">{r.user.name}</span>
                    <span className="text-xs text-muted-foreground">{fromNow(r.createdAt)}</span>
                  </div>
                  <p className="mt-0.5 text-sm leading-6 break-words">
                    {r.replyToUser ? (
                      <span className="text-muted-foreground">
                        回复 <span className="text-primary">@{r.replyToUser.name}</span>：
                      </span>
                    ) : null}
                    {r.content}
                  </p>
                  <div className="mt-1 flex items-center gap-3 text-xs text-muted-foreground">
                    {onReply ? (
                      <button type="button" onClick={() => setReplyTo(replyTo === r.commentId ? '' : r.commentId)} className="transition-colors hover:text-foreground">
                        回复
                      </button>
                    ) : null}
                    {onDelete && canDelete ? (
                      <button type="button" onClick={() => onDelete(r.commentId, false)} className="transition-colors hover:text-destructive">
                        删除
                      </button>
                    ) : null}
                  </div>
                  {replyTo === r.commentId && onReply ? (
                    <ReplyBox onSubmit={(content) => onReply(r.commentId, content)} onCancel={() => setReplyTo('')} />
                  ) : null}
                </div>
              </div>
            ))}
          </div>
        ) : null}
      </div>
    </div>
  )
}
