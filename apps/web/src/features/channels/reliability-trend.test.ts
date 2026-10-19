import { describe, expect, test } from 'vitest'
import type { Schema } from '@/lib/types'
import { dayLabel, trendRows, trendSpan, worthCharting } from './reliability-trend'

function point(day: string, overrides: Record<string, number> = {}) {
  return {
    day,
    healthy_ms: 3_600_000,
    unavailable_ms: 0,
    unknown_ms: 0,
    excluded_ms: 0,
    fault_count: 0,
    ...overrides,
  } as Schema<'ReliabilityTrendPoint'>
}

describe('trendRows', () => {
  // The API returns newest first because the table wants it that way; a time
  // series must read left to right, and doing the reverse here means no call site
  // can forget it.
  test('orders oldest first regardless of the input order', () => {
    const rows = trendRows([point('2026-10-18'), point('2026-10-16'), point('2026-10-17')])
    expect(rows.map((row) => row.raw)).toEqual([
      '2026-10-16',
      '2026-10-17',
      '2026-10-18',
    ])
  })

  // Durations in hours: a 30-day chart in milliseconds produces axis labels like
  // 86400000, which nobody reads.
  test('converts durations to hours', () => {
    const rows = trendRows([point('2026-10-18', { healthy_ms: 5_400_000 })])
    expect(rows[0].healthy).toBe(1.5)
    expect(rows[0].healthyMs).toBe(5_400_000)
  })

  // A day nobody observed must stay distinguishable from a day that was down:
  // both would be "0 healthy hours", and the colours tell them apart.
  test('keeps unknown and excluded separate from unavailable', () => {
    const rows = trendRows([
      point('2026-10-18', { healthy_ms: 0, unavailable_ms: 0, unknown_ms: 3_600_000 }),
      point('2026-10-19', { healthy_ms: 0, unavailable_ms: 3_600_000 }),
    ])
    expect(rows[0].unknown).toBe(1)
    expect(rows[0].unavailable).toBe(0)
    expect(rows[1].unavailable).toBe(1)
    expect(rows[1].unknown).toBe(0)
  })
})

describe('dayLabel', () => {
  test('shortens a date and passes anything else through', () => {
    expect(dayLabel('2026-10-18')).toBe('10-18')
    expect(dayLabel('unknown')).toBe('unknown')
  })
})

describe('worthCharting', () => {
  // One point is a number, not a trend: a single column invites reading a
  // direction that does not exist.
  test('needs more than one day', () => {
    expect(worthCharting(trendRows([point('2026-10-18')]))).toBe(false)
    expect(
      worthCharting(trendRows([point('2026-10-18'), point('2026-10-19')]))
    ).toBe(true)
  })
})

describe('trendSpan', () => {
  test('names the window so a 4-day and a 400-day trend are distinguishable', () => {
    const rows = trendRows([point('2026-10-16'), point('2026-10-18')])
    expect(trendSpan(rows)).toBe('2026-10-16 至 2026-10-18')
    expect(trendSpan([])).toBe('')
  })
})
