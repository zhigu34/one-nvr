/**
 * remainingLabel renders the time left, rounding down so a window that has 40
 * seconds left reads "40 秒" rather than "1 分钟" — the countdown must not
 * promise more time than exists.
 */
export function remainingLabel(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '已结束'
  const seconds = Math.floor(ms / 1000)
  if (seconds < 60) return `${seconds} 秒`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes} 分钟`
  const hours = Math.floor(minutes / 60)
  const rest = minutes % 60
  return rest ? `${hours} 小时 ${rest} 分` : `${hours} 小时`
}
