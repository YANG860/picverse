import { Bell, Plus, Search } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { buttonVariants } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { useMyProfile } from '@/api/hooks'
import { avatarPlaceholder } from '@/lib/placeholder'

/** 全局顶栏：logo + 搜索 + 发布 + 头像 */
export default function AppHeader() {
  const navigate = useNavigate()
  const [q, setQ] = useState('')
  const { data: me } = useMyProfile()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    const keyword = q.trim()
    if (keyword) navigate(`/search?q=${encodeURIComponent(keyword)}`)
  }
  return (
    <header className="sticky top-0 z-40 border-b bg-background/80 backdrop-blur">
      <div className="mx-auto flex h-14 w-full max-w-7xl items-center gap-4 px-4">
        {/* logo 回账号首页（真实登录态）：Outfit 几何字标 + mesh 品牌渐变字 */}
        <Link to="/">
          <span className="bg-gradient-to-r from-[#6D7DF2] via-[#A78BFA] to-[#E68FB8] bg-clip-text font-display text-[1.35rem] leading-none font-extrabold tracking-tight lowercase text-transparent">
            picverse
          </span>
        </Link>

        <form onSubmit={submit} className="relative ml-auto hidden w-72 sm:block">
          <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="搜索笔记（回车）"
            className="rounded-full pl-9"
            maxLength={64}
          />
        </form>

        <div className="ml-auto flex items-center gap-2 sm:ml-0">
          <Link to="/publish" className={buttonVariants({ size: 'sm', className: 'rounded-full' })}>
            <Plus className="size-4" />
            发布
          </Link>
          <button type="button" aria-label="通知" className="grid size-9 place-items-center rounded-md transition-colors hover:bg-muted">
            <Bell className="size-4" />
          </button>
          <Link to="/user/me" aria-label="我的主页">
            <img
              src={me?.brief?.avatar?.url || avatarPlaceholder(me?.brief?.name ?? '我', Number(me?.brief?.uid))}
              alt="我的头像"
              className="size-8 rounded-full bg-muted object-cover ring-2 ring-primary/20"
              onError={(e) => {
                e.currentTarget.src = avatarPlaceholder(me?.brief?.name ?? '我', Number(me?.brief?.uid))
              }}
            />
          </Link>
        </div>
      </div>
    </header>
  )
}
