import { describe, expect, it } from 'vitest'
import {
  formatClock,
  parseWallClock,
  quickRange,
  startOfZoneDay,
  wallClockInput,
  zoneParts,
} from './zone'

const base = Date.parse('2026-10-05T00:00:00Z')

describe('site timezone wall clock', () => {
  it('reads an instant in the requested zone', () => {
    expect(zoneParts(base, 'Asia/Shanghai')).toEqual({
      year: 2026,
      month: 10,
      day: 5,
      hour: 8,
      minute: 0,
      second: 0,
    })
    expect(formatClock(base, 'Asia/Shanghai')).toBe('2026-10-05 08:00:00')
    expect(formatClock(base, 'UTC')).toBe('2026-10-05 00:00:00')
    expect(formatClock(Number.NaN, 'UTC')).toBe('—')
  })

  it('round-trips a wall clock value through the site zone', () => {
    // A zero seconds component is dropped, exactly as the control reports it.
    expect(wallClockInput(base, 'Asia/Shanghai')).toBe('2026-10-05T08:00')
    expect(parseWallClock('2026-10-05T08:00', 'Asia/Shanghai')).toBe(base)
    expect(parseWallClock('2026-10-05T00:00:00', 'UTC')).toBe(base)
    // The zone decides the instant: the same text names a different moment in
    // each of these, so neither the browser zone nor a passthrough can be right.
    expect(parseWallClock('2026-10-05T08:00:00', 'UTC')).toBe(
      Date.parse('2026-10-05T08:00:00Z')
    )
    expect(parseWallClock('2026-10-05T08:00:00', 'America/New_York')).toBe(
      Date.parse('2026-10-05T12:00:00Z')
    )
  })

  it('keeps seconds when they are not zero', () => {
    const instant = Date.parse('2026-10-04T16:00:07Z')
    expect(wallClockInput(instant, 'Asia/Shanghai')).toBe('2026-10-05T00:00:07')
    expect(parseWallClock('2026-10-05T00:00:07', 'Asia/Shanghai')).toBe(instant)
  })

  it('accepts the minute-precision form a datetime-local control can emit', () => {
    expect(parseWallClock('2026-10-05T08:00', 'Asia/Shanghai')).toBe(base)
  })

  it('rejects values that are not real instants in the zone', () => {
    expect(parseWallClock('', 'UTC')).toBeNaN()
    expect(parseWallClock('2026-10-05', 'UTC')).toBeNaN()
    expect(parseWallClock('2026-13-01T00:00:00', 'UTC')).toBeNaN()
    expect(parseWallClock('2026-02-31T00:00:00', 'UTC')).toBeNaN()
    expect(parseWallClock('2026-10-05T24:00:00', 'UTC')).toBeNaN()
    // 02:30 does not exist on the US spring-forward date in this zone.
    expect(parseWallClock('2026-03-08T02:30:00', 'America/New_York')).toBeNaN()
  })

  it('keeps DST changes honest in both directions', () => {
    // 2026-03-08 03:00 local (EDT, UTC-4) is 07:00 UTC; 01:59 EST is 06:59 UTC.
    expect(parseWallClock('2026-03-08T03:00:00', 'America/New_York')).toBe(
      Date.parse('2026-03-08T07:00:00Z')
    )
    expect(parseWallClock('2026-03-08T01:59:00', 'America/New_York')).toBe(
      Date.parse('2026-03-08T06:59:00Z')
    )
    // The repeated hour in November resolves to the first occurrence.
    expect(parseWallClock('2026-11-01T01:30:00', 'America/New_York')).toBe(
      Date.parse('2026-11-01T05:30:00Z')
    )
  })

  it('finds the local day boundary, not the UTC one', () => {
    const shanghai = startOfZoneDay(base, 'Asia/Shanghai')
    expect(shanghai).toBe(Date.parse('2026-10-04T16:00:00Z'))
    expect(wallClockInput(shanghai, 'Asia/Shanghai')).toBe('2026-10-05T00:00')
    expect(startOfZoneDay(base, 'UTC')).toBe(base)
  })

  it('resolves the ranges operators ask for against the site clock', () => {
    const now = Date.parse('2026-10-05T09:30:00Z')
    expect(quickRange('hour', now, 'Asia/Shanghai')).toEqual({
      start: now - 3600_000,
      end: now,
    })
    expect(quickRange('day', now, 'Asia/Shanghai')).toEqual({
      start: now - 24 * 3600_000,
      end: now,
    })
    expect(quickRange('today', now, 'Asia/Shanghai')).toEqual({
      start: Date.parse('2026-10-04T16:00:00Z'),
      end: now,
    })
    expect(quickRange('yesterday', now, 'Asia/Shanghai')).toEqual({
      start: Date.parse('2026-10-03T16:00:00Z'),
      end: Date.parse('2026-10-04T16:00:00Z'),
    })
    // Yesterday is a local day, so it is not simply 24 hours on a DST change.
    const dst = Date.parse('2026-03-09T16:00:00Z')
    expect(quickRange('yesterday', dst, 'America/New_York')).toEqual({
      start: Date.parse('2026-03-08T05:00:00Z'),
      end: Date.parse('2026-03-09T04:00:00Z'),
    })
  })
})
