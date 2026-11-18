import dayjs from 'dayjs'
import relativeTime from 'dayjs/plugin/relativeTime'
import 'dayjs/locale/zh-cn'

dayjs.extend(relativeTime)
dayjs.locale('zh-cn')

/** unix 毫秒 → 中文相对时间（"3 小时前"） */
export function fromNow(ms: number): string {
  return dayjs(ms).fromNow()
}

/** unix 毫秒 → 日期（"2025-11-15"） */
export function formatDate(ms: number): string {
  return dayjs(ms).format('YYYY-MM-DD')
}
