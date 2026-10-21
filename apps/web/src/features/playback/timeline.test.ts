import { describe, expect, it } from 'vitest'
import type { Schema } from '@/lib/types'
import {
  blockedReason,
  type Entry,
  buildTimeline,
  entryAt,
  formatDuration,
  gapAt,
  isFault,
  mergeEntries,
  nextPlayableEntry,
  offsetIn,
  buildTicks,
  sliceView,
  zoomView,
} from './timeline'

const base = Date.parse('2026-10-05T00:00:00Z')
const at = (seconds: number) => new Date(base + seconds * 1000).toISOString()
function segment(
  id: string,
  from: number,
  to: number,
  overrides: Partial<Schema<'RecordingSegment'>> = {}
): Schema<'RecordingSegment'> {
  return {
    id,
    channel_id: 'channel',
    source_revision_id: 'revision',
    pool_id: 'pool',
    run_id: 'run',
    start: at(from),
    end: at(to),
    bytes: 1024,
    state: 'ready',
  protected: false,
  media: { codec: '', width: 0, height: 0, fps: 0, duration_ms: 0 },
    naming_timezone: 'Asia/Shanghai',
    utc_offset_seconds: 28800,
    check_state: 'ready',
    ...overrides,
  }
}

describe('recording timeline', () => {
  it('places segments on one clock and reports holes between them', () => {
    const timeline = buildTimeline(
      [segment('b', 120, 180), segment('a', 0, 60)],
      base,
      base + 240_000
    )
    expect(timeline.entries.map((entry) => entry.id)).toEqual(['a', 'b'])
    expect(timeline.gaps).toEqual([
      {
        start: base + 60_000,
        end: base + 120_000,
        reason: '该时间段没有已完成的录像',
      },
      {
        start: base + 180_000,
        end: base + 240_000,
        reason: '该时间段没有已完成的录像',
      },
    ])
  })

  it('treats overlapping segments as coverage instead of a fabricated hole', () => {
    const timeline = buildTimeline(
      [segment('a', 0, 90), segment('b', 30, 120)],
      base,
      base + 120_000
    )
    expect(timeline.gaps).toEqual([])
  })

  it('clips coverage to the requested range', () => {
    const timeline = buildTimeline(
      [segment('a', -60, 600)],
      base,
      base + 120_000
    )
    expect(timeline.gaps).toEqual([])
    expect(timeline.entries).toHaveLength(1)
  })

  it('never offers an unpublished or unreadable segment for playback', () => {
    for (const state of [
      'finalizing',
      'provisional',
      'missing',
      'damaged',
      'conflict',
    ] as const) {
      expect(blockedReason(segment('x', 0, 60, { state }))).not.toBe('')
    }
    expect(
      blockedReason(segment('x', 0, 60, { check_state: 'pool_unavailable' }))
    ).toBe('存储池不可用')
    expect(
      blockedReason(segment('x', 0, 60, { check_state: 'media_unavailable' }))
    ).toBe('文件当前无法读取')
    expect(blockedReason(segment('x', 0, 60))).toBe('')
  })

  // ARC-04: the content endpoint serves the archived copy when the local file is
  // gone, so a deleted or missing segment with a verified cloud copy plays.
  // Blocking it here made the file list's play button do nothing: the play
  // signal looked for a playable entry and found none.
  it('an archived segment without local media is playable', () => {
    for (const state of ['deleted', 'missing'] as const) {
      expect(blockedReason(segment('x', 0, 60, { state, archived: true }))).toBe('')
    }
    // Without a cloud copy there is nothing to serve.
    expect(blockedReason(segment('x', 0, 60, { state: 'deleted', archived: false }))).not.toBe('')
    expect(blockedReason(segment('x', 0, 60, { state: 'missing', archived: false }))).not.toBe('')
    // Every other non-ready state stays blocked regardless: a still-writing or
    // conflicting segment has no settled bytes anywhere.
    for (const state of ['finalizing', 'provisional', 'damaged', 'conflict'] as const) {
      expect(blockedReason(segment('x', 0, 60, { state, archived: true }))).not.toBe('')
    }
  })

  it('prefers the playable segment when two cover the same instant', () => {
    const timeline = buildTimeline(
      [
        segment('damaged', 0, 120, { state: 'damaged' }),
        segment('ready', 0, 120),
      ],
      base,
      base + 120_000
    )
    expect(entryAt(timeline, base + 10_000)?.id).toBe('ready')
    // The damaged copy still counts as coverage, so it is not a hole.
    expect(gapAt(timeline, base + 10_000)).toBeNull()
  })

  it('locates the segment and the offset inside it', () => {
    const timeline = buildTimeline([segment('a', 0, 60)], base, base + 60_000)
    const entry = entryAt(timeline, base + 30_000)
    expect(entry?.id).toBe('a')
    expect(offsetIn(entry!, base + 30_000)).toBe(30)
    expect(offsetIn(entry!, base - 5000)).toBe(0)
    expect(offsetIn(entry!, base + 900_000)).toBe(60)
  })

  it('finds the next playable segment past a position', () => {
    const timeline = buildTimeline(
      [
        segment('a', 0, 60),
        segment('b', 120, 180, { state: 'missing' }),
        segment('c', 240, 300),
      ],
      base,
      base + 300_000
    )
    expect(nextPlayableEntry(timeline, base + 60_000)?.id).toBe('c')
    expect(nextPlayableEntry(timeline, base + 300_000)).toBeNull()
  })

  it('renders segment durations', () => {
    expect(formatDuration(45_000)).toBe('0:45')
    expect(formatDuration(3_725_000)).toBe('1:02:05')
  })
})

