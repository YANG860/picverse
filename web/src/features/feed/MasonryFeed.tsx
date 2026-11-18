import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import type { PostCard } from '@/features/models'
import FeedCard from '@/features/feed/FeedCard'

interface Props {
  posts: PostCard[]
  hasMore: boolean
  loading?: boolean
  onLoadMore: () => void
  /** 卡片详情链接前缀（真实 /post/，mock 用 /mock/post/） */
  cardBasePath?: string
  /** 空态文案（posts 为空时展示；默认「暂无内容」） */
  emptyText?: string
}

/** 视口宽度 → 列数（断点对齐 Tailwind：sm=640px / xl=1280px，与原 columns-2/sm:3/xl:4 一致） */
function useColumnCount(): number {
  // 初始即取真实列数（客户端渲染，无 SSR）；边界偏差在 layout 阶段同步修正，不产生可见位移
  const [count, setCount] = useState(() => columnCountFor(window.innerWidth))
  useLayoutEffect(() => {
    const mqSm = window.matchMedia('(min-width: 640px)')
    const mqXl = window.matchMedia('(min-width: 1280px)')
    const update = () => setCount(mqXl.matches ? 4 : mqSm.matches ? 3 : 2)
    update()
    mqSm.addEventListener('change', update)
    mqXl.addEventListener('change', update)
    return () => {
      mqSm.removeEventListener('change', update)
      mqXl.removeEventListener('change', update)
    }
  }, [])
  return count
}

/**
 * 贪心按列分配（稳定瀑布流）：
 * 按输入顺序把卡片放到当前累计高度最矮的列，前缀分配结果只依赖前缀，
 * 因此追加新卡片不会移动已有卡片（根治 CSS columns 增量重排抖动）。
 * 高度用封面比例 + 文字区近似值估计，仅用于均衡，不要求精确。
 */
function distribute(posts: PostCard[], n: number): PostCard[][] {
  const cols: PostCard[][] = Array.from({ length: n }, () => [])
  const heights = new Array<number>(n).fill(0)
  for (const p of posts) {
    // 尺寸缺失（0/0）时用默认比例兜底：NaN 会污染贪心比较，导致全部卡片进第一列
    const ratio = p.cover.width > 0 && p.cover.height > 0 ? p.cover.height / p.cover.width : 1.25
    const h = ratio + 0.22
    let target = 0
    for (let i = 1; i < n; i++) {
      if (heights[i] < heights[target]) target = i
    }
    cols[target].push(p)
    heights[target] += h
  }
  return cols
}

/** 视口宽度 → 列数（供初始值用） */
function columnCountFor(width: number): number {
  return width < 640 ? 2 : width < 1280 ? 3 : 4
}

/** 稳定瀑布流：多列 flex 布局 + 贪心分列 + 触底自动加载（IntersectionObserver） */
export default function MasonryFeed({ posts, hasMore, loading, onLoadMore, cardBasePath, emptyText = '暂无内容' }: Props) {
  const sentinelRef = useRef<HTMLDivElement>(null)
  const loadMoreRef = useRef(onLoadMore)
  // 加载条件用 ref 持续同步；触发点：① 哨兵进入视口（滚动）② 加载完成后主动补查（哨兵仍在视口则继续填充）
  const gateRef = useRef({ hasMore, loading })
  useEffect(() => {
    loadMoreRef.current = onLoadMore
    gateRef.current = { hasMore, loading }
  }, [onLoadMore, hasMore, loading])

  const columnCount = useColumnCount()
  const columns = useMemo(() => distribute(posts, columnCount), [posts, columnCount])

  useEffect(() => {
    const el = sentinelRef.current
    if (!el) return
    const tryLoad = () => {
      const { hasMore, loading } = gateRef.current
      if (!hasMore || loading) return
      // 补查必须自己判可见：哨兵被推出视口即停止填充（自然节流，不连环拉页）
      if (el.getBoundingClientRect().top >= window.innerHeight) return
      // 同步占位防同一帧重复触发（下一次 effect 同步回真实值）
      gateRef.current = { hasMore, loading: true }
      loadMoreRef.current()
    }
    const observer = new IntersectionObserver((entries) => {
      if (entries.some((e) => e.isIntersecting)) tryLoad()
    })
    observer.observe(el)
    // 加载完成后补查：哨兵持续可见（一页不满屏）时继续填充，直到被推出视口或到底
    tryLoad()
    return () => observer.disconnect()
  }, [hasMore, loading])

  return (
    <div>
      {posts.length === 0 ? (
        loading ? (
          <div className="flex flex-col items-center gap-2 py-24 text-muted-foreground">
            <p className="text-sm">加载中…</p>
          </div>
        ) : (
          <div className="flex flex-col items-center gap-2 py-24 text-muted-foreground">
            <p className="text-sm">{emptyText}</p>
          </div>
        )
      ) : (
        <>
          <div className="flex items-start gap-4">
            {columns.map((col, i) => (
              <div key={i} className="flex min-w-0 flex-1 flex-col">
                {col.map((p) => (
                  <FeedCard key={p.postId} post={p} basePath={cardBasePath} />
                ))}
              </div>
            ))}
          </div>
          {/* 定高哨兵：文档流内（不可 sticky——sticky 常驻视口会让交叉状态永不变化，无法触发加载） */}
          <div ref={sentinelRef} className="flex h-20 items-center justify-center text-sm text-muted-foreground">
            {loading ? '加载中…' : hasMore ? '下滑加载更多' : '— 没有更多了 —'}
          </div>
        </>
      )}
    </div>
  )
}
