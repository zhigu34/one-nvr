/**
 * Weekly schedule helpers (PRD REC-02).
 *
 * Everything here is pure and operates on minutes since local midnight, matching
 * what the server stores. The server evaluates the schedule; this file only
 * formats and edits it, so it must not reimplement "is this active now" — that
 * answer comes from the API and is displayed, never recomputed from the
 * browser's clock.
 */

export const WEEKDAYS = [
  { value: 1, label: '周一', short: '一' },
  { value: 2, label: '周二', short: '二' },
  { value: 3, label: '周三', short: '三' },
  { value: 4, label: '周四', short: '四' },
  { value: 5, label: '周五', short: '五' },
  { value: 6, label: '周六', short: '六' },
  { value: 7, label: '周日', short: '日' },
] as const

export type ScheduleWindow = {
  weekday: number
  start_minute: number
  end_minute: number
}

/** "08:00" for 480. Minutes beyond a day wrap, which is how an overnight end is shown. */
export function clock(minutes: number): string {
  const normalised = ((minutes % 1440) + 1440) % 1440
  const hours = Math.floor(normalised / 60)
  const rest = normalised % 60
  return `${String(hours).padStart(2, '0')}:${String(rest).padStart(2, '0')}`
}

/**
 * "22:00 – 02:00" for a window that wraps, "08:00 – 12:00" otherwise.
 *
 * The second value is what the operator typed, not the stored split: a window
 * that ends past midnight is one window to a person even though it is two rows
 * in the database.
 */
export function windowLabel(window: ScheduleWindow): string {
  return `${clock(window.start_minute)} – ${clock(window.end_minute)}`
}

/** Parse "HH:MM" from an <input type="time">, or null when it is incomplete. */
export function parseClock(value: string): number | null {
  const match = /^(\d{1,2}):(\d{2})$/.exec(value.trim())
  if (!match) return null
  const hours = Number(match[1])
  const minutes = Number(match[2])
  if (hours > 23 || minutes > 59) return null
  return hours * 60 + minutes
}

/**
 * Turn a start/end pair into a window, or into a rejection reason.
 *
 * An end at or before the start means the window crosses midnight, which is
 * legal and common for overnight recording; it is not an error. An end exactly
 * equal to the start is rejected because it describes no time at all — the
 * operator most likely meant a full day and should say 00:00–24:00.
 */
export function buildWindow(
  weekday: number,
  start: number,
  end: number
): { window: ScheduleWindow } | { error: string } {
  if (end === start) {
    return { error: '开始与结束相同，这个时段不包含任何时间' }
  }
  const startMinute = Math.min(start, 1439)
  // end is exclusive: 24:00 from the time input arrives as 00:00, which must be
  // read as midnight at the end of the day rather than the start of the same one.
  const endMinute = end <= start ? end + 1440 : end
  if (endMinute > 2880) {
    return { error: '时段长度不能超过 24 小时' }
  }
  return { window: { weekday, start_minute: startMinute, end_minute: endMinute } }
}

/**
 * Do two windows on the same weekday overlap?
 *
 * Comparison happens in a single day's coordinate space, so a wrapping window's
 * tail is compared against the *next* weekday — the same split the server stores.
 * Getting this wrong would let the editor accept a week the API then rejects.
 */
export function overlaps(a: ScheduleWindow, b: ScheduleWindow): boolean {
  const segments = (window: ScheduleWindow) => {
    if (window.end_minute <= 1440) {
      return [{ weekday: window.weekday, start: window.start_minute, end: window.end_minute }]
    }
    return [
      { weekday: window.weekday, start: window.start_minute, end: 1440 },
      { weekday: (window.weekday % 7) + 1, start: 0, end: window.end_minute - 1440 },
    ]
  }
  for (const left of segments(a)) {
    for (const right of segments(b)) {
      if (left.weekday !== right.weekday) continue
      if (left.start < right.end && right.start < left.end) return true
    }
  }
  return false
}

/** Group windows by weekday for the editor's per-day columns. */
export function byWeekday(windows: ScheduleWindow[]): Map<number, ScheduleWindow[]> {
  const out = new Map<number, ScheduleWindow[]>()
  for (const day of WEEKDAYS) out.set(day.value, [])
  for (const window of windows) {
    const bucket = out.get(window.weekday)
    if (bucket) bucket.push(window)
  }
  for (const bucket of out.values()) {
    bucket.sort((a, b) => a.start_minute - b.start_minute)
  }
  return out
}

/** A compact summary for the row: "工作日 08:00–18:00" or "未设置时段". */
export function scheduleSummary(windows: ScheduleWindow[]): string {
  if (windows.length === 0) return '未设置时段'
  const days = new Set(windows.map((window) => window.weekday))
  const label =
    days.size === 7
      ? '每天'
      : days.size === 5 && [1, 2, 3, 4, 5].every((day) => days.has(day))
        ? '工作日'
        : days.size === 2 && days.has(6) && days.has(7)
          ? '周末'
          : `${days.size} 天`
  const ranges = [...new Set(windows.map(windowLabel))]
  return `${label} ${ranges.slice(0, 2).join('、')}${ranges.length > 2 ? ` 等 ${ranges.length} 段` : ''}`
}
