import { useMemo, useState } from 'react'
import AppHeader from '@/components/layout/AppHeader'
import FeedTabs from '@/features/feed/FeedTabs'
import MasonryFeed from '@/features/feed/MasonryFeed'
import type { FeedTab } from '@/features/models'
import { useInfiniteFeed } from '@/api/hooks'
import { describeError } from '@/api/errors'

/** 真实信息流：推荐/热门/关注三 tab，游标分页 + 稳定瀑布流 */
export default function FeedPage() {
  const [tab, setTab] = useState<FeedTab>('recommend')
  const { data, isLoading, isError, error, fetchNextPage, hasNextPage, isFetchingNextPage } = useInfiniteFeed(tab)

  const posts = useMemo(() => data?.pages.flatMap((p) => p.posts) ?? [], [data])

  return (
    <div className="min-h-svh bg-muted/30">
      <AppHeader />
      <main className="mx-auto w-full max-w-7xl px-4 py-6">
        <FeedTabs active={tab} onChange={setTab} />
        {isError ? (
          <div className="flex flex-col items-center gap-2 py-24 text-muted-foreground">
            <p className="text-sm text-destructive">{describeError(error)}</p>
            <button type="button" className="text-sm text-primary underline-offset-4 hover:underline" onClick={() => window.location.reload()}>
              重试
            </button>
          </div>
        ) : (
          <div className="pt-6">
            <MasonryFeed
              posts={posts}
              hasMore={Boolean(hasNextPage)}
              loading={isLoading || isFetchingNextPage}
              onLoadMore={() => void fetchNextPage()}
              cardBasePath="/post/"
            />
          </div>
        )}
      </main>
    </div>
  )
}
