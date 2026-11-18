import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { describeError, unwrap } from '@/api/errors'
import { bff } from '@/api/transport'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { useAuthStore } from '@/stores/auth'

/** 页面入口：已接入真实 API 的页面与 mock 演示（发布器未接入） */
const MOCK_ENTRIES = [
  { to: '/', title: '信息流', desc: '推荐 / 热门 / 关注（真实 API）' },
  { to: '/user/me', title: '个人主页', desc: '笔记 / 收藏 / 关注列表（真实 API）' },
  { to: '/mock/publish', title: '发布器', desc: '图文上传 / 话题 / 草稿（mock）' },
] as const

/** 账号首页：真实 GetMyProfile 展示登录用户 + 未实现页面导流 mock + 退出登录 */
export default function HomePage() {
  const navigate = useNavigate()
  const clearTokens = useAuthStore((s) => s.clearTokens)
  const [loggingOut, setLoggingOut] = useState(false)

  const { data: profile, isLoading, isError, error } = useQuery({
    queryKey: ['my-profile'],
    queryFn: async () => unwrap(await bff.getMyProfile({})).profile ?? null,
    retry: false,
  })

  async function onLogout() {
    setLoggingOut(true)
    try {
      // 尽力而为：服务端登出失败也清本地会话（令牌会自然过期）
      unwrap(await bff.logout({}))
    } catch (e) {
      console.error('[auth] 登出失败（已清理本地会话）', e)
    } finally {
      clearTokens()
      navigate('/login', { replace: true })
    }
  }

  return (
    <div className="mx-auto flex min-h-svh w-full max-w-5xl flex-col gap-6 p-8">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-semibold text-primary">picverse</h1>
        <Button variant="outline" onClick={onLogout} disabled={loggingOut}>
          {loggingOut ? '退出中…' : '退出登录'}
        </Button>
      </div>

      {/* 登录用户信息（GetMyProfile）；头像 URL 走媒体服务预签名（P1-5），此处占位 */}
      <Card>
        <CardHeader>
          <CardTitle className="text-lg">我的账号</CardTitle>
          <CardDescription>数据来自 BFF GetMyProfile</CardDescription>
        </CardHeader>
        <CardContent>
          {isLoading && <p className="text-sm text-muted-foreground">加载中…</p>}
          {isError && <p className="text-sm text-destructive">{describeError(error)}</p>}
          {profile && (
            <div className="flex items-center gap-4">
              <div className="flex size-16 items-center justify-center rounded-full bg-primary/10 text-2xl font-medium text-primary">
                {profile.brief?.name?.slice(0, 1) ?? '?'}
              </div>
              <div className="grid gap-1">
                <p className="text-lg font-medium">{profile.brief?.name}</p>
                <p className="text-sm text-muted-foreground">uid：{profile.brief?.uid}</p>
                {profile.bio && <p className="text-sm text-muted-foreground">{profile.bio}</p>}
                <p className="text-sm text-muted-foreground">
                  {String(profile.stat?.followers ?? 0)} 粉丝 · {String(profile.stat?.followings ?? 0)} 关注
                </p>
              </div>
            </div>
          )}
        </CardContent>
      </Card>

      {/* 未实现页面 → mock 演示 */}
      <div className="grid gap-4 sm:grid-cols-3">
        {MOCK_ENTRIES.map((m) => (
          <Link key={m.to} to={m.to} className="group">
            <Card className="h-full transition-shadow group-hover:shadow-md">
              <CardHeader>
                <CardTitle className="text-base">{m.title}</CardTitle>
                <CardDescription>{m.desc}</CardDescription>
              </CardHeader>
            </Card>
          </Link>
        ))}
      </div>
    </div>
  )
}
