import { useState, type FormEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { X } from 'lucide-react'
import AppHeader from '@/components/layout/AppHeader'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import MediaUploader, { type MediaItem } from '@/features/publish/MediaUploader'
import { useSubmitPost } from '@/api/publish'

const MAX_TITLE = 60
const MAX_TEXT = 1000
const MAX_TOPICS = 5

/** 真实发布器：选图直传 MinIO（预签名）→ CreateDraft → PublishDraft（两步发布） */
export default function PublishPage() {
  const navigate = useNavigate()
  const submit = useSubmitPost()

  const [medias, setMedias] = useState<MediaItem[]>([])
  const [title, setTitle] = useState('')
  const [fullText, setFullText] = useState('')
  const [topics, setTopics] = useState<string[]>([])
  const [topicDraft, setTopicDraft] = useState('')
  const [notice, setNotice] = useState('')

  function addTopic() {
    const t = topicDraft.trim().replace(/^#/, '')
    if (!t) return
    if (topics.length >= MAX_TOPICS) {
      setNotice(`最多 ${MAX_TOPICS} 个话题`)
      return
    }
    if (topics.includes(t)) {
      setNotice('话题已存在')
      return
    }
    setTopics([...topics, t])
    setTopicDraft('')
    setNotice('')
  }

  function onSubmit(publish: boolean, e?: FormEvent) {
    e?.preventDefault()
    // 真实上传只接受本地文件（合成示例图无 file，过滤）
    const files = medias.map((m) => m.file).filter((f): f is File => Boolean(f))
    if (files.length === 0) {
      setNotice('请至少添加一张图片')
      return
    }
    if (!title.trim()) {
      setNotice('请填写标题')
      return
    }
    setNotice('')
    submit.mutate(
      { files, title: title.trim(), fullText: fullText.trim(), topics, publish },
      {
        onSuccess: ({ published }) => {
          navigate('/user/me', { replace: true, state: { notice: published ? '已发布，审核通过后将对外展示' : '草稿已保存' } })
        },
        onError: (err) => {
          setNotice(err instanceof Error ? err.message : '发布失败，请重试')
        },
      },
    )
  }

  return (
    <div className="min-h-svh bg-muted/30">
      <AppHeader />
      <main className="mx-auto w-full max-w-2xl px-4 py-8">
        <h1 className="text-xl font-semibold">发布笔记</h1>
        <p className="mt-1 text-sm text-muted-foreground">图片直传对象存储，保存草稿或直接发布（发布后进入审核）</p>

        <div className="mt-6 grid gap-5">
          <div>
            <Label className="mb-2 block">图片（最多 9 张，首图为封面）</Label>
            <MediaUploader medias={medias} onChange={setMedias} allowSample={false} />
          </div>

          <div>
            <div className="mb-2 flex items-center justify-between">
              <Label htmlFor="title">标题</Label>
              <span className="text-xs text-muted-foreground">
                {title.length}/{MAX_TITLE}
              </span>
            </div>
            <Input
              id="title"
              value={title}
              maxLength={MAX_TITLE}
              onChange={(e) => setTitle(e.target.value)}
              placeholder="填个标题会有更多赞哦～"
            />
          </div>

          <div>
            <div className="mb-2 flex items-center justify-between">
              <Label htmlFor="content">正文</Label>
              <span className="text-xs text-muted-foreground">
                {fullText.length}/{MAX_TEXT}
              </span>
            </div>
            <Textarea
              id="content"
              value={fullText}
              maxLength={MAX_TEXT}
              rows={8}
              onChange={(e) => setFullText(e.target.value)}
              placeholder="添加正文…"
            />
          </div>

          <div>
            <Label htmlFor="topic">话题（最多 {MAX_TOPICS} 个）</Label>
            <div className="mt-2 flex flex-wrap items-center gap-2">
              {topics.map((t) => (
                <span key={t} className="flex items-center gap-1 rounded-full bg-primary/10 px-3 py-1 text-sm text-primary">
                  #{t}
                  <button type="button" aria-label={`移除话题 ${t}`} onClick={() => setTopics(topics.filter((x) => x !== t))}>
                    <X className="size-3.5" />
                  </button>
                </span>
              ))}
            </div>
            <div className="mt-2 flex gap-2">
              <Input
                id="topic"
                value={topicDraft}
                onChange={(e) => setTopicDraft(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.preventDefault()
                    addTopic()
                  }
                }}
                placeholder="输入话题后回车"
              />
              <Button type="button" variant="outline" onClick={addTopic}>
                添加
              </Button>
            </div>
          </div>

          {notice ? <p className="text-sm text-destructive">{notice}</p> : null}

          <div className="flex items-center gap-3 pb-8">
            <Button type="button" variant="outline" disabled={submit.isPending} onClick={() => onSubmit(false)}>
              {submit.isPending ? '处理中…' : '存草稿'}
            </Button>
            <Button type="button" disabled={submit.isPending} onClick={() => onSubmit(true)}>
              {submit.isPending ? '处理中…' : '发布'}
            </Button>
            <span className="text-xs text-muted-foreground">图片上传与处理约需数秒</span>
          </div>
        </div>
      </main>
    </div>
  )
}
