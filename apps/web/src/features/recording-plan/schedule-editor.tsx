import { useMemo, useState } from 'react'
import { Check, Copy, Plus, Trash2 } from 'lucide-react'
import { apiRequest, jsonRequest } from '@/lib/api-client'
import { Button } from '@/components/ui/button'
import {
  WEEKDAYS,
  overlaps,
  parseClock,
  windowLabel,
  type ScheduleWindow,
} from './schedule-format'
import { cn } from '@/lib/utils'

/**
 * Weekly recording schedule editor (PRD REC-02).
 *
 * It edits the whole week as one value and saves it with one PUT, because the
 * server replaces the week atomically: a per-window save would leave the schedule
 * in states the operator never chose, and a rejected week would half-apply.
 *
 * Two ways in, one way out. The quick bar answers "give these days 08:00–18:00"
 * in one gesture (the shape almost every site actually wants); the per-day rows
 * answer "I need to fine-tune one morning". Both edit the same draft; the single
 * "保存并启用定时录像" button is the only path to the server, so windows and the
 * mode switch land together and never leave the channel half-configured.
 */
export function ScheduleEditor({
  label,
  disabled,
  peers = [],
  windows: windowsProp,
  onWindowsChange,
  timezone,
  schedulePending,
  scheduleError,
  // The row owns the open state so the top control row can flip between
  // 编辑时段 and 收起, and so the page can run an accordion (opening one
  // channel's editor collapses the others' unsaved panels).
  open,
  onOpenChange,
  onCancel,
  onCommitPlanned,
}: {
  label: string
  disabled?: boolean
  // Other channels this operator may configure, for "copy to other channels"
  // (REC-02). Excluding the channel being edited is the caller's job: copying a
  // week onto itself is a no-op that only produces a confusing confirmation.
  peers?: { id: string; label: string }[]
  // The row owns the schedule read so its mini pills and this editor render
  // one answer. The editor edits a draft on top and reports back through
  // onWindowsChange; it never writes the schedule alone.
  windows: ScheduleWindow[]
  onWindowsChange: (windows: ScheduleWindow[] | null) => void
  timezone?: string
  schedulePending?: boolean
  scheduleError?: boolean
  open: boolean
  onOpenChange: (open: boolean) => void
  // Cancel when the mode is a draft reverts the whole pick (mode included);
  // cancel on a stored planned row only closes the panel.
  onCancel?: () => void
  // The row's single commit: windows to the schedule endpoint, then the mode
  // switch to 'planned'. Returning a truthy value means the mode change also
  // settled successfully.
  onCommitPlanned: () => Promise<boolean | null>
}) {
  const setOpen = onOpenChange
  const [draft, setDraft] = useState<ScheduleWindow[] | null>(null)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [copyTargets, setCopyTargets] = useState<Set<string>>(new Set())
  const [copyPending, setCopyPending] = useState(false)
  // Quick-bar state: a day selection plus one time range, applied in one go.
  const [pickedDays, setPickedDays] = useState<Set<number>>(new Set())
  const [quickStart, setQuickStart] = useState('08:00')
  const [quickEnd, setQuickEnd] = useState('18:00')
  const windows = draft ?? windowsProp
  const grouped = useMemo(() => {
    const map = new Map<number, ScheduleWindow[]>()
    for (const day of WEEKDAYS) map.set(day.value, [])
    for (const window of windows) {
      map.get(window.weekday)?.push(window)
    }
    for (const bucket of map.values()) {
      bucket.sort((a, b) => a.start_minute - b.start_minute)
    }
    return map
  }, [windows])
  const conflict = useMemo(() => {
    for (let i = 0; i < windows.length; i++) {
      for (let j = i + 1; j < windows.length; j++) {
        if (overlaps(windows[i], windows[j])) return true
      }
    }
    return false
  }, [windows])

  function edit(next: ScheduleWindow[]) {
    setDraft(next)
    onWindowsChange(next)
    setError('')
    setNotice('')
  }

  function togglePicked(weekday: number, checked: boolean) {
    setPickedDays((current) => {
      const next = new Set(current)
      if (checked) next.add(weekday)
      else next.delete(weekday)
      return next
    })
  }

  function pickShortcut(days: number[]) {
    setPickedDays(new Set(days))
  }

  /**
   * Apply the quick range to every picked day at once. Existing windows on a
   * picked day are replaced rather than merged: re-applying a range to days it
   * already covers must stay a no-op in effect, and a merged week would grow a
   * second overlapping window the server then rejects.
   */
  function applyQuickRange() {
    const start = parseClock(quickStart)
    const end = parseClock(quickEnd)
    if (start === null || end === null) {
      setError('时间格式应为 HH:MM')
      return
    }
    if (pickedDays.size === 0) {
      setError('请先选择要应用的星期')
      return
    }
    if (start === end) {
      setError('开始与结束相同，这个时段不包含任何时间')
      return
    }
    const kept = windows.filter((window) => !pickedDays.has(window.weekday))
    const built: ScheduleWindow[] = []
    for (const weekday of pickedDays) {
      // 24:00 arrives from the time input as 00:00; stored as 1440 it stays an
      // end-of-day exclusive bound, matching the server's split semantics.
      const endMinute = end <= start ? end + 1440 : end
      built.push({ weekday, start_minute: start, end_minute: endMinute })
    }
    edit([...kept, ...built])
    setError('')
    setNotice(`已应用到 ${pickedDays.size} 天，记得保存`)
  }

  function addWindow(weekday: number) {
    edit([
      ...windows,
      // A new window lands at a plausible time rather than 00:00-00:00, which
      // would be rejected on save and read as a broken control.
      { weekday, start_minute: 8 * 60, end_minute: 18 * 60 },
    ])
  }

  function removeWindow(target: ScheduleWindow) {
    edit(
      windows.filter(
        (window) =>
          !(
            window.weekday === target.weekday &&
            window.start_minute === target.start_minute &&
            window.end_minute === target.end_minute
          )
      )
    )
  }

  /**
   * The editor's only save path. It hands the draft to the row's commit, which
   * owns both writes in order — the schedule PUT, then the mode switch — with
   * recovery, settlement and error display shared with every other row action.
   * The editor itself never talks to the server, so there is exactly one place
   * a planned switch can land and one place its failures surface.
   */
  async function saveAndEnable() {
    if (conflict) {
      setError('同一星期几的时段不能重叠')
      return
    }
    setPending(true)
    setError('')
    setNotice('')
    try {
      const settled = await onCommitPlanned()
      if (settled) {
        setNotice(`已保存并启用 · ${windowLabel(windows[0])}${windows.length > 1 ? ` 等 ${windows.length} 段` : ''}`)
        setOpen(false)
      }
    } finally {
      setPending(false)
    }
  }

  // Copy the current week onto other channels (REC-02). Sequential, like the
  // batch binds: each write has its own audit row and parallel writes into the
  // same worker queue is how a batch of eight becomes a pile of conflicts. Every
  // channel reports its own result; a partial copy must not read as success.
  async function copyToChannels() {
    if (copyTargets.size === 0) return
    setCopyPending(true)
    setError('')
    setNotice('')
    const ok: string[] = []
    const failed: string[] = []
    try {
      for (const target of copyTargets) {
        const peer = peers.find((entry) => entry.id === target)
        try {
          await apiRequest(
            `/api/v1/channels/${target}/recording-schedule`,
            jsonRequest('PUT', { windows })
          )
          ok.push(peer?.label || target)
        } catch (cause) {
          failed.push(
            `${peer?.label || target}: ${
              cause instanceof Error ? cause.message : '复制失败'
            }`
          )
        }
      }
      const parts: string[] = []
      if (ok.length) parts.push(`已复制到 ${ok.join('、')}`)
      if (failed.length) parts.push(`失败：${failed.join('；')}`)
      setNotice(parts.join(' · '))
      if (failed.length === 0) setCopyTargets(new Set())
    } finally {
      setCopyPending(false)
    }
  }

  function close() {
    // Closing without saving drops the draft back to the stored week: an
    // editor that kept unsaved edits across opens would silently re-apply old
    // windows to a schedule someone else changed in the meantime.
    setDraft(null)
    onWindowsChange(null)
    setError('')
    setOpen(false)
    // An editor opened by the mode pick itself reverts the pick on cancel;
    // one opened from a stored planned row only closes.
    if (onCancel) onCancel()
  }

  if (!open) {
    return (
      <Button
        type='button'
        variant='outline'
        size='sm'
        className='h-7 shrink-0 px-2 text-xs'
        disabled={disabled || schedulePending || scheduleError}
        aria-label={`${label} 编辑时段`}
        aria-expanded={false}
        onClick={() => {
          setDraft(null)
          onWindowsChange(null)
          setOpen(true)
        }}
      >
        编辑时段
      </Button>
    )
  }

  return (
    <div className='w-full rounded-lg border bg-zinc-50/70 p-3 dark:bg-zinc-900/40'>
      <div className='mb-2 flex flex-wrap items-center justify-between gap-2'>
        <p className='text-xs text-muted-foreground'>
          整周一次保存 · 时区 {timezone || '—'}
        </p>
        <div className='flex items-center gap-2'>
          <Button
            type='button'
            variant='ghost'
            size='sm'
            className='h-7 px-2 text-xs'
            disabled={pending}
            onClick={close}
          >
            收起
          </Button>
          <Button
            type='button'
            size='sm'
            className='h-7 px-2.5 text-xs'
            disabled={pending || conflict}
            onClick={() => void saveAndEnable()}
          >
            <Check aria-hidden className='size-3' />
            保存并启用定时录像
          </Button>
        </div>
      </div>

      {/* Quick bar: several days plus one range in a single gesture. It edits
          the same draft as the rows below — nothing here saves on its own.
          Kept on one line: compact pills, tight gaps, no wrapping. */}
      <div className='mb-3 overflow-x-auto rounded-md border bg-background p-2'>
        <div className='flex items-center gap-1 whitespace-nowrap'>
          {WEEKDAYS.map((day) => {
            const picked = pickedDays.has(day.value)
            return (
              <button
                key={day.value}
                type='button'
                aria-pressed={picked}
                aria-label={`选择 ${day.label}`}
                onClick={() => togglePicked(day.value, !picked)}
                className={cn(
                  'h-6 shrink-0 rounded-md border px-1.5 text-xs transition-colors',
                  picked
                    ? 'border-emerald-500/40 bg-emerald-500/15 text-emerald-700 dark:text-emerald-400'
                    : 'text-muted-foreground hover:bg-accent'
                )}
              >
                {day.label}
              </button>
            )
          })}
          <span className='mx-0.5 h-4 w-px shrink-0 bg-border' aria-hidden />
          <Button
            type='button'
            variant='outline'
            size='sm'
            className='h-6 shrink-0 px-1.5 text-xs'
            onClick={() => pickShortcut([1, 2, 3, 4, 5])}
          >
            工作日
          </Button>
          <Button
            type='button'
            variant='outline'
            size='sm'
            className='h-6 shrink-0 px-1.5 text-xs'
            onClick={() => pickShortcut([6, 7])}
          >
            周末
          </Button>
          <Button
            type='button'
            variant='outline'
            size='sm'
            className='h-6 shrink-0 px-1.5 text-xs'
            onClick={() => pickShortcut([1, 2, 3, 4, 5, 6, 7])}
          >
            全周
          </Button>
          <span className='mx-0.5 h-4 w-px shrink-0 bg-border' aria-hidden />
          <TimeInput value={quickStart} onChange={setQuickStart} label='快捷开始' />
          <span className='shrink-0 text-xs text-muted-foreground'>–</span>
          <TimeInput value={quickEnd} onChange={setQuickEnd} label='快捷结束' />
          <Button
            type='button'
            size='sm'
            variant='outline'
            className='h-6 shrink-0 px-1.5 text-xs'
            onClick={applyQuickRange}
          >
            <Plus aria-hidden className='size-3' />
            应用到所选星期
          </Button>
        </div>
      </div>

      {/* Seven compact rows: badge + 24h timeline strip + editable tags. The
          strip is the at-a-glance shape of the day; the tags are the editor. */}
      <div className='space-y-1'>
        {WEEKDAYS.map((day) => {
          const dayWindows = grouped.get(day.value) || []
          return (
            <div
              key={day.value}
              className='flex flex-wrap items-center gap-2 rounded-md px-1 py-0.5'
              data-testid={`schedule-day-${day.value}`}
            >
              <span
                className={cn(
                  'inline-flex w-14 shrink-0 items-center gap-1 text-xs',
                  dayWindows.length > 0
                    ? 'font-medium text-foreground'
                    : 'text-zinc-500 dark:text-zinc-600'
                )}
              >
                <span
                  aria-hidden
                  className={cn(
                    'size-1.5 rounded-full',
                    dayWindows.length > 0 ? 'bg-emerald-500' : 'bg-zinc-700 dark:bg-zinc-700'
                  )}
                />
                {day.label}
              </span>
              <DayStrip windows={dayWindows} />
              {dayWindows.length === 0 ? (
                <span className='text-xs text-zinc-500 dark:text-zinc-600'>不录</span>
              ) : (
                dayWindows.map((window) => (
                  <DayWindow
                    key={`${window.start_minute}-${window.end_minute}`}
                    window={window}
                    onChange={(next) =>
                      edit(
                        windows.map((candidate) =>
                          candidate === window ? next : candidate
                        )
                      )
                    }
                    onRemove={() => removeWindow(window)}
                  />
                ))
              )}
              <Button
                type='button'
                variant='ghost'
                size='sm'
                className='h-6 px-1.5 text-xs'
                aria-label={`${day.label} 添加时段`}
                onClick={() => addWindow(day.value)}
              >
                <Plus aria-hidden className='size-3' />
              </Button>
              {dayWindows.length > 0 && (
                <Button
                  type='button'
                  variant='ghost'
                  size='sm'
                  className='h-6 px-1.5 text-xs text-muted-foreground'
                  aria-label={`${day.label} 清空`}
                  onClick={() =>
                    edit(windows.filter((window) => window.weekday !== day.value))
                  }
                >
                  清空
                </Button>
              )}
            </div>
          )
        })}
      </div>
      {peers.length > 0 && (
        <div className='mt-2 space-y-1.5 border-t pt-2'>
          <p className='text-xs text-muted-foreground'>
            复制此时段到其它通道（会覆盖目标通道的整周计划）
          </p>
          <div className='flex flex-wrap items-center gap-1.5'>
            {peers.map((peer) => {
              const picked = copyTargets.has(peer.id)
              return (
                <button
                  key={peer.id}
                  type='button'
                  aria-pressed={picked}
                  aria-label={`复制到 ${peer.label}`}
                  onClick={() =>
                    setCopyTargets((current) => {
                      const next = new Set(current)
                      if (picked) next.delete(peer.id)
                      else next.add(peer.id)
                      return next
                    })
                  }
                  className={cn(
                    'h-6 rounded-md border px-2 text-xs transition-colors',
                    picked
                      ? 'border-emerald-500/40 bg-emerald-500/15 text-emerald-700 dark:text-emerald-400'
                      : 'text-muted-foreground hover:bg-accent'
                  )}
                >
                  {peer.label}
                </button>
              )
            })}
            <Button
              type='button'
              variant='outline'
              size='sm'
              className='h-6 px-1.5 text-xs'
              disabled={copyPending || copyTargets.size === 0}
              onClick={() => void copyToChannels()}
            >
              <Copy aria-hidden className='size-3' />
              复制到所选通道
            </Button>
          </div>
          {notice && (
            <p role='status' className='text-xs text-muted-foreground'>
              {notice}
            </p>
          )}
        </div>
      )}
      {conflict && (
        <p role='alert' className='mt-2 text-xs text-red-500'>
          同一星期几的时段不能重叠
        </p>
      )}
      {error && (
        <p role='alert' className='mt-2 text-xs text-red-500'>
          {error}
        </p>
      )}
    </div>
  )
}