describe('view slicing', () => {
  const base = Date.parse('2026-10-12T00:00:00Z')
  const hour = 3_600_000
  const segment = (id: string, from: number, to: number): Entry => ({
    id, start: base + from * hour, end: base + to * hour, bytes: 1, state: 'ready', checkState: '', blocked: '', deleted: false, fault: false,
  })
  const full = { entries: [segment('a', 0, 1), segment('b', 2, 3)], gaps: [], from: base, to: base + 4 * hour }

  it('clips entries to the view and recounts gaps inside it', () => {
    const view = sliceView(full, base, base + 2.5 * hour)
    expect(view.from).toBe(base)
    expect(view.to).toBe(base + 2.5 * hour)
    expect(view.entries.map((e) => e.id)).toEqual(['a', 'b'])
    expect(view.entries[1].start).toBe(base + 2 * hour)
    expect(view.gaps).toHaveLength(1)
    expect(view.gaps[0].start).toBe(base + hour)
  })

  it('drops entries outside the view', () => {
    const view = sliceView(full, base + 3 * hour, base + 4 * hour)
    expect(view.entries).toHaveLength(0)
    expect(view.gaps).toHaveLength(1)
  })
})

describe('zoomView', () => {
  const full = { start: 0, end: 24 * 3_600_000 }
  it('halves the window around the anchor and stays inside the range', () => {
    const view = zoomView(0, full.end, 12 * 3_600_000, full)
    expect(view.start).toBe(6 * 3_600_000)
    expect(view.end).toBe(18 * 3_600_000)
  })
  it('clamps at the range edges without shrinking below a minute', () => {
    const view = zoomView(0, 2 * 3_600_000, 0, full)
    expect(view.start).toBe(0)
    expect(view.end).toBe(3_600_000)
    const tiny = zoomView(0, 90_000, 45_000, full)
    expect(tiny.end - tiny.start).toBeGreaterThanOrEqual(60_000)
  })
})

