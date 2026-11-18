import { useMemo, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { Search } from 'lucide-react'
import AppHeader from '@/components/layout/AppHeader'
import MasonryFeed from '@/features/feed/MasonryFeed'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { useSearchPosts } from '@/api/hooks'
import { describeError } from '@/api/errors'
import type { SearchSort } from '@gen/discover/v1/requests_pb'

const SORTS: { key: SearchSort; label: string }[] = [
  { key: 0, label: '综合' }, // SEARCH_SORT_UNSPECIFIED：相关度 × 时间衰减
  { key: 1, label: '最新' }, // SEARCH_SORT_LATEST：发布时间倒序
]

/** 搜索框（独立子组件：URL 关键词变化时用 key 重挂载重置草稿，避免 effect 里 setState） */
function SearchBar({ initialKeyword, sort, onSubmit }: { initialKeyword: string; sort: SearchSort; onSubmit: (q: string, s: SearchSort) => void }) {
  const [draft, setDraft] = useState(initialKeyword)
  const inputRef = useRef<HTMLInputElement>(null)
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        const q = draft.trim()
        if (!q) return
        onSubmit(q, sort)
        inputRef.current?.blur()
      }}
      className="mx-auto flex max-w-xl items-center gap-2"
    >
      <div className="relative flex-1">
        <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
        <Input ref={inputRef} value={draft} onChange={(e) => setDraft(e.target.value)} placeholder="搜索笔记（关键词）" className="rounded-full pl-9" maxLength={64} />
      </div>
      <Button type="submit" className="rounded-full">
        搜索
      </Button>
    </form>
  )
}

/** 真实搜索页：关键词 + 综合/最新排序，offset 游标翻页 + 稳定瀑布流（URL ?q=&sort= 驱动，刷新/分享友好） */
export default function SearchPage() {
  const [params, setParams] = useSearchParams()
  const keyword = params.get('q') ?? ''
  const sort: SearchSort = Number(params.get('sort') ?? 0) === 1 ? 1 : 0

  const { data, isLoading, isError, error, fetchNextPage, hasNextPage, isFetchingNextPage } = useSearchPosts(keyword, sort)
  const posts = useMemo(() => data?.pages.flatMap((p) => p.posts) ?? [], [data])

  const setSort = (s: SearchSort) => setParams({ q: keyword, sort: String(s) })

  return (
    <div className="min-h-svh bg-muted/30">
      <AppHeader />
      <main className="mx-auto w-full max-w-7xl px-4 py-6">
        <SearchBar key={keyword} initialKeyword={keyword} sort={sort} onSubmit={(q, s) => setParams({ q, sort: String(s) })} />

        {keyword && (
          <div className="mt-4 flex items-center gap-2">
            {SORTS.map((s) => (
              <button
                key={s.key}
                type="button"
                onClick={() => setSort(s.key)}
                className={`rounded-full px-4 py-1.5 text-sm transition-colors ${
                  sort === s.key ? 'bg-primary text-primary-foreground' : 'bg-muted text-muted-foreground hover:bg-muted/70'
                }`}
              >
                {s.label}
              </button>
            ))}
            <span className="ml-auto text-xs text-muted-foreground">“{keyword}” 的搜索结果</span>
          </div>
        )}

        {!keyword ? (
          <div className="flex flex-col items-center gap-2 py-24 text-muted-foreground">
            <Search className="size-10 opacity-40" />
            <p className="text-sm">输入关键词搜索笔记</p>
          </div>
        ) : isError ? (
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
              emptyText={isLoading ? '搜索中…' : '没有找到相关笔记'}
            />
          </div>
        )}
      </main>
    </div>
  )
}
