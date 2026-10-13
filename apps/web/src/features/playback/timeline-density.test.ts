import { describe, expect, test } from 'vitest'
import { buildTimeline, coverageDensity } from './timeline'
import type { Schema } from '@/lib/types'

// All values are epoch milliseconds, as in production: the timeline's from/to
// and every entry's start/end live on the same absolute clock.
const base = Date.parse('2026-10-05T00:00:00Z')
const at = (seconds: number) => new Date(base + seconds * 1000).toISOString()
const segment = (id: string, from: number, to: number): Schema<'RecordingSegment'> => ({
  id, channel_id: 'c', source_revision_id: 'r', pool_id: 'p', run_id: 'run',
  start: at(from), end: at(to), bytes: 1000, state: 'ready', protected: false,
  media: { codec: '', width: 0, height: 0, fps: 0, duration_ms: 0 },
  naming_timezone: 'UTC', utc_offset_seconds: 0, check_state: 'ready',
})

const HOUR = 3600_000

describe('coverageDensity', () => {
  test('empty timeline yields zero coverage', () => {
    const timeline = buildTimeline([], base, base + HOUR)
    const buckets = coverageDensity(timeline, 4)
    expect(buckets).toHaveLength(4)
    expect(buckets.every((b) => b.coveredMs === 0 && b.segments === 0)).toBe(true)
  })

  test('full coverage reads as full buckets', () => {
    const timeline = buildTimeline([segment('a', 0, 3600)], base, base + HOUR)
    const buckets = coverageDensity(timeline, 4)
    expect(buckets.every((b) => b.coveredMs === HOUR / 4)).toBe(true)
    expect(buckets[0].segments).toBe(1)
  })

  test('overlapping segments merge, never double-count', () => {
    const timeline = buildTimeline(
      [segment('a', 0, 3600), segment('b', 0, 3600)], base, base + HOUR)
    const buckets = coverageDensity(timeline, 4)
    expect(buckets.every((b) => b.coveredMs === HOUR / 4)).toBe(true)
    // segment count stays raw: two rows were busy here
    expect(buckets[0].segments).toBe(2)
  })

  test('partial coverage sums correctly across buckets', () => {
    // 0-30min covered, 30-60min empty
    const timeline = buildTimeline([segment('a', 0, 1800)], base, base + HOUR)
    const buckets = coverageDensity(timeline, 2)
    expect(buckets[0].coveredMs).toBe(1800_000)
    expect(buckets[1].coveredMs).toBe(0)
  })

  test('a segment spanning the whole range fills every bucket', () => {
    const timeline = buildTimeline(
      [segment('a', -3600, 7200)], base, base + HOUR)
    const buckets = coverageDensity(timeline, 6)
    expect(buckets.every((b) => b.coveredMs === HOUR / 6)).toBe(true)
  })
})