describe('buildTicks', () => {
  const base = Date.parse('2026-10-12T00:00:00Z')
  it('draws wall-clock hourly ticks across a six hour window', () => {
    // Asia/Shanghai is UTC+8: 00:00Z-06:00Z reads 08:00-14:00 local, so the
    // hourly ticks must land on local :00 boundaries.
    const ticks = buildTicks(base, base + 6 * 3_600_000, 1200, 'Asia/Shanghai')
    // A six hour window on a wide strip lands on 30-minute ticks; no midnight
    // inside means no major tick.
    expect(ticks.length).toBeGreaterThanOrEqual(12)
    expect(ticks[0].label).toBe('10-12 08:00')
    expect(ticks[1].label).toBe('08:30')
    expect(ticks.every((tick) => !tick.major)).toBe(true)
  })
  it('switches to day labels for multi-day windows', () => {
    const ticks = buildTicks(base, base + 7 * 86_400_000, 1200, 'Asia/Shanghai')
    // The window starts mid-day, so the first daily tick is the next midnight.
    expect(ticks.length).toBeGreaterThanOrEqual(6)
    expect(ticks[0].label).toBe('10-13')
    expect(ticks[1].label).toBe('10-14')
    expect(ticks.every((tick) => tick.major)).toBe(true)
  })
  it('produces minute ticks for a zoomed window', () => {
    const ticks = buildTicks(base, base + 5 * 60_000, 1200, 'Asia/Shanghai')
    expect(ticks[0].label).toBe('10-12 08:00')
    expect(ticks[1].label).toBe('08:01')
  })
  it('refuses degenerate inputs', () => {
    expect(buildTicks(0, 0, 1200, 'Asia/Shanghai')).toEqual([])
    expect(buildTicks(base, base + 3_600_000, 0, 'Asia/Shanghai')).toEqual([])
  })
})

// A deleted segment is unplayable but not BLOCKED, and the difference is the
// whole point: red on this strip means "the recording went wrong". Painting a
// deliberate deletion red sends an operator hunting for a fault that does not
// exist, and folding it into the blocked count inflates the failure figure with
// decisions the operator made on purpose.
describe('deleted segments', () => {
  const base = Date.parse('2026-10-12T00:00:00Z')
  const minute = 60_000
  const segment = (
    id: string,
    from: number,
    to: number,
    state: 'ready' | 'deleted' | 'damaged'
  ) => ({
    id,
    channel_id: 'c',
    source_revision_id: 'r',
    pool_id: 'p',
    run_id: 'u',
    start: new Date(base + from * minute).toISOString(),
    end: new Date(base + to * minute).toISOString(),
    bytes: 1,
    state,
    check_state: '',
    media: undefined,
  })

  // "Cannot play" and "went wrong" are separate questions. A deleted segment
  // cannot play — the bytes are gone — so it must still report a reason, or the
  // page offers a play button on footage that answers 404. What it must NOT do
  // is count as a fault, which is what the strip's colour reads.
  it('cannot play, but is not a fault', () => {
    const deleted = segment('a', 0, 1, 'deleted') as never
    expect(blockedReason(deleted)).not.toBe('')
    expect(isFault(deleted)).toBe(false)
    const damaged = segment('b', 0, 1, 'damaged') as never
    expect(blockedReason(damaged)).not.toBe('')
    expect(isFault(damaged)).toBe(true)
  })

  it('is merged into its own layer, not the blocked one', () => {
    const timeline = buildTimeline(
      [segment('a', 0, 1, 'ready'), segment('b', 1, 2, 'deleted')] as never,
      base,
      base + 2 * minute
    )
    const merged = mergeEntries(timeline.entries)
    expect(merged.deleted).toHaveLength(1)
    expect(merged.blocked).toHaveLength(0)
  })

  it('still counts as coverage, so a deletion is not reported as a gap', () => {
    const timeline = buildTimeline(
      [segment('a', 0, 1, 'deleted')] as never,
      base,
      base + minute
    )
    expect(timeline.gaps).toHaveLength(0)
  })
})
