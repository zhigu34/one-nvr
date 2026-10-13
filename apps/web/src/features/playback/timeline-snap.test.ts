import { describe, expect, test } from 'vitest'
import { buildTimeline, snapToPlayable } from './timeline'
import type { Schema } from '@/lib/types'

const base = Date.parse('2026-10-05T00:00:00Z')
const at = (seconds: number) => new Date(base + seconds * 1000).toISOString()
const segment = (id: string, from: number, to: number, state: Schema<'RecordingSegment'>['state'] = 'ready'): Schema<'RecordingSegment'> => ({
  id, channel_id: 'c', source_revision_id: 'r', pool_id: 'p', run_id: 'run',
  start: at(from), end: at(to), bytes: 1000, state, protected: false,
  media: { codec: '', width: 0, height: 0, fps: 0, duration_ms: 0 },
  naming_timezone: 'UTC', utc_offset_seconds: 0, check_state: 'ready',
})

const MIN = 60
const A = segment('a', 0, 10 * MIN)
const B = segment('b', 20 * MIN, 30 * MIN)
const DAMAGED = segment('d', 40 * MIN, 50 * MIN, 'damaged')
const C = segment('c', 60 * MIN, 70 * MIN)

describe('snapToPlayable', () => {
  const timeline = buildTimeline([A, B, DAMAGED, C], base, base + 80 * 60_000)

  test('a point inside a playable segment stays put', () => {
    const snap = snapToPlayable(timeline, base + 5 * 60_000)
    expect(snap.at).toBe(base + 5 * 60_000)
    expect(snap.entry?.id).toBe('a')
  })

  test('a point in a gap snaps to the nearer playable edge', () => {
    // The hole 10-20min: 12min in is closer to the left edge (2min vs 8min).
    const left = snapToPlayable(timeline, base + 12 * 60_000)
    expect(left.entry?.id).toBe('a')
    // 18min in is closer to the right edge.
    const right = snapToPlayable(timeline, base + 18 * 60_000)
    expect(right.entry?.id).toBe('b')
  })

  test('a point inside a damaged segment snaps out to playable', () => {
    // 45min sits inside the damaged span; nearest playable edges: b ends at
    // 30min (15min away), c starts at 60min (15min away). Tie -> left.
    const snap = snapToPlayable(timeline, base + 45 * 60_000)
    expect(snap.entry?.id).toBe('b')
  })

  test('before the first playable moment snaps to its start', () => {
    const snap = snapToPlayable(timeline, base - 60_000)
    expect(snap.entry?.id).toBe('a')
  })

  test('after the last playable moment snaps to the previous end', () => {
    const snap = snapToPlayable(timeline, base + 75 * 60_000)
    expect(snap.entry?.id).toBe('c')
  })

  test('an index with nothing playable never snaps anywhere', () => {
    const empty = buildTimeline([DAMAGED], base, base + 60 * 60_000)
    const snap = snapToPlayable(empty, base + 45 * 60_000)
    expect(snap.entry).toBeNull()
  })
})
