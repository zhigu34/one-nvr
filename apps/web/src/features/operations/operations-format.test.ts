import { describe, expect, test } from 'vitest'
import type { Schema } from '@/lib/types'
import {
  EMPTY_FILTERS,
  PAGE_SIZE,
  clampPage,
  slicePage,
  filterFaults,
  groupFlappingFaults,
  groupIsSevere,
  isSevere,
  orderFaults,
  relativeTime,
  spanShares,
} from './operations-format'

type Fault = Schema<'ReliabilityFault'>

function fault(overrides: Partial<Fault> = {}): Fault {
  return {
    id: 'f1',
    channel_id: 'ch-8',
    channel_no: 8,
    kind: 'sub',
    state: 'resolved',
    started_at: '2026-10-17T10:00:00Z',
    ended_at: '2026-10-17T10:00:10Z',
    duration_ms: 10_000,
    initial_reason: 'sub_source_unavailable',
    category: 'unknown',
    effective_category: 'unknown',
    note: '',
    maintenance: false,
    ...overrides,
  }
}

describe('filterFaults', () => {
  const items = [
    fault({ id: 'a', channel_id: 'ch-8', kind: 'sub', duration_ms: 10_000 }),
    fault({ id: 'b', channel_id: 'ch-8', kind: 'main', duration_ms: 540_000 }),
    fault({ id: 'c', channel_id: 'ch-5', kind: 'main', duration_ms: 60_000, state: 'open' }),
  ]

  test('returns everything with no filters', () => {
    expect(filterFaults(items, EMPTY_FILTERS)).toHaveLength(3)
  })

  test('filters by channel so a noisy test channel can be excluded', () => {
    const out = filterFaults(items, { ...EMPTY_FILTERS, channel: 'ch-5' })
    expect(out.map((f) => f.id)).toEqual(['c'])
  })

  test('filters by stream kind', () => {
    const out = filterFaults(items, { ...EMPTY_FILTERS, kind: 'main' })
    expect(out.map((f) => f.id)).toEqual(['b', 'c'])
  })

  test('filters short blips out with a duration floor', () => {
    const out = filterFaults(items, { ...EMPTY_FILTERS, minDurationMs: 60_000 })
    expect(out.map((f) => f.id)).toEqual(['b', 'c'])
  })

  test('filters by unresolved state', () => {
    const out = filterFaults(items, { ...EMPTY_FILTERS, state: 'open' })
    expect(out.map((f) => f.id)).toEqual(['c'])
  })

  test('combines filters', () => {
    const out = filterFaults(items, {
      ...EMPTY_FILTERS,
      channel: 'ch-8',
      kind: 'main',
      minDurationMs: 300_000,
    })
    expect(out.map((f) => f.id)).toEqual(['b'])
  })
})

describe('orderFaults', () => {
  // An unresolved fault is the only row that is happening now; it must not be
  // buried under resolved history just because that history is newer.
  test('puts open faults first, then newest first', () => {
    const items = [
      fault({ id: 'old-open', state: 'open', started_at: '2026-10-17T08:00:00Z' }),
      fault({ id: 'new-resolved', state: 'resolved', started_at: '2026-10-17T12:00:00Z' }),
      fault({ id: 'mid-resolved', state: 'resolved', started_at: '2026-10-17T10:00:00Z' }),
    ]
    expect(orderFaults(items).map((f) => f.id)).toEqual([
      'old-open',
      'new-resolved',
      'mid-resolved',
    ])
  })
})

