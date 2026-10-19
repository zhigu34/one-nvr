import { Link } from '@tanstack/react-router'
import { Zap } from 'lucide-react'
import type { Schema } from '@/lib/types'
import { Checkbox } from '@/components/ui/checkbox'
import { TableCell, TableRow } from '@/components/ui/table'
import { StatusBadge } from '@/features/foundation/status-badge'
import { useAPI } from '@/features/foundation/hooks'
import { cn } from '@/lib/utils'
import { WeekPills } from './week-pills'
import { weekPill } from './mode-format'
import { remainingLabel } from './manual-format'

/**
 * One channel's current recording plan, read only.
 *
 * Every control that used to live in this row — the mode select, the pool
 * dropdown, the event switch, the schedule editor, the manual-window button —
 * moved to the page's single configuration bar. A table of sixteen rows each
 * carrying four controls is sixteen places to change one setting, and the
 * operator had to work out per row which of them they were editing. The bar
 * states the plan once and applies it to whatever is selected.
 *
 * What remains here is the answer: what is this channel doing right now. Clicking
 * anywhere on the row selects it, because selecting is the only thing a row still
 * does.
 */
export function PolicyRow({
  channel,
  summary,
  selected,
  onSelect,
  zone,
  poolNames,
}: {
  channel: Schema<'Channel'>
  summary?: Schema<'ChannelSummary'>
  selected: boolean
  onSelect: (checked: boolean) => void
  zone: string
  // The summary carries the pool's id, not its name. The page already fetches
  // the pool list once, so the name is looked up there rather than requested per
  // row.
  poolNames?: Map<string, string>
}) {
  const label = `CH${String(channel.channel_no).padStart(2, '0')}`
  const configured =
    !!summary?.current_revision_id && summary?.main.state !== 'not_configured'
  const mode = summary?.recording_mode ?? 'none'
  const poolName = poolNames?.get(summary?.storage_pool_id ?? '') || ''
  const eventOn = !!summary?.event_recording_enabled
  // Online means the main stream is healthy right now. Anything else is shown as
  // its own state rather than collapsed into "offline": a channel that was never
  // observed and one whose source is unreachable need different reactions.
  const health: {
    tone: 'success' | 'destructive' | 'neutral'
    label: string
  } = !summary
    ? { tone: 'neutral', label: '未知' }
    : !configured
      ? { tone: 'neutral', label: '未配置' }
      : summary.main.state === 'healthy'
        ? { tone: 'success', label: '在线' }
        : summary.main.state === 'unavailable'
          ? { tone: 'destructive', label: '离线' }
          : { tone: 'neutral', label: '未知' }

  // The week's pills only mean something for a scheduled channel; fetching the
  // schedule for the other fifteen rows would be sixteen requests for a column
  // that renders nothing.
  const schedule = useAPI<Schema<'ChannelSchedule'>>(
    `/api/v1/channels/${channel.id}/recording-schedule`,
    configured && mode === 'planned'
  )
  const windows = schedule.data?.windows ?? []
  const pill = weekPill(windows)

  // A running manual window is the one state that is not a setting, so it is the
  // one badge that appears and disappears on its own.
  const manual = useAPI<Schema<'ManualRecording'>>(
    `/api/v1/channels/${channel.id}/manual-recording`,
    configured,
    15_000
  )
  const manualWindow = manual.error ? null : manual.data

  return (
    <TableRow
      data-testid='policy-row'
      data-selected={selected}
      // The whole row is the selection target: with the controls gone there is
      // nothing else to click, and a checkbox alone makes selecting sixteen rows
      // a game of aiming.
      onClick={() => onSelect(!selected)}
      className={cn(
        'cursor-pointer transition-colors',
        selected && 'bg-emerald-500/5 hover:bg-emerald-500/10'
      )}
    >
      <TableCell onClick={(event) => event.stopPropagation()}>
        <Checkbox
          aria-label={`选择 ${label}`}
          checked={selected}
          onCheckedChange={(state) => onSelect(state === true)}
        />
      </TableCell>
      <TableCell className='font-mono text-xs text-muted-foreground'>
        {label}
      </TableCell>
      <TableCell>
        <span className='flex items-center gap-1.5'>
          <span className='truncate'>{channel.channel_name}</span>
          <StatusBadge tone={health.tone}>{health.label}</StatusBadge>
        </span>
      </TableCell>
      <TableCell>
        {configured ? (
          poolName ? (
            <span className='inline-flex items-center rounded-full border bg-muted px-2 py-0.5 text-xs'>
              {poolName}
            </span>
          ) : (
            <Link
              to='/storage'
              search={{ tab: 'pools' }}
              onClick={(event) => event.stopPropagation()}
              className='inline-flex items-center rounded-full border border-amber-500/30 bg-amber-500/10 px-2 py-0.5 text-xs text-amber-600 dark:text-amber-400'
            >
              未绑定 · 去分配 ↗
            </Link>
          )
        ) : (
          <span className='text-xs text-zinc-600'>—</span>
        )}
      </TableCell>
      <TableCell>
        <span className='flex flex-wrap items-center gap-2'>
          <ModeBadge mode={configured ? mode : 'unconfigured'} />
          {configured && mode === 'planned' && (
            <>
              <WeekPills windows={windows} text={pill.text} label={label} />
              {schedule.data?.active && (
                <span className='inline-flex shrink-0 items-center gap-1 rounded-full border border-emerald-500/30 bg-emerald-500/10 px-2 py-0.5 text-xs text-emerald-600 dark:text-emerald-400'>
                  <span aria-hidden className='size-1.5 rounded-full bg-emerald-500' />
                  时段内
                </span>
              )}
            </>
          )}
          {configured && (
            <span
              data-testid='event-recording-badge'
              title={
                eventOn
                  ? '事件到达时保留前 5 秒到后 5 秒；仅在通道没有常规录像时启用缓冲'
                  : '事件不会额外保留画面'
              }
              className={cn(
                'inline-flex shrink-0 items-center gap-1 rounded-full border px-2 py-0.5 text-xs',
                eventOn
                  ? 'border-amber-500/40 bg-amber-500/10 text-amber-600 dark:text-amber-400'
                  : 'border-border text-muted-foreground'
              )}
            >
              <Zap aria-hidden className='size-3' />
              事件录像 · {eventOn ? '开' : '关'}
              {eventOn && (
                <span className='text-[10px] opacity-70'>前5s/后5s</span>
              )}
            </span>
          )}
          {manualWindow && (
            <span
              data-testid='manual-recording-active'
              className='inline-flex shrink-0 items-center gap-1 rounded-full border border-red-500/40 bg-red-500/10 px-2 py-0.5 text-xs text-red-600 dark:text-red-400'
            >
              <span
                aria-hidden
                className='size-1.5 animate-pulse rounded-full bg-red-500'
              />
              限时录像中（剩余 {remainingLabel(manualWindow.remaining_ms)}）
            </span>
          )}
        </span>
      </TableCell>
      <TableCell className='text-xs text-muted-foreground'>
        {summary?.last_error ? (
          <span className='text-red-500' title={summary.last_error}>
            {summary.last_error}
          </span>
        ) : (
          moment(summary?.updated_at, zone)
        )}
      </TableCell>
    </TableRow>
  )
}

