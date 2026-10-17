import type { Schema } from '@/lib/types'

/**
 * Fault list presentation: filtering, flapping aggregation, and severity.
 *
 * All of it is frontend state over one already-fetched list. The API contract is
 * untouched: it returns every fault in the window, and the page decides what to
 * show and in which order. That keeps the "how many were there" answer honest —
 * a filtered view always reports the filtered count, never a silently truncated
 * server response.
 */

export type Fault = Schema<'ReliabilityFault'>

/** Shortest-duration filters, labelled the way an operator reasons about them. */
export const DURATION_FILTERS = [
  { value: '0', label: '全部时长' },
  { value: '30000', label: '≥ 30 秒' },
  { value: '60000', label: '≥ 1 分钟' },
  { value: '300000', label: '≥ 5 分钟' },
] as const

export type FaultStateFilter = 'all' | 'open' | 'resolved'

export interface FaultFilters {
  channel: string
  kind: string
  minDurationMs: number
  state: FaultStateFilter
}

export const EMPTY_FILTERS: FaultFilters = {
  channel: '',
  kind: '',
  minDurationMs: 0,
  state: 'all',
}

export function filterFaults(items: Fault[], filters: FaultFilters): Fault[] {
  return items.filter((fault) => {
    if (filters.channel && fault.channel_id !== filters.channel) return false
    if (filters.kind && fault.kind !== filters.kind) return false
    if (filters.state !== 'all' && fault.state !== filters.state) return false
    if (filters.minDurationMs > 0 && (fault.duration_ms ?? 0) < filters.minDurationMs) {
      return false
    }
    return true
  })
}

/**
 * orderFaults puts an unresolved fault first.
 *
 * A fault that is still open is the only row on this table that is happening
 * right now; burying it under resolved history sorted purely by start time would
 * make the page's most urgent fact the least visible one. Within each state the
 * order stays newest-first.
 */
export function orderFaults(items: Fault[]): Fault[] {
  return [...items].sort((a, b) => {
    if (a.state !== b.state) return a.state === 'open' ? -1 : 1
    return new Date(b.started_at).getTime() - new Date(a.started_at).getTime()
  })
}

/**
 * A flapping channel produces many short faults in the same kind and category.
 * They are one problem observed repeatedly, not N independent incidents, so they
 * collapse into one row — and the row says how many, so the severity is not
 * hidden by the collapse.
 */
export interface FaultGroup {
  key: string
  /** Every fault in the group, newest first. */
  items: Fault[]
  /** The newest fault; its fields describe the group's current state. */
  latest: Fault
  count: number
  totalDurationMs: number
  /** True when any member is still open. */
  open: boolean
  firstStartedAt: string
  lastStartedAt: string
}

/**
 * groupFlappingFaults merges adjacent faults that describe the same ongoing
 * problem.
 *
 * Two faults belong together when they share channel, kind and category, and the
 * next one starts within `gapMs` of the previous one ending. The gap test is what
 * makes this "flapping" rather than "this channel had faults": a stream that
 * dropped at 10:00 and again at 10:00:20 is flapping, one that dropped at 10:00
 * and again at 15:00 is two separate incidents and must stay two rows.
 *
 * `overlapping` groups by channel/kind/category only, for operators who want to
 * see the whole history of a problem as one line.
 */
export function groupFlappingFaults(
  items: Fault[],
  opts: { gapMs?: number } = {}
): FaultGroup[] {
  const gapMs = opts.gapMs ?? 2 * 60 * 1000
  const ordered = orderFaults(items)
  const buckets = new Map<string, Fault[]>()
  const order: string[] = []
  for (const fault of ordered) {
    const key = `${fault.channel_id}|${fault.kind}|${fault.effective_category}`
    if (!buckets.has(key)) {
      buckets.set(key, [])
      order.push(key)
    }
    buckets.get(key)!.push(fault)
  }
  const groups: FaultGroup[] = []
  for (const key of order) {
    const bucket = buckets.get(key)!
    let current: Fault[] = []
    const flush = () => {
      if (current.length) groups.push(buildGroup(key, current))
      current = []
    }
    for (const fault of bucket) {
      if (current.length === 0) {
        current.push(fault)
        continue
      }
      const previous = current[current.length - 1]
      // Ordered newest-first, so "previous" started later than `fault`. They are
      // adjacent when this fault ended within gapMs of the previous one starting.
      const previousStart = new Date(previous.started_at).getTime()
      const faultEnd = fault.ended_at
        ? new Date(fault.ended_at).getTime()
        : previousStart
      if (Math.abs(previousStart - faultEnd) <= gapMs) {
        current.push(fault)
        continue
      }
      flush()
      current.push(fault)
    }
    flush()
  }
  // Groups are ordered by their newest member so the combined list still reads
  // newest-first overall.
  return groups.sort(
    (a, b) =>
      new Date(b.lastStartedAt).getTime() - new Date(a.lastStartedAt).getTime()
  )
}

