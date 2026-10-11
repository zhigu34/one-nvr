import type { Schema } from '@/lib/types'
import { startOfZoneDay, zoneParts } from './zone'

type Segment = Schema<'RecordingSegment'>

/**
 * One indexed segment placed on the site's absolute clock. The server owns the
 * truth about a segment's state; the timeline only renders it and decides
 * whether the segment may be offered for playback at all.
 */
export type Entry = {
  id: string
  start: number
  end: number
  bytes: number
  state: Segment['state']
  checkState: string
  /** Empty when the segment can be served, otherwise the reason it cannot. */
  blocked: string
}

/** Time with no indexed segment at all. A damaged segment is not a gap. */
export type Gap = { start: number; end: number; reason: string }

export type Timeline = {
  entries: Entry[]
  gaps: Gap[]
  from: number
  to: number
}

const STATE_REASON: Record<Segment['state'], string> = {
  ready: '',
  finalizing: '录像仍在写入，尚未完成发布',
  provisional: '绝对时间待确认，暂不可播放',
  missing: '文件缺失，可能已被移走',
  damaged: '文件损坏，与发布记录不一致',
  conflict: '文件冲突，等待人工确认',
  // A deliberate deletion is not a fault: say it is gone rather than imply it
  // might come back.
  deleted: '录像的本地副本已删除',
}

const CHECK_REASON: Record<string, string> = {
  pool_unavailable: '存储池不可用',
  media_unavailable: '文件当前无法读取',
}

/** Why a segment cannot be handed to the player, or an empty string. */
export function blockedReason(segment: Segment): string {
  if (segment.state !== 'ready') return STATE_REASON[segment.state]
  return CHECK_REASON[segment.check_state] || ''
}

/**
 * Places the returned segments, which may overlap when a source was switched
 * mid-run, onto one absolute timeline. Overlap is resolved only for playback
 * lookup; the index itself is never rewritten here. Gaps are the complement of
 * the merged coverage, so an overlap never fabricates a hole and a slow or
 * damaged segment is never mistaken for missing time.
 */
export function buildTimeline(
  segments: Segment[],
  from: number,
  to: number
): Timeline {
  const entries = segments
    .map((segment) => ({
      id: segment.id,
      start: Date.parse(segment.start),
      end: Date.parse(segment.end),
      bytes: segment.bytes,
      state: segment.state,
      checkState: segment.check_state,
      blocked: blockedReason(segment),
    }))
    .filter((entry) => Number.isFinite(entry.start) && entry.end > entry.start)
    .sort(
      (a, b) =>
        a.start - b.start ||
        Number(Boolean(a.blocked)) - Number(Boolean(b.blocked)) ||
        (a.id < b.id ? -1 : 1)
    )
  return { entries, gaps: complement(entries, from, to), from, to }
}

function complement(entries: Entry[], from: number, to: number): Gap[] {
  if (!(to > from)) return []
  const spans = entries
    .map((entry): [number, number] => [
      Math.max(entry.start, from),
      Math.min(entry.end, to),
    ])
    .filter(([start, end]) => end > start)
    .sort((a, b) => a[0] - b[0])
  const gaps: Gap[] = []
  let cursor = from
  for (const [start, end] of spans) {
    if (start > cursor)
      gaps.push({
        start: cursor,
        end: start,
        reason: '该时间段没有已完成的录像',
      })
    cursor = Math.max(cursor, end)
  }
  if (cursor < to)
    gaps.push({ start: cursor, end: to, reason: '该时间段没有已完成的录像' })
  return gaps
}

/** The playable segment containing `at`, preferring the earliest match. */
export function entryAt(timeline: Timeline, at: number): Entry | null {
  return (
    timeline.entries.find(
      (entry) => !entry.blocked && at >= entry.start && at < entry.end
    ) || null
  )
}

/** The first playable segment that extends past `after`. */
export function nextPlayableEntry(
  timeline: Timeline,
  after: number
): Entry | null {
  return (
    timeline.entries.find((entry) => !entry.blocked && entry.end > after) ||
    null
  )
}

export function gapAt(timeline: Timeline, at: number): Gap | null {
  return timeline.gaps.find((gap) => at >= gap.start && at < gap.end) || null
}

/** Seconds into a segment, clamped so a boundary never overshoots. */
export function offsetIn(entry: Entry, at: number): number {
  const seconds = (entry.end - entry.start) / 1000
  return Math.min(Math.max((at - entry.start) / 1000, 0), Math.max(seconds, 0))
}

