import { useState } from 'react'
import { Hash, X } from 'lucide-react'
import { useNavigate } from 'react-router-dom'
import AppHeader from '@/components/layout/AppHeader'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import MediaUploader, { type MediaItem } from '@/features/publish/MediaUploader'

const TITLE_MAX = 60
const CONTENT_MAX = 1000
const TOPIC_MAX = 5

/** mock 发布页：对应后端「草稿 → PublishDraft」两步流（纯本地态，不接 API） */
export default function MockPublishPage() {
  const navigate = useNavigate()

  const [medias, setMedias] = useState<MediaItem[]>([])
  const [title, setTitle] = useState('')
  const [content, setContent] = useState('')
  const [topicInput, setTopicInput] = useState('')
  const [topics, setTopics] = useState<string[]>([])
  const [notice, setNotice] = useState('')
  const [submitting, setSubmitting] = useState(false)

  const canSubmit = medias.length > 0 && title.trim().length > 0 && !submitting

  function addTopic() {
    const t = topicInput.trim().replace(/^#/, '')
    if (!t) return
    if (topics.length >= TOPIC_MAX) {
      setNotice(`最多 ${TOPIC_MAX} 个话题`)
      return
    }
    if (topics.includes(t)) {
      setNotice('话题已存在')
      return
    }
    setNotice('')
    setTopics([...topics, t])
    setTopicInput('')
  }

  /** 存草稿：mock 仅提示（真实链路 → CreateDraft / UpdateDraft） */
  function saveDraft() {
    setNotice('已存草稿（mock）')
  }

  /** 发布：mock 模拟「发布 → 审核中」两步流，成功后跳详情页看效果 */
  function publish() {
    if (!canSubmit) return
    setSubmitting(true)
    setNotice('')
    window.setTimeout(() => {
      setSubmitting(false)
      navigate(`/mock/post/${Date.now() % 100000}`)
    }, 800)
  }

  return (
    <div className="min-h-svh bg-muted/30">
      <AppHeader />
      <main className="mx-auto w-full max-w-5xl px-4 py-6">
        <h1 className="text-xl font-semibold">发布笔记</h1>

        <div className="mt-6 grid gap-8 lg:grid-cols-2">
          {/* 左：媒体九宫格 */}
          <MediaUploader medias={medias} onChange={setMedias} />

          {/* 右：标题 / 正文 / 话题 */}
          <div className="flex flex-col gap-5">
            <div>
              <div className="flex items-baseline justify-between">
                <label htmlFor="title" className="text-sm font-medium">
                  标题
                </label>
                <span className="text-xs text-muted-foreground">
                  {title.length} / {TITLE_MAX}
                </span>
              </div>
              <Input
                id="title"
                placeholder="填一个有吸引力的标题吧～"
                maxLength={TITLE_MAX}
                className="mt-1.5"
                value={title}
                onChange={(e) => setTitle(e.target.value)}
              />
            </div>

            <div>
              <div className="flex items-baseline justify-between">
                <label htmlFor="content" className="text-sm font-medium">
                  正文
                </label>
                <span className="text-xs text-muted-foreground">
                  {content.length} / {CONTENT_MAX}
                </span>
              </div>
              <Textarea
                id="content"
                placeholder="分享你的经验、心得或教程…"
                maxLength={CONTENT_MAX}
                rows={8}
                className="mt-1.5 resize-none"
                value={content}
                onChange={(e) => setContent(e.target.value)}
              />
            </div>

            <div>
              <label htmlFor="topic" className="text-sm font-medium">
                话题
              </label>
              <div className="mt-1.5 flex gap-2">
                <Input
                  id="topic"
                  placeholder="输入话题后回车添加"
                  value={topicInput}
                  onChange={(e) => setTopicInput(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') {
                      e.preventDefault()
                      addTopic()
                    }
                  }}
                />
                <Button type="button" variant="secondary" onClick={addTopic}>
                  添加
                </Button>
              </div>
              {topics.length ? (
                <div className="mt-2 flex flex-wrap gap-2">
                  {topics.map((t) => (
                    <span key={t} className="flex items-center gap-1 rounded-full bg-primary/10 px-2.5 py-1 text-xs text-primary">
                      <Hash className="size-3" />
                      {t}
                      <button
                        type="button"
                        aria-label={`移除话题 ${t}`}
                        onClick={() => setTopics(topics.filter((x) => x !== t))}
                        className="text-primary/60 transition-colors hover:text-primary"
                      >
                        <X className="size-3" />
                      </button>
                    </span>
                  ))}
                </div>
              ) : null}
            </div>
          </div>
        </div>

        {/* 底部操作条 */}
        <div className="sticky bottom-4 mt-8 flex items-center gap-3 rounded-full border bg-background/90 p-2 pl-5 shadow-sm backdrop-blur">
          <span className="mr-auto text-xs text-muted-foreground">
            {notice ? notice : '草稿会保留在草稿箱，发布后进入审核'}
          </span>
          <Button variant="outline" className="rounded-full" onClick={saveDraft} disabled={submitting}>
            存草稿
          </Button>
          <Button className="rounded-full" onClick={publish} disabled={!canSubmit}>
            {submitting ? '发布中…' : '发布'}
          </Button>
        </div>
      </main>
    </div>
  )
}
