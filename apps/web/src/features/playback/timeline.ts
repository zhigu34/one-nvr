import type { Schema } from '@/lib/types'

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

export function formatClock(epochMs: number, timezone: string): string {
  if (!Number.isFinite(epochMs)) return '—'
  const parts = new Intl.DateTimeFormat('zh-CN', {
    timeZone: timezone,
    hourCycle: 'h23',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  }).formatToParts(new Date(epochMs))
  const pick = (type: Intl.DateTimeFormatPartTypes) =>
    parts.find((part) => part.type === type)?.value || ''
  return `${pick('year')}-${pick('month')}-${pick('day')} ${pick('hour')}:${pick('minute')}:${pick('second')}`
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