describe('groupFlappingFaults', () => {
  // A flapping sub stream: ten-second drops every thirty seconds. They are one
  // problem seen repeatedly, so one row with a count — while still reporting the
  // real number of interruptions.
  test('merges adjacent short faults on the same channel, kind and category', () => {
    const items = [
      fault({ id: 'f1', started_at: '2026-10-17T10:00:00Z', ended_at: '2026-10-17T10:00:10Z' }),
      fault({ id: 'f2', started_at: '2026-10-17T10:01:00Z', ended_at: '2026-10-17T10:01:10Z' }),
      fault({ id: 'f3', started_at: '2026-10-17T10:02:00Z', ended_at: '2026-10-17T10:02:10Z' }),
    ]
    const groups = groupFlappingFaults(items)
    expect(groups).toHaveLength(1)
    expect(groups[0].count).toBe(3)
    expect(groups[0].totalDurationMs).toBe(30_000)
  })

  // The gap test is what makes this flapping rather than "this channel had
  // faults at some point". Two incidents hours apart are two incidents.
  test('keeps faults hours apart separate', () => {
    const items = [
      fault({ id: 'morning', started_at: '2026-10-17T08:00:00Z', ended_at: '2026-10-17T08:00:10Z' }),
      fault({ id: 'afternoon', started_at: '2026-10-17T15:00:00Z', ended_at: '2026-10-17T15:00:10Z' }),
    ]
    expect(groupFlappingFaults(items)).toHaveLength(2)
  })

  test('does not merge different kinds or categories', () => {
    const items = [
      fault({ id: 'sub', kind: 'sub' }),
      fault({ id: 'main', kind: 'main' }),
      fault({ id: 'other', effective_category: 'upstream_unavailable' }),
    ]
    expect(groupFlappingFaults(items)).toHaveLength(3)
  })

  test('does not merge different channels', () => {
    const items = [
      fault({ id: 'a', channel_id: 'ch-1', channel_no: 1 }),
      fault({ id: 'b', channel_id: 'ch-2', channel_no: 2 }),
    ]
    expect(groupFlappingFaults(items)).toHaveLength(2)
  })

  test('reports a group as open when any member is unresolved', () => {
    const items = [
      fault({ id: 'open', state: 'open', ended_at: null, started_at: '2026-10-17T10:00:00Z' }),
      fault({ id: 'closed', started_at: '2026-10-17T10:00:30Z', ended_at: '2026-10-17T10:00:40Z' }),
    ]
    const groups = groupFlappingFaults(items)
    expect(groups).toHaveLength(1)
    expect(groups[0].open).toBe(true)
  })

  // A single fault must still be a group of one: the table renders the same
  // shape either way, so a lone fault needs no special case.
  test('leaves a lone fault as a group of one', () => {
    const groups = groupFlappingFaults([fault()])
    expect(groups).toHaveLength(1)
    expect(groups[0].count).toBe(1)
  })
})

describe('isSevere', () => {
  // Five minutes is the line: below it a drop is noise in this table, at or
  // above it the outage deserves to be found at a glance.
  test('flags five minutes and longer', () => {
    expect(isSevere(299_999)).toBe(false)
    expect(isSevere(300_000)).toBe(true)
    expect(isSevere(19 * 60_000)).toBe(true)
  })

  test('treats a missing duration as not severe', () => {
    expect(isSevere(undefined)).toBe(false)
  })
})

describe('relativeTime', () => {
  const now = new Date('2026-10-17T22:35:30Z').getTime()

  test('describes recent and old timestamps', () => {
    expect(relativeTime('2026-10-17T22:35:20Z', now)).toBe('刚刚')
    expect(relativeTime('2026-10-17T22:31:30Z', now)).toBe('4 分钟前')
    expect(relativeTime('2026-10-17T18:35:30Z', now)).toBe('4 小时前')
    expect(relativeTime('2026-10-15T22:35:30Z', now)).toBe('2 天前')
  })

  // A clock skew that puts the timestamp slightly ahead must not print a
  // negative age.
  test('does not print negative ages', () => {
    expect(relativeTime('2026-10-17T23:00:00Z', now)).toBe('刚刚')
  })

  test('returns nothing for an unparseable value', () => {
    expect(relativeTime('not-a-date', now)).toBe('')
  })
})

