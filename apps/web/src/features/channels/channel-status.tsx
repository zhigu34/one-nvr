import type { Schema } from '@/lib/types'
import {
  statusReason,
  statusText,
  statusTextTones,
  statusTone,
} from './status-labels'

export function StatusLabel({
  value,
}: {
  value: {
    state: Schema<'ObservedStatus'>['state'] | 'pending'
    reason?: string
  }
}) {
  const reason = statusReason(value)
  return (
    <span
      title={reason || undefined}
      className={`inline-flex items-center gap-1.5 text-xs whitespace-nowrap ${statusTextTones[statusTone(value)]}`}
    >
      <span aria-hidden className='size-1.5 rounded-full bg-current' />
      {statusText(value)}
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
