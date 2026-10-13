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

/** True when the two marks would collide below the given pixel width. */
export function wouldCollide(
  a: { at: number },
  b: { at: number },
  from: number,
  span: number,
  widthPx: number,
  minPx: number
): boolean {
  if (span <= 0 || widthPx <= 0) return false
  const posA = ((a.at - from) / span) * widthPx
  const posB = ((b.at - from) / span) * widthPx
  return Math.abs(posB - posA) < minPx
}

/**
 * The densest useful window for the event query. The timeline span can be a
 * month but the events endpoint is built for recent activity; the page asks
 * for the view window clamped to a day so the track stays honest about what a
 * bounded query returns.
 */
export function eventWindowSize(
  viewStart: number,
  viewEnd: number
): { start: number; end: number } {
  const DAY = 86_400_000
  const now = Date.now()
  const end = Math.min(viewEnd, now)
  const start = Math.max(viewStart, end - DAY)
  return { start, end }
}

// zoneParts is re-exported for tests that assert bucket alignment without
// importing the zone module twice.
export { zoneParts }
