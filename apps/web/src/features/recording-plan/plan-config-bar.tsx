import { useMemo, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { CalendarClock, Check, Zap } from 'lucide-react'
import { ApiError, apiRequest, jsonRequest } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { useAPI, useAction } from '@/features/foundation/hooks'
import { sourceCommand } from '@/features/channels/api'
import { boundOrDefaultPool, verifyPoolWrite } from '@/features/storage-pools/pool-check'
import { cn } from '@/lib/utils'
import { MANUAL_DURATIONS } from './durations'
import { remainingLabel } from './manual-format'
import { MODE_LABELS } from './mode-format'
import { ScheduleEditor } from './schedule-editor'
import type { ScheduleWindow } from './schedule-format'

/**
 * The page's single configuration surface.
 *
 * Every recording decision is made here and applied to whatever is selected: the
 * mode, the weekly schedule, the event switch, the manual window. The table below
 * only reports what each channel ended up with.
 *
 * Why one bar rather than controls per row: a sixteen-row table where each row
 * carries a mode select, a pool dropdown, an event switch and a schedule editor is
 * sixteen places to change one setting. The operator also had to work out, per
 * row, which of four controls they were editing — and the two "manual" ones meant
 * different things.
 *
 * The bar echoes a single selected channel's current plan, so editing one channel
 * is the same gesture as editing sixteen: select, adjust, apply.
 */
export function PlanConfigBar({
  chosen,
  chosenCount,
  total,
  onSelectAll,
  onClear,
  summaries,
  peers,
  timezone,
  pools,
}: {
  chosen: Schema<'Channel'>[]
  chosenCount: number
  total: number
  onSelectAll: () => void
  onClear: () => void
  summaries: Map<string, Schema<'ChannelSummary'>>
  peers: { id: string; label: string }[]
  timezone: string
  pools: Schema<'Pool'>[]
}) {
  const client = useQueryClient()
  const action = useAction()
  const [mode, setMode] = useState<'none' | 'continuous' | 'planned'>('none')
  const [eventOn, setEventOn] = useState(false)
  const [windows, setWindows] = useState<ScheduleWindow[] | null>(null)
  const [scheduleOpen, setScheduleOpen] = useState(false)
  const [manualMinutes, setManualMinutes] = useState(30)
  const [manualPending, setManualPending] = useState(false)
  const [outcome, setOutcome] = useState('')

  // Echo the single selected channel's plan. With more than one selected there is
  // no "current" value to show, so the bar keeps whatever the operator set — a
  // mixed selection has no single state to echo, and inventing one ("the first
  // channel's") would be a lie the operator could act on.
  const solo = chosenCount === 1 ? chosen[0] : undefined
  const soloSummary = solo ? summaries.get(solo.id ?? '') : undefined
  const soloSchedule = useAPI<Schema<'ChannelSchedule'>>(
    `/api/v1/channels/${solo?.id ?? ''}/recording-schedule`,
    !!solo
  )
  const soloManual = useAPI<Schema<'ManualRecording'>>(
    `/api/v1/channels/${solo?.id ?? ''}/manual-recording`,
    !!solo,
    15_000
  )
  // The draft starts from the solo channel when one is selected, and is otherwise
  // whatever the operator chose. Recomputed only when the selection changes, so
  // typing in the schedule editor is not overwritten by a refetch.
  // The echo is an initial value, not an override. `draft` holds whatever the
  // operator has changed; until they change something it is null and the solo
  // channel's own plan shows. Treating the echo as authoritative (the first
  // version did) meant clicking the switch on a single selected channel changed
  // local state that was then immediately discarded — the control looked dead.
  const [draft, setDraft] = useState<{
    mode: 'none' | 'continuous' | 'planned'
    event: boolean
  } | null>(null)
  const echoed = useMemo(() => {
    if (!solo) return null
    return {
      mode: (soloSummary?.recording_mode ?? 'none') as
        | 'none'
        | 'continuous'
        | 'planned',
      event: !!soloSummary?.event_recording_enabled,
    }
  }, [solo, soloSummary?.recording_mode, soloSummary?.event_recording_enabled])
  const effectiveMode = draft?.mode ?? echoed?.mode ?? mode
  const effectiveEvent = draft?.event ?? echoed?.event ?? eventOn
  const scheduleWindows = windows ?? soloSchedule.data?.windows ?? []
  const manualWindow = soloManual.error ? null : soloManual.data

  const needsPool = effectiveMode === 'continuous' || effectiveMode === 'planned'
  const canApply = chosenCount > 0 && (!needsPool || pools.some((pool) => pool.enabled))

  function pickMode(next: 'none' | 'continuous' | 'planned') {
    setOutcome('')
    setMode(next)
    setDraft((current) => ({
      mode: next,
      event: current?.event ?? effectiveEvent,
    }))
    setWindows(null)
    if (next === 'planned') setScheduleOpen(true)
  }

  function toggleEvent(next: boolean) {
    setOutcome('')
    setEventOn(next)
    setDraft((current) => ({
      mode: current?.mode ?? effectiveMode,
      event: next,
    }))
  }

  /**
   * Apply the bar's plan to every selected channel.
   *
   * Three writes per channel, each with its own version token and its own audit
   * row: the schedule (only for planned), the mode switch, and the event toggle.
   * They are sequential for the same reason every batch on this product is —
   * parallel writes race each other's version checks.
   *
   * The mode is applied through the switch job, which is asynchronous: this waits
   * for it so a partial batch reports what actually happened rather than "queued".
   */
  async function apply() {
    if (!canApply) return
    setOutcome('')
    await action.run(async () => {
      const ok: string[] = []
      const failed: string[] = []
      for (const channel of chosen) {
        const label = `CH${String(channel.channel_no).padStart(2, '0')}`
        try {
          const policy = await apiRequest<Schema<'RecordingPolicy'>>(
            `/api/v1/channels/${channel.id}/recording-policy`
          )
          if (effectiveMode === 'planned' && windows) {
            await apiRequest<Schema<'ChannelSchedule'>>(
              `/api/v1/channels/${channel.id}/recording-schedule`,
              jsonRequest('PUT', { windows })
            )
          }
          if (policy.mode !== effectiveMode) {
            // A policy write is a source change: it queues a switch job, and the
            // server rejects it without an idempotency key. sourceCommand is the
            // helper that carries one, which is why the old per-row control
            // worked and this bar did not.
            //
            // One retry on 409: the token read a moment ago can be stale because
            // anything else on the site may have touched the channel between the
            // read and the write, and a batch that fails the whole row for that
            // reports a conflict the operator cannot resolve from here.
            let change: Schema<'SourceChange'>
            try {
              change = await sourceCommand<Schema<'SourceChange'>>(
                `/api/v1/channels/${channel.id}/recording-policy`,
                { mode: effectiveMode },
                policy.version,
                undefined,
                'PUT'
              )
            } catch (cause) {
              // Two different 409s arrive here and they need different
              // handling. A stale version is retried once with a fresh token; a
              // missing write proof is repaired by running one real pool check
              // and retrying. Treating both as "someone else changed it" — which
              // the first version did — told the operator to refresh when the
              // actual problem was storage evidence, and refreshing would never
              // fix it.
              if (!(cause instanceof ApiError) || cause.status !== 409) throw cause
              if (cause.code === 'zlm_write_evidence_unavailable') {
                const poolID = await boundOrDefaultPool(channel.id ?? '')
                if (!poolID) throw cause
                const verified = await verifyPoolWrite(poolID)
                if (!verified.ok) {
                  // Rethrow the original: it carries the server's own reason,
                  // and a fresh message would discard the context that says
                  // which check failed.
                  throw cause
                }
                const fresh = await apiRequest<Schema<'RecordingPolicy'>>(
                  `/api/v1/channels/${channel.id}/recording-policy`
                )
                change = await sourceCommand<Schema<'SourceChange'>>(
                  `/api/v1/channels/${channel.id}/recording-policy`,
                  { mode: effectiveMode },
                  fresh.version,
                  undefined,
                  'PUT'
                )
              } else {
                const fresh = await apiRequest<Schema<'RecordingPolicy'>>(
                  `/api/v1/channels/${channel.id}/recording-policy`
                )
                change = await sourceCommand<Schema<'SourceChange'>>(
                  `/api/v1/channels/${channel.id}/recording-policy`,
                  { mode: effectiveMode },
                  fresh.version,
                  undefined,
                  'PUT'
                )
              }
            }
            await settle(change.job_id)
          }
          if (!!policy.event_recording_enabled !== effectiveEvent) {
            // The token is re-read: the mode switch above advanced it.
            const fresh = await apiRequest<Schema<'RecordingPolicy'>>(
              `/api/v1/channels/${channel.id}/recording-policy`
            )
            await apiRequest(
              `/api/v1/channels/${channel.id}/event-recording`,
              jsonRequest('PUT', { enabled: effectiveEvent }, fresh.version)
            )
          }
          ok.push(label)
        } catch (cause) {
          failed.push(`${label}: ${describe(cause)}`)
        }
      }
      const parts: string[] = []
      if (ok.length) parts.push(`${ok.length} 路已应用`)
      if (failed.length) parts.push(`失败：${failed.join('；')}`)
      setOutcome(parts.join(' · '))
      await client.invalidateQueries()
    }, '')
  }

  async function settle(jobId: string) {
    // The switch job runs on the worker; without waiting, the batch would report
    // "applied" for a job that later fails.
    for (let attempt = 0; attempt < 60; attempt++) {
      await new Promise((resolve) => setTimeout(resolve, 500))
      const job = await apiRequest<Schema<'Job'>>(`/api/v1/jobs/${jobId}`)
      if (job.state === 'succeeded') return
      if (job.state === 'failed') throw new Error(job.error_code || '切换任务失败')
    }
    throw new Error('切换任务超时')
  }

  async function manual(kind: 'start' | 'stop') {
    if (chosenCount === 0) return
    setManualPending(true)
    setOutcome('')
    const ok: string[] = []
    const failed: string[] = []
    try {
      for (const channel of chosen) {
        const label = `CH${String(channel.channel_no).padStart(2, '0')}`
        try {
          if (kind === 'start') {
            await apiRequest(
              `/api/v1/channels/${channel.id}/manual-recording`,
              jsonRequest('POST', { minutes: manualMinutes })
            )
          } else {
            await apiRequest(
              `/api/v1/channels/${channel.id}/manual-recording`,
              { method: 'DELETE' }
            )
          }
          ok.push(label)
        } catch (cause) {
          failed.push(`${label}: ${describe(cause)}`)
        }
      }
      const parts: string[] = []
      if (ok.length) parts.push(`${ok.length} 路${kind === 'start' ? '已开始' : '已停止'}限时录像`)
      if (failed.length) parts.push(`失败：${failed.join('；')}`)
      setOutcome(parts.join(' · '))
      await client.invalidateQueries()
    } finally {
      setManualPending(false)
    }
  }

  return (
    <div className='rounded-xl border border-border bg-card shadow-2xs'>
      <div className='flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-2.5'>
        {/* Selection: the count plus the two shortcuts, because selecting sixteen
            rows one at a time is the most repeated action on this page. */}
        <span className='flex items-center gap-2 text-sm font-medium whitespace-nowrap'>
          已选 {chosenCount} / {total} 路
          <button
            type='button'
            className='text-xs font-normal text-muted-foreground underline-offset-2 hover:underline'
            onClick={onSelectAll}
          >
            全选
          </button>
          <button
            type='button'
            className='text-xs font-normal text-muted-foreground underline-offset-2 hover:underline'
            onClick={onClear}
          >
            清空
          </button>
        </span>
        {solo && (
          <span className='rounded-full border border-primary/40 bg-primary/10 px-2 py-0.5 font-mono text-xs text-primary'>
            CH{String(solo.channel_no).padStart(2, '0')}
          </span>
        )}
        {/* Nothing selected: every control on this bar is disabled, and without
            a word of explanation the page reads as broken. Say what to do
            instead of leaving an operator to guess why the buttons are dead. */}
        {chosenCount === 0 && (
          <span className='rounded-md border border-dashed border-border px-2 py-0.5 text-xs text-muted-foreground'>
            先在下方勾选要改的通道
          </span>
        )}

        {/* Mode as segmented buttons: three values with one meaning each, and a
            select hides two of them behind a click. */}
        <span className='flex items-center gap-1.5'>
          <span className='text-xs text-muted-foreground'>常规录像</span>
          <span className='inline-flex overflow-hidden rounded-md border'>
            {(['none', 'continuous', 'planned'] as const).map((value) => (
              <button
                key={value}
                type='button'
                aria-pressed={effectiveMode === value}
                onClick={() => pickMode(value)}
                className={cn(
                  'px-2.5 py-1 text-xs transition-colors',
                  effectiveMode === value
                    ? value === 'continuous'
                      ? 'bg-emerald-500 font-medium text-zinc-950'
                      : value === 'planned'
                        ? 'bg-teal-500 font-medium text-zinc-950'
                        : 'bg-zinc-600 font-medium text-white'
                    : 'text-muted-foreground hover:bg-accent hover:text-foreground'
                )}
              >
                {MODE_LABELS[value]}
              </button>
            ))}
          </span>
        </span>

        {/* The event switch is independent of the mode: it stacks on top of
            "off" or "scheduled" and is ignored where ordinary recording already
            covers the footage. */}
        <button
          type='button'
          aria-pressed={effectiveEvent}
          aria-label='事件录像开关'
          onClick={() => toggleEvent(!effectiveEvent)}
          title='事件到达时保留前 5 秒到后 5 秒；仅在通道没有常规录像时启用缓冲'
          className={cn(
            'inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs transition-colors',
            effectiveEvent
              ? 'border-amber-500/50 bg-amber-500/15 text-amber-600 dark:text-amber-400'
              : 'border-border text-muted-foreground hover:bg-accent'
          )}
        >
          <Zap aria-hidden className='size-3' />
          事件录像 · {effectiveEvent ? '开' : '关'}
          <span className='text-[10px] opacity-70'>前5s/后5s</span>
        </button>

        <Button
          type='button'
          size='sm'
          disabled={!canApply || action.pending}
          onClick={() => void apply()}
          className={cn(canApply && 'bg-emerald-500 font-medium text-zinc-950 hover:bg-emerald-400')}
        >
          <Check aria-hidden className='size-3.5' />
          应用到所选通道{chosenCount > 0 ? ` (${chosenCount})` : ''}
        </Button>
        {needsPool && !pools.some((pool) => pool.enabled) && (
          <span className='text-xs text-amber-600 dark:text-amber-400'>
            需要先在「存储」页启用一个存储池
          </span>
        )}

        {/* Emergency recording is separated by a rule: it is not a plan, it is a
            request with an expiry, and grouping it with the plan would invite
            reading it as one of the modes. */}
        <span className='ms-auto flex items-center gap-1.5 border-s ps-4'>
          <span className='text-xs text-muted-foreground'>限时录像</span>
          <select
            aria-label='限时录像时长'
            className='rounded-md border bg-background px-1.5 py-1 text-xs'
            value={manualMinutes}
            onChange={(event) => setManualMinutes(Number(event.target.value))}
          >
            {MANUAL_DURATIONS.map((minutes) => (
              <option key={minutes} value={minutes}>
                {minutes} 分钟
              </option>
            ))}
          </select>
          <Button
            type='button'
            size='sm'
            variant='outline'
            className='h-7 px-2 text-xs'
            disabled={chosenCount === 0 || manualPending}
            onClick={() => void manual('start')}
          >
            开始
          </Button>
          <Button
            type='button'
            size='sm'
            variant='ghost'
            className='h-7 px-2 text-xs'
            disabled={chosenCount === 0 || manualPending}
            onClick={() => void manual('stop')}
          >
            停止
          </Button>
          {manualWindow && (
            <span
              data-testid='manual-recording-active'
              className='inline-flex items-center gap-1 rounded-full border border-red-500/40 bg-red-500/10 px-2 py-0.5 text-xs text-red-600 dark:text-red-400'
            >
              <span aria-hidden className='size-1.5 animate-pulse rounded-full bg-red-500' />
              {remainingLabel(manualWindow.remaining_ms)}
            </span>
          )}
        </span>
      </div>

      {/* The schedule editor expands inside the bar, not in a table row: the
          schedule belongs to the plan being edited here, and a row-level editor
          was a second place to do the same thing. */}
      {effectiveMode === 'planned' && (
        <div className='border-t px-4 py-3'>
          <button
            type='button'
            aria-expanded={scheduleOpen}
            onClick={() => setScheduleOpen((value) => !value)}
            className='mb-2 inline-flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground'
          >
            <CalendarClock aria-hidden className='size-3.5' />
            {scheduleOpen ? '收起周计划' : '编辑周计划时段'}
            {!scheduleOpen && scheduleWindows.length > 0 && (
              <span className='text-foreground'>
                · {scheduleWindows.length} 段
              </span>
            )}
          </button>
          {scheduleOpen && (
            <ScheduleEditor
              peers={peers}
              windows={scheduleWindows}
              onWindowsChange={setWindows}
              timezone={timezone}
              open
              onOpenChange={(next) => setScheduleOpen(next)}
              onCancel={() => setScheduleOpen(false)}
              onCommitPlanned={async () => true}
            />
          )}
        </div>
      )}

      {outcome && (
        <p role='status' className='border-t px-4 py-2 text-xs text-muted-foreground'>
          {outcome}
        </p>
      )}
      {action.error && (
        <p role='alert' className='border-t px-4 py-2 text-xs text-red-500'>
          {action.error}
        </p>
      )}
    </div>
  )
}

function describe(cause: unknown): string {
  if (cause instanceof ApiError) {
    // A 409 is not one thing. Naming the wrong one sends the operator to
    // refresh a page that was never stale — which is exactly what happened
    // while this bar was being built.
    if (cause.code === 'zlm_write_evidence_unavailable') {
      return '存储池写入检查未通过，请到「存储」页运行一次检查'
    }
    if (cause.status === 409) return '配置已被其他人修改，请刷新后重试'
  }
  return cause instanceof Error && cause.message ? cause.message : '操作失败'
}
