// 客户端校验规则（镜像 packages/utils/validate.go，先拦一层减少无效请求）
export const EMAIL_RE = /^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$/

/** 密码：8~32 位，且同时含字母与数字 */
export function isValidPassword(pw: string): boolean {
  return pw.length >= 8 && pw.length <= 32 && /[A-Za-z]/.test(pw) && /[0-9]/.test(pw)
}

/** 昵称：去除首尾空白后非空、1~64 字符（按 rune 计）、不含控制字符 */
export function isValidName(name: string): boolean {
  const t = name.trim()
  // oxlint-disable-next-line no-control-regex -- 控制字符检测为本意（镜像后端 utils.ValidateName）
  return [...t].length >= 1 && [...t].length <= 64 && !/[\u0000-\u001f\u007f]/.test(t)
}

export function isValidEmail(email: string): boolean {
  return EMAIL_RE.test(email)
}
