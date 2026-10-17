import { relativeTime } from './operations-format'

/**
 * RelativeTime renders a compact time with the full timestamp and the relative
 * age in its title.
 *
 * The tables in this page are scanned, not read: an operator looking for "what
 * happened recently" wants an age, while anyone reconciling against a log needs
 * the exact second. Showing the compact time and putting the full date-time plus
 * the relative form in the title serves both without widening the column, and
 * `tabular-nums` keeps the column aligned.
 */
export function RelativeTime({ value }: { value: string }) {
  const date = new Date(value)
  if (!Number.isFinite(date.getTime())) {
    return <span className='text-muted-foreground'>—</span>
  }
  const full = date.toLocaleString('zh-CN', { hour12: false })
  const compact = date.toLocaleTimeString('zh-CN', { hour12: false })
  const age = relativeTime(value)
  return (
    <time dateTime={value} title={`${full}（${age}）`} className='tabular-nums'>
      {compact}
    </time>
  )
}