/** One editable HH:MM field bound to the quick bar. */
function TimeInput({
  value,
  onChange,
  label,
}: {
  value: string
  onChange: (value: string) => void
  label: string
}) {
  return (
    <input
      type='time'
      aria-label={label}
      className='w-20 rounded-md border bg-transparent px-1.5 py-0.5 text-xs tabular-nums outline-none'
      value={value}
      onChange={(event) => onChange(event.target.value)}
    />
  )
}

/**
 * The 24-hour shape of one day: a thin track with an emerald band per window.
 * Clicking a band does nothing — editing happens in the tags beside it — so the
 * strip stays a display, never a second competing input.
 */
function DayStrip({ windows }: { windows: ScheduleWindow[] }) {
  return (
    <span
      aria-hidden
      className='relative h-2.5 w-40 shrink-0 overflow-hidden rounded-full bg-zinc-200 dark:bg-zinc-800'
    >
      {windows.map((window) => {
        // A window past 24:00 draws only its first-day tail here; the wrap is
        // visible on the next day's strip, which is where the server splits it.
        const start = Math.min(window.start_minute, 1440)
        const end = Math.min(window.end_minute, 1440)
        if (end <= start) return null
        return (
          <span
            key={`${window.start_minute}-${window.end_minute}`}
            className='absolute inset-y-0 bg-emerald-500/80'
            style={{
              left: `${(start / 1440) * 100}%`,
              width: `${((end - start) / 1440) * 100}%`,
            }}
          />
        )
      })}
    </span>
  )
}

