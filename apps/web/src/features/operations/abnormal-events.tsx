import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { AlertTriangle, CheckCircle2, ChevronDown } from 'lucide-react'
import type { Schema } from '@/lib/types'
import { useAPI } from '@/features/foundation/hooks'
import { Panel, QueryState } from '@/features/foundation/ui'
import { cn } from '@/lib/utils'

/**
 * Abnormal events (PRD EVT-04).
 *
 * A worklist, not a metric. The operator's question is "what is broken right
 * now", and the answer is a short ordered list of things with names. Rates and
 * percentages live on the reliability tab, where they answer a different
 * question.
 *
 * Severity is expressed by how loud the row is rather than by a legend: critical
 * rows are red and first, warnings amber, informational grey. A condition that
 * has been seen many times shows its count, because a row that keeps reappearing
 * is a flapping fault rather than a new one.
 */
const SEVERITY_LABEL: Record<string, string> = {
  critical: '严重',
  warning: '警告',
  info: '提示',
}

const KIND_LABEL: Record<string, string> = {
  camera_offline: '摄像头离线',
  stream_degraded: '码流异常',
  recording_stalled: '录像停滞',
  storage_unwritable: '存储不可写',
  capacity_low: '容量不足',
  detector_unavailable: '检测服务不可用',
  broker_unavailable: '消息代理不可用',
  config_publish_failed: '配置发布失败',
  queue_backlog: '任务积压',
}

export function AbnormalEvents({ zone }: { zone: string }) {
  const [showResolved, setShowResolved] = useState(false)
  const events = useAPI<{ items: Schema<'AbnormalEvent'>[] }>(
    `/api/v1/operations/abnormal-events${showResolved ? '?include_resolved=true' : ''}`,
    true,
    10_000
  )
  // A failed read hides the panel rather than showing "0 异常": claiming the site
  // is healthy when the evidence is missing is the one thing this panel must not
  // do.
  if (events.error) return null
  const items = events.data?.items ?? []
  const firing = items.filter((item) => item.state === 'firing')
  const resolved = items.filter((item) => item.state === 'resolved')
  const shown = showResolved ? items : firing

  return (
    <Panel title='异常事件' anchor='abnormal' spaced={false}>
      <div className='mb-3 flex flex-wrap items-center justify-between gap-2'>
        <p className='text-xs text-muted-foreground'>
          持续同一故障只保留一条活动事件，恢复后保留历史
        </p>
        <button
          type='button'
          aria-pressed={showResolved}
          onClick={() => setShowResolved((value) => !value)}
          className={cn(
            'inline-flex items-center gap-1 rounded-full border px-2.5 py-0.5 text-xs transition-colors',
            showResolved
              ? 'border-primary bg-primary/10 text-primary'
              : 'border-border text-muted-foreground hover:bg-accent'
          )}
        >
          <ChevronDown aria-hidden className={cn('size-3', showResolved && 'rotate-180')} />
          含已恢复{resolved.length > 0 ? ` (${resolved.length})` : ''}
        </button>
      </div>
      <QueryState
        pending={events.isPending}
        error={events.error}
        retry={() => events.refetch()}
      />
      {events.data && shown.length === 0 && (
        <p className='flex items-center gap-1.5 text-sm text-emerald-600 dark:text-emerald-400'>
          <CheckCircle2 aria-hidden className='size-4' />
          当前没有异常事件
        </p>
      )}
      {shown.length > 0 && (
        <ul className='space-y-1.5'>
          {shown.map((item) => (
            <li
              key={item.id}
              data-testid='abnormal-event'
              className={cn(
                'flex flex-wrap items-start justify-between gap-2 rounded-lg border px-3 py-2 text-sm',
                item.state === 'resolved'
                  ? 'border-border bg-muted/40 text-muted-foreground'
                  : item.severity === 'critical'
                    ? 'border-red-500/40 bg-red-500/5'
                    : item.severity === 'warning'
                      ? 'border-amber-500/40 bg-amber-500/5'
                      : 'border-border'
              )}
            >
              <span className='flex min-w-0 flex-1 items-start gap-2'>
                <AlertTriangle
                  aria-hidden
                  className={cn(
                    'mt-0.5 size-3.5 shrink-0',
                    item.state === 'resolved'
                      ? 'text-muted-foreground'
                      : item.severity === 'critical'
                        ? 'text-red-500'
                        : 'text-amber-500'
                  )}
                />
                <span className='min-w-0'>
                  <span className='flex flex-wrap items-center gap-1.5'>
                    <span className='font-medium'>{KIND_LABEL[item.kind] || item.kind}</span>
                    <span className='text-xs text-muted-foreground'>{item.summary}</span>
                    {item.occurrences > 1 && item.state === 'firing' && (
                      <span
                        className='rounded bg-muted px-1.5 py-px text-[11px] text-muted-foreground'
                        title='同一故障被反复观察到，说明它在抖动而不是新发生'
                      >
                        反复 {item.occurrences} 次
                      </span>
                    )}
                  </span>
                  {item.detail && (
                    <span className='mt-0.5 block text-xs text-muted-foreground'>
                      {item.detail}
                    </span>
                  )}
                </span>
              </span>
              <span className='flex shrink-0 items-center gap-2 text-xs text-muted-foreground'>
                <span className='tabular-nums'>
                  {moment(item.state === 'resolved' ? item.ended_at : item.last_seen_at, zone)}
                </span>
                {item.channel_id && (
                  <Link
                    to='/channels/configure'
                    search={{ channel: item.channel_id }}
                    className='underline'
                  >
                    通道
                  </Link>
                )}
                <span
                  className={cn(
                    'rounded-full border px-1.5 py-px',
                    item.state === 'resolved'
                      ? 'border-border'
                      : item.severity === 'critical'
                        ? 'border-red-500/40 text-red-600 dark:text-red-400'
                        : 'border-amber-500/40 text-amber-600 dark:text-amber-400'
                  )}
                >
                  {item.state === 'resolved' ? '已恢复' : SEVERITY_LABEL[item.severity] || item.severity}
                </span>
              </span>
            </li>
          ))}
        </ul>
      )}
    </Panel>
  )
}

/** Wall-clock time in the site zone, matching every other timestamp on the page. */
function moment(value: string | null | undefined, zone: string): string {
  if (!value) return '—'
  return new Date(value).toLocaleString('zh-CN', {
    timeZone: zone || 'Asia/Shanghai',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  })
}
