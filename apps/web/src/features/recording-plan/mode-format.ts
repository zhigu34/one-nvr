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
 * carry windows, and the compact time text shown beside the pills.
 *
 * The pills carry the weekday names themselves, so `text` is only the time
 * range: one uniform range when every planned day shares it, "多时段" when they
 * disagree, and empty for a week with no windows (the row says 未设置时段
 * elsewhere).
 */
export function weekPill(
  windows: ScheduleWindow[]
): { days: Set<number>; text: string } {
  const grouped = byWeekday(windows)
  const days = new Set<number>()
  for (const day of WEEKDAYS) {
    if ((grouped.get(day.value) || []).length > 0) days.add(day.value)
  }
  const ranges = [...new Set(windows.map(windowLabel))]
  let text = ''
  if (windows.length > 0) {
    text = ranges.length === 1 ? ranges[0] : '多时段'
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
