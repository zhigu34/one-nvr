import { useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import {
  bytesLabel,
  coverage,
  errorText as uploadErrorText,
  MODE_LABEL,
  pendingAge,
  UPLOAD_STATE_LABEL,
  uploadState,
  type ChannelStatus,
  type TargetStatus,
} from './archive-status'

/**
 * RunStatus answers "is the archive working", which the target list cannot.
 *
 * It sits above the configuration form because an operator opening this page
 * normally wants to know whether footage is reaching the target, not to edit a
 * URL. Failures come with a reason and a retry, because a stopped archive that
 * can only be fixed in the database is a feature that is down with no way back.
 */
export function RunStatus({
  targets,
  statuses,
  pending,
  failed,
  error,
  onRetry,
}: {
  targets: { id?: string; name?: string }[]
  statuses: TargetStatus[]
  pending: boolean
  failed: boolean
  error: string
  onRetry: (targetID: string) => void
}) {
  // The page refetches every 15 seconds; a minute tick is enough to keep the
  // "waiting N minutes" label from going stale between renders.
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 60_000)
    return () => clearInterval(timer)
  }, [])
  if (pending) return null
  if (failed) {
    // No evidence is not an alarm: saying "全部正常" from a failed read is the
    // one thing this panel must never do.
    return (
      <section className='rounded-xl border border-dashed bg-card p-4 text-sm text-muted-foreground'>
        归档运行状态读不到，无法判断上传是否正常。
        {error && <span className='ml-1 text-destructive'>{error}</span>}
      </section>
    )
  }
  if (!targets.length) return null
  const byID = new Map(statuses.map((item) => [item.target_id ?? '', item]))
  const totalFailed = statuses.reduce((sum, item) => sum + (item.failed ?? 0), 0)
  return (
    <section className='space-y-3 rounded-xl border bg-card p-4' data-testid='archive-run-status'>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <div>
          <h3 className='text-sm font-medium'>运行状态</h3>
          <p className='mt-1 text-xs text-muted-foreground'>
            上传任务的实时统计，每 15 秒刷新；「已归档」按目标统计，同一片段上传到两个目标算两份。
          </p>
        </div>
        {totalFailed > 0 && (
          <span className='rounded-full border border-red-500/30 bg-red-500/10 px-2 py-0.5 text-[11px] text-red-500'>
            {totalFailed} 项失败
          </span>
        )}
      </div>
      <div className='space-y-2'>
        {targets.map((target) => {
          const id = target.id ?? ''
          const status = byID.get(id)
          const state = uploadState(status)
          const age = pendingAge(status?.oldest_pending_at, now)
          return (
            <div
              key={id}
              className='flex flex-wrap items-center gap-x-4 gap-y-1 rounded-lg border px-3 py-2 text-sm'
              data-testid='archive-status-row'
            >
              <span className='font-medium'>{target.name}</span>
              <StateBadge state={state} />
              <span className='text-xs text-muted-foreground'>
                已归档 <span className='tabular-nums text-foreground'>{status?.verified ?? 0}</span> 项 ·{' '}
                <span className='tabular-nums text-foreground'>{bytesLabel(status?.verified_bytes)}</span>
              </span>
              {(status?.uploading ?? 0) > 0 && (
                <span className='text-xs text-amber-600 dark:text-amber-400'>
                  上传中 {status?.uploading}
                </span>
              )}
              {age && <span className='text-xs text-muted-foreground'>{age}</span>}
              {state.kind === 'failing' && (
                <>
                  <span className='text-xs text-red-500'>
                    {state.count} 项失败 · {uploadErrorText(state.code)}
                  </span>
                  <Button size='sm' variant='outline' onClick={() => onRetry(id)}>
                    重试失败项
                  </Button>
                </>
              )}
            </div>
          )
        })}
      </div>
    </section>
  )
}

function StateBadge({ state }: { state: ReturnType<typeof uploadState> }) {
  const styles: Record<ReturnType<typeof uploadState>['kind'], string> = {
    idle: 'border-dashed text-muted-foreground',
    healthy: 'border-emerald-500/20 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400',
    uploading: 'border-amber-500/30 bg-amber-500/10 text-amber-600 dark:text-amber-400',
    failing: 'border-red-500/30 bg-red-500/10 text-red-500',
  }
  return (
    <span className={cn('rounded-full border px-1.5 py-0.5 text-[11px]', styles[state.kind])}>
      {UPLOAD_STATE_LABEL[state.kind]}
    </span>
  )
}

/**
 * ChannelOverview shows the binding from the footage's side: for each channel
 * that archives somewhere, how much of what it holds has actually arrived.
 *
 * The policy table above asks "where does this channel go"; this one answers
 * "did it get there", and lists the local retention window so the consequence
 * of a verified copy (local files are released after N hours) is visible on the
 * same line as the count it depends on.
 */
export function ChannelOverview({ items }: { items: ChannelStatus[] }) {
  if (!items.length) return null
  return (
    <section className='space-y-3 rounded-xl border bg-card p-4' data-testid='archive-channel-overview'>
      <div>
        <h3 className='text-sm font-medium'>通道归档总览</h3>
        <p className='mt-1 text-xs text-muted-foreground'>
          已绑定目标的通道。本机保留＝云端副本确认后，本地文件再留多少小时；为 0 表示未启用，本机按录像保留策略处理。
        </p>
      </div>
      <div className='space-y-1.5'>
        {items.map((item) => {
          const cover = coverage(item.copied, item.segments)
          const label = `CH${String(item.channel_no ?? 0).padStart(2, '0')} ${item.name ?? ''}`
          return (
            <div
              key={item.channel_id}
              className='flex flex-wrap items-center gap-x-4 gap-y-1 text-sm'
              data-testid='archive-channel-row'
            >
              <span className='w-44 truncate'>{label}</span>
              <span className='text-xs text-muted-foreground'>
                目标 <span className='text-foreground'>{item.target_name}</span>
                {!item.target_enabled && (
                  <span className='ml-1 rounded-full border border-dashed px-1.5 py-0.5 text-[11px]'>
                    已停用
                  </span>
                )}
              </span>
              <span className='text-xs text-muted-foreground'>
                {MODE_LABEL[item.mode ?? ''] ?? item.mode ?? ''}
              </span>
              {cover ? (
                <span className='flex items-center gap-2 text-xs text-muted-foreground'>
                  <span className='h-1.5 w-24 overflow-hidden rounded-full bg-muted'>
                    <span
                      className='block h-full rounded-full bg-emerald-500'
                      style={{ width: cover.percent + '%' }}
                    />
                  </span>
                  <span className='tabular-nums'>
                    {cover.copied}/{cover.segments} 段已归档
                  </span>
                </span>
              ) : (
                <span className='text-xs text-muted-foreground'>本机暂无待归档片段</span>
              )}
              {(item.failed ?? 0) > 0 && (
                <span className='text-xs text-red-500'>{item.failed} 项失败</span>
              )}
              <span className='text-xs text-muted-foreground'>
                本机保留{' '}
                <span className='text-foreground'>
                  {(item.local_retention_hours ?? 0) > 0 ? `${item.local_retention_hours} 小时` : '未启用'}
                </span>
              </span>
            </div>
          )
        })}
      </div>
    </section>
  )
}
