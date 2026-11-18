import { useState } from 'react'
import { ChevronLeft, ChevronRight } from 'lucide-react'
import type { MediaView } from '@/features/models'

/** 详情页图集：主图 + 左右切换 + 缩略图（视觉稿） */
export default function MediaGallery({ medias }: { medias: MediaView[] }) {
  const [index, setIndex] = useState(0)
  const current = medias[index]

  const go = (delta: number) => setIndex((i) => (i + delta + medias.length) % medias.length)

  return (
    // 移动端：整页滚动流内的定宽比图集；桌面（lg）：填满左栏高度（左图固定，右侧独立滚动）
    <div className="flex aspect-[4/5] flex-col overflow-hidden rounded-2xl bg-muted lg:h-full lg:aspect-auto">
      <div className="relative flex min-h-0 flex-1 items-center justify-center bg-muted">
        <img
          src={current.url || undefined}
          alt={`图片 ${index + 1}`}
          className="size-full object-contain"
          onError={(e) => {
            e.currentTarget.style.visibility = 'hidden'
          }}
        />
        {medias.length > 1 ? (
          <>
            <button
              type="button"
              aria-label="上一张"
              onClick={() => go(-1)}
              className="absolute left-3 grid size-9 place-items-center rounded-full bg-background/70 text-foreground backdrop-blur transition-opacity hover:opacity-90"
            >
              <ChevronLeft className="size-5" />
            </button>
            <button
              type="button"
              aria-label="下一张"
              onClick={() => go(1)}
              className="absolute right-3 grid size-9 place-items-center rounded-full bg-background/70 text-foreground backdrop-blur transition-opacity hover:opacity-90"
            >
              <ChevronRight className="size-5" />
            </button>
          </>
        ) : null}
        <span className="absolute top-3 right-3 rounded-full bg-background/70 px-2.5 py-1 text-xs text-foreground backdrop-blur">
          {index + 1} / {medias.length}
        </span>
      </div>
      {medias.length > 1 ? (
        <div className="flex gap-2 overflow-x-auto p-2">
          {medias.map((m, i) => (
            <button
              key={m.url}
              type="button"
              onClick={() => setIndex(i)}
              className={
                'size-14 shrink-0 overflow-hidden rounded-lg ring-2 transition-opacity ' +
                (i === index ? 'ring-primary' : 'ring-transparent opacity-60 hover:opacity-100')
              }
            >
              <img
                src={m.url || undefined}
                alt={`缩略图 ${i + 1}`}
                className="size-full object-cover"
                onError={(e) => {
                  e.currentTarget.style.visibility = 'hidden'
                }}
              />
            </button>
          ))}
        </div>
      ) : null}
    </div>
  )
}
