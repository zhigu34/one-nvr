import type { Schema } from '@/lib/types'

/**
 * Trend rows for the reliability chart.
 *
 * The chart answers "is this channel getting better or worse", which is a
 * question about direction over time. It deliberately plots durations rather than
 * a single percentage line: a day with no observation at all must not draw a 0%
 * point, because that would look like an outage and there is a separate colour
 * for "we could not see".
 */
export type TrendRow = {
  /** Short label for the axis: MM-DD, or the raw day when it does not parse. */
  label: string
  /** Stacked segments, in hours so a 30-day window still has readable numbers. */
  healthy: number
  unavailable: number
  unknown: number
  excluded: number
  /** Faults that started that day, for the secondary axis. */
  faults: number
  /** Kept so a tooltip can state the real duration instead of a rounded hour. */
  healthyMs: number
  unavailableMs: number
  unknownMs: number
  raw: string
}

/** Milliseconds in an hour, for the axis conversion. */
const HOUR = 3_600_000

/**
 * Shape one series into chart rows.
 *
 * Days arrive newest-first from the API (the list is ordered that way for the
 * table); a time series must read left to right, so they are reversed here rather
 * than at the call site where it would be easy to forget.
 */
export function trendRows(points: Schema<'ReliabilityTrendPoint'>[]): TrendRow[] {
  const rows = points.map((point) => ({
    label: dayLabel(point.day ?? ''),
    healthy: hours(point.healthy_ms ?? 0),
    unavailable: hours(point.unavailable_ms ?? 0),
    unknown: hours(point.unknown_ms ?? 0),
    excluded: hours(point.excluded_ms ?? 0),
    faults: point.fault_count ?? 0,
    healthyMs: point.healthy_ms ?? 0,
    unavailableMs: point.unavailable_ms ?? 0,
    unknownMs: point.unknown_ms ?? 0,
    raw: point.day ?? '',
  }))
  return rows.sort((a, b) => a.raw.localeCompare(b.raw))
}

/** "2026-10-18" → "10-18". A label the tooltip can explain in full. */
export function dayLabel(day: string): string {
  const match = /^\d{4}-(\d{2})-(\d{2})/.exec(day)
  return match ? `${match[1]}-${match[2]}` : day
}

function hours(ms: number): number {
  return Math.round((ms / HOUR) * 10) / 10
}

/**
 * Whether the series is worth drawing.
 *
 * One point is a number, not a trend: the table already shows it, and a chart
 * with a single column invites reading a direction that does not exist.
 */
export function worthCharting(rows: TrendRow[]): boolean {
  return rows.length > 1
}

/**
 * The window covered, for the caption. Stated because a trend over four days and
 * one over four hundred look identical once plotted.
 */
export function trendSpan(rows: TrendRow[]): string {
  if (rows.length === 0) return ''
  const first = rows[0].raw
  const last = rows[rows.length - 1].raw
  return first === last ? first : `${first} 至 ${last}`
}
