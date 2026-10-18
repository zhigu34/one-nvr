import { describe, expect, test } from 'vitest'
import {
  buildWindow,
  clock,
  overlaps,
  parseClock,
  scheduleSummary,
  windowLabel,
} from './schedule-format'

describe('schedule formatting', () => {
  test('clock formats minutes and wraps past a day', () => {
    expect(clock(0)).toBe('00:00')
    expect(clock(8 * 60)).toBe('08:00')
    expect(clock(23 * 60 + 59)).toBe('23:59')
    // An overnight end is stored above 1440; it must read as 02:00, not 26:00.
    expect(clock(26 * 60)).toBe('02:00')
    expect(clock(1440)).toBe('00:00')
  })

  test('parseClock rejects incomplete and out-of-range input', () => {
    expect(parseClock('08:30')).toBe(510)
    expect(parseClock('23:59')).toBe(1439)
    expect(parseClock('')).toBeNull()
    expect(parseClock('8')).toBeNull()
    expect(parseClock('24:00')).toBeNull()
    expect(parseClock('08:60')).toBeNull()
  })

  test('windowLabel shows an overnight window as one range', () => {
    expect(windowLabel({ weekday: 1, start_minute: 480, end_minute: 720 })).toBe('08:00 – 12:00')
    expect(windowLabel({ weekday: 1, start_minute: 1320, end_minute: 1560 })).toBe('22:00 – 02:00')
  })
})

describe('buildWindow', () => {
  test('an end at or before the start crosses midnight', () => {
    const built = buildWindow(1, 22 * 60, 2 * 60)
    expect('window' in built).toBe(true)
    if ('window' in built) {
      expect(built.window.end_minute).toBe(2 * 60 + 1440)
    }
  })

  test('an equal start and end is rejected rather than silently empty', () => {
    const built = buildWindow(1, 8 * 60, 8 * 60)
    expect('error' in built).toBe(true)
  })

  test('a plain same-day window is unchanged', () => {
    const built = buildWindow(3, 9 * 60, 17 * 60)
    if (!('window' in built)) throw new Error('rejected')
    expect(built.window).toEqual({ weekday: 3, start_minute: 540, end_minute: 1020 })
  })
})

describe('overlaps', () => {
  test('same-day overlap is detected', () => {
    expect(
      overlaps(
        { weekday: 1, start_minute: 480, end_minute: 720 },
        { weekday: 1, start_minute: 700, end_minute: 900 }
      )
    ).toBe(true)
  })

  // Adjacent windows meet but do not overlap: the interval is half-open, which
  // is what lets 08:00-12:00 sit next to 12:00-18:00 without a chosen winner.
  test('touching endpoints do not overlap', () => {
    expect(
      overlaps(
        { weekday: 1, start_minute: 480, end_minute: 720 },
        { weekday: 1, start_minute: 720, end_minute: 1080 }
      )
    ).toBe(false)
  })

  test('different weekdays never overlap', () => {
    expect(
      overlaps(
        { weekday: 1, start_minute: 480, end_minute: 720 },
        { weekday: 2, start_minute: 480, end_minute: 720 }
      )
    ).toBe(false)
  })

  // The tail belongs to the next day, so a Monday overnight window collides with
  // Tuesday's early window even though the two "days" differ.
  test('an overnight tail collides with the next day', () => {
    expect(
      overlaps(
        { weekday: 1, start_minute: 1320, end_minute: 1560 },
        { weekday: 2, start_minute: 0, end_minute: 180 }
      )
    ).toBe(true)
  })
})

describe('scheduleSummary', () => {
  test('names common day groupings', () => {
    const weekdays = [1, 2, 3, 4, 5].map((weekday) => ({
      weekday,
      start_minute: 480,
      end_minute: 1080,
    }))
    expect(scheduleSummary(weekdays)).toBe('工作日 08:00 – 18:00')
  })

  test('an empty schedule says so instead of looking like a bug', () => {
    expect(scheduleSummary([])).toBe('未设置时段')
  })
})