export function formatDuration(milliseconds: number): string {
  const total = Math.max(0, Math.round(milliseconds / 1000))
  const hours = Math.floor(total / 3600),
    minutes = Math.floor((total % 3600) / 60),
    seconds = total % 60
  return hours
    ? `${hours}:${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}`
    : `${minutes}:${String(seconds).padStart(2, '0')}`
}

/**
 * The view window: the timeline renders one slice of the fetched index so a
 * 24-hour window can be zoomed into minutes. Entries are clipped to the view
 * (a segment crossing the edge renders from the edge, not from off-screen),
 * and gaps are the complement of the clipped coverage — a hole outside the
 * view must not be counted, and one inside must not be hidden.
 */
export function sliceView(timeline: Timeline, viewStart: number, viewEnd: number): Timeline {
  if (!(viewEnd > viewStart)) return timeline
  const clipped = timeline.entries
    .map((entry) => ({ ...entry, start: Math.max(entry.start, viewStart), end: Math.min(entry.end, viewEnd) }))
    .filter((entry) => entry.end > entry.start)
  return { entries: clipped, gaps: complement(clipped, viewStart, viewEnd), from: viewStart, to: viewEnd }
}

/** The next smaller view window around an anchor, clamped to the full range. */
export function zoomView(from: number, to: number, anchor: number, full: { start: number; end: number }): { start: number; end: number } {
  const span = Math.max((to - from) / 2, 60_000)
  let start = anchor - span / 2
  let end = anchor + span / 2
  if (start < full.start) {
    end += full.start - start
    start = full.start
  }
  if (end > full.end) {
    start = Math.max(full.start, start - (end - full.end))
    end = full.end
  }
  return { start, end: Math.min(end, full.end) }
}

// Tick ladder in seconds: the ruler picks the first interval that yields a
// readable number of labels for the strip width.
const TICK_LADDER = [60, 120, 300, 600, 900, 1800, 3600, 7200, 10800, 21600, 43200, 86400, 172800, 604800]

export type TimelineTick = { at: number; label: string; major: boolean }

function tickPad(value: number, width = 2): string {
  return String(value).padStart(width, '0')
}

/**
 * The time ruler for one view window: wall-clock-aligned ticks in the site's
 * zone (hourly ticks land on :00, daily on midnight), with labels that stay
 * readable as the view zooms from a month down to a minute.
 */
export function buildTicks(viewStart: number, viewEnd: number, widthPx: number, timeZone: string): TimelineTick[] {
  if (!(viewEnd > viewStart) || !Number.isFinite(widthPx) || widthPx < 60 || !timeZone) return []
  const spanSeconds = (viewEnd - viewStart) / 1000
  const target = Math.max(4, Math.min(12, Math.floor(widthPx / 90)))
  let interval = TICK_LADDER[TICK_LADDER.length - 1]
  for (const candidate of TICK_LADDER) {
    if (spanSeconds / candidate <= target) {
      interval = candidate
      break
    }
  }
  const daily = interval >= 86400
  const dayStart = startOfZoneDay(viewStart, timeZone)
  if (!Number.isFinite(dayStart)) return []
  let at = dayStart
  if (!daily) {
    const parts = zoneParts(viewStart, timeZone)
    const secondOfDay = parts.hour * 3600 + parts.minute * 60 + parts.second
    at = dayStart + (secondOfDay - (secondOfDay % interval)) * 1000
  }
  const ticks: TimelineTick[] = []
  let lastDay = ''
  for (let guard = 0; guard <= 400; guard++) {
    if (at > viewEnd) break
    if (at >= viewStart) {
      const parts = zoneParts(at, timeZone)
      const day = `${tickPad(parts.year, 4)}-${tickPad(parts.month)}-${tickPad(parts.day)}`
      const isDayStart = parts.hour === 0 && parts.minute === 0
      let label: string
      if (daily) label = day.slice(5)
      else if (lastDay !== day) label = `${day.slice(5)} ${tickPad(parts.hour)}:${tickPad(parts.minute)}`
      else label = `${tickPad(parts.hour)}:${tickPad(parts.minute)}`
      ticks.push({ at, label, major: isDayStart })
      lastDay = day
    }
    at += interval * 1000
  }
  return ticks
}