describe('spanShares', () => {
  test('splits observed time into the three bands', () => {
    const shares = spanShares({ healthy_ms: 500, unavailable_ms: 300, unknown_ms: 200 })
    expect(shares).not.toBeNull()
    expect(shares!.healthy).toBeCloseTo(0.5)
    expect(shares!.unavailable).toBeCloseTo(0.3)
    expect(shares!.unknown).toBeCloseTo(0.2)
  })

  // A zero-observation row draws no bar at all: a zero-width bar reads like a
  // rendering failure, and "nothing observed" already has its own wording.
  test('returns null when nothing was observed', () => {
    expect(spanShares({ healthy_ms: 0, unavailable_ms: 0, unknown_ms: 0 })).toBeNull()
  })

  test('treats a row with only one band as full width', () => {
    const shares = spanShares({ healthy_ms: 1000, unavailable_ms: 0, unknown_ms: 0 })
    expect(shares!.healthy).toBe(1)
  })
})

describe('groupIsSevere', () => {
  // The distinction this exists for: twelve ten-second blips add up past five
  // minutes but are a flapping stream, while one nine-minute outage is a
  // different failure. Only the latter is highlighted.
  test('is false when short blips merely add up past five minutes', () => {
    // One drop every 30 seconds, each lasting 10 — the flapping pattern the
    // grouping exists for.
    const items = Array.from({ length: 40 }, (_, index) => {
      const start = new Date(Date.parse('2026-10-17T10:00:00Z') + index * 30_000)
      const end = new Date(start.getTime() + 10_000)
      return fault({
        id: `f${index}`,
        started_at: start.toISOString(),
        ended_at: end.toISOString(),
        duration_ms: 10_000,
      })
    })
    const groups = groupFlappingFaults(items)
    expect(groups).toHaveLength(1)
    // 40 × 10s = 400s of downtime, well past five minutes in total…
    expect(groups[0].totalDurationMs).toBeGreaterThan(300_000)
    // …but no single interruption lasted five minutes, so it is not severe.
    expect(groupIsSevere(groups[0])).toBe(false)
  })

  test('is true when any single member reaches five minutes', () => {
    const items = [
      fault({ id: 'a', started_at: '2026-10-17T10:00:00Z', ended_at: '2026-10-17T10:00:10Z' }),
      fault({
        id: 'b',
        started_at: '2026-10-17T10:01:00Z',
        ended_at: '2026-10-17T10:10:00Z',
        duration_ms: 540_000,
      }),
    ]
    const groups = groupFlappingFaults(items)
    expect(groups).toHaveLength(1)
    expect(groupIsSevere(groups[0])).toBe(true)
  })
})

describe('slicePage', () => {
  const rows = Array.from({ length: 25 }, (_, index) => index)

  test('shows ten rows per page', () => {
    const first = slicePage(rows, 1)
    expect(first.rows).toHaveLength(PAGE_SIZE)
    expect(first.rows[0]).toBe(0)
    expect(first.pageCount).toBe(3)
  })

  test('returns the tail on the last page', () => {
    const last = slicePage(rows, 3)
    expect(last.rows).toEqual([20, 21, 22, 23, 24])
    expect(last.page).toBe(3)
  })

  // Filters shrink the list underneath the current page; clamping keeps the
  // reader on real data instead of an empty page 7 of 3.
  test('clamps a page number past the end', () => {
    const clamped = slicePage(rows, 99)
    expect(clamped.page).toBe(3)
    expect(clamped.rows).toHaveLength(5)
  })

  test('clamps nonsense input to the first page', () => {
    expect(clampPage(0, 3)).toBe(1)
    expect(clampPage(-5, 3)).toBe(1)
    expect(clampPage(Number.NaN, 3)).toBe(1)
  })

  // An empty list is still "page 1 of 1", not "page 1 of 0".
  test('reports one page for an empty list', () => {
    const empty = slicePage([], 1)
    expect(empty.pageCount).toBe(1)
    expect(empty.rows).toEqual([])
  })
})
