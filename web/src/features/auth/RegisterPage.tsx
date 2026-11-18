import { useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { create } from '@bufbuild/protobuf'
import { Platform } from '@gen/auth/v1/auth_pb.js'
import { LoginRequestSchema, RegisterRequestSchema } from '@gen/bff/v1/auth_requests_pb.js'
import { describeError, unwrap } from '@/api/errors'
import { bff } from '@/api/transport'
import { useAuthStore } from '@/stores/auth'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { isValidEmail, isValidName, isValidPassword } from '@/lib/validate'

export default function RegisterPage() {
  const navigate = useNavigate()
  const setTokens = useAuthStore((s) => s.setTokens)

  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [name, setName] = useState('')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    setError('')
    // 校验规则镜像 packages/utils/validate.go，先拦一层减少无效请求
    if (!isValidEmail(email)) {
      setError('邮箱格式不正确')
      return
    }
    if (!isValidPassword(password)) {
      setError('密码需 8~32 位，且含字母和数字')
      return
    }
    if (!isValidName(name)) {
      setError('昵称需为 1~64 字符')
      return
    }
    setSubmitting(true)
    try {
      unwrap(await bff.register(create(RegisterRequestSchema, { email, password, name })))
      // 注册成功即自动登录（BFF 注册不签发令牌，同凭据 Login 换发），免二次输入直接进站
      const ok = unwrap(await bff.login(create(LoginRequestSchema, { email, password, platform: Platform.WEB })))
      setTokens(ok.accessToken?.value ?? '', ok.refreshToken?.value ?? '')
      navigate('/', { replace: true })
    } catch (err) {
      setError(describeError(err))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="flex min-h-svh items-center justify-center bg-muted/40 p-4">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle className="text-xl">注册 picverse</CardTitle>
          <CardDescription>创建账号后即可登录</CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={onSubmit} className="grid gap-4">
            <div className="grid gap-2">
              <Label htmlFor="name">昵称</Label>
              <Input id="name" autoComplete="nickname" value={name} onChange={(e) => setName(e.target.value)} required />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="email">邮箱</Label>
              <Input id="email" type="email" autoComplete="email" value={email} onChange={(e) => setEmail(e.target.value)} required />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="password">密码</Label>
              <Input
                id="password"
                type="password"
                autoComplete="new-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
              />
              <p className="text-xs text-muted-foreground">8~32 位，需同时含字母和数字</p>
            </div>
            {error ? <p className="text-sm text-destructive">{error}</p> : null}
            <Button type="submit" disabled={submitting} className="w-full">
              {submitting ? '注册中…' : '注册'}
            </Button>
            <p className="text-center text-sm text-muted-foreground">
              已有账号？
              <Link to="/login" className="text-primary underline-offset-4 hover:underline">
                去登录
              </Link>
            </p>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
