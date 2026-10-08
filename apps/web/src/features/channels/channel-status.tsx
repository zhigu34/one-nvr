import type { Schema } from '@/lib/types'
import { STATUS_REASONS, UNKNOWN_STATUS_LABELS } from './status-reasons'

const labels: Record<Schema<'ObservedStatus'>['state'] | 'pending', string> = {
  pending: '检查中',
  healthy: '正常',
  unavailable: '不可用',
  unknown: '未知',
  disabled: '已关闭',
  not_configured: '未配置',
  degraded: '降级',
}
export function StatusLabel({
  value,
}: {
  value: {
    state: Schema<'ObservedStatus'>['state'] | 'pending'
    reason?: string
  }
}) {
  const tone =
    value.state === 'healthy'
      ? 'text-emerald-700 dark:text-emerald-400'
      : value.state === 'unavailable'
        ? 'text-destructive'
        : value.state === 'degraded'
          ? 'text-amber-700 dark:text-amber-400'
          : 'text-muted-foreground'
  const text =
    value.state === 'unknown' && value.reason
      ? UNKNOWN_STATUS_LABELS[value.reason] || labels.unknown
      : labels[value.state]
  return (
    <span
      title={value.reason ? STATUS_REASONS[value.reason] : undefined}
      className={`inline-flex items-center gap-1.5 text-xs whitespace-nowrap ${tone}`}
    >
      <span aria-hidden className='size-1.5 rounded-full bg-current' />
      {text}
    </span>
  )
}
// Recording is a policy, not a connection state: it belongs on the recording
// plan page and must not sit next to the live stream indicators.
export function ChannelStatus({
  value,
}: {
  value: Schema<'ChannelSourceStatus'>
}) {
  return (
    <div className='flex flex-wrap gap-3 text-xs'>
      {(['main', 'sub'] as const).map((kind) => (
        <span
          key={kind}
          className='inline-flex items-center gap-1.5 rounded-md bg-muted/60 px-2 py-1'
        >
          {{ main: '主流', sub: '子流' }[kind]}：
          <StatusLabel value={value[kind]} />
        </span>
      ))}
    </div>
  )
}
