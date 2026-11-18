import { Share2, UserPlus, UserCheck, Pencil } from 'lucide-react'
import type { ProfileView } from '@/features/models'
import { avatarPlaceholder } from '@/lib/placeholder'
import GradientBanner from '@/features/ui/GradientBanner'
import { buttonVariants } from '@/components/ui/button'

interface Props {
  profile: ProfileView
  /** 自己视角：显示编辑资料；他人视角：显示关注按钮 */
  isSelf: boolean
  following: boolean
  onToggleFollow: () => void
  /** 点开关注/粉丝列表（契约仅“我的”列表 → 仅自己主页传入） */
  onOpenFollowList?: (kind: 'following' | 'followers') => void
  /** 打开编辑资料弹层（仅自己主页） */
  onEditProfile?: () => void
}

/** 个人主页头部：横幅 + 头像 + 资料 + 统计行 + 操作按钮 */
export default function ProfileHeader({ profile, isSelf, following, onToggleFollow, onOpenFollowList, onEditProfile }: Props) {
  return (
    <section>
      {/* 横幅：动态流光 mesh（多彩低饱和，超慢漂移） */}
      <GradientBanner seed={Number(profile.brief.uid) || 3} className="h-40 rounded-b-2xl sm:h-52" />

      {/* 资料区 */}
      <div className="px-2 sm:px-6">
        <div className="flex flex-wrap items-end gap-5">
          <img
            src={profile.brief.avatarUrl || avatarPlaceholder(profile.brief.name, Number(profile.brief.uid))}
            alt=""
            className="relative z-10 -mt-10 size-20 rounded-full bg-muted object-cover ring-4 ring-background sm:size-24"
            onError={(e) => {
              // URL 失效兜底换首字占位（data URI 不会二次报错）
              e.currentTarget.src = avatarPlaceholder(profile.brief.name, Number(profile.brief.uid))
            }}
          />
          <div className="min-w-0 flex-1">
            <h1 className="text-xl font-semibold">{profile.brief.name}</h1>
            <p className="mt-0.5 text-xs text-muted-foreground">uid：{profile.brief.uid}</p>
          </div>
          <div className="flex items-center gap-2">
            {isSelf ? (
              <>
                <button
                  type="button"
                  onClick={onEditProfile}
                  className={buttonVariants({ variant: 'outline', className: 'rounded-full' })}
                >
                  <Pencil className="size-4" />
                  编辑资料
                </button>
                <button type="button" aria-label="分享主页" className="grid size-9 place-items-center rounded-full border transition-colors hover:bg-muted">
                  <Share2 className="size-4" />
                </button>
              </>
            ) : (
              <>
                <button
                  type="button"
                  onClick={onToggleFollow}
                  className={buttonVariants({ variant: following ? 'outline' : 'default', className: 'rounded-full' })}
                >
                  {following ? <UserCheck className="size-4" /> : <UserPlus className="size-4" />}
                  {following ? '已关注' : '关注'}
                </button>
                <button type="button" className={buttonVariants({ variant: 'outline', className: 'rounded-full' })}>
                  私信
                </button>
              </>
            )}
          </div>
        </div>

        {/* 简介 */}
        <p className="mt-4 max-w-xl text-sm leading-6 whitespace-pre-wrap text-muted-foreground">{profile.bio}</p>

        {/* 统计行（关注/粉丝可点开列表；获赞数 BFF 不提供时隐藏） */}
        <div className="mt-4 flex items-center gap-6 text-sm">
          <button
            type="button"
            disabled={!onOpenFollowList}
            onClick={() => onOpenFollowList?.('following')}
            className="transition-opacity hover:opacity-80 disabled:opacity-100"
          >
            <b className="font-semibold">{profile.stat.followings}</b>
            <span className="ml-1 text-muted-foreground">关注</span>
          </button>
          <button
            type="button"
            disabled={!onOpenFollowList}
            onClick={() => onOpenFollowList?.('followers')}
            className="transition-opacity hover:opacity-80 disabled:opacity-100"
          >
            <b className="font-semibold">{profile.stat.followers}</b>
            <span className="ml-1 text-muted-foreground">粉丝</span>
          </button>
          {profile.likeReceived > 0 ? (
            <span>
              <b className="font-semibold">{profile.likeReceived}</b>
              <span className="ml-1 text-muted-foreground">获赞与收藏</span>
            </span>
          ) : null}
        </div>
      </div>
    </section>
  )
}
