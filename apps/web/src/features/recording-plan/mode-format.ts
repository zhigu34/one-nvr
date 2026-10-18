import type { Schema } from '@/lib/types'
import { WEEKDAYS, byWeekday, windowLabel, type ScheduleWindow } from './schedule-format'

/**
 * Recording-mode labels, defined once: the select on the recording plan, the
 * batch bar and the channel configure page must never drift apart, and the
 * product vocabulary ("全天录像" for continuous) is a single decision.
 */
export const MODE_LABELS: Record<string, string> = {
  none: '关闭录像',
  continuous: '全天录像',
  planned: '定时录像',
  event: '事件录像（后续开放）',
}

export function modeLabel(mode: string | undefined | null): string {
  return MODE_LABELS[mode || 'none'] || (mode ?? '') || '关闭录像'
}

/**
 * Weekday-pill summary for one channel's planned week: which of the seven days
 * carry windows, and a compact time range when every planned day shares one.
 *
 * The pills always render (a day with no plan is a deliberate dimmed square, not
 * a blank); `text` is empty unless every window in the week is the same range —
 * in that case the one range is the whole story and repeating it per day is
 * noise. Mixed ranges leave the text out; the pills and hover titles carry it.
 */
export function weekPill(
  windows: ScheduleWindow[]
): { days: Set<number>; text: string } {
  const grouped = byWeekday(windows)
  const days = new Set<number>()
  for (const day of WEEKDAYS) {
    if ((grouped.get(day.value) || []).length > 0) days.add(day.value)
  }
  // One compact label only when every planned day shares the same ranges. The
  // comparison is on the sorted label list per day, so a week where Saturday
  // also records 08:00–18:00 collapses to "周一至周日 08:00–18:00" style text.
  const ranges = [...new Set(windows.map(windowLabel))]
  let text = ''
  if (windows.length > 0 && ranges.length === 1) {
    const planned = WEEKDAYS.filter((day) => days.has(day.value))
    const sameEveryDay = planned.every(
      (day) =>
        (grouped.get(day.value) || []).length === 1 &&
        windowLabel((grouped.get(day.value) || [])[0]) === ranges[0]
    )
    if (sameEveryDay) {
      text =
        days.size === 7
          ? `每天 ${ranges[0]}`
          : `${planned.map((day) => day.label).join('、')} ${ranges[0]}`
    }
  }
  return { days, text }
}

/** Hover title for one weekday pill: "周日 08:00 – 09:00" or "周日 不录". */
export function dayWindowsLabel(
  windows: ScheduleWindow[],
  weekday: number
): string {
  const day = WEEKDAYS.find((entry) => entry.value === weekday)
  const list = windows
    .filter((window) => window.weekday === weekday)
    .sort((a, b) => a.start_minute - b.start_minute)
  if (list.length === 0) return `${day?.label || weekday} 不录`
  return `${day?.label || weekday} ${list.map(windowLabel).join('、')}`
}

/** Whether a channel row counts as configured (has an applied camera source). */
export function channelConfigured(
  summary: Schema<'ChannelSummary'> | undefined
): boolean {
  return !!(summary?.current_revision_id || summary?.source_ip) &&
    summary?.main.state !== 'not_configured'
}
