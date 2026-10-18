import { useEffect, useMemo, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ApiError, apiRequest, jsonRequest } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { Checkbox } from '@/components/ui/checkbox'
import { TableCell, TableRow } from '@/components/ui/table'
import { SelectField } from '@/features/foundation/ui'
import { StatusBadge } from '@/features/foundation/status-badge'
import { useAPI } from '@/features/foundation/hooks'
import { sourceCommand } from '@/features/channels/api'
import { settleJob, verifyPoolWrite } from '@/features/storage-pools/pool-check'
import { cn } from '@/lib/utils'
import { ManualRecordingControl } from './manual-recording'
import { ScheduleEditor } from './schedule-editor'
import { WeekPills } from './week-pills'
import { MODE_LABELS, modeLabel, weekPill } from './mode-format'
import type { ScheduleWindow } from './schedule-format'

/**
 * Only the modes the control plane implements are selectable. Event recording is
 * still a placeholder: it needs the rolling ZLM buffer that batch 3 delivers.
 */
const PLACEHOLDER_MODES = ['event']

/**
 * Modes that record into a storage pool. 'planned' does — the schedule decides
 * when, not whether bytes land — so it carries the same pool requirement and the
 * same write-evidence recovery as continuous.
 */
const RECORDING_MODES = new Set(['continuous', 'planned'])

function channelLabel(channel: Schema<'Channel'>) {
  return `CH${String(channel.channel_no).padStart(2, '0')}`
}

function message(error: unknown) {
  return error instanceof Error && error.message
    ? error.message
    : '操作失败，请重试'
}

