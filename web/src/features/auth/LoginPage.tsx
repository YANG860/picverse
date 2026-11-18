import { useState, type FormEvent } from 'react'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { create } from '@bufbuild/protobuf'
import { Platform } from '@gen/auth/v1/auth_pb.js'
import { LoginRequestSchema } from '@gen/bff/v1/auth_requests_pb.js'
import { BizError, describeError, unwrap } from '@/api/errors'
import { bff } from '@/api/transport'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { isValidEmail } from '@/lib/validate'
import { useAuthStore } from '@/stores/auth'

export default function LoginPage() {
  const navigate = useNavigate()
  const location = useLocation()
  const setTokens = useAuthStore((s) => s.setTokens)

  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  // 注册成功跳转携带的提示
  const notice = (location.state as { notice?: string } | null)?.notice
  // 登录后回跳来源页；无来源页时进真实信息流
  const from = (location.state as { from?: string } | null)?.from ?? '/'

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    setError('')
    if (!isValidEmail(email)) {
      setError('邮箱格式不正确')
      return
    }
    if (!password) {
      setError('请输入密码')
      return
    }
    setSubmitting(true)
    try {
      const res = await bff.login(create(LoginRequestSchema, { email, password, platform: Platform.WEB }))
      const ok = unwrap(res)
      setTokens(ok.accessToken?.value ?? '', ok.refreshToken?.value ?? '')
      navigate(from, { replace: true })
    } catch (err) {
      // 账密错误给具体提示，其余走统一文案
      if (err instanceof BizError) {
        setError(err.message)
      } else {
        setError(describeError(err))
      }
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="flex min-h-svh items-center justify-center bg-muted/40 p-4">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle className="text-xl">登录 picverse</CardTitle>
          <CardDescription>{notice ?? '使用邮箱密码登录'}</CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={onSubmit} className="grid gap-4">
            <div className="grid gap-2">
              <Label htmlFor="email">邮箱</Label>
              <Input id="email" type="email" autoComplete="email" value={email} onChange={(e) => setEmail(e.target.value)} required />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="password">密码</Label>
              <Input
                id="password"
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
              />
            </div>
            {error ? <p className="text-sm text-destructive">{error}</p> : null}
            <Button type="submit" disabled={submitting} className="w-full">
              {submitting ? '登录中…' : '登录'}
            </Button>
            <p className="text-center text-sm text-muted-foreground">
              还没有账号？
              <Link to="/register" className="text-primary underline-offset-4 hover:underline">
                去注册
              </Link>
            </p>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
