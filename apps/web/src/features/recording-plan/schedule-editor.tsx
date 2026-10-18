import { useMemo, useState } from 'react'
import { CalendarClock, Check, Copy, Plus, Trash2 } from 'lucide-react'
import { ApiError, apiRequest, jsonRequest } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { useAPI } from '@/features/foundation/hooks'
import {
  WEEKDAYS,
  byWeekday,
  overlaps,
  parseClock,
  scheduleSummary,
  windowLabel,
  type ScheduleWindow,
} from './schedule-format'

/**
 * Weekly recording schedule editor (PRD REC-02).
 *
 * It edits the whole week as one value and saves it with one PUT, because the
 * server replaces the week atomically: a per-window save would leave the schedule
 * in states the operator never chose, and a rejected week would half-apply.
 *
 * The vertical day list rather than a 7-column grid: on a 16-channel page each
 * row has one schedule, and a grid inside a table cell is unreadable at any
 * column width. Each day shows its windows and a "+" that adds a window to that
 * day, which is also how "copy to other days" is expressed — the copy targets are
 * the same seven rows.
 */
export function ScheduleEditor({
  channelId,
  label,
  disabled,
  peers = [],
}: {
  channelId: string
  label: string
  disabled?: boolean
  // Other channels this operator may configure, for "copy to other channels"
  // (REC-02). Excluding the channel being edited is the caller's job: copying a
  // week onto itself is a no-op that only produces a confusing confirmation.
  peers?: { id: string; label: string }[]
}) {
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState<ScheduleWindow[] | null>(null)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [copyTargets, setCopyTargets] = useState<Set<string>>(new Set())
  const [copyPending, setCopyPending] = useState(false)
  const schedule = useAPI<Schema<'ChannelSchedule'>>(
    `/api/v1/channels/${channelId}/recording-schedule`,
    true
  )
  const stored = schedule.data?.windows
  const windows = useMemo(() => draft ?? stored ?? [], [draft, stored])
  const grouped = useMemo(() => byWeekday(windows), [windows])
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
    setError('')
    setNotice('')
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

  // Copy one day's windows onto every other day. This is the "copy to other
  // days" of REC-02 expressed as an operation on the seven rows that are already
  // visible, so there is no separate copy dialog to learn.
  function copyToAll(weekday: number) {
    const source = grouped.get(weekday) || []
    if (source.length === 0) {
      setError('该日期没有时段可复制')
      return
    }
    const keep = windows.filter((window) => window.weekday === weekday)
    const copies: ScheduleWindow[] = []
    for (const day of WEEKDAYS) {
      if (day.value === weekday) continue
      for (const window of source) {
        copies.push({
          ...window,
          weekday: day.value,
          // Keep the wrap: a 22:00-02:00 window copied to another day must still
          // be 22:00-02:00, not 22:00-26:00 collapsed onto one day.
          end_minute: window.end_minute,
        })
      }
    }
    edit([...keep, ...copies])
  }

  async function save() {
    if (conflict) {
      setError('同一星期几的时段不能重叠')
      return
    }
    setPending(true)
    setError('')
    setNotice('')
    try {
      const saved = await apiRequest<Schema<'ChannelSchedule'>>(
        `/api/v1/channels/${channelId}/recording-schedule`,
        jsonRequest('PUT', { windows })
      )
      setDraft(null)
      setNotice(`已保存 · ${scheduleSummary(saved.windows)}`)
      await schedule.refetch()
      setOpen(false)
    } catch (cause) {
      if (cause instanceof ApiError && cause.code === 'schedule_overlap') {
        setError('同一星期几的时段不能重叠')
      } else {
        setError(cause instanceof Error ? cause.message : '保存失败')
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

  if (!open) {
    return (
      <div className='flex flex-wrap items-center gap-2'>
        <Button
          type='button'
          variant='outline'
          size='sm'
          className='h-7 px-2 text-xs'
          disabled={disabled}
          aria-label={`${label} 编辑时段`}
          aria-expanded={false}
          onClick={() => {
            // Every open starts from the server's answer: an editor that kept
            // unsaved edits across opens would silently re-apply old windows to
            // a schedule someone else changed in the meantime.
            setDraft(null)
            setOpen(true)
          }}
        >
          <CalendarClock aria-hidden className='size-3' />
          时段
        </Button>
        <span className='text-xs text-muted-foreground'>
          {schedule.isPending
            ? '…'
            : schedule.error
              ? '时段未知'
              : scheduleSummary(stored || [])}
        </span>
        {schedule.data?.active && (
          <span className='inline-flex items-center gap-1 rounded-full border border-emerald-500/30 bg-emerald-500/10 px-2 py-0.5 text-xs text-emerald-600 dark:text-emerald-400'>
            <span aria-hidden className='size-1.5 rounded-full bg-emerald-500' />
            时段内
          </span>
        )}
        {notice && <span className='text-xs text-emerald-600 dark:text-emerald-400'>{notice}</span>}
      </div>
    )
  }

  return (
    <div className='w-full rounded-lg border bg-zinc-50/70 p-3 dark:bg-zinc-900/40'>
      <div className='mb-2 flex flex-wrap items-center justify-between gap-2'>
        <p className='text-xs text-muted-foreground'>
          仅「定时录像」使用此时段；时区 {schedule.data?.timezone || '—'}
        </p>
        <div className='flex items-center gap-2'>
          <Button
            type='button'
            variant='ghost'
            size='sm'
            className='h-7 px-2 text-xs'
            onClick={() => {
              setDraft(null)
              setError('')
              setOpen(false)
            }}
          >
            取消
          </Button>
          <Button
            type='button'
            size='sm'
            className='h-7 px-2 text-xs'
            disabled={pending || conflict}
            onClick={() => void save()}
          >
            <Check aria-hidden className='size-3' />
            保存时段
          </Button>
        </div>
      </div>
      <div className='space-y-1'>
        {WEEKDAYS.map((day) => {
          const dayWindows = grouped.get(day.value) || []
          return (
            <div
              key={day.value}
              className='flex flex-wrap items-center gap-2 rounded-md px-1 py-0.5'
              data-testid={`schedule-day-${day.value}`}
            >
              <span className='w-10 shrink-0 text-xs text-muted-foreground'>
                {day.label}
              </span>
              {dayWindows.length === 0 ? (
                <span className='text-xs text-zinc-500'>不录</span>
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
                  aria-label={`${day.label} 复制到其它日期`}
                  onClick={() => copyToAll(day.value)}
                >
                  <Copy aria-hidden className='size-3' />
                  复制到其它日期
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
          <div className='flex flex-wrap items-center gap-2'>
            {peers.map((peer) => (
              <label
                key={peer.id}
                className='inline-flex items-center gap-1.5 text-xs'
              >
                <Checkbox
                  aria-label={`复制到 ${peer.label}`}
                  checked={copyTargets.has(peer.id)}
                  onCheckedChange={(state) =>
                    setCopyTargets((current) => {
                      const next = new Set(current)
                      if (state === true) next.add(peer.id)
                      else next.delete(peer.id)
                      return next
                    })
                  }
                />
                {peer.label}
              </label>
            ))}
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
