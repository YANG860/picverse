import { useMemo, useState } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'
import { X } from 'lucide-react'
import { describeError } from '@/api/errors'
import {
  useFavouritePosts,
  useFollowerList,
  useFollowingList,
  useMyInteraction,
  useMyProfile,
  useToggleFollow,
  useUserProfile,
  useUserPosts,
} from '@/api/hooks'
import { mapUserBrief } from '@/api/mappers'
import AppHeader from '@/components/layout/AppHeader'
import MasonryFeed from '@/features/feed/MasonryFeed'
import ProfileHeader from '@/features/profile/ProfileHeader'
import EditProfileDialog from '@/features/profile/EditProfileDialog'
import type { ProfileView, UserBrief } from '@/features/models'

type ProfileTab = 'notes' | 'favourites'
type FollowListKind = 'following' | 'followers' | null

/** 关注/粉丝列表弹层（契约仅“我的”列表 → 本人主页可用；游标触底加载） */
function FollowListDialog({ kind, onClose }: { kind: NonNullable<FollowListKind>; onClose: () => void }) {
  // Hooks 无条件调用（rules-of-hooks），按 kind 选用结果
  const followingQuery = useFollowingList()
  const followerQuery = useFollowerList()
  const query = kind === 'following' ? followingQuery : followerQuery
  const users = useMemo(() => query.data?.pages.flatMap((p) => p.users) ?? [], [query.data])
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4" onClick={onClose}>
      <div className="flex max-h-[70vh] w-full max-w-sm flex-col rounded-2xl bg-background" onClick={(e) => e.stopPropagation()}>
        <header className="flex items-center justify-between border-b px-4 py-3">
          <h2 className="text-sm font-medium">{kind === 'following' ? '关注列表' : '粉丝列表'}</h2>
          <button type="button" onClick={onClose} aria-label="关闭" className="text-muted-foreground hover:text-foreground">
            <X className="size-4" />
          </button>
        </header>
        <div className="min-h-0 flex-1 overflow-y-auto px-4">
          {users.map((u) => (
            <UserRow key={u.uid} user={u} onClose={onClose} />
          ))}
          {query.hasNextPage ? (
            <button
              type="button"
              onClick={() => void query.fetchNextPage()}
              disabled={query.isFetchingNextPage}
              className="w-full py-3 text-xs text-muted-foreground hover:text-foreground disabled:opacity-60"
            >
              {query.isFetchingNextPage ? '加载中…' : '加载更多'}
            </button>
          ) : users.length === 0 ? (
            <p className="py-8 text-center text-sm text-muted-foreground">暂无数据</p>
          ) : null}
        </div>
      </div>
    </div>
  )
}

/** 列表行：点击跳用户主页；关注/取关（预取集合判态） */
function UserRow({ user, onClose }: { user: UserBrief; onClose: () => void }) {
  const navigate = useNavigate()
  const { data: interaction } = useMyInteraction()
  const toggleFollow = useToggleFollow()
  const following = interaction?.following.has(user.uid) ?? false
  return (
    <div className="flex items-center gap-3 py-3">
      <button
        type="button"
        onClick={() => {
          onClose()
          navigate(`/user/${user.uid}`)
        }}
        className="flex min-w-0 flex-1 items-center gap-3 text-left"
      >
        {user.avatarUrl ? (
          <img src={user.avatarUrl} alt="" className="size-10 rounded-full object-cover" />
        ) : (
          <div className="flex size-10 items-center justify-center rounded-full bg-primary/10 text-primary">{user.name.slice(0, 1)}</div>
        )}
        <div className="min-w-0">
          <p className="truncate text-sm font-medium">{user.name}</p>
          <p className="text-xs text-muted-foreground">uid：{user.uid}</p>
        </div>
      </button>
      <button
        type="button"
        disabled={toggleFollow.isPending}
        onClick={() => toggleFollow.mutate(user.uid)}
        className={
          'shrink-0 rounded-full border px-3 py-1 text-xs transition-colors disabled:opacity-60 ' +
          (following ? 'text-muted-foreground hover:border-destructive hover:text-destructive' : 'border-primary bg-primary text-primary-foreground hover:opacity-90')
        }
      >
        {following ? '已关注' : '回关'}
      </button>
    </div>
  )
}