/**
 * The mode as a badge rather than a control.
 *
 * "全天录像" carries 7×24h because that is the fact an operator checks for: a
 * channel that records continuously and one that records on a schedule look
 * identical at a glance otherwise, and the difference decides whether an event
 * can link to footage.
 */
function ModeBadge({ mode }: { mode: string }) {
  switch (mode) {
    case 'continuous':
      return (
        <span className='inline-flex shrink-0 items-center gap-1 rounded-full border border-emerald-500/40 bg-emerald-500/10 px-2 py-0.5 text-xs font-medium text-emerald-600 dark:text-emerald-400'>
          <span aria-hidden className='size-1.5 rounded-full bg-emerald-500' />
          全天录像 (7×24h)
        </span>
      )
    case 'planned':
      return (
        <span className='inline-flex shrink-0 items-center gap-1 rounded-full border border-teal-500/40 bg-teal-500/10 px-2 py-0.5 text-xs font-medium text-teal-600 dark:text-teal-400'>
          <span aria-hidden className='size-1.5 rounded-full bg-teal-500' />
          定时录像
        </span>
      )
    case 'unconfigured':
      return <span className='text-xs text-zinc-600'>未配置源</span>
    default:
      return (
        <span className='inline-flex shrink-0 items-center gap-1 rounded-full border border-border px-2 py-0.5 text-xs text-muted-foreground'>
          <span aria-hidden className='size-1.5 rounded-full bg-zinc-500' />
          关闭录像
        </span>
      )
  }
}

function moment(value: string | null | undefined, zone: string): string {
  if (!value) return '—'
  const at = new Date(value)
  if (Number.isNaN(at.getTime())) return '—'
  return at.toLocaleString('zh-CN', {
    timeZone: zone || 'Asia/Shanghai',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  })
}
