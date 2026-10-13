import type { Schema } from '@/lib/types'
import { zoneParts } from './zone'

type Event = Schema<'Event'>

/**
 * One aggregated mark on the playback timeline's event track. Events are
 * fetched page-limited (the newest window), so the track shows what the API
 * vouches for and the page states that fact — never a fabricated full set.
 */
export type EventMark = {
  id: string
  at: number
  end: number
  category: string
  score: number
  handleState: string
}

/**
 * Places the channel's events on the same absolute clock as the timeline.
 * Events outside the view are dropped here, so the strip never renders a
 * marker it cannot position.
 */
export function eventMarks(
  events: Event[],
  viewStart: number,
  viewEnd: number
): EventMark[] {
  return events
    .map((event) => ({
      id: event.id ?? '',
      at: Date.parse(event.started_at ?? ''),
      end: Date.parse(event.ended_at ?? '') || Date.parse(event.started_at ?? ''),
      category: event.category ?? '',
      score: event.score ?? 0,
      handleState: event.handle_state ?? 'open',
    }))
    .filter(
      (mark) =>
        mark.id &&
        Number.isFinite(mark.at) &&
        mark.at < viewEnd &&
        mark.end >= viewStart
    )
    .sort((a, b) => a.at - b.at)
}

/**
 * The marker to draw for one event category. Icons are shared with the event
 * center's labels; the shape is a small dot on the track, so any category —
 * known or future — degrades to a neutral mark rather than nothing.
 */
export const MARK_LABELS: Record<string, string> = {
  person: '人',
  car: '车',
  truck: '卡车',
  bus: '巴士',
  motorcycle: '摩托车',
  bicycle: '自行车',
  dog: '狗',
  cat: '猫',
}

export function markLabel(category: string): string {
  return MARK_LABELS[category] || category || '事件'
}

/**
 * Merges marks that would overlap on a strip of `widthPx` pixels into
 * clusters, so three days of events render as a readable scatter instead of
 * one solid smear. Each cluster keeps its newest member's position and folds
 * the rest into a count the tooltip reports.
 */
export function clusterMarks(
  marks: EventMark[],
  from: number,
  to: number,
  widthPx: number,
  minPx = 8
): (EventMark & { count: number })[] {
  if (to <= from || widthPx <= 0) return []
  const pxPerMs = widthPx / (to - from)
  const clusters: (EventMark & { count: number })[] = []
  for (const mark of marks) {
    const last = clusters[clusters.length - 1]
    if (
      last &&
      ((mark.at - last.at) * pxPerMs < minPx ||
        last.at === mark.at)
    ) {
      last.count++
      if (mark.score > last.score) {
        last.score = mark.score
        last.category = mark.category
      }
      last.end = Math.max(last.end, mark.end)
    } else {
      clusters.push({ ...mark, count: 1 })
    }
  }
  return clusters
}

/**
 * The event query window: the timeline's own view window, clamped to now and
 * to the events index' 90-day retention. The track then covers whatever the
 * operator is actually looking at, not just the newest day.
 */
export function eventWindowSize(
  viewStart: number,
  viewEnd: number
): { start: number; end: number } {
  const RETENTION = 90 * 86_400_000
  const now = Date.now()
  const end = Math.min(viewEnd, now)
  const start = Math.max(viewStart, end - RETENTION)
  return { start, end }
}

// zoneParts is re-exported for tests that assert bucket alignment without
// importing the zone module twice.
export { zoneParts }
