import { Code, ConnectError, createClient, type Interceptor, type StreamRequest, type UnaryRequest } from '@connectrpc/connect'
import { createConnectTransport } from '@connectrpc/connect-web'
// 服务描述符（GenService）由 protoc-gen-es v2 生成，connect v2 直接消费；
// gen/ts 下的 *_connect.* 为旧版 protoc-gen-connect-es 产物，前端不使用
import { BFFService } from '@gen/bff/v1/bff_service_pb.js'
import { ERR } from '@/api/errors'
import { useAuthStore } from '@/stores/auth'

/** 刷新令牌方法：refresh token 从会话凭据（Authorization 头）解析，请求体为空 */
const REFRESH_METHOD = 'RefreshTokens'
/** 无会话即可调用的方法（不注 token） */
const PUBLIC_METHODS = new Set(['Register', 'Login'])

/** 业务层未授权判定：BFF 会话缺失返回 fail + err.code=1003（不是连接层错误） */
function isBizUnauthorized(message: unknown): boolean {
  if (typeof message !== 'object' || message === null) return false
  const m = message as { fail?: boolean; err?: { code?: bigint | number } | null }
  return m.fail === true && Number(m.err?.code) === ERR.Unauthorized
}

/** 按方法语义注入 Authorization 头（Bearer 方案，见 packages/jwt/interceptor.go） */
function applyAuthHeader(req: UnaryRequest | StreamRequest): void {
  const { accessToken, refreshToken } = useAuthStore.getState()
  const token = req.method.name === REFRESH_METHOD ? refreshToken : PUBLIC_METHODS.has(req.method.name) ? '' : accessToken
  if (token) {
    req.header.set('Authorization', `Bearer ${token}`)
  } else {
    req.header.delete('Authorization')
  }
}

// 刷新单飞：并发请求同时 1003 时只发一次 RefreshTokens，其余等同一结果
let refreshing: Promise<boolean> | null = null

/** 用 refresh token 换发新令牌；成功写回 store，会话失效则清空本地态 */
async function doRefresh(): Promise<boolean> {
  const { refreshToken, setTokens, clearTokens } = useAuthStore.getState()
  if (!refreshToken) return false
  try {
    const res = await bff.refreshTokens({})
    if (res.fail) {
      // 会话已失效（refresh token 无效/过期）→ 清空，由路由守卫跳登录；
      // 其余业务失败不动本地态（可能是瞬时故障，避免误杀会话）
      if (Number(res.err?.code) === ERR.Unauthorized) clearTokens()
      return false
    }
    // 响应回填当前有效 refresh token（轮换时为新值，未轮换时为旧值），直接覆盖存储
    setTokens(res.accessToken?.value ?? '', res.refreshToken?.value ?? refreshToken)
    return Boolean(res.accessToken?.value)
  } catch (e) {
    // 连接层异常（网络抖动等）：记录上下文，不清理本地态
    console.error('[auth] 刷新令牌失败', e)
    return false
  }
}

function refreshTokens(): Promise<boolean> {
  refreshing ??= doRefresh().finally(() => {
    refreshing = null
  })
  return refreshing
}

/**
 * 鉴权拦截器：
 * 1. 注入 Authorization 头（RefreshTokens 用 refresh token，其余业务方法用 access token）；
 * 2. 业务层未授权（fail+1003）或连接层 Unauthenticated 时，自动换发令牌并重放原请求一次
 *   （请求被鉴权拒绝时业务未执行，重放安全）。
 */
const authInterceptor: Interceptor = (next) => async (req) => {
  if (req.stream) return next(req)
  applyAuthHeader(req)
  try {
    const res = await next(req)
    if (isBizUnauthorized(res.message) && req.method.name !== REFRESH_METHOD && (await refreshTokens())) {
      applyAuthHeader(req)
      return await next(req)
    }
    return res
  } catch (e) {
    if (e instanceof ConnectError && e.code === Code.Unauthenticated && req.method.name !== REFRESH_METHOD && (await refreshTokens())) {
      applyAuthHeader(req)
      return await next(req)
    }
    throw e
  }
}

// Connect 协议走浏览器 fetch；开发期经 Vite 代理同源转发到 BFF（:8084）
export const transport = createConnectTransport({
  baseUrl: import.meta.env.VITE_BFF_BASE_URL ?? '/',
  interceptors: [authInterceptor],
})

export const bff = createClient(BFFService, transport)
