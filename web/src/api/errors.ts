// BFF 面客错误约定：响应统一 fail + err{code, msg}（见 doc/proto/proto.md）。
// 错误码为 packages/errmsg 的 4 位业务码（见 doc/packages/errmsg/err.md），此处保持镜像。
export const ERR = {
  // common
  InternalError: 1000,
  InvalidID: 1001,
  NotFound: 1002,
  Unauthorized: 1003,
  InvalidArgument: 1004,
  Forbidden: 1005,
  AlreadyExist: 1006,
  Unavailable: 1007,
  // auth
  InvalidEmail: 1101,
  InvalidPassword: 1102,
  IncorrectCredential: 1103,
  DuplicateEmail: 1104,
  // media
  InvalidFormat: 1201,
  // post
  InvalidTitle: 1301,
  InvalidContent: 1302,
  InvalidMedia: 1303,
  NotEditable: 1304,
  Banned: 1305,
  FavouriteLimitExceeded: 1306,
  // user
  InvalidName: 1401,
  InvalidBio: 1402,
  InvalidAvatar: 1403,
} as const

/** 业务失败（fail=true）：携带可枚举错误码 */
export class BizError extends Error {
  readonly code: number

  constructor(code: number, message: string) {
    super(message)
    this.name = 'BizError'
    this.code = code
  }
}

/** 错误码 → 面客文案 */
const ERROR_TEXT: Record<number, string> = {
  [ERR.InternalError]: '系统错误，请稍后重试',
  [ERR.InvalidID]: '无效的 ID',
  [ERR.NotFound]: '资源不存在',
  [ERR.Unauthorized]: '登录已过期，请重新登录',
  [ERR.InvalidArgument]: '参数不合法',
  [ERR.Forbidden]: '没有权限执行该操作',
  [ERR.AlreadyExist]: '资源已存在',
  [ERR.Unavailable]: '系统暂不可用，请稍后重试',
  [ERR.InvalidEmail]: '邮箱格式不正确',
  [ERR.InvalidPassword]: '密码不合规（8~32 位，需含字母和数字）',
  [ERR.IncorrectCredential]: '邮箱或密码错误',
  [ERR.DuplicateEmail]: '该邮箱已注册',
  [ERR.InvalidFormat]: '文件格式不支持',
  [ERR.InvalidTitle]: '标题不合法',
  [ERR.InvalidContent]: '正文不合法',
  [ERR.InvalidMedia]: '帖子媒体不合法',
  [ERR.NotEditable]: '帖子当前不可编辑',
  [ERR.Banned]: '帖子已被封禁',
  [ERR.FavouriteLimitExceeded]: '收藏夹已满',
  [ERR.InvalidName]: '昵称不合法（1~64 字符）',
  [ERR.InvalidBio]: '简介不合法',
  [ERR.InvalidAvatar]: '头像不合法',
}

export function bizErrorMessage(code: number, fallback?: string): string {
  return ERROR_TEXT[code] ?? fallback ?? '未知错误'
}

/** BFF 单值响应的 fail/err 形状（结构化判别，勿依赖具体消息类型） */
export interface FailShape {
  fail: boolean
  err?: { code: bigint | number; msg: string } | null
}

/** 解包 BFF 响应：fail=true 抛 BizError，否则原样返回 */
export function unwrap<T extends FailShape>(res: T): T {
  if (res.fail) {
    const code = Number(res.err?.code ?? ERR.InternalError)
    throw new BizError(code, bizErrorMessage(code, res.err?.msg))
  }
  return res
}

/** 把任意异常规整为可展示文案（业务错误 / 连接层错误 / 未知） */
export function describeError(e: unknown): string {
  if (e instanceof BizError) return e.message
  // 连接层错误（网络断开、超时、BFF 拒绝等）不泄漏内部细节，给通用文案
  return '网络异常，请稍后重试'
}