function DayWindow({
  window,
  onChange,
  onRemove,
}: {
  window: ScheduleWindow
  onChange: (next: ScheduleWindow) => void
  onRemove: () => void
}) {
  // The time input always speaks 24-hour clock within one day, so a wrapping end
  // (1560 minutes) is displayed as 02:00. The wrap itself is preserved in the
  // value; only the display is normalised.
  const startValue = clockForInput(window.start_minute)
  const endValue = clockForInput(window.end_minute)
  return (
    <span className='inline-flex items-center gap-1 rounded-md border bg-background px-1.5 py-0.5 text-xs'>
      <input
        type='time'
        aria-label={`${windowLabel(window)} 开始`}
        className='w-16 bg-transparent tabular-nums outline-none'
        value={startValue}
        onChange={(event) => {
          const start = parseClock(event.target.value)
          if (start === null) return
          const built = rebuild(window, start, window.end_minute)
          if (built) onChange(built)
        }}
      />
      <span className='text-muted-foreground'>–</span>
      <input
        type='time'
        aria-label={`${windowLabel(window)} 结束`}
        className='w-16 bg-transparent tabular-nums outline-none'
        value={endValue}
        onChange={(event) => {
          const end = parseClock(event.target.value)
          if (end === null) return
          const built = rebuild(window, window.start_minute, end)
          if (built) onChange(built)
        }}
      />
      {window.end_minute > 1440 && (
        <span className='text-[10px] text-muted-foreground'>次日</span>
      )}
      <button
        type='button'
        aria-label={`删除 ${windowLabel(window)}`}
        className='text-muted-foreground hover:text-red-500'
        onClick={onRemove}
      >
        <Trash2 aria-hidden className='size-3' />
      </button>
    </span>
  )
}

/** Minutes to the "HH:MM" a time input expects, wrapping past midnight. */
function clockForInput(minutes: number): string {
  const normalised = minutes % 1440
  return `${String(Math.floor(normalised / 60)).padStart(2, '0')}:${String(normalised % 60).padStart(2, '0')}`
}

/**
 * Apply a new start or end while keeping the existing wrap.
 *
 * A stored end above 1440 means "the next day"; the input can only show 02:00,
 * so rebuilding from the displayed value would silently turn an overnight window
 * into an invalid same-day one. The wrap is carried over instead: if the window
 * already wrapped and the new end is still before the start, it keeps wrapping.
 */
function rebuild(
  current: ScheduleWindow,
  start: number,
  end: number
): ScheduleWindow | null {
  const wrapped = current.end_minute > 1440
  const endMinute = end <= start || (wrapped && end < start) ? end + 1440 : end
  if (end === start) return null
  if (endMinute > 2880) return null
  return { weekday: current.weekday, start_minute: start, end_minute: endMinute }
}
