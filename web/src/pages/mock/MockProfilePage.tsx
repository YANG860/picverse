import { useMemo, useState } from 'react'
import { useParams } from 'react-router-dom'
import AppHeader from '@/components/layout/AppHeader'
import MasonryFeed from '@/features/feed/MasonryFeed'
import ProfileHeader from '@/features/profile/ProfileHeader'
import { getProfile, listUserPosts } from '@/mocks/data'

type ProfileTab = 'notes' | 'favourites'

/** mock 个人主页：纯合成数据（/mock/profile 自己视角，/mock/profile/:uid 他人视角） */
export default function MockProfilePage() {
  const { uid } = useParams()
  const isSelf = !uid

  const profile = useMemo(() => getProfile(uid ?? 'self'), [uid])
  const [tab, setTab] = useState<ProfileTab>('notes')
  const [following, setFollowing] = useState(profile.relation.isFollowing)

  const posts = useMemo(() => listUserPosts(uid ?? 'self', tab, isSelf), [uid, tab, isSelf])

  // 他人视角的收藏仅自己可见：展示私密空态（对齐契约语义）
  const showPrivateEmpty = !isSelf && tab === 'favourites'

  return (
    <div className="min-h-svh bg-muted/30">
      <AppHeader />
      <main className="mx-auto w-full max-w-5xl px-2 pb-10 sm:px-4">
        <ProfileHeader profile={profile} isSelf={isSelf} following={following} onToggleFollow={() => setFollowing((v) => !v)} />

        {/* Tab：笔记 / 收藏 */}
        <nav className="mt-6 flex items-center justify-center gap-8 border-b">
          {(
            [
              { key: 'notes', label: isSelf ? '我的笔记' : '笔记' },
              { key: 'favourites', label: '收藏' },
            ] as { key: ProfileTab; label: string }[]
          ).map((t) => (
            <button
              key={t.key}
              type="button"
              onClick={() => setTab(t.key)}
              className={
                'relative -mb-px border-b-2 px-1 py-3 text-sm transition-colors ' +
                (tab === t.key ? 'border-primary font-medium text-foreground' : 'border-transparent text-muted-foreground hover:text-foreground')
              }
            >
              {t.label}
            </button>
          ))}
        </nav>

        <div className="pt-6">
          {showPrivateEmpty ? (
            <div className="flex flex-col items-center gap-2 py-24 text-muted-foreground">
              <p className="text-sm">收藏仅自己可见</p>
              <p className="text-xs">快去发现喜欢的笔记吧～</p>
            </div>
          ) : (
            <MasonryFeed posts={posts} hasMore={false} onLoadMore={() => undefined} />
          )}
        </div>
      </main>
    </div>
  )
}
