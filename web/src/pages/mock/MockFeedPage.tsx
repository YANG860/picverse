import { useEffect, useMemo, useState } from 'react'
import AppHeader from '@/components/layout/AppHeader'
import FeedTabs from '@/features/feed/FeedTabs'
import MasonryFeed from '@/features/feed/MasonryFeed'
import type { FeedTab, PostCard } from '@/features/models'
import { MAX_PAGE, listFeed } from '@/mocks/data'

/** mock 信息流页：纯合成数据，仅用于视觉效果验证 */
export default function MockFeedPage() {
  const [tab, setTab] = useState<FeedTab>('recommend')
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(false)

  // 刷新回顶：浏览器默认恢复历史滚动位置，但刷新后只有首屏内容，
  // 会引发「位置被夹 → 哨兵连续自动加载 → 瀑布流反复重排」的抖动
  useEffect(() => {
    const prev = history.scrollRestoration
    history.scrollRestoration = 'manual'
    window.scrollTo(0, 0)
    return () => {
      history.scrollRestoration = prev
    }
  }, [])

  const posts = useMemo<PostCard[]>(() => {
    const out: PostCard[] = []
    for (let p = 1; p <= page; p++) out.push(...listFeed(tab, p))
    return out
  }, [tab, page])

  const hasMore = page < MAX_PAGE

  function loadMore() {
    if (loading || !hasMore) return
    setLoading(true)
    // mock：加一点延迟观察"加载中"态
    window.setTimeout(() => {
      setPage((p) => p + 1)
      setLoading(false)
    }, 300)
  }

  function changeTab(t: FeedTab) {
    setTab(t)
    setPage(1)
  }

  return (
    <div className="min-h-svh bg-muted/30">
      <AppHeader />
      <main className="mx-auto w-full max-w-7xl px-4">
        <FeedTabs active={tab} onChange={changeTab} />
        <div className="pt-6">
          <MasonryFeed posts={posts} hasMore={hasMore} loading={loading} onLoadMore={loadMore} />
        </div>
      </main>
    </div>
  )
}