/** 真实用户主页：/user/me（自己）或 /user/:uid（他人） */
export default function UserProfilePage() {
  const { uid: rawUid = '' } = useParams()
  const location = useLocation()
  // 发布器跳转携带的结果提示（发布成功/存草稿）
  const notice = (location.state as { notice?: string } | null)?.notice
  const { data: me } = useMyProfile()
  const uid = rawUid === 'me' ? (me?.brief?.uid ?? '') : rawUid
  const isSelf = Boolean(uid) && uid === me?.brief?.uid

  const { data: view, isLoading, isError, error } = useUserProfile(uid)
  const postsQuery = useUserPosts(uid, isSelf)
  const { data: favourites } = useFavouritePosts()
  const { data: interaction } = useMyInteraction()
  const toggleFollow = useToggleFollow()

  const [tab, setTab] = useState<ProfileTab>('notes')
  const [followList, setFollowList] = useState<FollowListKind>(null)
  const [editing, setEditing] = useState(false)
  // 编辑资料保存成功的本地提示（notice 为发布器跳转携带，语义不同）
  const [savedNotice, setSavedNotice] = useState('')

  // 自己视角资料取自 GetMyProfile（GetUserProfile 语义为他人视角聚合）
  const profile: ProfileView | undefined = useMemo(() => {
    if (isSelf && me) {
      return {
        brief: mapUserBrief(me.brief),
        bio: me.bio ?? '',
        stat: { followers: Number(me.stat?.followers ?? 0), followings: Number(me.stat?.followings ?? 0) },
        relation: { isFollowing: false, isFollower: false },
        likeReceived: 0,
      }
    }
    return view ?? undefined
  }, [isSelf, me, view])

  const posts = useMemo(() => postsQuery.data?.pages.flatMap((p) => p.posts) ?? [], [postsQuery.data])
  // 他人视角收藏仅自己可见：私密空态（契约无他人收藏接口）
  const showPrivateEmpty = !isSelf && tab === 'favourites'

  if (isLoading) {
    return (
      <div className="flex min-h-svh items-center justify-center text-muted-foreground">
        <p className="text-sm">加载中…</p>
      </div>
    )
  }
  if (isError || !profile) {
    return (
      <div className="flex min-h-svh flex-col items-center justify-center gap-2 text-muted-foreground">
        <p className="text-sm text-destructive">{describeError(error)}</p>
        <a href="/" className="text-sm text-primary underline-offset-4 hover:underline">
          返回信息流
        </a>
      </div>
    )
  }

  return (
    <div className="min-h-svh bg-muted/30">
      <AppHeader />
      <main className="mx-auto w-full max-w-5xl px-2 pb-10 sm:px-4">
        {notice || savedNotice ? (
          <p className="mt-4 rounded-lg bg-primary/10 px-4 py-2.5 text-sm text-primary">{notice || savedNotice}</p>
        ) : null}
        <ProfileHeader
          profile={profile}
          isSelf={isSelf}
          // 关注态统一预取集合本地判态（随切换即时更新），relation 仅作初始兑底
          following={interaction?.following.has(profile.brief.uid) ?? profile.relation.isFollowing}
          onToggleFollow={() => toggleFollow.mutate(profile.brief.uid)}
          // 列表 RPC 契约仅“我的”（GetFollowingList/GetFollowerList 无 uid 参数）：他人主页不开放，避免错配显示自己的列表
          onOpenFollowList={isSelf ? (kind) => setFollowList(kind) : undefined}
          onEditProfile={
            isSelf
              ? () => {
                  setSavedNotice('')
                  setEditing(true)
                }
              : undefined
          }
        />

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
          ) : tab === 'favourites' ? (
            <MasonryFeed posts={favourites ?? []} hasMore={false} loading={false} onLoadMore={() => undefined} cardBasePath="/post/" />
          ) : (
            <MasonryFeed
              posts={posts}
              hasMore={Boolean(postsQuery.hasNextPage)}
              loading={Boolean(postsQuery.isFetchingNextPage)}
              onLoadMore={() => void postsQuery.fetchNextPage()}
              cardBasePath="/post/"
            />
          )}
        </div>
      </main>

      {followList ? <FollowListDialog kind={followList} onClose={() => setFollowList(null)} /> : null}
      {editing && profile ? (
        <EditProfileDialog
          initial={{ name: profile.brief.name, bio: profile.bio, avatarUrl: profile.brief.avatarUrl }}
          onClose={() => setEditing(false)}
          onSaved={() => {
            setEditing(false)
            setSavedNotice('资料已更新')
          }}
        />
      ) : null}
    </div>
  )
}
