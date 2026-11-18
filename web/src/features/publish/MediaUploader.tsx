import { useEffect, useRef, useState } from 'react'
import { ImagePlus, Star, X } from 'lucide-react'
import { gradientImage } from '@/mocks/data'

export interface MediaItem {
  id: string
  url: string
  /** 本地文件本体（真实上传用；合成示例图无 file） */
  file?: File
}

interface Props {
  medias: MediaItem[]
  onChange: (medias: MediaItem[]) => void
  /** mock 模式可添加合成示例图；真实发布传 false（示例图无文件本体无法上传） */
  allowSample?: boolean
}

/** 发布器媒体九宫格：本地选图预览 + 合成示例图；可移除、可设封面（首图为默认封面） */
export default function MediaUploader({ medias, onChange, allowSample = true }: Props) {
  const fileInputRef = useRef<HTMLInputElement>(null)
  // 本地文件的 objectURL 需要在卸载时释放，避免内存泄漏
  const objectUrlsRef = useRef<Set<string>>(new Set())
  const [notice, setNotice] = useState('')

  useEffect(() => {
    const urls = objectUrlsRef.current
    return () => {
      for (const u of urls) URL.revokeObjectURL(u)
    }
  }, [])

  function addFiles(files: FileList | null) {
    if (!files?.length) return
    const items = Array.from(files)
      .filter((f) => f.type.startsWith('image/'))
      .map((f) => {
        const url = URL.createObjectURL(f)
        objectUrlsRef.current.add(url)
        return { id: url, url, file: f }
      })
    if (items.length === 0) {
      // 空 catch 禁止：给用户可操作的反馈
      setNotice('仅支持图片文件')
      return
    }
    setNotice('')
    // mock：最多 9 张（对齐小红书九宫格上限）
    onChange([...medias, ...items].slice(0, 9))
  }

  function addSample() {
    if (medias.length >= 9) return
    const seed = Date.now() % 100000
    setNotice('')
    onChange([...medias, { id: `sample-${seed}`, url: gradientImage(seed, 400, 500) }])
  }

  function remove(id: string) {
    onChange(medias.filter((m) => m.id !== id))
  }

  /** 设为封面：移到首位（封面即首图，对齐 BFF 帖子封面语义） */
  function setCover(id: string) {
    const target = medias.find((m) => m.id === id)
    if (!target) return
    onChange([target, ...medias.filter((m) => m.id !== id)])
  }

  return (
    <div>
      <div className="grid grid-cols-3 gap-2">
        {medias.map((m, i) => (
          <div key={m.id} className="group relative aspect-square overflow-hidden rounded-lg bg-muted">
            <img src={m.url} alt={`待发布图片 ${i + 1}`} className="size-full object-cover" />
            <div className="absolute inset-x-0 top-0 flex items-start justify-between p-1.5">
              {i === 0 ? (
                <span className="rounded-full bg-primary px-2 py-0.5 text-xs text-primary-foreground">封面</span>
              ) : (
                <button
                  type="button"
                  onClick={() => setCover(m.id)}
                  className="flex items-center gap-1 rounded-full bg-background/80 px-2 py-0.5 text-xs opacity-0 backdrop-blur transition-opacity group-hover:opacity-100"
                >
                  <Star className="size-3" />
                  设封面
                </button>
              )}
              <button
                type="button"
                aria-label="移除图片"
                onClick={() => remove(m.id)}
                className="grid size-6 place-items-center rounded-full bg-background/80 backdrop-blur transition-opacity group-hover:opacity-100 md:opacity-0"
              >
                <X className="size-3.5" />
              </button>
            </div>
          </div>
        ))}
        {medias.length < 9 ? (
          <button
            type="button"
            onClick={() => fileInputRef.current?.click()}
            className="grid aspect-square place-items-center gap-1 rounded-lg border-2 border-dashed text-muted-foreground transition-colors hover:border-primary hover:text-primary"
          >
            <ImagePlus className="size-6" />
            <span className="text-xs">选图</span>
          </button>
        ) : null}
      </div>

      <input ref={fileInputRef} type="file" accept="image/*" multiple hidden onChange={(e) => addFiles(e.target.files)} />

      <div className="mt-2 flex items-center gap-3 text-xs text-muted-foreground">
        {allowSample ? (
          <button type="button" onClick={addSample} className="text-primary underline-offset-2 hover:underline">
            + 添加示例图
          </button>
        ) : null}
        <span>{medias.length} / 9 · 首图为封面</span>
        {notice ? <span className="text-destructive">{notice}</span> : null}
      </div>
    </div>
  )
}
