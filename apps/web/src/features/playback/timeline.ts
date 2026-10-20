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
  /**
   * True when the local copy was removed on purpose.
   *
   * A deletion is the operator's decision, not a recording fault, so it must not
   * be painted like one: red is what "the stream failed" looks like, and a
   * timeline full of red over footage someone chose to discard sends the reader
   * hunting for a fault that does not exist. Deleted time is drawn as its own
   * neutral texture and stays out of the blocked count.
   */
  deleted: boolean
  /** Carried for the player-frame metadata pills; optional because the
   *  placement in this view can be built without it. */
  media?: Segment['media']
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
  // A deleted segment is unplayable but not BLOCKED: nothing went wrong, and the
  // reason is already stated by its own texture and by the selection card.
  if (segment.state === 'deleted') return ''
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
      deleted: segment.state === 'deleted',
      media: segment.media,
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

/**
 * The largest interval between adjacent segments that still reads as a
 * continuous recording. Slice hand-off jitter is milliseconds to a couple of
 * seconds (63 s segments regularly touch within ~1 s), so anything at or
 * under this tolerance is not a dropout — counting it as one reported 267
 * "gaps" across 1225 continuous segments. Only a silence STRICTLY LONGER
 * than this is a real gap, on the canvas and in the stats.
 */
export const GAP_TOLERANCE_MS = 5000

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
    // A sliver of jitter between segments is not a dropout: absorb it into
    // the covered span instead of reporting a hairline gap.
    if (start - cursor > GAP_TOLERANCE_MS)
      gaps.push({
        start: cursor,
        end: start,
        reason: '该时间段没有已完成的录像',
      })
    cursor = Math.max(cursor, end)
  }
  if (to - cursor > GAP_TOLERANCE_MS)
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

/**
 * The raw (possibly blocked) segment containing `at`, earliest match first.
 * Canvas-strip hit-testing uses this: clicking an amber blocked bar must
 * select that segment so its blocked reason is visible, not fall through.
 */
export function entryAtAny(timeline: Timeline, at: number): Entry | null {
  return (
    timeline.entries.find(
      (entry) => at >= entry.start && at < entry.end
    ) || null
  )
}

/**
 * Merges entries whose interval gaps are ≤ `toleranceMs` into continuous
 * spans, separately for playable and blocked segments. The canvas strip draws
 * from this: adjacent segments drawn as individual rects produce sub-pixel
 * seams that read as fake dropouts, while a true gap — anything over the
 * tolerance — stays untouched, so 断录缺口 still shows as empty canvas.
 * The default mirrors `GAP_TOLERANCE_MS`: hand-off jitter is not a dropout.
 */
export function mergeEntries(
  entries: Entry[],
  toleranceMs = GAP_TOLERANCE_MS
): {
  playable: { start: number; end: number }[]
  blocked: { start: number; end: number }[]
  deleted: { start: number; end: number }[]
} {
  const playable: { start: number; end: number }[] = []
  const blocked: { start: number; end: number }[] = []
  // Deleted spans are merged on their own so the canvas can draw them as a
  // third, neutral layer: unplayable, but not a fault.
  const deleted: { start: number; end: number }[] = []
  const absorb = (
    spans: { start: number; end: number }[],
    start: number,
    end: number
  ) => {
    const last = spans[spans.length - 1]
    if (last && start - last.end <= toleranceMs) {
      last.end = Math.max(last.end, end)
    } else {
      spans.push({ start, end })
    }
  }
  for (const entry of entries) {
    if (entry.deleted) absorb(deleted, entry.start, entry.end)
    else if (entry.blocked) absorb(blocked, entry.start, entry.end)
    else absorb(playable, entry.start, entry.end)
  }
  return { playable, blocked, deleted }
}

/**
 * The playable moment a click or a released scrub lands on. A point inside a
 * playable segment stays put; a point in a gap snaps to the nearest playable
 * edge — left or right, whichever is closer, capped at one gap-width away.
 * UniFi-style timelines never dump the operator into a dead hole and make
 * them hunt for a jump button; the seek itself navigates.
 */
export function snapToPlayable(
  timeline: Timeline,
  at: number
): { at: number; entry: Entry | null } {
  const containing = entryAt(timeline, at)
  if (containing) return { at, entry: containing }
  if (!timeline.entries.length) return { at, entry: null }
  const playable = timeline.entries.filter((entry) => !entry.blocked)
  if (!playable.length) return { at, entry: null }
  const before = [...playable]
    .reverse()
    .find((entry) => entry.end <= at)
  const after = playable.find((entry) => entry.start >= at)
  if (before && after) {
    const toBefore = at - before.end
    const toAfter = after.start - at
    return toBefore <= toAfter
      ? { at: before.end - 1, entry: before }
      : { at: after.start, entry: after }
  }
  if (before) return { at: before.end - 1, entry: before }
  if (after) return { at: after.start, entry: after }
  return { at, entry: null }
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

/** One density bucket of the timeline: covered milliseconds, nothing more. */
export type DensityBucket = {
  from: number
  to: number
  /** Milliseconds of merged coverage inside the bucket, 0..bucket span. */
  coveredMs: number
  /** Segment count before merging (how busy, not how long). */
  segments: number
}

/**
 * Coverage density for the strip's second layer. Overlapping segments (a
 * source switched mid-run) are merged first — density measures recorded
 * time, not index rows. The bucket count adapts to the view span so both a
 * month and an hour render as a smooth, readable profile. Two passes, both
 * linear: a merge sweep, then per bucket a bounded intersection walk — each
 * merge span is visited only while it overlaps the bucket's window, so a
 * long span is re-entered from the start of the bucket scan instead of
 * tracked across iterations (bucketCount is bounded by the strip width).
 */
export function coverageDensity(
  timeline: Timeline,
  bucketCount: number
): DensityBucket[] {
  if (!(timeline.to > timeline.from) || bucketCount < 1) return []
  const span = timeline.to - timeline.from
  const width = span / bucketCount
  const spans: [number, number][] = timeline.entries
    .map((entry): [number, number] => [entry.start, entry.end])
    .filter(([start, end]) => end > start)
    .sort((a, b) => a[0] - b[0])
  const merged: [number, number][] = []
  for (const [start, end] of spans) {
    const last = merged[merged.length - 1]
    if (last && start <= last[1]) last[1] = Math.max(last[1], end)
    else merged.push([start, end])
  }
  const buckets: DensityBucket[] = []
  for (let index = 0; index < bucketCount; index++) {
    const from = timeline.from + index * width
    const to = from + width
    let coveredMs = 0
    let segments = 0
    for (const entry of timeline.entries) {
      if (entry.end > from && entry.start < to) segments++
    }
    for (const [start, end] of merged) {
      if (end <= from) continue
      if (start >= to) break
      coveredMs += Math.min(end, to) - Math.max(start, from)
    }
    buckets.push({ from, to, coveredMs: Math.min(coveredMs, width), segments })
  }
  return buckets
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
  // The zoom floor is five minutes (a minute view is useless when segments
  // are ≥ 1 minute) and the ceiling seven days — the widest range the search
  // form itself allows to stay useful on one screen.
  const MIN_SPAN = 5 * 60_000
  const MAX_SPAN = 7 * 86_400_000
  const span = Math.min(Math.max((to - from) / 2, MIN_SPAN), MAX_SPAN)
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
