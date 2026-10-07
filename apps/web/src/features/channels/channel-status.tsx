import type { Schema } from '@/lib/types'

const labels: Record<Schema<'ObservedStatus'>['state'] | 'pending', string> = {
  pending: '检查中',
  healthy: '正常',
  unavailable: '不可用',
  unknown: '未知',
  disabled: '已关闭',
  not_configured: '未配置',
  degraded: '降级',
}
const reasons: Record<string, string> = {
  bitrate_unknown: '码率证据不足，等待新采样',
  pool_unavailable: '存储池不可用',
  low_space: '空间不足，已暂停录像',
  source_unavailable: '无法读取视频',
  source_frozen: '视频帧未更新',
  recording_disabled: '已关闭录像',
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
  return (
    <span
      title={value.reason ? reasons[value.reason] : undefined}
      className={`inline-flex items-center gap-1.5 text-xs whitespace-nowrap ${tone}`}
    >
      <span aria-hidden className='size-1.5 rounded-full bg-current' />
      {labels[value.state]}
    </span>
  )
}
export function ChannelStatus({
  value,
}: {
  value: Schema<'ChannelSourceStatus'>
}) {
  return (
    <div className='flex flex-wrap gap-3 text-xs'>
      {(['main', 'sub', 'recording'] as const).map((kind) => (
        <span
          key={kind}
          className='inline-flex items-center gap-1.5 rounded-md bg-muted/60 px-2 py-1'
        >
          {{ main: '主流', sub: '子流', recording: '录像' }[kind]}：
          <StatusLabel value={value[kind]} />
        </span>
      ))}
    </div>
  )
}
