import type { Schema } from '@/lib/types'

/**
 * Why an event has no picture, in the operator's terms (PRD EVT-04 detail).
 *
 * The gallery used to say "无抓拍图像" for every missing snapshot, which reads as
 * "the system lost it". Measured on the live site, that was wrong for a quarter of
 * the short events: Frigate writes snapshots while an event runs, so a detection
 * that lasts one second can end before a single frame is written. The detector
 * never had the picture — there is nothing to recover and nothing to fix.
 *
 * Telling those apart matters because the two demand different reactions: a lost
 * snapshot is a fault to investigate, a too-short event is simply too short. The
 * threshold is three seconds because the live data separates cleanly there (25%
 * missing under 3s, 3% between 3 and 10s, 1% beyond).
 */
export const SHORT_EVENT_SECONDS = 3

export type MissingSnapshotReason = 'too_short' | 'absent' | 'not_applicable'

/**
 * Classify a missing snapshot. Only called when the picture really is missing:
 * a ready/pending snapshot is a different question.
 *
 * A running event is not classified — its picture may still arrive.
 */
export function missingSnapshotReason(
  event: Schema<'Event'>
): MissingSnapshotReason {
  if (event.snapshot_state !== 'gone') return 'not_applicable'
  if (!event.ended_at || !event.started_at) return 'absent'
  const seconds =
    (Date.parse(event.ended_at) - Date.parse(event.started_at)) / 1000
  if (!Number.isFinite(seconds)) return 'absent'
  return seconds < SHORT_EVENT_SECONDS ? 'too_short' : 'absent'
}

/** Whole seconds an event lasted, or null while it runs or without an end. */
export function eventSeconds(event: Schema<'Event'>): number | null {
  if (!event.ended_at || !event.started_at) return null
  const seconds = Math.round(
    (Date.parse(event.ended_at) - Date.parse(event.started_at)) / 1000
  )
  return Number.isFinite(seconds) && seconds > 0 ? seconds : null
}
