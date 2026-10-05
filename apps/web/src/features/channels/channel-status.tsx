import type { Schema } from '@/lib/types'

const labels: Record<Schema<'ObservedStatus'>['state'], string> = {
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
          title={reasons[value[kind].reason] || value[kind].reason}
          className='rounded-md border px-2 py-1'
        >
          {{ main: '主流', sub: '子流', recording: '录像' }[kind]}：
          {labels[value[kind].state]}
        </span>
      ))}
    </div>
  )
}