function buildGroup(key: string, items: Fault[]): FaultGroup {
  const latest = items[0]
  const oldest = items[items.length - 1]
  return {
    key,
    items,
    latest,
    count: items.length,
    totalDurationMs: items.reduce((sum, item) => sum + (item.duration_ms ?? 0), 0),
    open: items.some((item) => item.state === 'open'),
    firstStartedAt: oldest.started_at,
    lastStartedAt: latest.started_at,
  }
}

/** A fault at or above this duration is highlighted: five minutes of outage is
 *  long enough to matter, while ten-second drops are noise in the same table. */
export const SEVERE_FAULT_MS = 5 * 60 * 1000

export function isSevere(durationMs: number | undefined): boolean {
  return (durationMs ?? 0) >= SEVERE_FAULT_MS
}

/**
 * groupIsSevere reports whether an aggregated group contains a single outage of
 * five minutes or more.
 *
 * This is deliberately NOT "the summed duration crosses five minutes". Twelve
 * ten-second blips add up to two minutes of downtime spread across an hour, which
 * is a flapping stream; one nine-minute outage is a different failure with a
 * different cause and a different remedy. Highlighting both the same way would
 * make the number that matters — the longest single interruption — unreadable.
 */
export function groupIsSevere(group: { items: { duration_ms?: number }[] }): boolean {
  return group.items.some((item) => isSevere(item.duration_ms))
}

/**
 * relativeTime renders "just now", "4 小时前" and so on.
 *
 * Timestamps in this table are read to answer "when did this happen", and for
 * anything older than a few minutes the exact second is noise; the full time is
 * still available on hover. Negative or unparseable input returns an empty
 * string rather than a fabricated duration.
 */
export function relativeTime(iso: string, now = Date.now()): string {
  const at = new Date(iso).getTime()
  if (!Number.isFinite(at)) return ''
  const delta = now - at
  if (delta < 0) return '刚刚'
  const minutes = Math.floor(delta / 60_000)
  if (minutes < 1) return '刚刚'
  if (minutes < 60) return `${minutes} 分钟前`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours} 小时前`
  const days = Math.floor(hours / 24)
  if (days < 30) return `${days} 天前`
  const months = Math.floor(days / 30)
  if (months < 12) return `${months} 个月前`
  return `${Math.floor(days / 365)} 年前`
}

/**
 * share splits a row's observed time into the three bands the sparkbar draws.
 *
 * The three are computed against observed_ms, not against the window: the bar
 * describes what was seen, and sampleNote already says how much of the window
 * that was. Rows with no observation produce no bar at all rather than an empty
 * one, because a zero-width bar reads as a rendering failure.
 */
export interface SpanShares {
  healthy: number
  unavailable: number
  unknown: number
  total: number
}

export function spanShares(row: {
  healthy_ms: number
  unavailable_ms: number
  unknown_ms: number
}): SpanShares | null {
  const total = row.healthy_ms + row.unavailable_ms + row.unknown_ms
  if (total <= 0) return null
  return {
    healthy: row.healthy_ms / total,
    unavailable: row.unavailable_ms / total,
    unknown: row.unknown_ms / total,
    total,
  }
}

/** The minimum observation a row needs before its rates are comparable. Mirrors
 *  reliability.sampleNote so the tooltip and the per-row tag cannot disagree. */
export const MINIMUM_SAMPLE_MS = 60 * 60 * 1000