export function PolicyRow({
  channel,
  summary,
  pools = [],
  selected,
  onSelect,
  onApplied,
  onDirtyChange,
  schedulePeers,
  editorOpen = false,
  onEditorOpenChange,
}: {
  channel: Schema<'Channel'>
  summary?: Schema<'ChannelSummary'>
  // Pool rows of the site, for the read-only name pill: the summary carries the
  // binding's id, and the name lives with the pool.
  pools?: Schema<'Pool'>[]
  // Other channels that may receive this channel's week (REC-02's "copy to other
  // channels"). Passed in rather than fetched per row: every row would otherwise
  // repeat the same channel list request.
  schedulePeers?: { id: string; label: string }[]
  selected: boolean
  onSelect: (checked: boolean) => void
  onApplied: () => void
  onDirtyChange: (dirty: boolean) => void
  // Panel-open state from the page's accordion: true only for the one row
  // whose editor is currently open. All opening and closing flows through the
  // page's id, which is what makes one-open-panel-at-a-time enforceable.
  editorOpen?: boolean
  // The page's accordion: called with this row's channel id when its editor
  // opens, so the page can collapse the other rows' panels.
  onEditorOpenChange?: (channelId: string) => void
}) {
  const client = useQueryClient()
  // The recording plan reads mode, pool and health from the one summary
  // response the page already fetched: a status request per row was the N+1
  // that slowed first paint on a 32-channel site. Writes still read the live
  // version right before they send. `mode` overlays the operator's unsaved
  // choice on the stored value until the write lands.
  const storedMode = summary?.recording_mode ?? 'none'
  const boundPool = summary?.storage_pool_id || ''
  const [draftMode, setDraftMode] = useState<string | null>(null)
  const mode = draftMode ?? storedMode
  const [pending, setPending] = useState('')
  const [notice, setNotice] = useState('')
  const [error, setError] = useState('')
  const [succeeded, setSucceeded] = useState('')
  const controller = useRef<AbortController | null>(null)
  useEffect(() => () => controller.current?.abort(), [])

  // The row's schedule is read for the mini pills. It only matters for a
  // channel that is (or is being switched to) planned; the hook still runs
  // unconditionally so the editor's open state and this summary share one
  // source of truth from the same endpoint.
  const [scheduleOverride, setScheduleOverride] = useState<
    ScheduleWindow[] | null
  >(null)
  // The editor's current draft, mirrored for the commit callback: the save must
  // write the windows the operator is looking at, not the stored ones.
  const editorWindowsRef = useRef<ScheduleWindow[]>([])
  const schedule = useAPI<Schema<'ChannelSchedule'>>(
    `/api/v1/channels/${channel.id}/recording-schedule`,
    mode === 'planned'
  )
  const fetchedWindows = schedule.data?.windows
  const storedWindows = useMemo(
    () => scheduleOverride ?? fetchedWindows ?? [],
    [scheduleOverride, fetchedWindows]
  )
  useEffect(() => {
    editorWindowsRef.current = storedWindows
  }, [storedWindows])
  const pill = weekPill(storedWindows)

  const label = channelLabel(channel)
  const configured = channel.permissions.includes('configure')
  // No applied camera means nothing to record and no pool to bind: the whole
  // row goes inert until the channel is connected (same rule as detection).
  const unconfigured =
    !summary?.current_revision_id && summary?.main.state === 'not_configured'
  const inert = unconfigured || !configured
  const poolLabel = pools.find((entry) => entry.id === boundPool)?.name || ''
  const health = !summary
    ? 'unknown'
    : unconfigured
      ? 'unconfigured'
      : !channel.enabled
        ? 'disabled'
        : summary.recording.state === 'healthy'
          ? 'recording'
          : summary.main.state === 'healthy'
            ? 'online'
            : 'offline'
  const HEALTH: Record<
    typeof health,
    { tone: 'success' | 'neutral' | 'warning'; text: string } | null
  > = {
    recording: { tone: 'success', text: '录制中' },
    online: { tone: 'success', text: '在线' },
    offline: { tone: 'warning', text: '离线' },
    disabled: { tone: 'neutral', text: '通道已停用' },
    unconfigured: { tone: 'neutral', text: '未配置源' },
    unknown: null,
  }
  const badge = HEALTH[health]
  // Compare the draft with the stored value, not with the overlaid display
  // value: `mode` already folds the draft in, so comparing against it would
  // make the edit state collapse the moment the dropdown commits.
  const modeDirty = draftMode !== null && draftMode !== storedMode
  // Report only on an actual transition: an unstable parent callback in the
  // effect deps would otherwise loop render → report → setState → render.
  const dirty = modeDirty
  const reported = useRef(false)
  useEffect(() => {
    if (dirty !== reported.current) {
      reported.current = dirty
      onDirtyChange(dirty)
    }
  }, [dirty, onDirtyChange])

  async function sendWithRecovery(
    body: unknown,
    version: number,
    abort: AbortController
  ) {
    const send = () =>
      sourceCommand<Schema<'SourceChange'>>(
        `/api/v1/channels/${channel.id}/recording-policy`,
        body,
        version,
        abort.signal,
        'PUT'
      )
    try {
      return await send()
    } catch (cause) {
      if (abort.signal.aborted) return null
      // The write proof is only valid for 30 seconds. Instead of asking the
      // operator to act faster, recover by running one real pool check and
      // retrying once; the evidence requirement itself is unchanged.
      if (
        !(cause instanceof ApiError) ||
        cause.code !== 'zlm_write_evidence_unavailable' ||
        !boundPool
      )
        throw cause
      setNotice('存储池写入检查已过期，正在自动重新验证…')
      const verified = await verifyPoolWrite(boundPool, abort.signal)
      if (abort.signal.aborted) return null
      if (!verified.ok) {
        setError(verified.message)
        return null
      }
      setNotice('验证通过，正在继续…')
      return await send()
    }
  }

  /**
   * One write per mode change. none/continuous take effect from the select
   * itself (a visible mode flip that needs a second confirm click reads as two
   * controls agreeing about one thing); planned opens the editor, whose single
   * "保存并启用" button commits windows and mode together.
   */
  async function applyMode(target: string) {
    if (pending || inert) return
    const abort = new AbortController()
    controller.current = abort
    setPending('policy')
    setError('')
    setNotice('')
    setSucceeded('')
    try {
      const policy = await apiRequest<Schema<'RecordingPolicy'>>(
        `/api/v1/channels/${channel.id}/recording-policy`,
        { signal: abort.signal }
      )
      if (abort.signal.aborted) return
      const change = await sendWithRecovery(
        { mode: target },
        policy.version,
        abort
      )
      if (abort.signal.aborted || change === null) return
      setDraftMode(null)
      onApplied()
      await client.invalidateQueries()
      const settled = await settleJob(change.job_id, abort.signal)
      if (abort.signal.aborted) return
      if (settled.state === 'succeeded') {
        setSucceeded('policy')
      } else if (settled.state === 'failed') {
        setError(settled.message || '任务失败')
      }
    } catch (cause) {
      if (!abort.signal.aborted) {
        setError(message(cause))
      }
    } finally {
      if (!abort.signal.aborted) setPending('')
    }
  }

  function pickMode(next: string) {
    setError('')
    setNotice('')
    setSucceeded('')
    if (next === 'planned') {
      // No write yet, and the panel opens with the pick: an editor you still
      // have to find contradicts the single "保存并启用" story. Cancelling the
      // panel reverts the whole pick. The open state lives with the page's
      // accordion id, so one open panel at a time is enforced in one place.
      setDraftMode(next)
      setScheduleOverride(null)
      onEditorOpenChange?.(channel.id ?? '')
      return
    }
    // Leaving planned closes this row's panel (the page flips the id away);
    // its draft is dropped unsaved.
    if (mode === 'planned') {
      setScheduleOverride(null)
      onEditorOpenChange?.('')
    }
    // none/continuous are one value with one meaning; the select IS the apply.
    setDraftMode(next)
    void applyMode(next)
  }

  // Called by the schedule editor's "保存并启用定时录像" with the editor's draft:
  // the schedule PUT first, then the mode switch, in one visual act. Either
  // write failing keeps the row's error.
  async function commitPlanned(windows: ScheduleWindow[]) {
    if (pending || inert) return null
    const abort = new AbortController()
    controller.current = abort
    setPending('policy')
    setError('')
    setNotice('')
    try {
      try {
        await apiRequest<Schema<'ChannelSchedule'>>(
          `/api/v1/channels/${channel.id}/recording-schedule`,
          jsonRequest('PUT', { windows })
        )
      } catch (cause) {
        if (cause instanceof ApiError && cause.code === 'schedule_overlap') {
          setError('同一星期几的时段不能重叠')
        } else {
          setError(message(cause))
        }
        return null
      }
      if (abort.signal.aborted) return null
      setScheduleOverride(windows)
      const policy = await apiRequest<Schema<'RecordingPolicy'>>(
        `/api/v1/channels/${channel.id}/recording-policy`,
        { signal: abort.signal }
      )
      if (abort.signal.aborted) return null
      const change = await sendWithRecovery(
        { mode: 'planned' },
        policy.version,
        abort
      )
      if (abort.signal.aborted || change === null) return null
      setDraftMode(null)
      onApplied()
      await client.invalidateQueries()
      const settled = await settleJob(change.job_id, abort.signal)
      if (abort.signal.aborted) return null
      if (settled.state === 'succeeded') {
        setSucceeded('policy')
        return true
      }
      if (settled.state === 'failed') setError(settled.message)
      else setError('任务超时，请刷新查看状态')
      return null
    } catch (cause) {
      if (!abort.signal.aborted) setError(message(cause))
      return null
    } finally {
      if (!abort.signal.aborted) setPending('')
    }
  }

  return (
    <TableRow
      data-testid='policy-row'
      className={cn(
        health === 'recording' && 'bg-emerald-950/15 hover:bg-emerald-950/25',
        inert && 'opacity-40'
      )}
    >
      <TableCell>
        <Checkbox
          aria-label={`选择 ${label}`}
          className='rounded border-zinc-600'
          checked={selected}
          disabled={inert}
          onCheckedChange={(state) => onSelect(state === true)}
        />
      </TableCell>
      <TableCell className='font-mono text-xs'>{label}</TableCell>
      <TableCell>
        {inert ? (
          // Static placeholder: unconfigured rows render no interactive
          // controls at all, so 16 selects never mount for empty slots.
          <div className='flex items-center gap-2'>
            <span className='text-zinc-500'>{channel.channel_name}</span>
            <span className='inline-flex shrink-0'>
              <StatusBadge tone='neutral'>未配置源</StatusBadge>
            </span>
          </div>
        ) : (
          <div className='flex items-center gap-2'>
            <span className='font-medium'>{channel.channel_name}</span>
            {badge && (
              <span className='inline-flex shrink-0'>
                <StatusBadge tone={badge.tone}>{badge.text}</StatusBadge>
              </span>
            )}
          </div>
        )}
      </TableCell>
      <TableCell>
        {inert ? (
          <span className='text-xs text-zinc-600'>—</span>
        ) : boundPool ? (
          // Read-only since binding moved to the pool page: a dropdown beside
          // the mode select invited a second unsaved draft per row, and the
          // pool question ("where do these bytes go?") is asked once per pool,
          // not once per channel.
          <span className='inline-flex items-center gap-1.5'>
            <span className='inline-flex items-center rounded-full border bg-muted px-2 py-0.5 text-xs font-medium text-foreground'>
              {poolLabel || '已绑定'}
            </span>
            <Link
              to='/storage'
              search={{ tab: 'pools' }}
              aria-label={`前往存储页管理 ${poolLabel || '绑定'} 的绑定`}
              title='前往存储页管理绑定'
              className='text-xs text-muted-foreground hover:text-foreground'
            >
              ↗
            </Link>
          </span>
        ) : (
          <Link
            to='/storage'
            search={{ tab: 'pools' }}
            className='inline-flex items-center gap-1 rounded-full border border-amber-500/30 bg-amber-500/10 px-2 py-0.5 text-xs text-amber-600 hover:bg-amber-500/20 dark:text-amber-400'
          >
            未绑定 · 去分配 ↗
          </Link>
        )}
      </TableCell>
      <TableCell>
        {inert ? (
          <span className='text-xs text-zinc-600'>—</span>
        ) : (
          <div className='flex flex-col gap-1.5'>
            {/* One horizontal control row: mode select, the week pills, the
                summary time, the editor toggle, manual recording — never two
                stacked lines for what is one decision. */}
            <div className='flex flex-nowrap items-center gap-2.5 overflow-x-auto'>
              <SelectField
                ariaLabel={`${label} 录像方式`}
                className='shrink-0 text-sm'
                value={mode}
                disabled={!!pending}
                onValueChange={pickMode}
                options={[
                  { value: 'none', label: MODE_LABELS.none },
                  { value: 'continuous', label: MODE_LABELS.continuous },
                  { value: 'planned', label: MODE_LABELS.planned },
                  ...PLACEHOLDER_MODES.map((value) => ({
                    value,
                    label: MODE_LABELS[value],
                    disabled: true,
                  })),
                ]}
              />
              {/* The mini week sits right beside the select for a planned
                  channel; hidden while its editor is open, where the same
                  seven days appear at full size. */}
              {mode === 'planned' && !editorOpen && (
                <>
                  <WeekPills windows={storedWindows} text={pill.text} label={label} />
                  {schedule.data?.active && (
                    <span className='inline-flex shrink-0 items-center gap-1 rounded-full border border-emerald-500/30 bg-emerald-500/10 px-2 py-0.5 text-xs text-emerald-600 dark:text-emerald-400'>
                      <span aria-hidden className='size-1.5 rounded-full bg-emerald-500' />
                      时段内
                    </span>
                  )}
                </>
              )}
              {/* The apply button lives only where a single select change is
                  not the whole story: planned keeps its commit inside the
                  editor, so the row never carries a second ambiguous button. */}
              {pending === 'policy' && (
                <span className='shrink-0 text-xs text-muted-foreground'>应用中…</span>
              )}
              {succeeded === 'policy' && pending !== 'policy' && (
                <span
                  data-testid='applied-policy'
                  className='inline-flex shrink-0 items-center gap-1 rounded-full border border-emerald-500/20 bg-emerald-500/10 px-2 py-0.5 text-xs text-emerald-400'
                >
                  ✓ 已生效
                </span>
              )}
              {error && (
                <span className='shrink-0 text-xs text-red-500' role='alert'>
                  ✕ {error}
                </span>
              )}
              {!error && notice && succeeded !== 'policy' && (
                <span className='shrink-0 text-xs text-muted-foreground'>{notice}</span>
              )}
              {mode !== 'planned' && !pending && (
                <span className='shrink-0 text-xs text-muted-foreground'>
                  {modeLabel(mode)}
                </span>
              )}
              {/* Manual recording is a separate request, not a mode: it runs
                  alongside whatever the policy says and expires on its own
                  (PRD REC-03/04). */}
              <ManualRecordingControl
                channelId={channel.id}
                label={label}
                disabled={!!pending}
              />
              {RECORDING_MODES.has(mode) && !boundPool && (
                <Link
                  to='/storage'
                  search={{ tab: 'pools' }}
                  className='shrink-0 text-xs text-amber-600 hover:underline dark:text-amber-400'
                >
                  请先在「存储」页绑定存储池 ↗
                </Link>
              )}
            </div>
            {/* The editor panel: full-size schedule editing under the control
                row. Opened by picking 定时录像 (the mode pick IS the open) or
                by the row's 编辑时段 toggle. */}
            {mode === 'planned' && (
              <ScheduleEditor
                label={label}
                disabled={!!pending}
                peers={schedulePeers || []}
                windows={storedWindows}
                onWindowsChange={setScheduleOverride}
                timezone={schedule.data?.timezone || ''}
                schedulePending={schedule.isPending}
                scheduleError={!!schedule.error}
                open={editorOpen}
                onOpenChange={(next) => {
                  // Opening reports this row to the page's accordion; closing
                  // reports the empty id so another row may take the panel.
                  onEditorOpenChange?.(next ? (channel.id ?? '') : '')
                  if (!next) setScheduleOverride(null)
                }}
                onCancel={() => setDraftMode(null)}
                onCommitPlanned={() => commitPlanned(editorWindowsRef.current)}
              />
            )}
          </div>
        )}
      </TableCell>
    </TableRow>
  )
}
